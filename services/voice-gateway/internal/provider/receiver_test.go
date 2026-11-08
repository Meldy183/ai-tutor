package provider

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/diagnostic"
)

func TestReceiverHeadersAndChunkIdentity(t *testing.T) {
	ctx, err := diagnostic.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first, _ := http.NewRequestWithContext(ctx, "POST", "http://receiver", nil)
	setReceiverHeaders(first, "test-token")
	second, _ := http.NewRequestWithContext(ctx, "POST", "http://receiver", nil)
	setReceiverHeaders(second, "test-token")
	third, _ := http.NewRequestWithContext(context.WithValue(ctx, partKey{}, 1), "POST", "http://receiver", nil)
	setReceiverHeaders(third, "test-token")
	if first.Header.Get("Authorization") != "Bearer test-token" || first.Header.Get("Idempotency-Key") == "" {
		t.Fatal("missing receiver identity")
	}
	if first.Header.Get("Idempotency-Key") != second.Header.Get("Idempotency-Key") || first.Header.Get("Idempotency-Key") == third.Header.Get("Idempotency-Key") {
		t.Fatal("unstable or shared chunk identity")
	}
}

func TestReceiverExecutionState(t *testing.T) {
	for _, test := range []struct {
		state  string
		status int
		held   bool
	}{
		{"not-started", 504, false}, {"finished", 504, false}, {"uncertain", 503, true}, {"", 504, true},
	} {
		response := httptest.NewRecorder()
		response.Header().Set("X-Voice-Execution-State", test.state)
		response.WriteHeader(test.status)
		err := responseFailure(response.Result(), false)
		if diagnostic.Info(err).Uncertain != test.held {
			t.Fatalf("state %q status %d: %+v", test.state, test.status, err)
		}
	}
}

// Set both URLs to a running Python receiver using local model stubs.
func TestReceiverHTTPIntegration(t *testing.T) {
	sttURL, ttsURL := os.Getenv("TEST_VOICE_RECEIVER_STT_URL"), os.Getenv("TEST_VOICE_RECEIVER_TTS_URL")
	if sttURL == "" || ttsURL == "" {
		t.Skip("TEST_VOICE_RECEIVER_STT_URL/TTS_URL are not set")
	}
	token := os.Getenv("TEST_VOICE_RECEIVER_TOKEN")
	if token == "" {
		t.Fatal("TEST_VOICE_RECEIVER_TOKEN is required")
	}
	p := HTTP{STT: map[string]STT{"stt": {URL: sttURL, Token: token, Timeout: 10 * time.Second}}, TTS: map[string]TTS{"tts": {URL: ttsURL, Token: token, Timeout: 10 * time.Second, Profiles: map[string]Options{"voice": {Seed: 7, CFGValue: 2, InferenceTimesteps: 20}}}}}
	text, err := p.Transcribe(context.Background(), "stt", wav(), "audio/wav")
	if err != nil || text != "контракт" {
		t.Fatalf("STT integration: %q %v", text, err)
	}
	audio, err := p.Synthesize(context.Background(), "tts", "voice", "Привет")
	if err != nil || !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Fatalf("TTS integration: %v", err)
	}
}
