package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallerClosesIdleUpstreamConnectionBeforeTheServerDoes(t *testing.T) {
	t.Parallel()
	var conns atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"completion-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	server.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	caller := NewCaller(Config{UpstreamTimeout: 5 * time.Second})
	defer caller.client.CloseIdleConnections()
	in := InstanceConfig{ID: "model-a", BaseURL: server.URL + "/v1", Model: "m", Timeout: 5 * time.Second}
	request, err := ParseRequest([]byte(`{"messages":[{"role":"user","content":"hi"}],"complexity":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	call := func() {
		t.Helper()
		if _, failure := caller.call(context.Background(), in, request, "r", false); failure != nil {
			t.Fatalf("call failed: %+v", failure)
		}
	}
	call()
	time.Sleep(200 * time.Millisecond)
	call()
	if got := conns.Load(); got != 1 {
		t.Fatalf("connections after a short pause = %d, want 1 (reused)", got)
	}
	// uvicorn closes an idle keep-alive connection after 5 s; the gateway must have
	// dropped it before that, so a call after 4.5 s needs a new connection.
	const serverKeepAlive = 5 * time.Second
	time.Sleep(serverKeepAlive - 500*time.Millisecond)
	call()
	if got := conns.Load(); got != 2 {
		t.Fatalf("connections after a pause below the server keep-alive = %d, want 2", got)
	}
}
