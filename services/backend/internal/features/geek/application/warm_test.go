package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/audiopreparation"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// countingVoice is safe for the warmer goroutine and reports each synthesis.
type countingVoice struct {
	mu     sync.Mutex
	calls  []string
	called chan struct{}
}

func (v *countingVoice) Synthesize(_ context.Context, text string) ([]byte, error) {
	v.mu.Lock()
	v.calls = append(v.calls, text)
	v.mu.Unlock()
	v.called <- struct{}{}
	return wav(10), nil
}

type syncAudio struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (a *syncAudio) Get(_ context.Context, key string) ([]byte, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, ok := a.data[key]
	return data, ok, nil
}

func (a *syncAudio) size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.data)
}

func (a *syncAudio) Put(_ context.Context, key string, data []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.data[key] = data
	return nil
}

func TestWarmerVoicesPreparedQuestionsOnceBeforeStudentsAsk(t *testing.T) {
	voice := &countingVoice{called: make(chan struct{}, 8)}
	store := &syncAudio{data: map[string][]byte{}}
	warmer := NewWarmer(NewSpeechCache(voice, store, staticVoiceName("steps=20"), time.Minute), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = warmer.Run(ctx); close(done) }()

	if n := warmer.Prepare([]string{" Почему A* оптимален? ", "", "Зачем эвристика?"}); n != 2 {
		t.Fatalf("queued %d", n)
	}
	<-voice.called
	<-voice.called
	// The signal comes from the synthesis; the WAV is cached right after it.
	for deadline := time.Now().Add(5 * time.Second); store.size() < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the warmed questions were not cached")
		}
	}
	// A question already in the cache is not synthesized again.
	warmer.Prepare([]string{"Почему A* оптимален?"})
	speech := NewSpeechCache(voice, store, staticVoiceName("steps=20"), time.Minute)
	if _, err := speech.Part(context.Background(), "Зачем эвристика?"); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	voice.mu.Lock()
	defer voice.mu.Unlock()
	if len(voice.calls) != 2 || voice.calls[0] != "Почему A* оптимален?" || voice.calls[1] != "Зачем эвристика?" {
		t.Fatalf("synthesized %q", voice.calls)
	}
}

func TestRepeatedTrackRestoredPreservesProgressForTheSameGraph(t *testing.T) {
	warmer := NewWarmer(NewSpeechCache(nil, nil, staticVoiceName("voice-v1"), time.Minute), nil)
	warmer.TrackRestored("course", "graph-v1", []string{"Вопрос?"})
	key := progressKey("Вопрос?")
	warmer.progress.TransitionSubject("course", key, audiopreparation.StatusReady)
	before, _ := warmer.Snapshot("course")

	warmer.TrackRestored("course", "graph-v1", []string{"Вопрос?"})
	after, _ := warmer.Snapshot("course")
	if after.Ready != 1 || after.ObservedAt != before.ObservedAt {
		t.Fatalf("repeat restore changed progress: before=%+v after=%+v", before, after)
	}
}

// flakyAudio fails reads until healthy is set, like a bucket that is not
// created yet.
type flakyAudio struct {
	syncAudio
	healthy atomic.Bool
	fails   atomic.Int32
}

func (a *flakyAudio) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if !a.healthy.Load() {
		a.fails.Add(1)
		return nil, false, errors.New("NoSuchBucket")
	}
	return a.syncAudio.Get(ctx, key)
}

