package provider

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/diagnostic"
	"google.golang.org/grpc/codes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func wav() []byte {
	data := make([]byte, 46)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], 38)
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 16000)
	binary.LittleEndian.PutUint32(data[28:], 32000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], 2)
	return data
}
func TestExistingModelProtocols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stt":
			if r.URL.Query().Get("model") != "example" {
				t.Error("missing model")
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			file.Close()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"text":"распознано"}`))
		case "/tts":
			var body struct {
				Text string `json:"text"`
				Seed int    `json:"seed"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text != "Привет!!!" || body.Seed != 7 {
				t.Errorf("unexpected request: %+v %v", body, err)
			}
			w.Write(wav())
		default:
			t.Error("unexpected path")
		}
	}))
	defer server.Close()
	p := HTTP{STT: map[string]STT{"stt": {URL: server.URL + "/stt", Model: "example", Timeout: time.Second}}, TTS: map[string]TTS{"tts": {URL: server.URL + "/tts", Timeout: time.Second, Profiles: map[string]Options{"voice": {Seed: 7}}}}}
	text, err := p.Transcribe(context.Background(), "stt", []byte("audio"), "audio/ogg")
	if err != nil || text != "распознано" {
		t.Fatalf("stt %q %v", text, err)
	}
	data, err := p.Synthesize(context.Background(), "tts", "voice", "Привет")
	if err != nil || len(data) != 46 {
		t.Fatalf("tts %d %v", len(data), err)
	}
}

func TestHTTPFailureUsesOptionalExecutionState(t *testing.T) {
	for _, status := range []int{401, 403, 408, 422, 429, 500, 502, 503, 504} {
		for _, state := range []string{"", "unknown", "uncertain", "not-started", "finished"} {
			for _, stt := range []bool{true, false} {
				response := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}
				if state != "" {
					response.Header.Set("X-Voice-Execution-State", state)
				}
				want := status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout
				if state != "" {
					want = state != "not-started" && state != "finished"
				}
				if info := diagnostic.Info(responseFailure(response, stt)); info.Uncertain != want {
					t.Fatalf("status=%d state=%q stt=%v: %+v", status, state, stt, info)
				}
			}
		}
	}
	response := &http.Response{StatusCode: 503, Header: http.Header{"X-Voice-Execution-State": {"finished", "uncertain"}}}
	if !diagnostic.Info(responseFailure(response, false)).Uncertain {
		t.Fatal("conflicting execution headers released slot")
	}
}

func TestRedirectsNeverForwardReceiverCredentialsOrPayload(t *testing.T) {
	for _, status := range []int{307, 308} {
		for _, state := range []string{"", "not-started", "finished", "uncertain"} {
			for _, op := range []string{"stt", "tts"} {
				t.Run(fmt.Sprintf("%s/%d/state=%s", op, status, state), func(t *testing.T) {
					var forwarded atomic.Int32
					target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
					defer target.Close()
					source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Authorization") != "Bearer private-test-token" {
							t.Error("source did not receive token")
						}
						if state != "" {
							w.Header().Set("X-Voice-Execution-State", state)
						}
						w.Header().Set("Location", target.URL)
						w.WriteHeader(status)
					}))
					defer source.Close()
					client := source.Client()
					var policyCalls atomic.Int32
					client.CheckRedirect = func(*http.Request, []*http.Request) error { policyCalls.Add(1); return nil }
					p := testProvider(client, source.URL)
					stt, tts := p.STT["stt"], p.TTS["tts"]
					stt.Token, tts.Token = "private-test-token", "private-test-token"
					p.STT["stt"], p.TTS["tts"] = stt, tts
					err := invokeProvider(p, op, context.Background())
					if info := diagnostic.Info(err); err == nil || !info.Uncertain || info.HTTPStatus != status {
						t.Fatalf("redirect: %+v", info)
					}
					if forwarded.Load() != 0 || policyCalls.Load() != 0 {
						t.Fatal("provider followed redirect")
					}
					if client.CheckRedirect == nil {
						t.Fatal("shared client policy was mutated")
					}
					if err := client.CheckRedirect(nil, nil); err != nil || policyCalls.Load() != 1 {
						t.Fatal("shared policy changed")
					}
				})
			}
		}
	}
}

func TestCancelledBeforeSendDoesNotHoldSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := HTTP{STT: map[string]STT{"stt": {URL: "http://localhost/stt", Timeout: time.Second}}, TTS: map[string]TTS{"tts": {URL: "http://localhost/tts", Timeout: time.Second, Profiles: map[string]Options{"voice": {}}}}}
	_, err := p.Transcribe(ctx, "stt", []byte("audio"), "audio/ogg")
	if f, ok := err.(*diagnostic.Error); !ok || f.Uncertain || f.Code != codes.Canceled {
		t.Fatalf("STT: %v", err)
	}
	_, err = p.Synthesize(ctx, "tts", "voice", "text")
	if f, ok := err.(*diagnostic.Error); !ok || f.Uncertain || f.Code != codes.Canceled {
		t.Fatalf("TTS: %v", err)
	}
}

func TestCancellationDuringDialDoesNotHoldSlot(t *testing.T) {
	for _, op := range []string{"stt", "tts"} {
		t.Run(op, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
				close(started)
				<-release
				return nil, context.Canceled
			}}
			defer transport.CloseIdleConnections()
			p := testProvider(&http.Client{Transport: transport}, "http://model.invalid")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- invokeProvider(p, op, ctx) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("dial did not start")
			}
			cancel()
			select {
			case err := <-done:
				if f, ok := err.(*diagnostic.Error); !ok || f.Uncertain || f.Code != codes.Canceled {
					t.Fatalf("cancellation during dial: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled request did not return")
			}
		})
	}
}

func TestCancellationAfterSendKeepsSlot(t *testing.T) {
	for _, op := range []string{"stt", "tts"} {
		t.Run(op, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-release
			}))
			defer server.Close()
			defer close(release)
			p := testProvider(server.Client(), server.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- invokeProvider(p, op, ctx) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request did not reach model")
			}
			cancel()
			select {
			case err := <-done:
				if f, ok := err.(*diagnostic.Error); !ok || !f.Uncertain || f.Code != codes.Canceled {
					t.Fatalf("cancellation after sending request: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled request did not return")
			}
		})
	}
}

func testProvider(client *http.Client, target string) *HTTP {
	return &HTTP{Client: client,
		STT: map[string]STT{"stt": {URL: target, Timeout: time.Second}},
		TTS: map[string]TTS{"tts": {URL: target, Timeout: time.Second, Profiles: map[string]Options{"voice": {}}}},
	}
}

func invokeProvider(p *HTTP, op string, ctx context.Context) error {
	if op == "stt" {
		_, err := p.Transcribe(ctx, "stt", []byte("audio"), "audio/ogg")
		return err
	}
	_, err := p.Synthesize(ctx, "tts", "voice", "текст")
	return err
}
