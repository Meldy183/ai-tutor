package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Circuit struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	probing   bool
	// generation grows on every state change (closed -> open, outcome of the
	// half-open probe). A call counts only if it started in the current
	// generation: the result of a call sent before the breaker opened says
	// nothing about the instance now.
	generation uint64
}

// circuitTicket is what a call holds from reserve to release.
type circuitTicket struct {
	generation uint64
	probe      bool
}

// probeTicket is the ticket of a call that already holds the half-open probe.
// While the probe is held, only the probe itself changes the generation.
func (c *Circuit) probeTicket() circuitTicket {
	c.mu.Lock()
	defer c.mu.Unlock()
	return circuitTicket{generation: c.generation, probe: true}
}

// release records the outcome of a finished call. A ticket with probe gives
// the probe back, so a call that started earlier cannot free the probe of
// another one. A call of an older generation is ignored: it neither resets the
// failure count, nor adds to it, nor extends the pause.
func (c *Circuit) release(ok bool, ticket circuitTicket, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ticket.probe {
		c.probing = false
	}
	if ticket.generation != c.generation {
		return
	}
	if ok {
		if c.failures >= 3 {
			c.generation++
		}
		c.failures = 0
		c.openUntil = time.Time{}
		return
	}
	c.failures++
	if c.failures >= 3 {
		c.openUntil = now.Add(15 * time.Second)
		c.generation++
	}
}

type UpstreamResult struct {
	Body       []byte
	Status     int
	RetryAfter time.Duration
}
type UpstreamFailure struct {
	Status     int
	Code       string
	Err        error
	BeforeSend bool
	RetryAfter time.Duration
	// JobDeadline marks a call cut by the request deadline (it includes the queue
	// wait) rather than by the instance. Code and Err still say what happened on
	// the wire, so the outcome stays uncertain when the request had been sent.
	JobDeadline bool
	// InstanceTimeout marks a call whose context was ended by the instance's own
	// timeout, even if the transport reported a broken connection (unexpected
	// EOF, connection reset) instead of the context error. It excludes
	// JobDeadline. Code and Err still say what happened on the wire.
	InstanceTimeout bool
}

func (e *UpstreamFailure) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

// upstreamIdleConnTimeout is how long an idle upstream connection is kept. The
// models' servers (uvicorn) close an idle keep-alive connection after 5 s; the
// gateway must close it first, otherwise a call can be written into a connection
// the server is closing and fail with EOF. Keep it below every upstream's
// keep-alive timeout; recheck when adding a server type.
const upstreamIdleConnTimeout = 4 * time.Second

type Caller struct {
	cfg      Config
	client   *http.Client
	circuits map[string]*Circuit
	mu       sync.Mutex
}

func NewCaller(cfg Config) *Caller {
	return &Caller{cfg: cfg, client: &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, MaxIdleConns: 128, IdleConnTimeout: upstreamIdleConnTimeout, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: cfg.UpstreamTimeout}, Timeout: cfg.UpstreamTimeout}, circuits: make(map[string]*Circuit)}
}
func (c *Caller) circuit(id string) *Circuit {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.circuits[id] == nil {
		c.circuits[id] = &Circuit{}
	}
	return c.circuits[id]
}

// errInstanceTimeout is the cause of a call context ended by the instance's own
// timeout. It wraps context.DeadlineExceeded like any other timeout.
var errInstanceTimeout = fmt.Errorf("upstream instance timeout: %w", context.DeadlineExceeded)

