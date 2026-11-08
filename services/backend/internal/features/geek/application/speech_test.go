package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSplitSpeechKeepsPartsWithinTheLimitAtSentenceEnds(t *testing.T) {
	sentence := strings.Repeat("слово ", 15) + "конец. "
	text := strings.Repeat(sentence, 10)
	parts := SplitSpeech(text, 200)
	if len(parts) < 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	for _, p := range parts {
		if utf8.RuneCountInString(p) > 200 || !strings.HasSuffix(p, ".") {
			t.Fatalf("part %q", p)
		}
	}
	if got := strings.Join(parts, " "); got != strings.TrimSpace(text) {
		t.Fatal("text changed")
	}
	if parts := SplitSpeech("Короткий вопрос?", 500); len(parts) != 1 {
		t.Fatalf("short text split: %v", parts)
	}
	if parts := SplitSpeech(strings.Repeat("я", 450), 200); len(parts) != 3 {
		t.Fatalf("long word parts = %d", len(parts))
	}
}

func TestWholeSpeechCacheChangesWithVoice(t *testing.T) {
	store := &syncAudio{data: map[string][]byte{}}
	voice := &countingVoice{called: make(chan struct{}, 3)}
	ctx := context.Background()
	for _, name := range []string{"prompt=true", "prompt=true", "prompt=false"} {
		cache := NewSpeechCache(voice, store, staticVoiceName(name), time.Minute)
		if _, err := cache.Get(ctx, "geek/session/turn.wav", "Персональная реплика"); err != nil {
			t.Fatal(err)
		}
	}
	if len(voice.calls) != 2 {
		t.Fatalf("syntheses = %d, want 2", len(voice.calls))
	}
}

func TestPartReusesLegacyCacheKey(t *testing.T) {
	const name = "seed=607 cfg=2 steps=20 style=calm-v1"
	const text = "Подготовленный вопрос?"
	sum := sha256.Sum256([]byte(name + "\n" + text))
	oldKey := "geek/speech/" + hex.EncodeToString(sum[:]) + ".wav"
	stored := wav(10)
	store := &syncAudio{data: map[string][]byte{oldKey: stored}}
	voice := &countingVoice{called: make(chan struct{}, 1)}
	got, err := NewSpeechCache(voice, store, staticVoiceName(name), time.Minute).Part(context.Background(), text)
	if err != nil || !bytes.Equal(got, stored) || len(voice.calls) != 0 {
		t.Fatalf("legacy cache not reused: calls=%d error=%v", len(voice.calls), err)
	}
}

func staticVoiceName(name string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return name, nil }
}

type speechVoiceFunc func(context.Context, string) ([]byte, error)

func (f speechVoiceFunc) Synthesize(ctx context.Context, text string) ([]byte, error) {
	return f(ctx, text)
}

func TestSpeechBudgetIncludesVoiceLookupAndAllSequentialChunks(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprintf("whole=%v", whole), func(t *testing.T) {
			store := &syncAudio{data: map[string][]byte{}}
			deadlines := make(chan time.Time, 3)
			finished := make(chan struct{})
			var calls int
			voice := speechVoiceFunc(func(ctx context.Context, _ string) ([]byte, error) {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Error("synthesis has no overall deadline")
				}
				deadlines <- deadline
				calls++
				if calls == 1 {
					return wav(10), nil
				}
				defer close(finished)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			var lookupDeadline time.Time
			name := func(ctx context.Context) (string, error) {
				lookupDeadline, _ = ctx.Deadline()
				return "voice", nil
			}
			cache := NewSpeechCache(voice, store, name, 100*time.Millisecond)
			text := strings.Repeat("я", 3*speechLimit)
			var err error
			if whole {
				_, err = cache.Get(context.Background(), "turn.wav", text)
			} else {
				_, err = cache.Part(context.Background(), text)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("budget: %v", err)
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("detached synthesis outlived total budget")
			}
			first, second := <-deadlines, <-deadlines
			if !first.Equal(lookupDeadline) || !second.Equal(first) {
				t.Fatal("voice lookup/chunks renewed the budget")
			}
			if calls != 2 || store.size() != 0 {
				t.Fatal("expired speech started another chunk or cached partial audio")
			}
		})
	}
}
