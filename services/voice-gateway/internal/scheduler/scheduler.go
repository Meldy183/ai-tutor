// Package scheduler assigns waiting operations to compatible model slots.
package scheduler

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

var ErrQueueFull = errors.New("voice queue is full")
var ErrNoCompatibleInstance = errors.New("no compatible voice instance")

// ErrInsufficientBudget reports a job refused before its call was sent.
var ErrInsufficientBudget = errors.New("not enough time left for a voice call")

type Instance struct {
	ID       string
	Profiles []string
	Slots    int
}

type Result struct {
	Value any
	Err   error
	// HoldSlot leaves capacity reserved when an upstream may still be running.
	HoldSlot bool
}

type job struct {
	ctx     context.Context
	profile string
	call    func(context.Context, string) Result
	result  chan Result
}

type poolInstance struct {
	Instance
	busy int
}

// Queue owns one operation's waiting jobs and slots. Use distinct Queue values
// for STT and TTS to keep their capacity and waiting work independent.
type Queue struct {
	mu        sync.Mutex
	instances []poolInstance
	waiting   []*job
	limit     int
	next      int
	minBudget time.Duration
}

// RequireBudget refuses jobs with less than d left before their deadline. A
// call cut off by its deadline after sending would hold the slot.
func (q *Queue) RequireBudget(d time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.minBudget = d
}

// HasBudget reports whether ctx still leaves the required time for a call.
func (q *Queue) HasBudget(ctx context.Context) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.hasBudgetLocked(ctx)
}

func (q *Queue) hasBudgetLocked(ctx context.Context) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) >= q.minBudget
}

func New(instances []Instance, maxWaiting int) (*Queue, error) {
	if len(instances) == 0 || maxWaiting < 1 {
		return nil, errors.New("instances and positive queue limit are required")
	}
	q := &Queue{limit: maxWaiting}
	ids := make(map[string]bool)
	for _, instance := range instances {
		if instance.ID == "" || instance.Slots < 1 || ids[instance.ID] {
			return nil, errors.New("invalid or duplicate instance")
		}
		ids[instance.ID] = true
		instance.Profiles = append([]string(nil), instance.Profiles...)
		q.instances = append(q.instances, poolInstance{Instance: instance})
	}
	return q, nil
}

func compatible(i *poolInstance, profile string) bool {
	if profile == "" {
		return true
	}
	for _, p := range i.Profiles {
		if p == profile {
			return true
		}
	}
	return false
}

// Submit waits for a compatible slot and executes call exactly once. The call
// must not return before the upstream slot is known to be free.
func (q *Queue) Submit(ctx context.Context, profile string, call func(context.Context, string) Result) Result {
	if err := ctx.Err(); err != nil {
		return Result{Err: err}
	}
	if call == nil {
		return Result{Err: errors.New("missing call")}
	}
	j := &job{ctx: ctx, profile: profile, call: call, result: make(chan Result, 1)}
	q.mu.Lock()
	found := false
	for i := range q.instances {
		if compatible(&q.instances[i], profile) {
			found = true
			break
		}
	}
	if !found {
		q.mu.Unlock()
		return Result{Err: ErrNoCompatibleInstance}
	}
	q.dispatchLocked()
	if len(q.waiting) >= q.limit {
		q.mu.Unlock()
		return Result{Err: ErrQueueFull}
	}
	q.waiting = append(q.waiting, j)
	q.dispatchLocked()
	q.mu.Unlock()
	select {
	case result := <-j.result:
		return result
	case <-ctx.Done():
		q.mu.Lock()
		for i, pending := range q.waiting {
			if pending == j {
				q.waiting = slices.Delete(q.waiting, i, i+1)
				break
			}
		}
		q.dispatchLocked()
		q.mu.Unlock()
		return Result{Err: ctx.Err()}
	}
}

func (q *Queue) dispatchLocked() {
	for j := 0; j < len(q.waiting); {
		pending := q.waiting[j]
		if pending.ctx.Err() != nil {
			q.waiting = slices.Delete(q.waiting, j, j+1)
			continue
		}
		if !q.hasBudgetLocked(pending.ctx) {
			q.waiting = slices.Delete(q.waiting, j, j+1)
			pending.result <- Result{Err: ErrInsufficientBudget}
			continue
		}
		chosen := -1
		for offset := 0; offset < len(q.instances); offset++ {
			i := (q.next + offset) % len(q.instances)
			candidate := &q.instances[i]
			if !compatible(candidate, pending.profile) || candidate.busy >= candidate.Slots {
				continue
			}
			if chosen < 0 || candidate.busy*q.instances[chosen].Slots < q.instances[chosen].busy*candidate.Slots {
				chosen = i
			}
		}
		if chosen < 0 {
			j++
			continue
		}
		q.waiting = slices.Delete(q.waiting, j, j+1)
		q.instances[chosen].busy++
		q.next = (chosen + 1) % len(q.instances)
		id := q.instances[chosen].ID
		go q.execute(pending, chosen, id)
	}
}

func (q *Queue) execute(j *job, index int, id string) {
	result := j.call(j.ctx, id)
	q.mu.Lock()
	if !result.HoldSlot {
		q.instances[index].busy--
	}
	q.dispatchLocked()
	q.mu.Unlock()
	j.result <- result
}

// RestoreHeld reserves slots whose upstream execution was interrupted.
func (q *Queue) RestoreHeld(id string, count int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.instances {
		if q.instances[i].ID == id {
			if count < 0 || q.instances[i].busy+count > q.instances[i].Slots {
				return errors.New("restored slots exceed instance capacity")
			}
			q.instances[i].busy += count
			return nil
		}
	}
	return errors.New("held instance absent from configuration")
}

type InstanceStats struct {
	ID          string
	Busy, Slots int
}
type Stats struct {
	Waiting   int
	Instances []InstanceStats
}

func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := Stats{Waiting: len(q.waiting)}
	for _, in := range q.instances {
		s.Instances = append(s.Instances, InstanceStats{ID: in.ID, Busy: in.busy, Slots: in.Slots})
	}
	return s
}

// ReleaseHeld is called after an operator confirms upstream execution has
// stopped or after the reservation's hold timeout has passed.
func (q *Queue) ReleaseHeld(id string, count int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.instances {
		if q.instances[i].ID == id {
			if count < 0 || q.instances[i].busy < count {
				return errors.New("invalid held slot count")
			}
			q.instances[i].busy -= count
			q.dispatchLocked()
			return nil
		}
	}
	return errors.New("unknown instance")
}