func (c *Caller) call(ctx context.Context, in InstanceConfig, req Request, requestID string, probeReserved bool) (UpstreamResult, *UpstreamFailure) {
	cb := c.circuit(in.ID)
	var ticket circuitTicket
	if probeReserved {
		ticket = cb.probeTicket()
	} else {
		var ok bool
		if ticket, ok = cb.reserve(time.Now()); !ok {
			return UpstreamResult{}, &UpstreamFailure{Code: "upstream_circuit_open", Err: fmt.Errorf("instance temporarily unavailable"), BeforeSend: true}
		}
	}
	params := make(map[string]json.RawMessage, len(req.Parameters))
	for k, v := range req.Parameters {
		params[k] = v
	}
	fields := make(map[string]json.RawMessage, len(params)+2)
	for key, value := range params {
		fields[key] = append(json.RawMessage(nil), value...)
	}
	var effort string
	if raw, ok := params["reasoning_effort"]; ok {
		_ = json.Unmarshal(raw, &effort)
	}
	for key, value := range in.ReasoningEffortBody[effort] {
		if _, set := fields[key]; !set {
			fields[key] = append(json.RawMessage(nil), value...)
		}
	}
	model, _ := json.Marshal(in.Model)
	messages, _ := json.Marshal(req.Messages)
	fields["model"] = model
	fields["messages"] = messages
	body, err := json.Marshal(fields)
	if err != nil {
		cb.release(false, ticket, time.Now())
		return UpstreamResult{}, &UpstreamFailure{Code: "invalid_upstream_request", Err: err, BeforeSend: true}
	}
	endpoint := strings.TrimRight(in.BaseURL, "/") + "/v1/chat/completions"
	if strings.HasSuffix(strings.TrimRight(in.BaseURL, "/"), "/v1") {
		endpoint = strings.TrimRight(in.BaseURL, "/") + "/chat/completions"
	}
	callCtx, cancel := context.WithTimeoutCause(ctx, minDuration(c.cfg.UpstreamTimeout, in.Timeout), errInstanceTimeout)
	defer cancel()
	// The job deadline cut the call only if it, not the instance timeout, ended
	// the call context: the transport may report the error after both passed.
	jobDeadline := func() bool {
		return errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(context.Cause(callCtx), errInstanceTimeout)
	}
	// The instance timeout ended the call if it, not the job deadline, is the
	// cause of the call context, whatever error the transport reported.
	instanceTimeout := func() bool {
		return !jobDeadline() && errors.Is(context.Cause(callCtx), errInstanceTimeout)
	}
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		cb.release(false, ticket, time.Now())
		return UpstreamResult{}, &UpstreamFailure{Code: "invalid_upstream_url", Err: err, BeforeSend: true}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if in.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+in.Token)
	}
	httpReq.Header.Set("X-Request-ID", requestID)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			if ticket.probe {
				cb.releaseReservation()
			}
			return UpstreamResult{}, &UpstreamFailure{Code: "request_canceled", Err: err}
		}
		var ne net.Error
		before := strings.Contains(err.Error(), "connect: ") || strings.Contains(err.Error(), "no such host") || strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "network is unreachable")
		if errorsAs(err, &ne) && !ne.Timeout() {
			before = before || strings.Contains(err.Error(), "dial")
		}
		if jobDeadline() {
			if ticket.probe {
				cb.releaseReservation()
			}
			return UpstreamResult{}, &UpstreamFailure{Code: "upstream_transport_error", Err: err, BeforeSend: before, JobDeadline: true}
		}
		cb.release(false, ticket, time.Now())
		return UpstreamResult{}, &UpstreamFailure{Code: "upstream_transport_error", Err: err, BeforeSend: before, InstanceTimeout: instanceTimeout()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			if ticket.probe {
				cb.releaseReservation()
			}
			return UpstreamResult{}, &UpstreamFailure{Code: "request_canceled", Err: err}
		}
		if jobDeadline() {
			if ticket.probe {
				cb.releaseReservation()
			}
			return UpstreamResult{}, &UpstreamFailure{Code: "upstream_response_error", Err: err, JobDeadline: true}
		}
		cb.release(false, ticket, time.Now())
		return UpstreamResult{}, &UpstreamFailure{Code: "upstream_response_error", Err: err, InstanceTimeout: instanceTimeout()}
	}
	if resp.StatusCode >= 500 {
		cb.release(false, ticket, time.Now())
	} else if resp.StatusCode < 500 {
		cb.release(true, ticket, time.Now())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		d := parseRetryAfter(resp.Header.Get("Retry-After"))
		return UpstreamResult{}, &UpstreamFailure{Status: resp.StatusCode, Code: "upstream_error", Err: fmt.Errorf("upstream returned HTTP %d", resp.StatusCode), RetryAfter: d}
	}
	if len(raw) > 32<<20 {
		return UpstreamResult{}, &UpstreamFailure{Code: "upstream_response_too_large", Err: fmt.Errorf("upstream response exceeded 32 MiB")}
	}
	var completion map[string]json.RawMessage
	if json.Unmarshal(raw, &completion) != nil {
		return UpstreamResult{}, &UpstreamFailure{Code: "upstream_invalid_response", Err: fmt.Errorf("upstream response is not valid JSON")}
	}
	var shape struct {
		ID      string            `json:"id"`
		Object  string            `json:"object"`
		Created int64             `json:"created"`
		Choices []json.RawMessage `json:"choices"`
	}
	if json.Unmarshal(raw, &shape) != nil || shape.ID == "" || shape.Object != "chat.completion" || shape.Created <= 0 || len(shape.Choices) == 0 {
		return UpstreamResult{}, &UpstreamFailure{Code: "upstream_invalid_response", Err: fmt.Errorf("upstream returned an incomplete chat completion")}
	}
	completion["model"], _ = json.Marshal(in.Model)
	out, err := json.Marshal(completion)
	if err != nil {
		return UpstreamResult{}, &UpstreamFailure{Code: "upstream_invalid_response", Err: err}
	}
	return UpstreamResult{Body: out, Status: resp.StatusCode}, nil
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func minDuration(a, b time.Duration) time.Duration {
	if b <= 0 || b > a {
		return a
	}
	return b
}
func parseRetryAfter(v string) time.Duration {
	if n, e := strconv.Atoi(v); e == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, e := http.ParseTime(v); e == nil && t.After(time.Now()) {
		return time.Until(t)
	}
	return 0
}

var errorsAs = errors.As