func runWarmer(t *testing.T, w *Warmer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

func TestWarmerRetriesStoreFailuresUntilVoiced(t *testing.T) {
	voice := &countingVoice{called: make(chan struct{}, 8)}
	// The bucket is not created yet when the warmer starts.
	store := &flakyAudio{syncAudio: syncAudio{data: map[string][]byte{}}}
	core, logs := observer.New(zap.WarnLevel)
	warmer := NewWarmer(NewSpeechCache(voice, store, staticVoiceName("steps=20"), time.Minute), zap.New(core))
	warmer.retryMin, warmer.retryMax = time.Millisecond, 2*time.Millisecond
	runWarmer(t, warmer)

	warmer.Prepare([]string{"Первый?", "Второй?"})
	// Every failure is logged and retried, none lost.
	waitFor(t, "failures were not retried", func() bool { return store.fails.Load() >= 4 })
	if logs.FilterMessage("prepared question not voiced; retrying").Len() == 0 {
		t.Fatal("the failure was not logged")
	}
	if warmer.Pending() != 2 {
		t.Fatalf("pending = %d", warmer.Pending())
	}
	store.healthy.Store(true)
	waitFor(t, "the questions were not voiced after the store recovered", func() bool { return store.size() == 2 })
	waitFor(t, "the queue was not emptied", func() bool { return warmer.Pending() == 0 })
}

// failingVoice fails the first fail syntheses.
type failingVoice struct {
	countingVoice
	fail atomic.Int32
}

func (v *failingVoice) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if v.fail.Add(-1) >= 0 {
		return nil, errors.New("speech service is down")
	}
	return v.countingVoice.Synthesize(ctx, text)
}

func TestWarmerRetriesAFailedSynthesisBehindTheOtherTexts(t *testing.T) {
	voice := &failingVoice{countingVoice: countingVoice{called: make(chan struct{}, 8)}}
	voice.fail.Store(1)
	store := &syncAudio{data: map[string][]byte{}}
	warmer := NewWarmer(NewSpeechCache(voice, store, staticVoiceName("steps=20"), time.Minute), nil)
	warmer.retryMin, warmer.retryMax = time.Millisecond, time.Millisecond
	runWarmer(t, warmer)

	warmer.Prepare([]string{"Первый?", "Второй?"})
	<-voice.called
	<-voice.called
	voice.mu.Lock()
	defer voice.mu.Unlock()
	if !slices.Equal(voice.calls, []string{"Второй?", "Первый?"}) {
		t.Fatalf("synthesized %q", voice.calls)
	}
}

func TestWarmerQueuesAWaitingTextOnceAndHasNoQueueLimit(t *testing.T) {
	store := &syncAudio{data: map[string][]byte{}}
	// Not running: everything stays queued.
	warmer := NewWarmer(NewSpeechCache(&countingVoice{called: make(chan struct{}, 1)}, store, staticVoiceName("steps=20"), time.Minute), nil)
	texts := make([]string, 5000)
	for i := range texts {
		texts[i] = fmt.Sprintf("Вопрос %d?", i)
	}
	if n := warmer.Prepare(texts); n != len(texts) {
		t.Fatalf("queued %d", n)
	}
	if n := warmer.Prepare([]string{"Вопрос 1?", " Вопрос 2? ", "Вопрос 1?"}); n != 0 || warmer.Pending() != len(texts) {
		t.Fatalf("queued %d again, pending %d", n, warmer.Pending())
	}
}

// gatedVoice holds every synthesis until release is closed.
type gatedVoice struct {
	countingVoice
	release chan struct{}
}

func (v *gatedVoice) Synthesize(ctx context.Context, text string) ([]byte, error) {
	data, err := v.countingVoice.Synthesize(ctx, text)
	<-v.release
	return data, err
}

func TestConcurrentMissesOfOneQuestionSynthesizeOnce(t *testing.T) {
	voice := &gatedVoice{countingVoice: countingVoice{called: make(chan struct{}, 8)}, release: make(chan struct{})}
	speech := NewSpeechCache(voice, &syncAudio{data: map[string][]byte{}}, staticVoiceName("steps=20"), time.Minute)
	// The warmer starts the synthesis, then a student leaves while waiting
	// and another one asks for the same question.
	warmer := make(chan error, 1)
	go func() { _, err := speech.Part(context.Background(), "Зачем эвристика?"); warmer <- err }()
	<-voice.called
	leaving, leave := context.WithCancel(context.Background())
	left := make(chan error, 1)
	go func() { _, err := speech.Part(leaving, "Зачем эвристика?"); left <- err }()
	staying := make(chan error, 1)
	go func() { _, err := speech.Part(context.Background(), "Зачем эвристика?"); staying <- err }()
	leave()
	if err := <-left; err == nil {
		t.Fatal("the student who left still waited for the synthesis")
	}
	close(voice.release)
	if err := <-warmer; err != nil {
		t.Fatal(err)
	}
	if err := <-staying; err != nil {
		t.Fatal(err)
	}
	voice.mu.Lock()
	defer voice.mu.Unlock()
	if len(voice.calls) != 1 {
		t.Fatalf("the same question was synthesized %d times", len(voice.calls))
	}
}

