package application

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/audioasset"
)

type fakeQueue struct {
	mu              sync.Mutex
	assets          []audioasset.Asset
	completed       []Claim
	completeDetails []struct{ bucket, uri, url string }
	failed          []struct {
		claim Claim
		code  string
		delay time.Duration
	}
	cancelled   []Claim
	ready       map[string]audioasset.Asset
	current     bool
	completeOK  bool
	completeErr error
	completeCh  chan struct{}
	failedCh    chan struct{}
}

func newFakeQueue(count int) *fakeQueue {
	assets := make([]audioasset.Asset, count)
	for i := range assets {
		id := fmt.Sprintf("asset-%02d", i)
		assets[i] = audioasset.Asset{ID: id, Instruction: "Скажите ответ", ObjectKey: "task-audio/v1/" + id + ".wav", Status: audioasset.Pending}
	}
	return &fakeQueue{assets: assets, completeOK: true, current: true, completeCh: make(chan struct{}, count), failedCh: make(chan struct{}, count)}
}
func (q *fakeQueue) Prioritize(context.Context, string) (bool, error) { return false, nil }
func (q *fakeQueue) FindReady(_ context.Context, instruction, excludeID string) (audioasset.Asset, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	asset, ok := q.ready[instruction]
	if !ok || asset.ID == excludeID {
		return audioasset.Asset{}, false, nil
	}
	return asset, true, nil
}
func (q *fakeQueue) IsCurrent(context.Context, Claim) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.current, nil
}
func (q *fakeQueue) Cancel(_ context.Context, claim Claim) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelled = append(q.cancelled, claim)
	return true, nil
}

func (q *fakeQueue) Claim(_ context.Context, token string, _ time.Duration) (Claim, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.assets) == 0 {
		return Claim{}, false, nil
	}
	asset := q.assets[0]
	q.assets = q.assets[1:]
	asset.Attempts++
	asset.Status = audioasset.Processing
	return Claim{Asset: asset, Token: token}, true, nil
}
func (q *fakeQueue) ClaimRepair(_ context.Context, assetID, token string, _ time.Duration) (Claim, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, asset := range q.assets {
		if asset.ID == assetID && asset.Status == audioasset.Ready {
			q.assets = append(q.assets[:i], q.assets[i+1:]...)
			asset.Attempts++
			asset.Status = audioasset.Processing
			return Claim{Asset: asset, Token: token}, true, nil
		}
	}
	return Claim{}, false, nil
}
func (q *fakeQueue) Complete(_ context.Context, claim Claim, bucket, uri, url string) (bool, error) {
	q.mu.Lock()
	q.completed = append(q.completed, claim)
	q.completeDetails = append(q.completeDetails, struct{ bucket, uri, url string }{bucket, uri, url})
	ok, err := q.completeOK, q.completeErr
	q.mu.Unlock()
	q.completeCh <- struct{}{}
	return ok, err
}
func (q *fakeQueue) Release(ctx context.Context, claim Claim) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	asset := claim.Asset
	asset.Status = audioasset.Pending
	asset.Attempts--
	q.assets = append(q.assets, asset)
	return true, nil
}
func (q *fakeQueue) Fail(_ context.Context, claim Claim, code string, delay time.Duration) (bool, error) {
	q.mu.Lock()
	q.failed = append(q.failed, struct {
		claim Claim
		code  string
		delay time.Duration
	}{claim, code, delay})
	q.mu.Unlock()
	q.failedCh <- struct{}{}
	return true, nil
}

type fakeStorage struct {
	mu         sync.Mutex
	ensure     int
	statInfo   ObjectInfo
	found      bool
	statErr    error
	statErrors []error
	statCalls  int
	afterStat  func()
	putCount   int
	putData    []byte
}

func (s *fakeStorage) EnsureBucket(context.Context) error {
	s.mu.Lock()
	s.ensure++
	s.mu.Unlock()
	return nil
}
func (s *fakeStorage) Stat(context.Context, string, string) (ObjectInfo, bool, error) {
	s.mu.Lock()
	var info ObjectInfo
	var found bool
	var err error
	if s.statCalls < len(s.statErrors) {
		err = s.statErrors[s.statCalls]
		s.statCalls++
		info, found = s.statInfo, s.found
	} else {
		s.statCalls++
		info, found, err = s.statInfo, s.found, s.statErr
	}
	afterStat := s.afterStat
	s.mu.Unlock()
	if afterStat != nil {
		afterStat()
	}
	return info, found, err
}
func (s *fakeStorage) Put(_ context.Context, _, _, _ string, data []byte) error {
	s.mu.Lock()
	s.putCount++
	s.putData = append([]byte(nil), data...)
	s.mu.Unlock()
	return nil
}
func (s *fakeStorage) Open(context.Context, string, string) (io.ReadCloser, ObjectInfo, error) {
	return io.NopCloser(nilReader{}), ObjectInfo{}, nil
}

