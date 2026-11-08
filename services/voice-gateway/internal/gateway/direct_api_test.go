package gateway

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/voicegateway/v1"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/execution"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/journal"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/provider"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/scheduler"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/testutil"
)

func TestDirectAPIHTTPFailureDoesNotBlockNextRequest(t *testing.T) {
	for _, op := range []string{"stt", "tts"} {
		for _, code := range []int{401, 403, 413, 415, 422, 429, 500, 502, 503} {
			t.Run(fmt.Sprintf("%s/%d", op, code), func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) == 1 {
						w.WriteHeader(code)
						return
					}
					if op == "stt" {
						_, _ = w.Write([]byte(`{"text":"распознано"}`))
					} else {
						_, _ = w.Write(testutil.WAV())
					}
				}))
				defer upstream.Close()
				p := &provider.HTTP{
					STT: map[string]provider.STT{"model": {URL: upstream.URL, Token: "local-test-token", Timeout: time.Second}},
					TTS: map[string]provider.TTS{"model": {URL: upstream.URL, Token: "local-test-token", Timeout: time.Second, Profiles: map[string]provider.Options{"ru": {}}}},
				}
				queue, err := scheduler.New([]scheduler.Instance{{ID: "model", Slots: 1, Profiles: []string{"ru"}}}, 2)
				if err != nil {
					t.Fatal(err)
				}
				blobs, err := journal.OpenBlobs(t.TempDir(), 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				defer blobs.Close()
				engine := execution.New(&testutil.Memory{}, blobs, map[string]*scheduler.Queue{op: queue}, time.Hour, func(string) bool { return false }, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if err := engine.Restore(context.Background()); err != nil {
					t.Fatal(err)
				}
				s := New(engine, p, map[string]string{"ru": "rev"}, time.Second)
				invoke := func() error {
					if op == "stt" {
						_, err := s.Transcribe(context.Background(), &pb.TranscribeRequest{Audio: testutil.WAV(), MediaType: "audio/wav"})
						return err
					}
					_, err := s.Synthesize(context.Background(), &pb.SynthesizeRequest{Text: "Привет", VoiceProfile: "ru", VoiceRevision: "rev"})
					return err
				}
				if err := invoke(); err == nil {
					t.Fatal("first request should fail")
				}
				if err := invoke(); err != nil {
					t.Fatalf("next request could not use the only slot: %v", err)
				}
				if calls.Load() != 2 {
					t.Fatalf("upstream calls = %d, want 2", calls.Load())
				}
			})
		}
	}
}
