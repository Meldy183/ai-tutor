package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AskarKasimov/ai-tutor/services/audio"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	"golang.org/x/sync/singleflight"
)

// SplitSpeech cuts text into parts of at most limit runes for synthesis,
// preferring sentence ends, then spaces; a word longer than limit is cut.
func SplitSpeech(text string, limit int) []string {
	text = strings.TrimSpace(text)
	parts := []string{}
	for utf8.RuneCountInString(text) > limit {
		runes := []rune(text)
		cut := -1
		for i := limit - 1; i > limit/2; i-- {
			if strings.ContainsRune(".!?…", runes[i]) {
				cut = i + 1
				break
			}
		}
		if cut < 0 {
			for i := limit; i > limit/2; i-- {
				if runes[i] == ' ' {
					cut = i
					break
				}
			}
		}
		if cut < 0 {
			cut = limit
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		text = strings.TrimSpace(string(runes[cut:]))
	}
	if text != "" {
		parts = append(parts, text)
	}
	return parts
}

// SpeechCache synthesizes texts within the TTS limit and keeps the WAV in the
// audio store. It is shared by the turns students listen to and by the warmer
// that voices prepared questions ahead of them.
type SpeechCache struct {
	voice   Synthesizer
	timeout time.Duration
	audio   AudioStore
	// name identifies the TTS settings used to namespace all cached audio.
	name func(context.Context) (string, error)
	// flights joins concurrent misses of one key into one synthesis: the
	// speech service works through requests one by one, so a duplicate costs
	// a whole request in its queue.
	flights singleflight.Group
}

func NewSpeechCache(voice Synthesizer, audio AudioStore, name func(context.Context) (string, error), timeout time.Duration) *SpeechCache {
	return &SpeechCache{voice: voice, audio: audio, name: name, timeout: timeout}
}

// Part voices a turn part, cached by its text, so the same prepared question
// is synthesized once for every student.
func (c *SpeechCache) Part(ctx context.Context, text string) ([]byte, error) {
	deadline := time.Now().Add(c.timeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	text = strings.TrimSpace(text)
	name, err := c.name(ctx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(name + "\n" + text))
	return c.get(ctx, "geek/speech/"+hex.EncodeToString(sum[:])+".wav", text, deadline)
}

// Get returns the WAV cached under key or synthesizes text by parts within
// the TTS limit, joins and caches it. Callers that miss the same key at once
// wait for one synthesis. It runs detached from the caller that started it,
// so a listener who leaves does not fail the others; each caller still stops
// waiting when its own context ends.
func (c *SpeechCache) Get(ctx context.Context, key, text string) ([]byte, error) {
	deadline := time.Now().Add(c.timeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	name, err := c.name(ctx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(name))
	key = "geek/voice/" + hex.EncodeToString(sum[:]) + "/" + key
	return c.get(ctx, key, text, deadline)
}

func (c *SpeechCache) get(ctx context.Context, key, text string, deadline time.Time) ([]byte, error) {
	result := c.flights.DoChan(key, func() (any, error) {
		// Shared synthesis survives a listener leaving, with one deadline
		// for voice lookup, cache I/O and every sequential synthesis chunk.
		loadCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		defer cancel()
		return c.load(loadCtx, key, text)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]byte), nil
	}
}

func (c *SpeechCache) load(ctx context.Context, key, text string) ([]byte, error) {
	if data, ok, err := c.audio.Get(ctx, key); err != nil {
		return nil, err
	} else if ok {
		return data, nil
	}
	parts := [][]byte{}
	for _, chunk := range SplitSpeech(text, speechLimit) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := c.voice.Synthesize(ctx, chunk)
		if err != nil {
			return nil, err
		}
		parts = append(parts, data)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := audio.JoinWAV(parts)
	if !ok {
		return nil, fault.New(fault.Upstream, "SYNTHESIS_FAILED", "Сервис синтеза вернул несовместимые части.")
	}
	if err := c.audio.Put(ctx, key, data); err != nil {
		return nil, err
	}
	return data, nil
}