type nilReader struct{}

func (nilReader) Read([]byte) (int, error) { return 0, io.EOF }

type fakeSynth struct {
	mu      sync.Mutex
	calls   int
	active  int
	max     int
	data    []byte
	err     error
	started chan struct{}
	release <-chan struct{}
}

func (s *fakeSynth) Synthesize(ctx context.Context, _ string) ([]byte, error) {
	s.mu.Lock()
	s.calls++
	s.active++
	if s.active > s.max {
		s.max = s.active
	}
	s.mu.Unlock()
	if s.started != nil {
		s.started <- struct{}{}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			s.mu.Lock()
			s.active--
			s.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	return append([]byte(nil), s.data...), s.err
}

type testWorkerConfig struct{ concurrency int }

func TestRunRequeuesInterruptedClaimBeforeLeaseExpires(t *testing.T) {
	queue := newFakeQueue(1)
	queue.assets[0].Attempts = 4
	synth := &fakeSynth{data: testWAV(), started: make(chan struct{}, 1), release: make(chan struct{})}
	config := WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Minute, S3Timeout: time.Second, Bucket: "task-audio"}
	worker := NewWorker(queue, &fakeStorage{}, synth, config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-synth.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start synthesis")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	if len(queue.assets) != 1 || queue.assets[0].Status != audioasset.Pending || queue.assets[0].Attempts != 4 {
		t.Fatalf("interrupted final attempt was not returned to the queue: %#v", queue.assets)
	}
	restarted := NewWorker(queue, &fakeStorage{}, &fakeSynth{data: testWAV()}, config)
	if processed, err := restarted.ProcessOne(context.Background()); !processed || err != nil || len(queue.completed) != 1 {
		t.Fatalf("restart did not finish the interrupted recording: processed=%v err=%v completed=%d", processed, err, len(queue.completed))
	}
}

type independentSynth func(context.Context, string) ([]byte, error)

func (s independentSynth) Synthesize(ctx context.Context, text string) ([]byte, error) {
	return s(ctx, text)
}

