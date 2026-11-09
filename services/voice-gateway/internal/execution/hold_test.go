package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/journal"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/scheduler"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stateStore records the job state of every journal write.
type stateStore struct {
	*testutil.Memory
	mu     sync.Mutex
	states []string
}

func (s *stateStore) Append(ctx context.Context, r journal.Record) error {
	var job Job
	if json.Unmarshal(r.Value, &job) == nil {
		s.mu.Lock()
		s.states = append(s.states, job.State)
		s.mu.Unlock()
	}
	return s.Memory.Append(ctx, r)
}

func (s *stateStore) written() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.states)
}

func untilBusy(q *scheduler.Queue, want int) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if q.Stats().Instances[0].Busy == want {
			return true
		}
	}
	return false
}

func journaled(t *testing.T, store *testutil.Memory, op, id string) Job {
	t.Helper()
	store.Mu.Lock()
	defer store.Mu.Unlock()
	var job Job
	if err := json.Unmarshal(store.Records[op+"/"+id].Value, &job); err != nil {
		t.Fatalf("journal record %s/%s: %v", op, id, err)
	}
	return job
}

func TestCancelAfterSlotAssignmentNeverStartsCall(t *testing.T) {
	memory := &testutil.Memory{}
	e := setup(t, memory, t.TempDir())
	store := &stateStore{Memory: memory}
	e.store = store
	q := e.queues["stt"]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	locked := make(chan struct{})
	go func() {
		<-locked
		// The callback of the assigned job is now blocked on the engine lock.
		if !untilBusy(q, 1) {
			t.Error("slot was not assigned")
		}
		cancel()
		e.mu.Unlock()
	}()
	var called atomic.Bool
	r, id := e.Submit(ctx, "stt", "", "", []byte("audio"), func(string) {
		e.mu.Lock()
		close(locked)
	}, func(context.Context, string) scheduler.Result {
		called.Store(true)
		return scheduler.Result{Value: []byte("result")}
	})
	if !errors.Is(r.Err, context.Canceled) {
		t.Fatalf("result: %+v", r)
	}
	// Submit can return before the callback does; a free slot means it finished.
	if !untilBusy(q, 0) {
		t.Fatal("slot was not freed")
	}
	if called.Load() {
		t.Fatal("cancelled job reached the model")
	}
	if states := store.written(); !slices.Equal(states, []string{"queued", "failed"}) {
		t.Fatalf("journal states: %v", states)
	}
	e.mu.Lock()
	job := *e.jobs[id]
	e.mu.Unlock()
	if job.State != "failed" || codes.Code(job.Code) != codes.Canceled || job.Reason != "canceled" || job.Stage != "request" || job.Held {
		t.Fatalf("final job: %+v", job)
	}
	metrics := metricsText(e)
	if strings.Contains(metrics, "voice_route_selections_total{") || strings.Contains(metrics, "voice_upstream_calls_total{") {
		t.Fatalf("cancelled job counted as a model call: %s", metrics)
	}
}

// cancelOnRunning cancels the caller while the running state is being saved.
type cancelOnRunning struct {
	*testutil.Memory
	cancel context.CancelFunc
}

func (s *cancelOnRunning) Append(ctx context.Context, r journal.Record) error {
	var job Job
	if json.Unmarshal(r.Value, &job) == nil && job.State == "running" {
		s.cancel()
	}
	return s.Memory.Append(ctx, r)
}

