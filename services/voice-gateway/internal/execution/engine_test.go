package execution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/diagnostic"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/journal"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/provider"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/scheduler"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func setup(t *testing.T, store *testutil.Memory, dir string) *Engine {
	t.Helper()
	blobs, err := journal.OpenBlobs(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(blobs.Close)
	q, err := scheduler.New([]scheduler.Instance{{ID: "one", Profiles: []string{"voice"}, Slots: 1}}, 4)
	if err != nil {
		t.Fatal(err)
	}
	tts, _ := scheduler.New([]scheduler.Instance{{ID: "tts", Profiles: []string{"voice"}, Slots: 1}}, 4)
	e := New(store, blobs, map[string]*scheduler.Queue{"stt": q, "tts": tts}, time.Hour, func(string) bool { return false }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err = e.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}
func TestDurableResultAndIdempotentReplay(t *testing.T) {
	store := &testutil.Memory{}
	dir := t.TempDir()
	e := setup(t, store, dir)
	calls := 0
	call := func(context.Context, string) scheduler.Result {
		calls++
		return scheduler.Result{Value: []byte("private result")}
	}
	r, id := e.Submit(context.Background(), "stt", "", "same", []byte("private request"), func(string) {}, call)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	r, replayID := e.Submit(context.Background(), "stt", "", "same", []byte("private request"), func(string) {}, call)
	if r.Err != nil || id != replayID || calls != 1 {
		t.Fatalf("replay: %+v calls %d", r, calls)
	}
	r, _ = e.Submit(context.Background(), "stt", "", "same", []byte("changed"), func(string) {}, call)
	if status.Code(r.Err) != codes.AlreadyExists {
		t.Fatal(r.Err)
	}
	recorder := httptest.NewRecorder()
	e.Metrics(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(recorder.Body.String(), "private") {
		t.Fatal("content leaked into metrics")
	}
	e.blobs.Close()
	restarted := setup(t, store, dir)
	r, replayID = restarted.Submit(context.Background(), "stt", "", "same", []byte("private request"), func(string) {}, call)
	if r.Err != nil || id != replayID || calls != 1 {
		t.Fatal("restart lost result")
	}
}

func TestWaitingReplayReturnsExpiredResultAfterCleanup(t *testing.T) {
	e := setup(t, &testutil.Memory{}, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finish := make(chan struct{})
	original := make(chan scheduler.Result, 1)
	idReady := make(chan string, 1)
	go func() {
		r, _ := e.Submit(ctx, "stt", "", "same", []byte("request"), func(id string) { idReady <- id }, func(context.Context, string) scheduler.Result {
			select {
			case <-finish:
				return scheduler.Result{Value: []byte("result")}
			case <-ctx.Done():
				return scheduler.Result{Err: ctx.Err()}
			}
		})
		original <- r
	}()
	var id string
	select {
	case id = <-idReady:
	case <-ctx.Done():
		t.Fatal("original request did not start")
	}
	announced, resume := make(chan struct{}), make(chan struct{}, 1)
	defer close(resume)
	replay := make(chan scheduler.Result, 1)
	go func() {
		r, _ := e.Submit(ctx, "stt", "", "same", []byte("request"), func(string) {
			close(announced)
			select {
			case <-resume:
			case <-ctx.Done():
			}
		}, func(context.Context, string) scheduler.Result {
			return scheduler.Result{Err: errors.New("unexpected second model call")}
		})
		replay <- r
	}()
	select {
	case <-announced:
	case <-ctx.Done():
		t.Fatal("replay did not enter announce")
	}
	close(finish)
	select {
	case r := <-original:
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	case <-ctx.Done():
		t.Fatal("original request did not finish")
	}
	e.mu.Lock()
	err := e.cleanLocked(ctx, time.Now().Add(2*time.Hour))
	removed := e.jobs[id] == nil
	e.mu.Unlock()
	if err != nil || !removed {
		t.Fatalf("cleanup: removed=%v err=%v", removed, err)
	}
	// Resume announce without closing resume twice in the deferred cleanup.
	resume <- struct{}{}
	select {
	case r := <-replay:
		if status.Code(r.Err) != codes.NotFound {
			t.Fatalf("expired replay: %v", r.Err)
		}
	case <-ctx.Done():
		t.Fatal("replay did not finish")
	}
}
func TestFailedPersistenceDoesNotCallModel(t *testing.T) {
	store := &testutil.Memory{}
	e := setup(t, store, t.TempDir())
	store.Mu.Lock()
	store.Fail = true
	store.Mu.Unlock()
	called := false
	r, _ := e.Submit(context.Background(), "stt", "", "", []byte("audio"), func(string) {}, func(context.Context, string) scheduler.Result { called = true; return scheduler.Result{} })
	if r.Err == nil || called || e.Ready() {
		t.Fatalf("failed journal admitted call: %+v", r)
	}
}
func TestRecoveryKeepsUncertainSlot(t *testing.T) {
	store := &testutil.Memory{}
	dir := t.TempDir()
	e := setup(t, store, dir)
	r, _ := e.Submit(context.Background(), "stt", "", "", []byte("audio"), func(string) {}, func(context.Context, string) scheduler.Result {
		return scheduler.Result{Err: context.DeadlineExceeded, HoldSlot: true}
	})
	if !errors.Is(r.Err, context.DeadlineExceeded) {
		t.Fatal(r.Err)
	}
	e.blobs.Close()
	restarted := setup(t, store, dir)
	if restarted.queues["stt"].Stats().Instances[0].Busy != 1 {
		t.Fatal("uncertain slot lost on restart")
	}
}

func TestOperatorReleaseIsDurable(t *testing.T) {
	store := &testutil.Memory{}
	dir := t.TempDir()
	e := setup(t, store, dir)
	e.Submit(context.Background(), "stt", "", "", []byte("audio"), func(string) {}, func(context.Context, string) scheduler.Result {
		return scheduler.Result{Err: context.DeadlineExceeded, HoldSlot: true}
	})
	if err := e.ReleaseHeld("one"); err != nil {
		t.Fatal(err)
	}
	if e.queues["stt"].Stats().Instances[0].Busy != 0 {
		t.Fatal("slot still held")
	}
	e.blobs.Close()
	restored := setup(t, store, dir)
	if restored.queues["stt"].Stats().Instances[0].Busy != 0 {
		t.Fatal("release was not committed")
	}
}

func holdOneMinute(string) time.Duration { return time.Minute }

func submitHeld(t *testing.T, e *Engine) time.Time {
	t.Helper()
	_, id := e.Submit(context.Background(), "stt", "", "", []byte("audio"), func(string) {}, func(context.Context, string) scheduler.Result {
		return scheduler.Result{Err: context.DeadlineExceeded, HoldSlot: true}
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.jobs[id].Finished
}

func expire(e *Engine, now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.expireHeldLocked(context.Background(), now, holdOneMinute)
}

func busy(e *Engine) int { return e.queues["stt"].Stats().Instances[0].Busy }

func TestHeldSlotExpiresDurablyAndAdmitsNextCall(t *testing.T) {
	store := &testutil.Memory{}
	dir := t.TempDir()
	e := setup(t, store, dir)
	finished := submitHeld(t, e)
	if err := expire(e, finished.Add(time.Minute-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if busy(e) != 1 {
		t.Fatal("slot released before hold timeout")
	}
	if err := expire(e, finished.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if busy(e) != 0 {
		t.Fatal("slot still held after hold timeout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := e.Submit(ctx, "stt", "", "", []byte("next"), func(string) {}, func(context.Context, string) scheduler.Result {
		return scheduler.Result{Value: []byte("ok")}
	})
	if r.Err != nil {
		t.Fatalf("next call stayed blocked: %v", r.Err)
	}
	e.blobs.Close()
	if restored := setup(t, store, dir); busy(restored) != 0 {
		t.Fatal("expiry was not committed")
	}
}

func TestRestartKeepsHoldDeadline(t *testing.T) {
	store := &testutil.Memory{}
	dir := t.TempDir()
	e := setup(t, store, dir)
	finished := submitHeld(t, e)
	e.blobs.Close()
	restored := setup(t, store, dir)
	if err := expire(restored, finished.Add(59*time.Second)); err != nil {
		t.Fatal(err)
	}
	if busy(restored) != 1 {
		t.Fatal("restart shortened the hold")
	}
	if err := expire(restored, finished.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if busy(restored) != 0 {
		t.Fatal("restart extended the hold")
	}
}

func TestExpiryDoesNotReleaseRunningCall(t *testing.T) {
	e := setup(t, &testutil.Memory{}, t.TempDir())
	started, finish := make(chan struct{}), make(chan struct{})
	done := make(chan scheduler.Result, 1)
	go func() {
		r, _ := e.Submit(context.Background(), "stt", "", "", []byte("audio"), func(string) {}, func(context.Context, string) scheduler.Result {
			close(started)
			<-finish
			return scheduler.Result{Value: []byte("ok")}
		})
		done <- r
	}()
	<-started
	if err := expire(e, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if busy(e) != 1 {
		t.Fatal("running call lost its slot")
	}
	close(finish)
	if r := <-done; r.Err != nil {
		t.Fatal(r.Err)
	}
	if busy(e) != 0 {
		t.Fatal("finished call kept its slot")
	}
}

func TestHeldSlotStaysReservedWhenExpiryIsNotSaved(t *testing.T) {
	store := &testutil.Memory{}
	e := setup(t, store, t.TempDir())
	finished := submitHeld(t, e)
	store.Mu.Lock()
	store.Fail = true
	store.Mu.Unlock()
	if err := expire(e, finished.Add(time.Minute)); err == nil {
		t.Fatal("journal failure ignored")
	}
	if busy(e) != 1 {
		t.Fatal("slot released without a committed record")
	}
}

func TestMaintainExpiresHeldSlots(t *testing.T) {
	e := setup(t, &testutil.Memory{}, t.TempDir())
	submitHeld(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Maintain(ctx, func(string) time.Duration { return 0 })
	deadline := time.Now().Add(2 * heldCheckInterval)
	for busy(e) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("maintenance did not release the held slot")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type countingStore struct {
	*testutil.Memory
	writes int
}

func (s *countingStore) Append(ctx context.Context, r journal.Record) error {
	s.writes++
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Memory.Append(ctx, r)
}
func TestRestoreWritesOnlyTransitionsAndHonorsContext(t *testing.T) {
	store := &countingStore{Memory: &testutil.Memory{}}
	blobs, err := journal.OpenBlobs(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	q, _ := scheduler.New([]scheduler.Instance{{ID: "one", Slots: 1}}, 4)
	queues := map[string]*scheduler.Queue{"stt": q}
	for _, job := range []Job{
		{ID: strings.Repeat("a", 32), Operation: "stt", State: "succeeded", Finished: time.Now()},
		{ID: strings.Repeat("b", 32), Operation: "stt", State: "queued"},
	} {
		data, _ := json.Marshal(job)
		if err := store.Append(context.Background(), journal.Record{ID: job.ID, Operation: job.Operation, Value: data}); err != nil {
			t.Fatal(err)
		}
	}
	store.writes = 0
	e := New(store, blobs, queues, time.Hour, func(string) bool { return false }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := e.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 {
		t.Fatalf("restore wrote %d jobs, want 1", store.writes)
	}
	data, _ := json.Marshal(Job{ID: strings.Repeat("c", 32), Operation: "stt", State: "queued"})
	if err := store.Append(context.Background(), journal.Record{ID: strings.Repeat("c", 32), Operation: "stt", Value: data}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e = New(store, blobs, queues, time.Hour, func(string) bool { return false }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := e.Restore(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore: %v", err)
	}
}

type cancelRunningStore struct {
	*testutil.Memory
	cancel context.CancelFunc
}

func (s *cancelRunningStore) Append(ctx context.Context, r journal.Record) error {
	err := s.Memory.Append(ctx, r)
	var job Job
	if json.Unmarshal(r.Value, &job) == nil && job.State == "running" {
		s.cancel()
	}
	return err
}
func TestCancelAfterAssignmentReleasesSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &cancelRunningStore{Memory: &testutil.Memory{}, cancel: cancel}
	blobs, err := journal.OpenBlobs(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	q, _ := scheduler.New([]scheduler.Instance{{ID: "one", Slots: 1}}, 4)
	e := New(store, blobs, map[string]*scheduler.Queue{"stt": q}, time.Hour, func(string) bool { return false }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := e.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := provider.HTTP{STT: map[string]provider.STT{"one": {URL: "http://127.0.0.1:1", Timeout: time.Second}}}
	e.Submit(ctx, "stt", "", "", []byte("audio"), func(string) {}, func(ctx context.Context, id string) scheduler.Result {
		_, err := p.Transcribe(ctx, id, []byte("audio"), "audio/ogg")
		f, ok := err.(*diagnostic.Error)
		if !ok {
			return scheduler.Result{Err: err, HoldSlot: true}
		}
		return scheduler.Result{Err: err, HoldSlot: f.Uncertain}
	})
	e.Wait()
	deadline := time.Now().Add(time.Second)
	for q.Stats().Instances[0].Busy != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if q.Stats().Instances[0].Busy != 0 {
		t.Fatal("slot held after cancellation before HTTP call")
	}
}
