package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRequireBudgetRefusesOnlyJobsWithoutEnoughTime(t *testing.T) {
	q, err := New([]Instance{{ID: "one", Slots: 1}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	q.RequireBudget(time.Minute)
	short, cancelShort := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShort()
	ran := false
	refused := q.Submit(short, "", func(context.Context, string) Result { ran = true; return Result{} })
	if !errors.Is(refused.Err, ErrInsufficientBudget) || ran {
		t.Fatalf("short job was sent: %+v", refused)
	}
	if busy := q.Stats().Instances[0].Busy; busy != 0 {
		t.Fatalf("refused job took a slot: %d", busy)
	}
	long, cancelLong := context.WithTimeout(context.Background(), time.Hour)
	defer cancelLong()
	for name, ctx := range map[string]context.Context{"deadline": long, "none": context.Background()} {
		if r := q.Submit(ctx, "", func(context.Context, string) Result { return Result{Value: "ok"} }); r.Err != nil {
			t.Fatalf("%s: %v", name, r.Err)
		}
	}
}

func TestStopFinishesWaitingJobsAndKeepsRunningCalls(t *testing.T) {
	q, err := New([]Instance{{ID: "one", Slots: 1}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	running := make(chan Result, 1)
	go func() {
		running <- q.Submit(context.Background(), "", func(context.Context, string) Result {
			close(started)
			<-release
			return Result{Value: "ok"}
		})
	}()
	<-started
	waiting := make(chan Result, 1)
	go func() {
		waiting <- q.Submit(context.Background(), "", func(context.Context, string) Result {
			t.Error("waiting job was dispatched after stop")
			return Result{}
		})
	}()
	for deadline := time.Now().Add(2 * time.Second); q.Stats().Waiting != 1; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("job did not start waiting")
		}
	}
	stopped := errors.New("stopped")
	q.Stop(stopped)
	if r := <-waiting; !errors.Is(r.Err, stopped) {
		t.Fatalf("waiting job: %+v", r)
	}
	if r := q.Submit(context.Background(), "", func(context.Context, string) Result { return Result{} }); !errors.Is(r.Err, stopped) {
		t.Fatalf("job after stop: %+v", r)
	}
	close(release)
	if r := <-running; r.Err != nil {
		t.Fatalf("running call: %+v", r)
	}
	if busy := q.Stats().Instances[0].Busy; busy != 0 {
		t.Fatalf("running call kept its slot: %d", busy)
	}
}

func TestSeparateQueuesAndCompatibleInstances(t *testing.T) {
	stt, err := New([]Instance{{ID: "stt", Slots: 1}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	tts, err := New([]Instance{{ID: "voice", Slots: 1, Profiles: []string{"ru"}}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	finished := make(chan Result, 1)
	go func() {
		finished <- stt.Submit(context.Background(), "", func(_ context.Context, id string) Result {
			close(started)
			<-release
			return Result{Value: id}
		})
	}()
	<-started
	result := tts.Submit(context.Background(), "ru", func(_ context.Context, id string) Result { return Result{Value: id} })
	if result.Err != nil || result.Value != "voice" {
		t.Fatalf("tts: %+v", result)
	}
	if result = tts.Submit(context.Background(), "other", func(context.Context, string) Result { return Result{} }); !errors.Is(result.Err, ErrNoCompatibleInstance) {
		t.Fatalf("profile: %+v", result)
	}
	close(release)
	if result = <-finished; result.Err != nil || result.Value != "stt" {
		t.Fatalf("stt: %+v", result)
	}
}

func TestCancelWaitingDoesNotOccupySlot(t *testing.T) {
	q, err := New([]Instance{{ID: "one", Slots: 1}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan Result, 1)
	go func() {
		done <- q.Submit(context.Background(), "", func(context.Context, string) Result { close(started); <-release; return Result{} })
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	called := make(chan struct{}, 1)
	result := q.Submit(ctx, "", func(context.Context, string) Result { called <- struct{}{}; return Result{} })
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", result.Err)
	}
	close(release)
	<-done
	result = q.Submit(context.Background(), "", func(context.Context, string) Result { return Result{Value: "ok"} })
	if result.Err != nil || result.Value != "ok" {
		t.Fatalf("next: %+v", result)
	}
	select {
	case <-called:
		t.Fatal("cancelled waiting call ran")
	default:
	}
}

func TestUncertainExecutionKeepsSlotReserved(t *testing.T) {
	q, err := New([]Instance{{ID: "one", Slots: 1}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := q.Submit(context.Background(), "", func(context.Context, string) Result { return Result{Err: context.DeadlineExceeded, HoldSlot: true} })
	if !errors.Is(first.Err, context.DeadlineExceeded) {
		t.Fatalf("unexpected first result: %+v", first)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	ran := false
	next := q.Submit(ctx, "", func(context.Context, string) Result { ran = true; return Result{} })
	if !errors.Is(next.Err, context.DeadlineExceeded) || ran {
		t.Fatalf("slot released after uncertain execution: %+v", next)
	}
}

func TestParallelModelsAndQueueLimit(t *testing.T) {
	q, err := New([]Instance{{ID: "one", Slots: 1}, {ID: "two", Slots: 1}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan Result, 2)
	call := func(ctx context.Context, id string) Result {
		started <- id
		select {
		case <-release:
			return Result{Value: id}
		case <-ctx.Done():
			return Result{Err: ctx.Err()}
		}
	}
	go func() { done <- q.Submit(ctx, "", call) }()
	var first string
	select {
	case first = <-started:
	case <-ctx.Done():
		t.Fatal("first model did not start")
	}
	go func() { done <- q.Submit(ctx, "", call) }()
	select {
	case second := <-started:
		if second == first {
			t.Fatal("busy model received a second call")
		}
	case <-ctx.Done():
		t.Fatal("second model blocked behind first")
	}
	waitingCtx, cancelWaiting := context.WithCancel(ctx)
	defer cancelWaiting()
	waiting := make(chan Result, 1)
	go func() { waiting <- q.Submit(waitingCtx, "", call) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for q.Stats().Waiting != 1 {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("request did not enter queue")
		}
	}
	r := q.Submit(ctx, "", call)
	if !errors.Is(r.Err, ErrQueueFull) {
		t.Fatalf("queue limit ignored: %+v", r)
	}
	cancelWaiting()
	if r = <-waiting; !errors.Is(r.Err, context.Canceled) {
		t.Fatal(r.Err)
	}
	close(release)
	for range 2 {
		if r = <-done; r.Err != nil {
			t.Fatal(r.Err)
		}
	}
}

func TestCancelledQueueClearsBackingArrayReferences(t *testing.T) {
	q, _ := New([]Instance{{ID: "model", Slots: 1}}, 8)
	if err := q.RestoreHeld("model", 1); err != nil {
		t.Fatal(err)
	}
	cancels := []context.CancelFunc{}
	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		data := make([]byte, 8<<20)
		go func() {
			q.Submit(ctx, "", func(context.Context, string) Result { return Result{Value: data} })
			done <- struct{}{}
		}()
		deadline := time.Now().Add(time.Second)
		for q.Stats().Waiting != i+1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if q.Stats().Waiting != i+1 {
			t.Fatal("request did not queue")
		}
	}
	for i := len(cancels) - 1; i >= 0; i-- {
		cancels[i]()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cancel did not return")
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.waiting) != 0 {
		t.Fatal("queue not empty")
	}
	for _, pending := range q.waiting[:cap(q.waiting)] {
		if pending != nil {
			t.Fatal("removed callback retained in backing array")
		}
	}
}