func TestCancelWhileSavingRunningNeverStartsCall(t *testing.T) {
	memory := &testutil.Memory{}
	e := setup(t, memory, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e.store = &cancelOnRunning{Memory: memory, cancel: cancel}
	var called atomic.Bool
	r, id := e.Submit(ctx, "stt", "", "", []byte("audio"), func(string) {}, func(context.Context, string) scheduler.Result {
		called.Store(true)
		return scheduler.Result{Value: []byte("result")}
	})
	if !errors.Is(r.Err, context.Canceled) {
		t.Fatalf("result: %+v", r)
	}
	// Submit can return before the callback does; a free slot means it finished.
	if !untilBusy(e.queues["stt"], 0) {
		t.Fatal("slot was not freed")
	}
	if called.Load() {
		t.Fatal("cancelled job reached the model")
	}
	e.mu.Lock()
	job := *e.jobs[id]
	e.mu.Unlock()
	if job.State != "failed" || codes.Code(job.Code) != codes.Canceled || job.Reason != "canceled" || job.Stage != "request" || job.Held {
		t.Fatalf("final job: %+v", job)
	}
	metrics := metricsText(e)
	if strings.Contains(metrics, "voice_route_selections_total{") || strings.Contains(metrics, "voice_upstream_calls_total{") {
		t.Fatalf("cancelled job counted as a model call: %s", metrics)
	}
}

func TestUncertainCallSlotFollowsAbortOnDisconnect(t *testing.T) {
	failures := []struct {
		name string
		err  error
	}{
		{"cancelled", context.Canceled},
		{"deadline", context.DeadlineExceeded},
		{"uncertain_cancelled", &diagnostic.Error{Code: codes.Canceled, Stage: "upstream", Reason: "transport_failure", Uncertain: true}},
		{"uncertain_deadline", &diagnostic.Error{Code: codes.DeadlineExceeded, Stage: "upstream", Reason: "transport_failure", Uncertain: true}},
	}
	for _, abort := range []bool{false, true} {
		for _, failure := range failures {
			t.Run(fmt.Sprintf("abort=%v/%s", abort, failure.name), func(t *testing.T) {
				store := &testutil.Memory{}
				e := setupAbort(t, store, t.TempDir(), abort)
				q := e.queues["stt"]
				started, finish := make(chan struct{}), make(chan struct{})
				first := make(chan string, 1)
				go func() {
					_, id := e.Submit(context.Background(), "stt", "", "", []byte("first"), func(string) {}, func(context.Context, string) scheduler.Result {
						close(started)
						<-finish
						return scheduler.Result{Err: failure.err}
					})
					first <- id
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("first call did not start")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var nextCalled atomic.Bool
				next := make(chan scheduler.Result, 1)
				go func() {
					r, _ := e.Submit(ctx, "stt", "", "", []byte("next"), func(string) {}, func(context.Context, string) scheduler.Result {
						nextCalled.Store(true)
						return scheduler.Result{Value: []byte("ok")}
					})
					next <- r
				}()
				for deadline := time.Now().Add(5 * time.Second); q.Stats().Waiting != 1; time.Sleep(time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatal("next job did not start waiting")
					}
				}
				close(finish)
				id := <-first
				held := 0
				if abort {
					if r := <-next; r.Err != nil || !nextCalled.Load() {
						t.Fatalf("next job after a freed slot: %+v, called %v", r, nextCalled.Load())
					}
					if !untilBusy(q, 0) {
						t.Fatal("slot stayed reserved")
					}
				} else {
					held = 1
					if stats := q.Stats(); stats.Instances[0].Busy != 1 || stats.Waiting != 1 || nextCalled.Load() {
						t.Fatalf("hold did not keep the next job waiting: %+v, called %v", stats, nextCalled.Load())
					}
				}
				if job := journaled(t, store, "stt", id); job.Held != !abort || job.State != "failed" {
					t.Fatalf("journal: %+v", job)
				}
				metrics := metricsText(e)
				for _, series := range []string{
					fmt.Sprintf(`voice_instance_held{operation="stt",model_instance="one"} %d`, held),
					`voice_held_slots_released_total{operation="stt",model_instance="one",reason="expired"} 0`,
					`voice_held_slots_released_total{operation="stt",model_instance="one",reason="operator"} 0`,
				} {
					if !strings.Contains(metrics, series) {
						t.Fatalf("missing %s: %s", series, metrics)
					}
				}
				cancel()
				if !abort {
					if r := <-next; status.Code(r.Err) == codes.OK || nextCalled.Load() {
						t.Fatalf("next job reached the held slot: %+v", r)
					}
				}
			})
		}
	}
}

func TestRestoreFreesSlotsOfAbortOnDisconnectInstances(t *testing.T) {
	store := &testutil.Memory{}
	running, failed := strings.Repeat("a", 32), strings.Repeat("b", 32)
	now := time.Now()
	for _, job := range []Job{
		{ID: running, Operation: "stt", State: "running", Instance: "one", Held: true, Started: now},
		{ID: failed, Operation: "tts", State: "failed", Instance: "tts", Held: true, Code: int32(codes.DeadlineExceeded), Started: now, Finished: now},
	} {
		data, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Append(context.Background(), journal.Record{ID: job.ID, Operation: job.Operation, Value: data}); err != nil {
			t.Fatal(err)
		}
	}
	e := setupAbort(t, store, t.TempDir(), true)
	for _, op := range []string{"stt", "tts"} {
		if q := e.queues[op].Stats().Instances[0]; q.Busy != 0 {
			t.Fatalf("%s slot stayed reserved: %+v", op, q)
		}
	}
	if job := journaled(t, store, "stt", running); job.Held || job.State != "failed" || codes.Code(job.Code) != codes.Aborted {
		t.Fatalf("restored running job: %+v", job)
	}
	if job := journaled(t, store, "tts", failed); job.Held || job.State != "failed" || codes.Code(job.Code) != codes.DeadlineExceeded {
		t.Fatalf("restored held job: %+v", job)
	}
}

func TestHoldsSlot(t *testing.T) {
	uncertain := &diagnostic.Error{Code: codes.Canceled, Stage: "upstream", Reason: "transport_failure", Uncertain: true}
	confirmed := &diagnostic.Error{Code: codes.Canceled, Stage: "connect", Reason: "connection_failed"}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain cancel", context.Canceled, true},
		{"plain deadline", context.DeadlineExceeded, true},
		{"wrapped cancel", fmt.Errorf("call: %w", context.Canceled), true},
		{"wrapped deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), true},
		{"uncertain diagnostic", uncertain, true},
		{"wrapped uncertain diagnostic", fmt.Errorf("call: %w", uncertain), true},
		{"confirmed diagnostic", confirmed, false},
		{"wrapped confirmed diagnostic", fmt.Errorf("call: %w", confirmed), false},
		{"diagnostic outranks context error", errors.Join(confirmed, context.Canceled), false},
		{"mapped cancel status", status.Error(codes.Canceled, "request cancelled"), false},
		{"mapped deadline status", status.Error(codes.DeadlineExceeded, "request timed out"), false},
		{"other error", errors.New("unknown instance"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keeps := &Engine{abort: func(string) bool { return false }}
			if got := keeps.holdsSlot("one", test.err); got != test.want {
				t.Fatalf("abort=false: %v, want %v", got, test.want)
			}
			frees := &Engine{abort: func(string) bool { return true }}
			if frees.holdsSlot("one", test.err) {
				t.Fatal("abort=true held the slot")
			}
		})
	}
	perInstance := &Engine{abort: func(instance string) bool { return instance == "frees" }}
	if perInstance.holdsSlot("frees", context.Canceled) || !perInstance.holdsSlot("keeps", context.Canceled) {
		t.Fatal("decision ignored the instance")
	}
}
