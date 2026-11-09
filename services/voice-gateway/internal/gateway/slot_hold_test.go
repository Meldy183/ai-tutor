package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/voicegateway/v1"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/execution"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/journal"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/provider"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/scheduler"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFailurePreservesErrorDetailsForEngine(t *testing.T) {
	uncertain := &diagnostic.Error{Code: codes.Unavailable, Stage: "upstream", Reason: "transport_failure", Uncertain: true}
	tests := []struct {
		name   string
		err    error
		same   bool
		code   codes.Code
		reason string
		stage  string
	}{
		{"plain cancel", context.Canceled, true, codes.Canceled, "canceled", "request"},
		{"plain deadline", context.DeadlineExceeded, true, codes.DeadlineExceeded, "deadlineexceeded", "request"},
		{"diagnostic", uncertain, true, codes.Unavailable, "transport_failure", "upstream"},
		{"plain error", errors.New("private upstream error"), false, codes.Unavailable, "unavailable", "request"},
		{"status error", status.Error(codes.Internal, "invalid provider response"), true, codes.Internal, "internal", "request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := failure(test.err)
			if test.same && !errors.Is(got, test.err) {
				t.Fatalf("failure replaced the error: %v", got)
			}
			if info := diagnostic.Info(got); info.Code != test.code || info.Reason != test.reason || info.Stage != test.stage {
				t.Fatalf("diagnostic details: %+v", info)
			}
			if code := status.Code(mapError(got)); code != test.code {
				t.Fatalf("gRPC code = %v, want %v", code, test.code)
			}
		})
	}
}

func TestSlotAfterModelCallFollowsAbortOnDisconnect(t *testing.T) {
	type handler = func(w http.ResponseWriter, r *http.Request, success []byte, release <-chan struct{})
	respond := func(code int, state string) handler {
		return func(w http.ResponseWriter, _ *http.Request, _ []byte, _ <-chan struct{}) {
			if state != "" {
				w.Header().Set("X-Voice-Execution-State", state)
			}
			w.WriteHeader(code)
		}
	}
	cases := []struct {
		name     string
		upstream handler
		closed   bool
		deadline bool
		code     codes.Code
		held     int
	}{
		{name: "accept then close", upstream: func(w http.ResponseWriter, _ *http.Request, _ []byte, _ <-chan struct{}) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		}, code: codes.Unavailable, held: 1},
		{name: "504", upstream: respond(http.StatusGatewayTimeout, ""), code: codes.DeadlineExceeded, held: 1},
		{name: "503 uncertain", upstream: respond(http.StatusServiceUnavailable, "uncertain"), code: codes.Unavailable, held: 1},
		{name: "503 not started", upstream: respond(http.StatusServiceUnavailable, "not-started"), code: codes.Unavailable, held: 0},
		{name: "302", upstream: func(w http.ResponseWriter, _ *http.Request, _ []byte, _ <-chan struct{}) {
			w.Header().Set("Location", "http://127.0.0.1:1/elsewhere")
			w.WriteHeader(http.StatusFound)
		}, code: codes.Internal, held: 1},
		{name: "truncated body", upstream: func(w http.ResponseWriter, _ *http.Request, _ []byte, _ <-chan struct{}) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("partial"))
		}, code: codes.Unavailable, held: 1},
		{name: "hung receiver", upstream: func(_ http.ResponseWriter, r *http.Request, _ []byte, release <-chan struct{}) {
			// The server notices a disconnect only after the body has been read.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}, deadline: true, code: codes.DeadlineExceeded, held: 1},
		{name: "success", upstream: func(w http.ResponseWriter, _ *http.Request, success []byte, _ <-chan struct{}) {
			_, _ = w.Write(success)
		}, code: codes.OK, held: 0},
		{name: "closed port", closed: true, code: codes.Unavailable, held: 0},
	}
	for _, op := range []string{"stt", "tts"} {
		for _, abort := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/abort=%v/%s", op, abort, tc.name), func(t *testing.T) {
					success := testutil.WAV()
					if op == "stt" {
						success = []byte(`{"text":"распознано"}`)
					}
					release := make(chan struct{})
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if tc.upstream != nil {
							tc.upstream(w, r, success, release)
						}
					}))
					defer server.Close()
					defer close(release)
					if tc.closed {
						server.Close()
					}
					p := &provider.HTTP{
						STT: map[string]provider.STT{"model": {URL: server.URL, Timeout: 10 * time.Second}},
						TTS: map[string]provider.TTS{"model": {URL: server.URL, Timeout: 10 * time.Second, Profiles: map[string]provider.Options{"ru": {}}}},
					}
					queues := map[string]*scheduler.Queue{}
					for _, name := range []string{"stt", "tts"} {
						q, err := scheduler.New([]scheduler.Instance{{ID: "model", Slots: 1, Profiles: []string{"ru"}}}, 2)
						if err != nil {
							t.Fatal(err)
						}
						queues[name] = q
					}
					blobs, err := journal.OpenBlobs(t.TempDir(), 1<<20)
					if err != nil {
						t.Fatal(err)
					}
					defer blobs.Close()
					engine := execution.New(&testutil.Memory{}, blobs, queues, time.Hour, func(string) bool { return abort }, slog.New(slog.NewTextHandler(io.Discard, nil)))
					if err := engine.Restore(context.Background()); err != nil {
						t.Fatal(err)
					}
					s := New(engine, p, map[string]string{"ru": "rev"}, 10*time.Second)
					ctx := context.Background()
					if tc.deadline {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
						defer cancel()
					}
					if op == "stt" {
						_, err = s.Transcribe(ctx, &pb.TranscribeRequest{Audio: testutil.WAV(), MediaType: "audio/wav"})
					} else {
						_, err = s.Synthesize(ctx, &pb.SynthesizeRequest{Text: "Привет", VoiceProfile: "ru", VoiceRevision: "rev"})
					}
					if status.Code(err) != tc.code {
						t.Fatalf("code = %v, want %v: %v", status.Code(err), tc.code, err)
					}
					// On a deadline the RPC can return while the call is still
					// unwinding, so wait for it before reading the final slot state.
					engine.Wait()
					want := tc.held
					if abort {
						want = 0
					}
					for deadline := time.Now().Add(5 * time.Second); queues[op].Stats().Instances[0].Busy != want; time.Sleep(time.Millisecond) {
						if time.Now().After(deadline) {
							t.Fatalf("busy = %d, want %d", queues[op].Stats().Instances[0].Busy, want)
						}
					}
					if series := fmt.Sprintf(`voice_instance_held{operation=%q,model_instance="model"} %d`, op, want); !strings.Contains(metrics(engine), series) {
						t.Fatalf("missing %s: %s", series, metrics(engine))
					}
				})
			}
		}
	}
}