func TestRunCompletesReadyRecordingWhileAnotherIsStillSynthesizing(t *testing.T) {
	queue := newFakeQueue(2)
	queue.assets[0].Instruction = "slow"
	queue.assets[1].Instruction = "fast"
	slowStarted := make(chan struct{})
	synth := independentSynth(func(ctx context.Context, text string) ([]byte, error) {
		if text == "slow" {
			close(slowStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		<-slowStarted
		return testWAV(), nil
	})
	worker := NewWorker(queue, &fakeStorage{}, synth, WorkerConfig{Concurrency: 2, PollInterval: time.Millisecond, VoiceTimeout: 5 * time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-queue.completeCh:
		queue.mu.Lock()
		completed := append([]Claim(nil), queue.completed...)
		queue.mu.Unlock()
		if len(completed) != 1 || completed[0].Asset.ID != "asset-01" {
			t.Fatalf("independent completion = %#v", completed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ready recording waited for the slow recording")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestProcessOneSynthesizesOnceAndPersistsStableReferences(t *testing.T) {
	queue, storage, synth := newFakeQueue(1), &fakeStorage{}, &fakeSynth{data: testWAV()}
	worker := NewWorker(queue, storage, synth, WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne: processed=%v err=%v", processed, err)
	}
	if synth.calls != 1 || storage.putCount != 1 || len(queue.completed) != 1 {
		t.Fatalf("calls synth=%d put=%d complete=%d", synth.calls, storage.putCount, len(queue.completed))
	}
	claim := queue.completed[0]
	if claim.Asset.ID != "asset-00" {
		t.Fatalf("completed asset=%q", claim.Asset.ID)
	}
	details := queue.completeDetails[0]
	if details.bucket != "task-audio" || details.uri != "s3://task-audio/task-audio/v1/asset-00.wav" || details.url != "/learn/task-audio/asset-00/file" {
		t.Fatalf("persisted object references = %#v", details)
	}
}

func TestProcessOneRecoversExistingObjectWithoutSynthesis(t *testing.T) {
	queue := newFakeQueue(1)
	storage := &fakeStorage{found: true, statInfo: ObjectInfo{Size: int64(len(testWAV())), ContentType: "audio/wav", AssetID: "asset-00"}}
	synth := &fakeSynth{data: testWAV()}
	worker := NewWorker(queue, storage, synth, WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("ProcessOne: %v %v", processed, err)
	}
	if synth.calls != 0 || storage.putCount != 0 || len(queue.completed) != 1 {
		t.Fatalf("existing object caused work: synth=%d put=%d complete=%d", synth.calls, storage.putCount, len(queue.completed))
	}
}

func TestProcessOneRechecksCurrentTaskAfterHeadBeforeSynthesis(t *testing.T) {
	queue := newFakeQueue(1)
	storage := &fakeStorage{}
	storage.afterStat = func() {
		queue.mu.Lock()
		queue.current = false
		queue.mu.Unlock()
	}
	synth := &fakeSynth{data: testWAV()}
	worker := NewWorker(queue, storage, synth, WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne: processed=%v err=%v", processed, err)
	}
	synth.mu.Lock()
	calls := synth.calls
	synth.mu.Unlock()
	if calls != 0 {
		t.Fatalf("synthesized task after map changed between Claim and HEAD: calls=%d", calls)
	}
	if len(queue.cancelled) != 1 || queue.cancelled[0].Asset.ID != "asset-00" {
		t.Fatalf("stale claim was not cancelled: %#v", queue.cancelled)
	}
}

func TestProcessOneHeadFailureRetriesWithoutTTS(t *testing.T) {
	queue := newFakeQueue(1)
	storage := &fakeStorage{statErr: errors.New("S3 unavailable")}
	synth := &fakeSynth{data: testWAV()}
	worker := NewWorker(queue, storage, synth, WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	if _, err := worker.ProcessOne(context.Background()); err == nil {
		t.Fatal("HEAD failure was hidden")
	}
	if synth.calls != 0 || storage.putCount != 0 || len(queue.failed) != 1 {
		t.Fatalf("HEAD failure: synth=%d put=%d failed=%d", synth.calls, storage.putCount, len(queue.failed))
	}
}

func TestProcessOneInvalidWAVFailsWithoutUpload(t *testing.T) {
	queue, storage := newFakeQueue(1), &fakeStorage{}
	synth := &fakeSynth{data: []byte("not wav")}
	worker := NewWorker(queue, storage, synth, WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	if _, err := worker.ProcessOne(context.Background()); err == nil {
		t.Fatal("invalid WAV was accepted")
	}
	if storage.putCount != 0 || len(queue.failed) != 1 {
		t.Fatalf("invalid WAV uploaded=%d failed=%d", storage.putCount, len(queue.failed))
	}
}

func TestRetryDelayAndFifthAttemptExhaustion(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second, 4: 40 * time.Second, 5: 60 * time.Second, 9: 60 * time.Second} {
		if got := RetryDelay(attempt); got != want {
			t.Fatalf("RetryDelay(%d)=%s want %s", attempt, got, want)
		}
	}
	queue, storage := newFakeQueue(1), &fakeStorage{statErr: errors.New("HEAD error")}
	queue.assets[0].Attempts = 4
	worker := NewWorker(queue, storage, &fakeSynth{}, WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	_, _ = worker.ProcessOne(context.Background())
	if len(queue.failed) != 1 || queue.failed[0].claim.Asset.Attempts != 5 {
		t.Fatalf("fifth attempt failure = %#v", queue.failed)
	}
}

func TestRunBoundsConcurrentSynthesisAndStopsOnCancellation(t *testing.T) {
	queue := newFakeQueue(20)
	storage := &fakeStorage{}
	release := make(chan struct{})
	synth := &fakeSynth{data: testWAV(), started: make(chan struct{}, 20), release: release}
	worker := NewWorker(queue, storage, synth, WorkerConfig{Concurrency: 2, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	for i := 0; i < 20; i++ {
		select {
		case <-synth.started:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("worker did not claim bounded jobs")
		}
		release <- struct{}{}
	}
	for i := 0; i < 20; i++ {
		select {
		case <-queue.completeCh:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("worker did not complete all jobs")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not wait for worker lanes to stop")
	}
	if synth.max > 2 {
		t.Fatalf("maximum concurrent TTS calls = %d", synth.max)
	}
	if storage.ensure != 1 {
		t.Fatalf("EnsureBucket calls = %d", storage.ensure)
	}
}

func testWAV() []byte {
	data := make([]byte, 46)
	copy(data[:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], 38)
	copy(data[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 1)
	binary.LittleEndian.PutUint32(data[24:28], 16000)
	binary.LittleEndian.PutUint32(data[28:32], 32000)
	binary.LittleEndian.PutUint16(data[32:34], 2)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], 2)
	return data
}

func TestRunContinuesAfterOneJobFailure(t *testing.T) {
	queue := newFakeQueue(2)
	storage := &fakeStorage{statErrors: []error{errors.New("temporary storage error"), nil}}
	worker := NewWorker(queue, storage, &fakeSynth{data: testWAV()}, WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-queue.failedCh:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("worker did not record first job failure")
	}
	select {
	case <-queue.completeCh:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("worker stopped instead of processing the next job")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	if len(queue.failed) != 1 || len(queue.completed) != 1 {
		t.Fatalf("job course_map_outcomes failed=%d complete=%d", len(queue.failed), len(queue.completed))
	}
}

func TestProcessOneDoesNotTurnLostCompleteTokenIntoFailure(t *testing.T) {
	queue, storage := newFakeQueue(1), &fakeStorage{}
	queue.completeOK = false
	worker := NewWorker(queue, storage, &fakeSynth{data: testWAV()}, WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: "task-audio"})
	if processed, err := worker.ProcessOne(context.Background()); !processed || err != nil {
		t.Fatalf("lost completion token: processed=%v err=%v", processed, err)
	}
	if len(queue.failed) != 0 || len(queue.completed) != 1 {
		t.Fatalf("stale completion course_map_outcomes failed=%d complete=%d", len(queue.failed), len(queue.completed))
	}
}

type readyStorage struct {
	*fakeStorage
	data    []byte
	info    ObjectInfo
	openErr error
}

func (s *readyStorage) Open(context.Context, string, string) (io.ReadCloser, ObjectInfo, error) {
	if s.openErr != nil {
		return nil, ObjectInfo{}, s.openErr
	}
	return io.NopCloser(bytes.NewReader(s.data)), s.info, nil
}

func TestProcessOneCopiesReadyRecordingOfSameInstruction(t *testing.T) {
	bucket := "task-audio"
	wav := testWAV()
	for _, tc := range []struct {
		name      string
		openErr   error
		wantSynth int
	}{
		{name: "copies without TTS", wantSynth: 0},
		{name: "falls back to TTS when the copy fails", openErr: errors.New("s3 down"), wantSynth: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queue := newFakeQueue(1)
			queue.ready = map[string]audioasset.Asset{"Скажите ответ": {
				ID: "ready-1", Instruction: "Скажите ответ", ObjectKey: "task-audio/v1/ready-1.wav",
				Bucket: &bucket, Status: audioasset.Ready,
			}}
			storage := &readyStorage{fakeStorage: &fakeStorage{}, data: wav, openErr: tc.openErr,
				info: ObjectInfo{Size: int64(len(wav)), ContentType: "audio/wav", AssetID: "ready-1"}}
			synth := &fakeSynth{data: testWAV()}
			worker := NewWorker(queue, storage, synth, WorkerConfig{Concurrency: 1, PollInterval: time.Millisecond, VoiceTimeout: time.Second, S3Timeout: time.Second, Bucket: bucket})
			if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
				t.Fatalf("ProcessOne: processed=%v err=%v", processed, err)
			}
			if synth.calls != tc.wantSynth || storage.putCount != 1 || !bytes.Equal(storage.putData, wav) || len(queue.completed) != 1 {
				t.Fatalf("synth=%d put=%d same=%v completed=%d", synth.calls, storage.putCount, bytes.Equal(storage.putData, wav), len(queue.completed))
			}
			if queue.completed[0].Asset.ID != "asset-00" {
				t.Fatalf("copy completed the wrong asset: %q", queue.completed[0].Asset.ID)
			}
		})
	}
}