func TestWarmerCompletionUpdatesNewlyPublishedGraphWhileTextPending(t *testing.T) {
	warmer := NewWarmer(NewSpeechCache(&countingVoice{called: make(chan struct{}, 1)}, &syncAudio{data: map[string][]byte{}}, staticVoiceName("voice"), time.Minute), nil)
	text := "Общий вопрос?"
	warmer.TrackPublished("course", "old", []string{text})
	warmer.Prepare([]string{text})
	// The cache operation has completed, but the pending entry has not yet been removed.
	warmer.progress.Transition(progressKey(text), audiopreparation.StatusReady)
	warmer.TrackPublished("course", "new", []string{text})
	warmer.Prepare([]string{text})
	warmer.done(text)
	snapshot, _ := warmer.Snapshot("course")
	if snapshot.Ready != 1 || snapshot.Unchecked != 0 {
		t.Fatalf("new graph lost completed audio: %+v", snapshot)
	}
}

func TestWarmerCountsNewlyPublishedWaitingTextsAsQueued(t *testing.T) {
	warmer := NewWarmer(NewSpeechCache(&countingVoice{called: make(chan struct{}, 1)}, &syncAudio{data: map[string][]byte{}}, staticVoiceName("voice"), time.Minute), nil)
	warmer.TrackPublished("course", "graph", []string{"Первый?", "Второй?"})
	warmer.Prepare([]string{"Первый?", "Второй?"})
	snapshot, _ := warmer.Snapshot("course")
	if snapshot.Queued != 2 || snapshot.Unchecked != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestWarmerRecoveryCompletionAndRestoredKeys(t *testing.T) {
	warmer := NewWarmer(NewSpeechCache(&countingVoice{called: make(chan struct{}, 1)}, &syncAudio{data: map[string][]byte{}}, staticVoiceName("voice"), time.Minute), nil)
	if warmer.RecoveryComplete() {
		t.Fatal("recovery must start incomplete")
	}
	warmer.TrackRestored("course", "graph", []string{"Вопрос?"})
	warmer.Prepare([]string{"Вопрос?"})
	snapshot, _ := warmer.Snapshot("course")
	if snapshot.Unchecked != 1 {
		t.Fatalf("restored key should await cache check: %+v", snapshot)
	}
	warmer.MarkRecoveryComplete()
	if !warmer.RecoveryComplete() {
		t.Fatal("recovery should be complete")
	}
}

func TestPublishedGraphInheritsPendingTextState(t *testing.T) {
	warmer := NewWarmer(NewSpeechCache(&countingVoice{called: make(chan struct{}, 1)}, &syncAudio{data: map[string][]byte{}}, staticVoiceName("voice"), time.Minute), nil)
	warmer.Prepare([]string{"Вопрос?"})
	warmer.transition("Вопрос?", audiopreparation.StatusProcessing)
	warmer.TrackPublished("course", "graph", []string{"Вопрос?"})
	snapshot, _ := warmer.Snapshot("course")
	if snapshot.Processing != 1 || snapshot.Queued != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	warmer.transition("Вопрос?", audiopreparation.StatusRetryWait)
	warmer.TrackPublished("course", "graph2", []string{"Вопрос?"})
	snapshot, _ = warmer.Snapshot("course")
	if snapshot.RetryWait != 1 || snapshot.Queued != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestWarmerVoiceIdentityRetriesLookup(t *testing.T) {
	calls := 0
	name := func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("voice unavailable")
		}
		return "profile:revision", nil
	}
	warmer := NewWarmer(NewSpeechCache(&countingVoice{called: make(chan struct{}, 1)}, &syncAudio{data: map[string][]byte{}}, name, time.Minute), nil)
	if _, err := warmer.VoiceName(context.Background()); err == nil {
		t.Fatal("expected voice lookup error")
	}
	got, err := warmer.VoiceName(context.Background())
	if err != nil || got != "profile:revision" {
		t.Fatalf("voice=%q err=%v", got, err)
	}
}
