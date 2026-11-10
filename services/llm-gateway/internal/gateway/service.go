package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type durableRequest struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	ClientID       string    `json:"client_id"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	Hash           string    `json:"hash"`
	Request        Request   `json:"request"`
	SubmittedAt    time.Time `json:"submitted_at"`
	Deadline       time.Time `json:"deadline"`
}
type durableJob struct {
	Version        int             `json:"version"`
	ID             string          `json:"id"`
	ClientID       string          `json:"client_id"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Hash           string          `json:"hash"`
	Request        Request         `json:"request"`
	SubmittedAt    time.Time       `json:"submitted_at"`
	Deadline       time.Time       `json:"deadline"`
	NotBefore      time.Time       `json:"not_before,omitempty"`
	StartedAt      time.Time       `json:"started_at,omitempty"`
	FinishedAt     time.Time       `json:"finished_at,omitempty"`
	Offset         int64           `json:"offset"`
	Status         string          `json:"status"`
	Attempt        int             `json:"attempt"`
	Instance       string          `json:"instance,omitempty"`
	SlotHeld       bool            `json:"slot_held,omitempty"`
	ProbeReserved  bool            `json:"-"`
	Response       json.RawMessage `json:"response,omitempty"`
	Error          *storedError    `json:"error,omitempty"`
}
type storedError struct {
	Status     int    `json:"status"`
	Type       string `json:"type"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	RetryAfter int    `json:"retry_after,omitempty"`
}
type submitResult struct {
	status int
	body   []byte
}

type routeSelection struct {
	complexity string
	instance   string
}

type Service struct {
	cfg               Config
	submitter         *kgo.Client
	stateWriter       *kgo.Client
	ingest            *kgo.GroupTransactSession
	caller            *Caller
	mu                sync.Mutex
	stateMu           sync.Mutex
	workers           sync.WaitGroup
	jobs              map[string]*durableJob
	keys              map[string]string
	accepted          map[string]*durableRequest
	stateIDs          map[string]string
	waiters           map[string]chan struct{}
	activeWaiters     map[string]int
	transportCalls    map[string]map[int]uint64
	upstreamTimeouts  map[string]uint64
	routeSelections   map[routeSelection]uint64
	cancelled         map[string]bool
	jobCancels        map[string]context.CancelFunc
	ephemeral         map[string]submitResult
	inFlight          map[string]int
	reserved          int
	reservedBytes     int64
	bytes             int64
	connections       chan struct{}
	wake              chan struct{}
	roundRobin        map[string]int
	expected          map[string]float64   // moving average of each instance's upstream call time (spec, section 5)
	cooling           map[string]time.Time // until when an instance that failed goes after healthy ones
	ready             bool
	brokerAvailable   bool
	stateUncertain    bool
	repairs           map[string]*stateRepair
	writeStateFn      func(ctx context.Context, key, value []byte) error
	stopping          bool
	started           time.Time
	idempotentReplays uint64
}

func NewService(cfg Config) (*Service, error) {
	cfg.defaults()
	s := &Service{cfg: cfg, jobs: map[string]*durableJob{}, keys: map[string]string{}, accepted: map[string]*durableRequest{}, stateIDs: map[string]string{}, waiters: map[string]chan struct{}{}, activeWaiters: map[string]int{}, cancelled: map[string]bool{}, jobCancels: map[string]context.CancelFunc{}, ephemeral: map[string]submitResult{}, inFlight: map[string]int{}, expected: map[string]float64{}, routeSelections: map[routeSelection]uint64{}, connections: make(chan struct{}, cfg.MaxConnections), wake: make(chan struct{}, 1), roundRobin: map[string]int{}, started: time.Now()}
	brokers := cfg.KafkaBrokers
	var err error
	s.submitter, err = kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RequiredAcks(kgo.AllISRAcks()), kgo.RecordRetries(3), kgo.ProducerLinger(5*time.Millisecond), kgo.MaxBufferedRecords(cfg.MaxConnections), kgo.ProducerBatchMaxBytes(2<<20))
	if err != nil {
		return nil, fmt.Errorf("create request producer: %w", err)
	}
	s.stateWriter, err = kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.TransactionalID(cfg.TransactionalID+"-state"), kgo.RequiredAcks(kgo.AllISRAcks()), kgo.RecordRetries(3), kgo.ProducerBatchMaxBytes(40<<20))
	if err != nil {
		s.submitter.Close()
		return nil, fmt.Errorf("create state producer: %w", err)
	}
	s.ingest, err = kgo.NewGroupTransactSession(kgo.SeedBrokers(brokers...), kgo.TransactionalID(cfg.TransactionalID+"-ingest"), kgo.ConsumerGroup(cfg.ConsumerGroup), kgo.ConsumeTopics(cfg.RequestsTopic), kgo.ConsumeStartOffset(kgo.NewOffset().AtStart()), kgo.ConsumeResetOffset(kgo.NoResetOffset()), kgo.FetchIsolationLevel(kgo.ReadCommitted()), kgo.DisableAutoCommit(), kgo.RequiredAcks(kgo.AllISRAcks()), kgo.ProducerBatchMaxBytes(40<<20))
	if err != nil {
		s.submitter.Close()
		s.stateWriter.Close()
		return nil, fmt.Errorf("create request consumer: %w", err)
	}
	s.caller = NewCaller(cfg)
	return s, nil
}

func (s *Service) Close() {
	s.mu.Lock()
	s.stopping = true
	s.ready = false
	s.mu.Unlock()
	s.ingest.Close()
	s.stateWriter.Close()
	s.submitter.Close()
}

func (s *Service) Run(ctx context.Context) error {
	if err := s.restore(ctx); err != nil {
		return err
	}
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	go s.monitorReadiness(monitorCtx)
	s.mu.Lock()
	s.brokerAvailable = true
	s.ready = true
	s.mu.Unlock()
	workCtx, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	consumeDone := make(chan struct{})
	go func() { defer close(consumeDone); s.consume(ctx) }()
	dispatchDone := make(chan struct{})
	go func() { defer close(dispatchDone); s.dispatch(ctx, workCtx) }()
	go s.compactLoop(ctx)
	<-ctx.Done()
	s.mu.Lock()
	s.ready = false
	s.stopping = true
	s.mu.Unlock()
	<-dispatchDone
	<-consumeDone
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(s.cfg.GracePeriod):
		cancelWork()
		<-done
	}
	return nil
}

func (s *Service) monitorReadiness(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			err := s.submitter.Ping(pingCtx)
			cancel()
			s.readinessTick(ctx, err)
		}
	}
}

// readinessTick applies one broker check. A reachable broker only lets the
// gateway try to re-save the state it could not write; readiness returns when
// those writes have succeeded, not when the broker answers a ping.
func (s *Service) readinessTick(ctx context.Context, pingErr error) {
	if pingErr == nil {
		s.reconcileState(ctx)
	}
	s.mu.Lock()
	s.brokerAvailable = pingErr == nil
	lagged := false
	for _, request := range s.accepted {
		if time.Since(request.SubmittedAt) > s.cfg.MaxInputLag {
			lagged = true
			break
		}
	}
	if !s.stopping {
		s.ready = pingErr == nil && !lagged && !s.stateUncertain
	}
	s.mu.Unlock()
	if pingErr == nil {
		s.signal()
	}
}

// stateRepair is a state write that failed, so that the durable value of its
// key is either the previous one or the intended one. The gateway stays
// unassigned until every repair is written again, which settles the key on the
// intended value; a newer outcome already stored for the job wins (save refuses
// to overwrite it).
type stateRepair struct {
	place string
	job   durableJob
	// The slot of the job is released once, when the repair is resolved: after
	// the write succeeded, or after another outcome of the job turned out to be
	// stored. False where the failed path had already released it.
	releaseOnSaved      bool
	releaseOnSuperseded bool
}

// registerStateRepair blocks readiness and assignments until the intended
// state of the job is written again by reconcileState. notice, when set, is the
// temporary answer for the waiting clients; it is published under the same lock
// as the repair, so a repair that completes right away cannot be followed by a
// stale notice.
func (s *Service) registerStateRepair(place string, j *durableJob, releaseOnSaved, releaseOnSuperseded bool, notice *submitResult, cause error) {
	r := &stateRepair{place: place, job: *j, releaseOnSaved: releaseOnSaved, releaseOnSuperseded: releaseOnSuperseded}
	r.job.ProbeReserved = false
	s.mu.Lock()
	if s.repairs == nil {
		s.repairs = map[string]*stateRepair{}
	}
	old := s.repairs[j.ID]
	// A decided cancellation is the outcome of the job: a repair of another
	// outcome (for instance the deadline, which failed in the same moment) must
	// not replace it.
	if !isCancellationJob(&r.job) && (old != nil && isCancellationJob(&old.job) || s.cancelDecidedLocked(j.ID)) {
		if old != nil && isCancellationJob(&old.job) {
			r.job = old.job
		} else {
			r.job = cancellationJob(r.job)
		}
		r.place = "cancellation"
		r.releaseOnSaved = r.releaseOnSaved || releaseOnSuperseded && !r.job.SlotHeld
	}
	if old != nil {
		// A later failure for the same job replaces the intended state but keeps
		// the slot obligation of the earlier one.
		r.releaseOnSaved = r.releaseOnSaved || old.releaseOnSaved || old.releaseOnSuperseded && !r.job.SlotHeld
		r.releaseOnSuperseded = r.releaseOnSuperseded || old.releaseOnSuperseded
	}
	s.repairs[j.ID] = r
	if notice != nil {
		s.ephemeral[j.ID] = *notice
		if ch := s.waiters[j.ID]; ch != nil {
			close(ch)
			delete(s.waiters, j.ID)
		}
	}
	s.ready = false
	s.brokerAvailable = false
	s.stateUncertain = true
	pending := len(s.repairs)
	s.mu.Unlock()
	slog.Error("state write failed, assignments stay blocked until it is saved again", "place", place, "gateway_request_id", j.ID, "pending_repairs", pending, "error", cause)
}

func isCancellationJob(j *durableJob) bool {
	return j.Status == "failed" && j.Error != nil && j.Error.Code == "request_canceled"
}

// cancellationJob is the stored outcome of a canceled request.
func cancellationJob(j durableJob) durableJob {
	j.Status = "failed"
	j.SlotHeld = false
	j.FinishedAt = time.Now()
	j.Response = nil
	j.Error = &storedError{Status: 409, Type: "request_cancelled", Code: "request_canceled", Message: "The request was canceled because no clients are waiting."}
	return j
}

// cancelDecidedLocked reports a cancellation of a job that is still pending (or
// not yet known), so that its outcome is not stored yet. s.mu is held.
func (s *Service) cancelDecidedLocked(id string) bool {
	if !s.cancelled[id] {
		return false
	}
	j := s.jobs[id]
	return j == nil || isPending(j.Status)
}

// reconcileState writes the intended state of every failed write again. It
// stops at the first failure: the broker is then most likely still unusable.
// The uncertainty ends only when no repair is left; a failure that registered
// itself meanwhile keeps it.
func (s *Service) reconcileState(ctx context.Context) {
	s.mu.Lock()
	pending := make([]*stateRepair, 0, len(s.repairs))
	for _, r := range s.repairs {
		pending = append(pending, r)
	}
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	sort.Slice(pending, func(a, b int) bool { return pending[a].job.ID < pending[b].job.ID })
	repaired := 0
	for _, r := range pending {
		if ctx.Err() != nil {
			return
		}
		job := r.job
		saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.save(saveCtx, &job)
		cancel()
		switch {
		case err == nil:
			s.completeStateRepair(r, true)
		case errors.Is(err, errCanceledJob):
			s.completeStateRepair(r, false)
		default:
			slog.Warn("state repair failed, will retry", "place", r.place, "gateway_request_id", r.job.ID, "error", err)
			return
		}
		repaired++
	}
	s.mu.Lock()
	cleared := len(s.repairs) == 0 && s.stateUncertain
	if cleared {
		s.stateUncertain = false
	}
	s.mu.Unlock()
	if cleared {
		slog.Info("state is consistent again, assignments resume", "repaired", repaired)
	}
}

// completeStateRepair finishes a repair whose write landed (saved) or was
// refused because another outcome of the job is stored.
func (s *Service) completeStateRepair(r *stateRepair, saved bool) {
	id := r.job.ID
	s.mu.Lock()
	if s.repairs[id] != r {
		// Replaced meanwhile: the newer repair carries the obligations.
		s.mu.Unlock()
		return
	}
	if !saved && !isCancellationJob(&r.job) && s.cancelDecidedLocked(id) {
		// Another outcome was refused because of a cancellation that is not
		// stored yet: keep the cancellation as the thing to write.
		c := &stateRepair{place: "cancellation", job: cancellationJob(r.job), releaseOnSaved: r.releaseOnSuperseded, releaseOnSuperseded: r.releaseOnSuperseded}
		s.repairs[id] = c
		s.mu.Unlock()
		return
	}
	delete(s.repairs, id)
	// The 503 given to the clients that were waiting is no longer the outcome:
	// a retry with the same key reads the stored one.
	delete(s.ephemeral, id)
	if ch := s.waiters[id]; ch != nil {
		close(ch)
		delete(s.waiters, id)
	}
	s.mu.Unlock()
	release := r.releaseOnSuperseded
	if saved {
		release = r.releaseOnSaved
	}
	if release {
		job := r.job
		s.releaseSlot(&job)
		return
	}
	s.signal()
}

func (s *Service) restore(ctx context.Context) error {
	reader, err := kgo.NewClient(kgo.SeedBrokers(s.cfg.KafkaBrokers...), kgo.FetchIsolationLevel(kgo.ReadCommitted()), kgo.ConsumeResetOffset(kgo.NoResetOffset()), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{s.cfg.StateTopic: {0: kgo.NewOffset().AtStart()}}), kgo.FetchMaxWait(100*time.Millisecond))
	if err != nil {
		return fmt.Errorf("create state restore reader: %w", err)
	}
	defer reader.Close()
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	err = reader.Ping(pingCtx)
	pingCancel()
	if err != nil {
		return fmt.Errorf("connect to Redpanda before state recovery: %w", err)
	}
	barrierValue, err := s.publishRestoreBarrier(ctx)
	if err != nil {
		return fmt.Errorf("publish gateway state recovery barrier: %w", err)
	}
	for {
		c, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		fs := reader.PollFetches(c)
		cancel()
		for _, e := range fs.Errors() {
			if errors.Is(e.Err, context.DeadlineExceeded) || errors.Is(e.Err, context.Canceled) {
				continue
			}
			return fmt.Errorf("read gateway state: %w", e.Err)
		}
		barrierSeen := false
		for it := fs.RecordIter(); !it.Done(); {
			r := it.Next()
			if string(r.Key) == restoreBarrierKey {
				if isRestoreBarrier(r, barrierValue) {
					barrierSeen = true
					break
				}
				continue
			}
			if len(r.Value) == 0 {
				s.removeByStateKey(r.Key)
				continue
			}
			var j durableJob
			if err := json.Unmarshal(r.Value, &j); err != nil {
				return fmt.Errorf("decode gateway state: %w", err)
			}
			s.install(&j)
		}
		if barrierSeen {
			s.mu.Lock()
			var recoveries []*durableJob
			for _, j := range s.jobs {
				if j.Error != nil && j.Error.Code == "execution_interrupted" || j.Status == "queued" {
					copy := *j
					if copy.Status == "queued" {
						if !time.Now().Before(copy.Deadline) {
							copy.Status = "expired"
							copy.Error = &storedError{Status: 504, Type: "gateway_timeout", Code: "request_deadline_exceeded", Message: "LLM request deadline exceeded."}
						} else {
							copy.Status = "failed"
							copy.Error = &storedError{Status: 409, Type: "request_cancelled", Code: "client_disconnected", Message: "No client remains connected after gateway restart."}
						}
					}
					recoveries = append(recoveries, &copy)
				}
			}
			s.mu.Unlock()
			for _, job := range recoveries {
				if err := s.save(ctx, job); err != nil {
					return fmt.Errorf("persist recovered gateway state: %w", err)
				}
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

const restoreBarrierKey = "\x00llm-gateway-restore-barrier"

func isRestoreBarrier(record *kgo.Record, value string) bool {
	return record != nil && string(record.Key) == restoreBarrierKey && string(record.Value) == value
}

func (s *Service) publishRestoreBarrier(ctx context.Context) (string, error) {
	value := newID()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err := s.stateWriter.BeginTransaction(); err != nil {
		return "", err
	}
	result := s.stateWriter.ProduceSync(ctx, &kgo.Record{Topic: s.cfg.StateTopic, Key: []byte(restoreBarrierKey), Value: []byte(value)})
	if err := result.FirstErr(); err != nil {
		_ = s.stateWriter.EndTransaction(context.Background(), kgo.TryAbort)
		return "", err
	}
	if err := s.stateWriter.EndTransaction(ctx, kgo.TryCommit); err != nil {
		return "", err
	}
	return value, nil
}

func (s *Service) install(j *durableJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expiredForRetention(j, time.Now(), s.cfg.Retention) {
		s.forgetExpiredJobLocked(j.ID)
		return
	}
	if old := s.jobs[j.ID]; old != nil {
		if isPending(old.Status) {
			s.bytes -= int64(len(mustJSON(old.Request)))
		}
		if old.SlotHeld && old.Instance != "" && s.inFlight[old.Instance] > 0 {
			s.inFlight[old.Instance]--
		}
	}
	copy := *j
	if copy.Status == "running" {
		copy.Status = "failed"
		copy.Error = &storedError{Status: 502, Type: "server_error", Code: "execution_interrupted", Message: "LLM execution was interrupted by gateway restart."}
		copy.SlotHeld = true
		copy.FinishedAt = time.Now()
	}
	if !isPending(copy.Status) && copy.SlotHeld && (s.upstreamAbortsOnDisconnect(copy.Instance) || s.uncertainSlotExpired(&copy, time.Now())) {
		// A verified disconnect-abort contract or server bound can release it.
		copy.SlotHeld = false
	}
	s.jobs[j.ID] = &copy
	s.stateIDs[string(stateKey(&copy))] = copy.ID
	if isPending(copy.Status) {
		s.bytes += int64(len(mustJSON(copy.Request)))
	}
	if accepted := s.accepted[j.ID]; accepted != nil {
		delete(s.accepted, j.ID)
		s.reserved--
		s.reservedBytes -= int64(len(mustJSON(accepted)))
	}
	if copy.IdempotencyKey != "" {
		s.keys[identity(copy.ClientID, copy.IdempotencyKey)] = copy.ID
	}
	if copy.SlotHeld && copy.Instance != "" {
		s.inFlight[copy.Instance]++
	}
}

// forgetExpiredJobLocked removes all process-local state for a key whose
// retention has elapsed, including transient cancellation state that could
// otherwise poison a new request reusing the same stable job ID.
func (s *Service) forgetExpiredJobLocked(id string) {
	if old := s.jobs[id]; old != nil {
		if isPending(old.Status) {
			s.bytes -= int64(len(mustJSON(old.Request)))
		}
		if old.SlotHeld && old.Instance != "" && s.inFlight[old.Instance] > 0 {
			s.inFlight[old.Instance]--
		}
		delete(s.jobs, id)
	}
	for key, jobID := range s.stateIDs {
		if jobID == id {
			delete(s.stateIDs, key)
		}
	}
	for key, jobID := range s.keys {
		if jobID == id {
			delete(s.keys, key)
		}
	}
	delete(s.cancelled, id)
}

func (s *Service) removeByStateKey(key []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.stateIDs[string(key)]
	if id == "" {
		return
	}
	j := s.jobs[id]
	if j != nil {
		if isPending(j.Status) {
			s.bytes -= int64(len(mustJSON(j.Request)))
		}
		if j.SlotHeld && j.Instance != "" && s.inFlight[j.Instance] > 0 {
			s.inFlight[j.Instance]--
		}
		if j.IdempotencyKey != "" {
			index := identity(j.ClientID, j.IdempotencyKey)
			if s.keys[index] == id {
				delete(s.keys, index)
			}
		}
		delete(s.jobs, id)
	}
	delete(s.stateIDs, string(key))
}

func (s *Service) consume(ctx context.Context) {
	defer s.ingest.Close()
	for ctx.Err() == nil {
		c, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		fs := s.ingest.PollRecords(c, 1)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if fs.Empty() {
			continue
		}
		errs := fs.Errors()
		if len(errs) > 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		it := fs.RecordIter()
		if it.Done() {
			continue
		}
		record := it.Next()
		var request durableRequest
		if json.Unmarshal(record.Value, &request) != nil {
			continue
		}
		s.mu.Lock()
		duplicate := s.cancelled[request.ID]
		if request.IdempotencyKey != "" {
			if existingID, ok := s.keys[identity(request.ClientID, request.IdempotencyKey)]; ok && existingID != request.ID {
				duplicate = true
			}
		}
		if _, ok := s.jobs[request.ID]; ok {
			duplicate = true
		}
		orphaned := !duplicate && (s.accepted[request.ID] == nil || s.activeWaiters[request.ID] == 0)
		canceled := s.cancelled[request.ID]
		s.mu.Unlock()
		s.stateMu.Lock()
		if err := s.ingest.Begin(); err != nil {
			s.stateMu.Unlock()
			time.Sleep(time.Second)
			continue
		}
		var job *durableJob
		if !duplicate {
			job = &durableJob{Version: 1, ID: request.ID, ClientID: request.ClientID, IdempotencyKey: request.IdempotencyKey, Hash: request.Hash, Request: request.Request, SubmittedAt: request.SubmittedAt, Deadline: request.Deadline, Offset: record.Offset, Status: "queued"}
			if orphaned {
				code, message := "client_disconnected", "No client remains connected to receive this request."
				if canceled {
					code, message = "request_canceled", "The request was canceled because no clients are waiting."
				}
				job.Status = "failed"
				job.FinishedAt = time.Now()
				job.Error = &storedError{Status: 409, Type: "request_cancelled", Code: code, Message: message}
			}
			raw, _ := json.Marshal(job)
			if got := s.ingest.ProduceSync(ctx, &kgo.Record{Topic: s.cfg.StateTopic, Key: stateKey(job), Value: raw}); got.FirstErr() != nil {
				_, _ = s.ingest.End(context.Background(), kgo.TryAbort)
				s.stateMu.Unlock()
				time.Sleep(time.Second)
				continue
			}
		}
		committed, err := s.ingest.End(context.Background(), kgo.TryCommit)
		if err != nil || !committed {
			s.stateMu.Unlock()
			continue
		}
		if job != nil {
			s.install(job)
			if job.Status == "queued" {
				s.signal()
			}
		}
		s.stateMu.Unlock()
	}
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) dispatch(ctx, workCtx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(time.Second):
		}
		for {
			s.releaseExpiredUncertainSlots(time.Now())
			s.expireQueued(time.Now())
			j, in, ok := s.next()
			if !ok {
				break
			}
			if time.Now().After(j.Deadline) {
				s.expireDispatched(j)
				continue
			}
			j.Status = "running"
			if j.StartedAt.IsZero() {
				j.StartedAt = time.Now()
			}
			j.Attempt++
			j.Instance = in.ID
			j.SlotHeld = true
			if err := s.save(ctx, j); err != nil {
				s.rollbackDispatch(j)
				break
			}
			if time.Now().After(j.Deadline) {
				s.expireDispatched(j)
				continue
			}
			jobCtx, jobCancel := context.WithCancel(workCtx)
			s.mu.Lock()
			s.jobCancels[j.ID] = jobCancel
			if s.cancelled[j.ID] {
				jobCancel()
			}
			s.mu.Unlock()
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer jobCancel()
				defer func() { s.mu.Lock(); delete(s.jobCancels, j.ID); s.mu.Unlock() }()
				s.execute(jobCtx, j, in)
			}()
		}
	}
}

// expireDispatched ends a job whose deadline passed after next() assigned it an
// instance and before its call started. The call never runs, so nothing else
// settles the half-open probe it may hold: release it here, with the slot.
func (s *Service) expireDispatched(j *durableJob) {
	if j.ProbeReserved {
		s.caller.circuit(j.Instance).releaseReservation()
		j.ProbeReserved = false
	}
	j.SlotHeld = false
	s.finish(j, submitResult{504, errorBody("gateway_timeout", "request_deadline_exceeded", "LLM request deadline exceeded.")}, "expired")
	s.releaseSlot(j)
}

func (s *Service) rollbackDispatch(j *durableJob) {
	if j.ProbeReserved {
		s.caller.circuit(j.Instance).releaseReservation()
	}
	s.mu.Lock()
	if current := s.jobs[j.ID]; current != nil && current.Status == "running" {
		current.Status = "queued"
		current.Instance = ""
		current.SlotHeld = false
	}
	if s.inFlight[j.Instance] > 0 {
		s.inFlight[j.Instance]--
	}
	s.mu.Unlock()
	s.signal()
}

func (s *Service) expireQueued(now time.Time) {
	s.mu.Lock()
	var expired []*durableJob
	for _, job := range s.jobs {
		if job.Status == "queued" && !now.Before(job.Deadline) && s.repairs[job.ID] == nil && !s.cancelled[job.ID] {
			job.Status = "expiring"
			copy := *job
			expired = append(expired, &copy)
		}
	}
	s.mu.Unlock()
	for _, job := range expired {
		if s.finish(job, submitResult{504, errorBody("gateway_timeout", "request_deadline_exceeded", "LLM request deadline exceeded.")}, "expired") == finishFailed {
			s.mu.Lock()
			if current := s.jobs[job.ID]; current != nil && current.Status == "expiring" {
				current.Status = "queued"
			}
			s.mu.Unlock()
		}
	}
}

func (s *Service) next() (*durableJob, InstanceConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, InstanceConfig{}, false
	}
	if !s.brokerAvailable || s.stateUncertain {
		return nil, InstanceConfig{}, false
	}
	var pending []*durableJob
	for _, j := range s.jobs {
		if j.Status == "queued" && !s.cancelled[j.ID] {
			pending = append(pending, j)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Offset < pending[j].Offset })
	for pass := 0; pass < 2; pass++ {
		ordered := pending
		if pass == 1 {
			ordered = append([]*durableJob(nil), pending...)
			sort.SliceStable(ordered, func(i, j int) bool {
				ri := complexityRank(ordered[i].Request.Complexity)
				rj := complexityRank(ordered[j].Request.Complexity)
				if ri != rj {
					return ri > rj
				}
				return ordered[i].Offset < ordered[j].Offset
			})
		}
		for _, job := range ordered {
			if time.Now().Before(job.NotBefore) {
				continue
			}
			if (pass == 0) != job.Request.IsUrgent() {
				continue
			}
			pool := s.eligibleInstances(job.Request.Complexity)
			var candidates []InstanceConfig
			for _, in := range pool {
				if s.inFlight[in.ID] >= in.MaxInFlight || !supports(in, job.Request) || !s.caller.circuit(in.ID).available(time.Now()) {
					continue
				}
				candidates = append(candidates, in)
			}
			if len(candidates) == 0 {
				continue
			}
			// The free instance that answers soonest: least loaded, then the
			// shortest expected call (an instance without samples first, so
			// every one is tried), then the less capable tier, then round-robin.
			// An instance cooling down after a failure goes after the healthy
			// ones, so a model that keeps answering 429 cannot keep the "not
			// measured yet" priority.
			now := time.Now()
			rank := func(in InstanceConfig) (bool, float64, float64, int) {
				return now.Before(s.cooling[in.ID]), float64(s.inFlight[in.ID]) / float64(in.MaxInFlight), s.expected[in.ID], s.instanceTier(in.ID)
			}
			sort.SliceStable(candidates, func(a, b int) bool {
				ca, ra, ea, ta := rank(candidates[a])
				cb, rb, eb, tb := rank(candidates[b])
				if ca != cb {
					return !ca
				}
				if ra != rb {
					return ra < rb
				}
				if ea != eb {
					return ea < eb
				}
				return ta < tb
			})
			bestCooling, bestRatio, bestExpected, bestTier := rank(candidates[0])
			ties := make([]InstanceConfig, 0, len(candidates))
			for _, candidate := range candidates {
				if c, r, e, t := rank(candidate); c == bestCooling && r == bestRatio && e == bestExpected && t == bestTier {
					ties = append(ties, candidate)
				}
			}
			cursor := s.roundRobin[job.Request.Complexity] % len(ties)
			var selected InstanceConfig
			probeReserved := false
			for n := 0; n < len(ties); n++ {
				candidate := ties[(cursor+n)%len(ties)]
				ticket, ok := s.caller.circuit(candidate.ID).reserve(time.Now())
				if ok {
					selected = candidate
					probeReserved = ticket.probe
					s.roundRobin[job.Request.Complexity] = (cursor + n + 1) % len(ties)
					break
				}
			}
			if selected.ID == "" {
				continue
			}
			s.inFlight[selected.ID]++
			if s.routeSelections == nil {
				s.routeSelections = make(map[routeSelection]uint64)
			}
			s.routeSelections[routeSelection{complexity: job.Request.Complexity, instance: selected.ID}]++
			job.Status = "running"
			job.Instance = selected.ID
			job.SlotHeld = true
			j := *job
			j.Instance = selected.ID
			j.SlotHeld = true
			j.ProbeReserved = probeReserved
			return &j, selected, true
		}
	}
	return nil, InstanceConfig{}, false
}

func (c *Circuit) available(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failures < 3 || !now.Before(c.openUntil) && !c.probing
}
func (c *Circuit) reserve(now time.Time) (circuitTicket, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failures < 3 {
		return circuitTicket{generation: c.generation}, true
	}
	if now.Before(c.openUntil) || c.probing {
		return circuitTicket{}, false
	}
	c.probing = true
	return circuitTicket{generation: c.generation, probe: true}, true
}
func (c *Circuit) releaseReservation() { c.mu.Lock(); c.probing = false; c.mu.Unlock() }
func (s *Service) instance(id string) InstanceConfig {
	for _, in := range s.cfg.Instances {
		if in.ID == id {
			return in
		}
	}
	return InstanceConfig{}
}

var complexityLevels = [...]string{"low", "medium", "high"}

func complexityRank(complexity string) int {
	for rank, level := range complexityLevels {
		if complexity == level {
			return rank
		}
	}
	return -1
}

func (s *Service) instanceTier(id string) int {
	tier := -1
	for complexity, instances := range s.cfg.Pools {
		for _, candidate := range instances {
			if candidate == id && complexityRank(complexity) > tier {
				tier = complexityRank(complexity)
			}
		}
	}
	return tier
}

func (s *Service) eligibleInstances(complexity string) []InstanceConfig {
	rank := complexityRank(complexity)
	if rank < 0 {
		return nil
	}
	seen := make(map[string]bool)
	var instances []InstanceConfig
	for _, level := range complexityLevels {
		if complexityRank(level) < rank {
			continue
		}
		for _, id := range s.cfg.Pools[level] {
			if seen[id] {
				continue
			}
			seen[id] = true
			if instance := s.instance(id); instance.ID != "" {
				instances = append(instances, instance)
			}
		}
	}
	return instances
}

func supports(in InstanceConfig, r Request) bool {
	if !supportsParameters(in, r) {
		return false
	}
	promptTokens := int64(0)
	for _, m := range r.Messages {
		promptTokens += int64(len(m.Content))/2 + 8
	}
	remaining := int64(in.ContextWindow) - promptTokens
	if remaining < 0 {
		return false
	}
	for _, name := range []string{"max_tokens", "max_completion_tokens"} {
		raw, ok := r.Parameters[name]
		if !ok {
			continue
		}
		var budget int64
		if err := json.Unmarshal(raw, &budget); err != nil || budget <= 0 || budget > remaining {
			return false
		}
		remaining -= budget
	}
	return true
}

func supportsParameters(in InstanceConfig, r Request) bool {
	supported := map[string]bool{}
	for _, p := range in.SupportedParameters {
		supported[p] = true
	}
	for p := range r.Parameters {
		if !supported[p] {
			return false
		}
		if p == "response_format" {
			var format struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(r.Parameters[p], &format) != nil {
				return false
			}
			known := false
			for _, value := range in.SupportedResponseFormats {
				if value == format.Type {
					known = true
					break
				}
			}
			if !known {
				return false
			}
		}
	}
	return true
}

func (s *Service) execute(ctx context.Context, j *durableJob, in InstanceConfig) {
	if time.Now().After(j.Deadline) {
		s.expireDispatched(j)
		return
	}
	callCtx, cancel := context.WithDeadline(ctx, j.Deadline)
	started := time.Now()
	result, failure := s.caller.call(callCtx, in, j.Request, j.ID, j.ProbeReserved)
	cancel()
	s.observeCall(j, in, failure, time.Since(started))
	s.recordUpstreamTimeout(failure)
	logUpstreamCall(j, in, failure, time.Since(started))
	if failure != nil {
		if s.isCanceled(j.ID) {
			s.releaseSlot(j)
			return
		}
		if j.Attempt < 2 && (failure.BeforeSend || failure.Status == 429) && time.Now().Add(failure.RetryAfter).Before(j.Deadline) {
			s.releaseSlot(j)
			j.Status = "queued"
			j.SlotHeld = false
			j.Instance = ""
			j.NotBefore = time.Now().Add(failure.RetryAfter)
			j.ProbeReserved = false
			if err := s.save(ctx, j); err == nil {
				s.signal()
			} else if !errors.Is(err, errCanceledJob) {
				// The durable state is still "running". Do not dispatch more work
				// while local state claims this retry is queued.
				s.registerStateRepair("reschedule", j, false, false, &submitResult{503, errorBody("server_error", "state_unavailable", "Gateway could not durably reschedule the LLM request.")}, err)
			}
			return
		}
		status, typ, code, msg := upstreamFailureResponse(failure)
		body := errorBody(typ, code, msg)
		uncertain := uncertainUpstreamOutcomeForInstance(in, failure)
		if !uncertain {
			j.SlotHeld = false
		}
		if err := s.persistResult(ctx, j, status, body, "failed", failure.RetryAfter); err == nil || errors.Is(err, errCanceledJob) {
			if !uncertain || errors.Is(err, errCanceledJob) {
				s.releaseSlot(j)
			}
		}
		return
	}
	if s.isCanceled(j.ID) {
		s.releaseSlot(j)
		return
	}
	if int64(len(result.Body)) > 32<<20 {
		j.SlotHeld = false
		if err := s.persistResult(ctx, j, 502, errorBody("server_error", "upstream_response_too_large", "The upstream response exceeded the gateway limit."), "failed"); err == nil || errors.Is(err, errCanceledJob) {
			s.releaseSlot(j)
		}
		return
	}
	j.SlotHeld = false
	if err := s.persistResult(ctx, j, 200, result.Body, "succeeded"); err == nil || errors.Is(err, errCanceledJob) {
		s.releaseSlot(j)
	}
}

// recordUpstreamTimeout counts a call ended by a timeout, by what ended it: the
// request deadline or the instance's own timeout. Every call counts, retries
// included. A request that expired in the queue made no call and is not counted.
func (s *Service) recordUpstreamTimeout(failure *UpstreamFailure) {
	reason := ""
	switch {
	case failure == nil || failure.Code == "request_canceled":
		return
	case failure.JobDeadline:
		reason = "request_deadline"
	case instanceTimedOut(failure):
		reason = "instance_timeout"
	default:
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upstreamTimeouts == nil {
		s.upstreamTimeouts = make(map[string]uint64)
	}
	s.upstreamTimeouts[reason]++
}

// upstreamFailureResponse maps a failed call to the stored error. The job
// deadline (which includes the queue wait) and the instance's own timeout are
// both 504, but only the second is a verdict on the instance.
func upstreamFailureResponse(failure *UpstreamFailure) (int, string, string, string) {
	switch {
	case failure.JobDeadline:
		return 504, "gateway_timeout", "request_deadline_exceeded", "LLM request deadline exceeded."
	case instanceTimedOut(failure):
		return 504, "gateway_timeout", "upstream_timeout", "The upstream LLM request timed out."
	case failure.Status == 429:
		return 429, "rate_limit_error", "upstream_rate_limit", "The upstream LLM rate limit was reached."
	}
	return 502, "server_error", failure.Code, "The upstream LLM request failed."
}

func logUpstreamCall(j *durableJob, in InstanceConfig, failure *UpstreamFailure, elapsed time.Duration) {
	attrs := []any{"gateway_request_id", j.ID, "instance", in.ID, "attempt", j.Attempt, "duration", elapsed.Seconds()}
	if failure == nil {
		slog.Info("upstream call", attrs...)
		return
	}
	attrs = append(attrs, "error_code", failure.Code, "upstream_status", failure.Status, "error", failure.Error())
	if failure.Code == "request_canceled" {
		slog.Info("upstream call canceled", attrs...)
		return
	}
	if failure.JobDeadline {
		slog.Info("upstream call cut by the request deadline", attrs...)
		return
	}
	slog.Warn("upstream call failed", attrs...)
}

// expectedAlpha weighs the newest call in an instance's expected duration.
const expectedAlpha = 0.3

// failureCooldown is how long a failed instance goes after healthy ones when
// the upstream gave no Retry-After; it matches the breaker pause.
const failureCooldown = 15 * time.Second

// observeCall learns from a finished call. A success and an upstream timeout
// are duration samples (a timeout marks a slow instance). A 429, a 5xx or a
// transport failure puts the instance on cooldown instead: they say nothing
// about speed, but without it an unmeasured instance that keeps failing would
// keep the first place. A cancelled request, a call cut by the request deadline
// (it includes the queue wait, not only the instance) and an open breaker
// change nothing.
func (s *Service) observeCall(j *durableJob, in InstanceConfig, failure *UpstreamFailure, elapsed time.Duration) {
	if s.isCanceled(j.ID) || failure != nil && (failure.Code == "request_canceled" || failure.JobDeadline || failure.Code == "upstream_circuit_open") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expected == nil {
		s.expected = make(map[string]float64)
	}
	if s.cooling == nil {
		s.cooling = make(map[string]time.Time)
	}
	sample := func() {
		seconds := elapsed.Seconds()
		if previous, ok := s.expected[in.ID]; ok {
			seconds = expectedAlpha*seconds + (1-expectedAlpha)*previous
		}
		s.expected[in.ID] = seconds
	}
	timedOut := instanceTimedOut(failure)
	switch {
	case failure == nil:
		sample()
		delete(s.cooling, in.ID)
	case failure.BeforeSend || failure.Status == 429 || failure.Status >= 500 || failure.Status == 0 && !timedOut:
		cooldown := failure.RetryAfter
		if cooldown <= 0 {
			cooldown = failureCooldown
		}
		s.cooling[in.ID] = time.Now().Add(cooldown)
	case timedOut:
		sample()
	}
}

// instanceTimedOut tells that the instance's own timeout ended the call: either
// the call recorded it as the cause, or the error is the context deadline. A
// call cut by the request deadline is not one, whatever its error says.
func instanceTimedOut(failure *UpstreamFailure) bool {
	if failure == nil || failure.JobDeadline {
		return false
	}
	return failure.InstanceTimeout || errors.Is(failure.Err, context.DeadlineExceeded)
}

func uncertainUpstreamOutcome(failure *UpstreamFailure) bool {
	if failure == nil {
		return false
	}
	return errors.Is(failure.Err, context.DeadlineExceeded) || failure.Code == "upstream_response_error" || (failure.Code == "upstream_transport_error" && !failure.BeforeSend)
}

func uncertainUpstreamOutcomeForInstance(in InstanceConfig, failure *UpstreamFailure) bool {
	if !uncertainUpstreamOutcome(failure) {
		return false
	}
	// Once the call has ended, its connection is closed, whoever closed it: the
	// gateway by the instance timeout or the job deadline, or the model by EOF,
	// a reset or a cut body. A model verified to stop generating on disconnect
	// is done with the request then; only trust this for explicitly verified
	// models.
	return !in.AbortOnDisconnect
}

func (s *Service) persistResult(ctx context.Context, j *durableJob, status int, body []byte, state string, retryAfter ...time.Duration) error {
	j.Status = state
	j.FinishedAt = time.Now()
	if status == 200 {
		j.Response = append([]byte(nil), body...)
	} else {
		var envelope ErrorEnvelope
		_ = json.Unmarshal(body, &envelope)
		j.Error = &storedError{Status: status, Type: envelope.Error.Type, Code: envelope.Error.Code, Message: envelope.Error.Message}
		if len(retryAfter) > 0 && retryAfter[0] > 0 {
			j.Error.RetryAfter = int(math.Ceil(retryAfter[0].Seconds()))
		}
	}
	for {
		if err := s.save(ctx, j); err == nil {
			break
		} else {
			if errors.Is(err, errCanceledJob) {
				return err
			}
			if time.Now().After(j.Deadline) || ctx.Err() != nil {
				// execute releases the slot after a result that was saved; the
				// repair does it after this one is. A slot kept on purpose for an
				// uncertain upstream outcome stays kept.
				s.registerStateRepair("result", j, !j.SlotHeld, true, &submitResult{503, errorBody("server_error", "state_unavailable", "Gateway could not durably save the LLM result.")}, err)
				return err
			}
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-time.After(time.Until(j.Deadline)):
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	s.mu.Lock()
	delete(s.ephemeral, j.ID)
	if ch := s.waiters[j.ID]; ch != nil {
		close(ch)
		delete(s.waiters, j.ID)
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) compactLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.compactExpired(ctx, now)
		}
	}
}

func (s *Service) compactExpired(ctx context.Context, now time.Time) {
	s.mu.Lock()
	var expired []*durableJob
	for _, j := range s.jobs {
		if expiredForRetention(j, now, s.cfg.Retention) {
			copy := *j
			expired = append(expired, &copy)
		}
	}
	s.mu.Unlock()
	for _, j := range expired {
		s.stateMu.Lock()
		s.mu.Lock()
		current := s.jobs[j.ID]
		stillExpired := current != nil && current.ID == j.ID && expiredForRetention(current, now, s.cfg.Retention)
		s.mu.Unlock()
		if !stillExpired {
			s.stateMu.Unlock()
			continue
		}
		if err := s.writeState(ctx, stateKey(j), nil); err != nil {
			s.stateMu.Unlock()
			continue
		}
		s.mu.Lock()
		if current := s.jobs[j.ID]; current != nil && expiredForRetention(current, now, s.cfg.Retention) {
			s.forgetExpiredJobLocked(j.ID)
		}
		s.mu.Unlock()
		s.stateMu.Unlock()
	}
}

// finishOutcome tells what became of the terminal state finish tried to store.
type finishOutcome int

const (
	finishSaved finishOutcome = iota
	// finishSuperseded: another outcome (a cancellation, stored by its own path
	// or its repair, or a terminal state already stored) decides the job.
	finishSuperseded
	// finishFailed: the write failed and a repair is registered.
	finishFailed
)

func (s *Service) finish(j *durableJob, result submitResult, state string) finishOutcome {
	j.Status = state
	j.FinishedAt = time.Now()
	var e ErrorEnvelope
	_ = json.Unmarshal(result.body, &e)
	j.Error = &storedError{Status: result.status, Type: e.Error.Type, Code: e.Error.Code, Message: e.Error.Message}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.save(ctx, j)
	switch {
	case err == nil:
		s.mu.Lock()
		if ch := s.waiters[j.ID]; ch != nil {
			close(ch)
			delete(s.waiters, j.ID)
		}
		s.mu.Unlock()
		return finishSaved
	case errors.Is(err, errCanceledJob):
		return finishSuperseded
	}
	// Every caller releases the slot itself, or holds none.
	j.SlotHeld = false
	s.registerStateRepair("finish", j, false, false, &submitResult{503, errorBody("server_error", "state_unavailable", "Gateway could not durably save the request state.")}, err)
	return finishFailed
}
func (s *Service) releaseSlot(j *durableJob) {
	if j.Instance == "" {
		return
	}
	s.mu.Lock()
	if s.inFlight[j.Instance] > 0 {
		s.inFlight[j.Instance]--
	}
	j.SlotHeld = false
	if saved := s.jobs[j.ID]; saved != nil {
		saved.SlotHeld = false
	}
	s.mu.Unlock()
	s.signal()
}

func (s *Service) uncertainSlotExpired(j *durableJob, now time.Time) bool {
	if j == nil || !j.SlotHeld || j.FinishedAt.IsZero() {
		return false
	}
	for _, in := range s.cfg.Instances {
		if in.ID == j.Instance {
			if in.ServerExecutionLimit > 0 {
				// Reserve a full server window after the last uncertain outcome. The
				// first attempt's StartedAt cannot bound a later retry or time in IO.
				return !now.Before(j.FinishedAt.Add(in.ServerExecutionLimit))
			}
			return false
		}
	}
	return false
}

func (s *Service) upstreamAbortsOnDisconnect(instanceID string) bool {
	for _, in := range s.cfg.Instances {
		if in.ID == instanceID {
			return in.AbortOnDisconnect
		}
	}
	return false
}

func (s *Service) releaseExpiredUncertainSlots(now time.Time) {
	s.mu.Lock()
	var expired []*durableJob
	for _, job := range s.jobs {
		if !isPending(job.Status) && s.uncertainSlotExpired(job, now) && s.repairs[job.ID] == nil {
			copy := *job
			copy.SlotHeld = false
			expired = append(expired, &copy)
		}
	}
	s.mu.Unlock()

	for _, job := range expired {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.save(ctx, job)
		cancel()
		if err != nil {
			if !errors.Is(err, errCanceledJob) {
				s.registerStateRepair("slot_release", job, true, true, nil, err)
			}
			continue
		}
		s.releaseSlot(job)
	}
}

// writeState puts one record into the state topic in its own transaction. The
// caller holds stateMu: the gateway is the only writer of the topic.
func (s *Service) writeState(ctx context.Context, key, value []byte) error {
	if s.writeStateFn != nil {
		return s.writeStateFn(ctx, key, value)
	}
	return s.transactState(ctx, key, value)
}

// transactState is the Redpanda write behind writeState. franz-go leaves the
// producer inside the transaction when EndTransaction fails (the outcome is
// unconfirmed) and refuses BeginTransaction until that transaction is aborted;
// without the abort every later write would fail even after the broker is back.
func (s *Service) transactState(ctx context.Context, key, value []byte) error {
	if err := s.stateWriter.BeginTransaction(); err != nil {
		if abortErr := s.abortStateTransaction(); abortErr != nil {
			return fmt.Errorf("begin state transaction: %w (abort of the previous one: %v)", err, abortErr)
		}
		if err = s.stateWriter.BeginTransaction(); err != nil {
			return err
		}
	}
	res := s.stateWriter.ProduceSync(ctx, &kgo.Record{Topic: s.cfg.StateTopic, Key: key, Value: value})
	if err := res.FirstErr(); err != nil {
		_ = s.abortStateTransaction()
		return err
	}
	if err := s.stateWriter.EndTransaction(ctx, kgo.TryCommit); err != nil {
		// The commit may or may not have happened. The abort retry reloads the
		// producer ID, which fences whatever is left ongoing in the broker.
		_ = s.abortStateTransaction()
		return err
	}
	return nil
}

// abortStateTransaction brings the state producer back outside a transaction.
// It is a no-op when none is open and fails, leaving the producer as it was,
// when the broker is unreachable; the next write tries again.
func (s *Service) abortStateTransaction() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.stateWriter.AbortBufferedRecords(ctx); err != nil {
		return err
	}
	return s.stateWriter.EndTransaction(ctx, kgo.TryAbort)
}

func (s *Service) save(ctx context.Context, j *durableJob) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.mu.Lock()
	current := s.jobs[j.ID]
	if s.cancelled[j.ID] && (j.Error == nil || j.Error.Code != "request_canceled") {
		s.mu.Unlock()
		return errCanceledJob
	}
	if current != nil && !isPending(current.Status) {
		currentCode, nextCode := "", ""
		if current.Error != nil {
			currentCode = current.Error.Code
		}
		if j.Error != nil {
			nextCode = j.Error.Code
		}
		if current.Status != j.Status || currentCode != nextCode {
			s.mu.Unlock()
			return errCanceledJob
		}
	}
	s.mu.Unlock()
	copy := *j
	raw, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	if err = s.writeState(ctx, stateKey(j), raw); err != nil {
		return err
	}
	s.mu.Lock()
	old := s.jobs[j.ID]
	if old == nil {
		if isPending(j.Status) {
			s.bytes += int64(len(mustJSON(j.Request)))
		}
	} else {
		if isPending(old.Status) {
			s.bytes -= int64(len(mustJSON(old.Request)))
		}
		if isPending(j.Status) {
			s.bytes += int64(len(mustJSON(j.Request)))
		}
	}
	if !isPending(j.Status) {
		if accepted := s.accepted[j.ID]; accepted != nil {
			delete(s.accepted, j.ID)
			s.reserved--
			s.reservedBytes -= int64(len(mustJSON(accepted)))
		}
	}
	saved := *j
	s.jobs[j.ID] = &saved
	s.stateIDs[string(stateKey(&saved))] = saved.ID
	if saved.IdempotencyKey != "" {
		s.keys[identity(saved.ClientID, saved.IdempotencyKey)] = saved.ID
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health/live" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.URL.Path == "/health/ready" && r.Method == http.MethodGet {
		s.mu.Lock()
		ready := s.ready && !s.stopping
		s.mu.Unlock()
		if !ready {
			http.Error(w, "not ready", 503)
			return
		}
		w.WriteHeader(200)
		return
	}
	if r.URL.Path == "/metrics" && r.Method == http.MethodGet {
		s.writeMetrics(w)
		return
	}
	if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
		writeError(w, 404, "invalid_request_error", "not_found", "Not found.")
		return
	}
	w.Header().Set("X-Request-ID", newID())
	select {
	case s.connections <- struct{}{}:
		defer func() { <-s.connections }()
	default:
		s.recordCall("http", 429)
		writeError(w, 429, "rate_limit_error", "connection_limit", "Too many concurrent requests.")
		return
	}
	if !s.Ready() {
		s.recordCall("http", 503)
		writeError(w, 503, "server_error", "gateway_not_ready", "Gateway is not ready.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	body, err := ioReadAll(r.Body)
	if err != nil {
		s.recordCall("http", 413)
		writeError(w, 413, "invalid_request_error", "request_too_large", "Request body is too large.")
		return
	}
	req, err := ParseRequest(body)
	if err != nil {
		s.recordCall("http", 400)
		writeStatusError(w, err)
		return
	}
	result, id, err := s.Complete(r.Context(), req, strings.TrimSpace(r.Header.Get("Idempotency-Key")), "http", true, func(id string) { w.Header().Set("X-Request-ID", id) })
	w.Header().Set("X-Request-ID", id)
	if err != nil {
		writeStatusError(w, err)
		return
	}
	if result.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(result.retryAfter))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.status)
	_, _ = w.Write(result.body)
}

func (s *Service) retryPublish(payload durableRequest, raw []byte, size int64) {
	ctx, cancel := context.WithDeadline(context.Background(), payload.Deadline)
	defer cancel()
	for {
		if ctx.Err() != nil || s.isCanceled(payload.ID) {
			break
		}
		res := s.submitter.ProduceSync(ctx, &kgo.Record{Topic: s.cfg.RequestsTopic, Key: []byte(payload.ID), Value: raw})
		if res.FirstErr() == nil {
			return
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if s.isCanceled(payload.ID) {
		return
	}
	s.mu.Lock()
	if _, stored := s.jobs[payload.ID]; !stored {
		if _, ok := s.accepted[payload.ID]; ok {
			delete(s.accepted, payload.ID)
			s.reserved--
			s.reservedBytes -= size
			if payload.IdempotencyKey != "" && s.keys[identity(payload.ClientID, payload.IdempotencyKey)] == payload.ID {
				delete(s.keys, identity(payload.ClientID, payload.IdempotencyKey))
			}
			delete(s.waiters, payload.ID)
		}
	}
	s.mu.Unlock()
}

func (s *Service) validatePool(r Request) error {
	parametersSupported := false
	for _, in := range s.eligibleInstances(r.Complexity) {
		if supportsParameters(in, r) {
			parametersSupported = true
		}
		if supports(in, r) {
			return nil
		}
	}
	if !parametersSupported {
		return apiErr(400, "invalid_request_error", "unsupported_parameter", "No instance in the compatible complexity pools supports all requested parameters.")
	}
	return apiErr(400, "invalid_request_error", "context_length_exceeded", "The request exceeds the context window of every compatible instance.")
}

func (s *Service) writeMetrics(rw http.ResponseWriter) {
	rw.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	// Под s.mu только копируем значения; запись клиенту идёт после разблокировки,
	// чтобы медленный читатель не останавливал приём и планировщик.
	s.mu.Lock()
	queued := map[string]int{}
	oldest := map[string]float64{}
	results := map[string]int{"succeeded": 0, "failed": 0, "expired": 0}
	retries := 0
	waits, executions := []float64{}, []float64{}
	inputBacklog := 0
	oldestInput := 0.0
	for _, request := range s.accepted {
		inputBacklog++
		age := time.Since(request.SubmittedAt).Seconds()
		if age > oldestInput {
			oldestInput = age
		}
	}
	for _, j := range s.jobs {
		if j.Status == "queued" {
			key := j.Request.Complexity + "/" + strconv.FormatBool(j.Request.IsUrgent())
			queued[key]++
			age := time.Since(j.SubmittedAt).Seconds()
			if age > oldest[key] {
				oldest[key] = age
			}
		}
		if _, ok := results[j.Status]; ok {
			results[j.Status]++
			retries += max(0, j.Attempt-1)
			if !j.StartedAt.IsZero() {
				waits = append(waits, j.StartedAt.Sub(j.SubmittedAt).Seconds())
				if !j.FinishedAt.IsZero() {
					executions = append(executions, j.FinishedAt.Sub(j.StartedAt).Seconds())
				}
			}
		}
	}
	inFlight := make(map[string]int, len(s.inFlight))
	for id, n := range s.inFlight {
		inFlight[id] = n
	}
	expected := make(map[string]float64, len(s.expected))
	for id, seconds := range s.expected {
		expected[id] = seconds
	}
	routeSelections := make(map[routeSelection]uint64, len(s.routeSelections))
	for key, n := range s.routeSelections {
		routeSelections[key] = n
	}
	transportCalls := make(map[string]map[int]uint64, len(s.transportCalls))
	for transport, calls := range s.transportCalls {
		copied := make(map[int]uint64, len(calls))
		for status, n := range calls {
			copied[status] = n
		}
		transportCalls[transport] = copied
	}
	upstreamTimeouts := make(map[string]uint64, len(s.upstreamTimeouts))
	for reason, n := range s.upstreamTimeouts {
		upstreamTimeouts[reason] = n
	}
	idempotentReplays := s.idempotentReplays
	connections := len(s.connections)
	s.mu.Unlock()

	var w bytes.Buffer
	fmt.Fprintln(&w, "# HELP gateway_queue_depth Requests waiting for an LLM slot.")
	fmt.Fprintln(&w, "# TYPE gateway_queue_depth gauge")
	for _, complexity := range []string{"low", "medium", "high"} {
		for _, urgent := range []string{"false", "true"} {
			key := complexity + "/" + urgent
			fmt.Fprintf(&w, "gateway_queue_depth{complexity=%q,urgent=%q} %d\n", complexity, urgent, queued[key])
			fmt.Fprintf(&w, "gateway_queue_oldest_seconds{complexity=%q,urgent=%q} %.3f\n", complexity, urgent, oldest[key])
		}
	}
	fmt.Fprintln(&w, "# TYPE gateway_instance_in_flight gauge")
	for _, in := range s.cfg.Instances {
		open := 0
		if !s.caller.circuit(in.ID).available(time.Now()) {
			open = 1
		}
		fmt.Fprintf(&w, "gateway_instance_in_flight{instance=%q} %d\ngateway_instance_capacity{instance=%q} %d\ngateway_circuit_open{instance=%q} %d\n", in.ID, inFlight[in.ID], in.ID, in.MaxInFlight, in.ID, open)
	}
	fmt.Fprintln(&w, "# HELP gateway_instance_expected_seconds Moving average of an instance's upstream call time; the scheduler prefers the free instance that answers sooner.")
	fmt.Fprintln(&w, "# TYPE gateway_instance_expected_seconds gauge")
	for _, in := range s.cfg.Instances {
		if seconds, ok := expected[in.ID]; ok {
			fmt.Fprintf(&w, "gateway_instance_expected_seconds{instance=%q} %.3f\n", in.ID, seconds)
		}
	}
	fmt.Fprintln(&w, "# HELP gateway_route_eligible Whether an instance can serve requests at the given complexity under the configured pools.")
	fmt.Fprintln(&w, "# TYPE gateway_route_eligible gauge")
	for _, complexity := range complexityLevels {
		for _, in := range s.eligibleInstances(complexity) {
			fmt.Fprintf(&w, "gateway_route_eligible{complexity=%q,instance=%q,model=%q,tier=%q} 1\n", complexity, in.ID, in.Model, complexityLevels[s.instanceTier(in.ID)])
		}
	}
	fmt.Fprintln(&w, "# HELP gateway_route_selections_total Requests assigned by the scheduler to an upstream instance, including retries.")
	fmt.Fprintln(&w, "# TYPE gateway_route_selections_total counter")
	for _, complexity := range complexityLevels {
		for _, in := range s.eligibleInstances(complexity) {
			key := routeSelection{complexity: complexity, instance: in.ID}
			fmt.Fprintf(&w, "gateway_route_selections_total{complexity=%q,instance=%q,model=%q,tier=%q} %d\n", complexity, in.ID, in.Model, complexityLevels[s.instanceTier(in.ID)], routeSelections[key])
		}
	}
	fmt.Fprintln(&w, "# TYPE gateway_calls_total counter")
	for _, transport := range []string{"http", "grpc"} {
		for status, count := range transportCalls[transport] {
			fmt.Fprintf(&w, "gateway_calls_total{transport=%q,status=%q} %d\n", transport, strconv.Itoa(status), count)
		}
	}
	fmt.Fprintln(&w, "# HELP gateway_upstream_timeouts_total Upstream calls ended by a timeout, by reason: the request deadline or the instance timeout. Retries count; a request that expired in the queue does not.")
	fmt.Fprintln(&w, "# TYPE gateway_upstream_timeouts_total counter")
	for _, reason := range []string{"request_deadline", "instance_timeout"} {
		fmt.Fprintf(&w, "gateway_upstream_timeouts_total{reason=%q} %d\n", reason, upstreamTimeouts[reason])
	}
	fmt.Fprintln(&w, "# TYPE gateway_requests_total counter")
	for status, count := range results {
		fmt.Fprintf(&w, "gateway_requests_total{status=%q} %d\n", status, count)
	}
	fmt.Fprintf(&w, "gateway_idempotent_replays_total %d\ngateway_retries_total %d\ngateway_connections_in_flight %d\ngateway_unregistered_requests %d\ngateway_input_lag_seconds %.3f\ngateway_uptime_seconds %.0f\n", idempotentReplays, retries, connections, inputBacklog, oldestInput, time.Since(s.started).Seconds())
	writeHistogram(&w, "gateway_queue_wait_seconds", waits)
	writeHistogram(&w, "gateway_execution_seconds", executions)
	_, _ = w.WriteTo(rw)
}

func writeHistogram(w io.Writer, name string, samples []float64) {
	buckets := []float64{1, 5, 10, 30, 60, 120, 180}
	sum := 0.0
	for _, sample := range samples {
		sum += sample
	}
	for _, bound := range buckets {
		count := 0
		for _, sample := range samples {
			if sample <= bound {
				count++
			}
		}
		fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, strconv.FormatFloat(bound, 'f', -1, 64), count)
	}
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %.3f\n%s_count %d\n", name, len(samples), name, sum, name, len(samples))
}
func writeError(w http.ResponseWriter, status int, typ, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: APIError{Message: message, Type: typ, Param: nil, Code: code}})
}
func writeStatusError(w http.ResponseWriter, err error) {
	var e statusError
	if errors.As(err, &e) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(e.status)
		_ = json.NewEncoder(w).Encode(e.body)
		return
	}
	writeError(w, 400, "invalid_request_error", "invalid_request", err.Error())
}
func errorBody(typ, code, msg string) []byte {
	b, _ := json.Marshal(ErrorEnvelope{Error: APIError{Message: msg, Type: typ, Code: code, Param: nil}})
	return b
}
func identity(client, key string) string { return client + "\x00" + key }
func isPending(status string) bool {
	return status == "queued" || status == "running" || status == "expiring"
}
func expiredForRetention(j *durableJob, now time.Time, retention time.Duration) bool {
	if j == nil || isPending(j.Status) || j.SlotHeld {
		return false
	}
	completedAt := j.FinishedAt
	if completedAt.IsZero() {
		completedAt = j.SubmittedAt
	}
	return !completedAt.IsZero() && now.Sub(completedAt) >= retention
}
func stateKey(j *durableJob) []byte {
	if j.IdempotencyKey == "" {
		return []byte(j.ID)
	}
	h := sha256.Sum256([]byte(identity(j.ClientID, j.IdempotencyKey)))
	return []byte(hex.EncodeToString(h[:]))
}
func stableJobID(client, key string) string {
	h := sha256.Sum256([]byte(identity(client, key)))
	return hex.EncodeToString(h[:16])
}
func newID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
func ioReadAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

func (s *Service) Handler() http.Handler { return logHTTP(http.HandlerFunc(s.handler)) }
