package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// modelClosedCases break the connection on the model's side after the whole
// request was read, the way a model server can: an idle keep-alive closed
// under the request, a crash or restart, a cut body.
var modelClosedCases = []struct {
	name      string
	breakConn func(net.Conn)
	code      string
}{
	{"closed after the request", func(c net.Conn) { _ = c.Close() }, "upstream_transport_error"},
	{"connection reset", func(c net.Conn) {
		_ = c.(*net.TCPConn).SetLinger(0)
		_ = c.Close()
	}, "upstream_transport_error"},
	{"body cut short", func(c net.Conn) {
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"choices\"")
		_ = c.Close()
	}, "upstream_response_error"},
}

// closingUpstream reads each request in full and then breaks its connection.
func closingUpstream(t *testing.T, breakConn func(net.Conn)) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		breakConn(conn)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

func TestModelClosedConnectionReleasesVerifiedSlot(t *testing.T) {
	for _, tc := range modelClosedCases {
		for _, abort := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s abort=%v", tc.name, abort), func(t *testing.T) {
				s, stub := repairFixture(t)
				// One slot, as in the deployed config, and no bystander holding it.
				s.mu.Lock()
				delete(s.jobs, "bystander")
				s.inFlight["model-a"] = 0
				s.mu.Unlock()
				in := &s.cfg.Instances[0]
				in.MaxInFlight = 1
				in.BaseURL = closingUpstream(t, tc.breakConn)
				in.Timeout = 5 * time.Second
				in.AbortOnDisconnect = abort
				j := runningJob(s, "j", time.Now().Add(time.Minute))
				s.execute(context.Background(), j, *in)

				got := s.job("j")
				if e := got.Error; got.Status != "failed" || e == nil || e.Status != 502 || e.Code != tc.code {
					t.Fatalf("abort=%v stored %s %+v, want failed 502 %s", abort, got.Status, e, tc.code)
				}
				// With abort the connection being gone means the verified model
				// stopped the request; without it the model may still be generating.
				wantHeld, wantSlots := !abort, 0
				if wantHeld {
					wantSlots = 1
				}
				if got.SlotHeld != wantHeld || s.slots() != wantSlots {
					t.Fatalf("abort=%v held=%v in flight=%d, want held=%v in flight=%d", abort, got.SlotHeld, s.slots(), wantHeld, wantSlots)
				}
				// The stored state record carries the same slot decision.
				w := stub.written("j")
				if len(w) == 0 {
					t.Fatal("no state record written for the job")
				}
				if last := w[len(w)-1]; last.ID != "j" || last.Status != "failed" || last.SlotHeld != !abort {
					t.Fatalf("abort=%v last record %s %s SlotHeld=%v, want failed SlotHeld=%v", abort, last.ID, last.Status, last.SlotHeld, !abort)
				}
				addQueued(s, "q")
				next, _, dispatched := s.next()
				if abort && (!dispatched || next.ID != "q") {
					t.Fatal("abort=true: the next request was not dispatched to the freed slot")
				}
				if !abort && dispatched {
					t.Fatalf("abort=false dispatched %s to a held slot", next.ID)
				}
			})
		}
	}
}
