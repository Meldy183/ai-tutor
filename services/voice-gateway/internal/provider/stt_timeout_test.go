package provider

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/diagnostic"
	"google.golang.org/grpc/codes"
)

func twoPartWAV() []byte {
	const samples = 21 * 16000
	data := make([]byte, 44+samples*2)
	copy(data, wav()[:44])
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	binary.LittleEndian.PutUint32(data[40:], uint32(len(data)-44))
	return data
}

func delayedSTT(t *testing.T, delay time.Duration, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		file.Close()
		// Each HTTP fragment can independently wait in the receiver queue.
		select {
		case <-time.After(delay):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"часть"}`))
		case <-r.Context().Done():
		}
	}))
}

func TestSTTFragmentsEachHaveUpstreamTimeout(t *testing.T) {
	var calls atomic.Int32
	server := delayedSTT(t, 600*time.Millisecond, &calls)
	defer server.Close()
	p := HTTP{STT: map[string]STT{"stt": {URL: server.URL, Timeout: time.Second}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	text, err := p.Transcribe(ctx, "stt", twoPartWAV(), "audio/wav")
	if err != nil || text != "часть часть" || calls.Load() != 2 {
		t.Fatalf("two sequential queue waits: text=%q calls=%d err=%v", text, calls.Load(), err)
	}
}

func TestSTTFragmentsRespectCallerDeadline(t *testing.T) {
	var calls atomic.Int32
	server := delayedSTT(t, 600*time.Millisecond, &calls)
	defer server.Close()
	p := HTTP{STT: map[string]STT{"stt": {URL: server.URL, Timeout: time.Second}}}
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	text, err := p.Transcribe(ctx, "stt", twoPartWAV(), "audio/wav")
	info := diagnostic.Info(err)
	if err == nil || info.Code != codes.DeadlineExceeded || !info.Uncertain || text != "" || calls.Load() != 2 {
		t.Fatalf("caller deadline: text=%q calls=%d diagnostic=%+v", text, calls.Load(), info)
	}
}

func TestSTTFragmentStillHasIndividualTimeout(t *testing.T) {
	var calls atomic.Int32
	server := delayedSTT(t, 2*time.Second, &calls)
	defer server.Close()
	p := HTTP{STT: map[string]STT{"stt": {URL: server.URL, Timeout: 500 * time.Millisecond}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	text, err := p.Transcribe(ctx, "stt", twoPartWAV(), "audio/wav")
	info := diagnostic.Info(err)
	if err == nil || info.Code != codes.DeadlineExceeded || !info.Uncertain || text != "" || calls.Load() != 1 {
		t.Fatalf("fragment deadline: text=%q calls=%d diagnostic=%+v", text, calls.Load(), info)
	}
}
