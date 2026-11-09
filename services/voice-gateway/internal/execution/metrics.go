package execution

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sort"
	"strings"

	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/diagnostic"
)

var buckets = []float64{.01, .05, .1, .5, 1, 2, 5, 10, 30, 60, 120, 300}

type histogram struct {
	Count   uint64
	Sum     float64
	Buckets []uint64
}

func (e *Engine) observe(op, kind string, value float64) {
	key := op + "/" + kind
	h := e.durations[key]
	if h == nil {
		h = &histogram{Buckets: make([]uint64, len(buckets))}
		e.durations[key] = h
	}
	h.Count++
	h.Sum += value
	for i, b := range buckets {
		if value <= b {
			h.Buckets[i]++
		}
	}
}
func (e *Engine) RecordRequest(op string, err error) {
	d := diagnostic.Info(err)
	e.requestMu.Lock()
	defer e.requestMu.Unlock()
	e.requests[op+"/"+d.Code.String()+"/"+d.Reason]++
}
func (e *Engine) Metrics(w http.ResponseWriter, r *http.Request) {
	var snapshot bytes.Buffer
	e.writeMetrics(&snapshot)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write(snapshot.Bytes())
}

// Render to memory before writing to the client: a slow scrape must not hold
// either the engine lock or the request counter lock during network I/O.
func (e *Engine) writeMetrics(w io.Writer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fmt.Fprintf(w, "# TYPE voice_gateway_ready gauge\nvoice_gateway_ready %d\n", boolInt(e.ready))
	fmt.Fprintf(w, "# TYPE voice_payload_bytes gauge\nvoice_payload_bytes %d\n", e.blobs.Usage())
	fmt.Fprintln(w, "# TYPE voice_queue_depth gauge\n# TYPE voice_instance_busy gauge\n# TYPE voice_instance_slots gauge\n# TYPE voice_instance_held gauge")
	for _, op := range []string{"stt", "tts"} {
		s := e.queues[op].Stats()
		fmt.Fprintf(w, "voice_queue_depth{operation=%q} %d\n", op, s.Waiting)
		for _, in := range s.Instances {
			held := 0
			for _, j := range e.jobs {
				if j.Instance == in.ID && j.Held && j.State != "running" {
					held++
				}
			}
			fmt.Fprintf(w, "voice_instance_busy{operation=%q,model_instance=%q} %d\nvoice_instance_slots{operation=%q,model_instance=%q} %d\nvoice_instance_held{operation=%q,model_instance=%q} %d\n", op, in.ID, in.Busy, op, in.ID, in.Slots, op, in.ID, held)
		}
	}
	e.requestMu.Lock()
	requests := maps.Clone(e.requests)
	e.requestMu.Unlock()
	fmt.Fprintln(w, "# TYPE voice_requests_total counter")
	requestKeys := make([]string, 0, len(requests))
	for key := range requests {
		requestKeys = append(requestKeys, key)
	}
	sort.Strings(requestKeys)
	for _, key := range requestKeys {
		parts := strings.SplitN(key, "/", 3)
		fmt.Fprintf(w, "voice_requests_total{operation=%q,code=%q,reason=%q} %d\n", parts[0], parts[1], parts[2], requests[key])
	}
	fmt.Fprintln(w, "# TYPE voice_route_selections_total counter")
	selections := make([]string, 0, len(e.selected))
	for key := range e.selected {
		selections = append(selections, key)
	}
	sort.Strings(selections)
	for _, key := range selections {
		op, in, _ := strings.Cut(key, "/")
		fmt.Fprintf(w, "voice_route_selections_total{operation=%q,model_instance=%q} %d\n", op, in, e.selected[key])
	}
	fmt.Fprintln(w, "# TYPE voice_upstream_calls_total counter")
	keys := make([]string, 0, len(e.completed))
	for key := range e.completed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		op, code, _ := strings.Cut(key, "/")
		fmt.Fprintf(w, "voice_upstream_calls_total{operation=%q,code=%q} %d\n", op, code, e.completed[key])
	}
	fmt.Fprintln(w, "# TYPE voice_held_slots_released_total counter")
	// Every series starts at 0: Prometheus increase() misses a series' first event.
	for _, op := range []string{"stt", "tts"} {
		for _, in := range e.queues[op].Stats().Instances {
			for _, reason := range []string{releaseExpired, releaseOperator} {
				fmt.Fprintf(w, "voice_held_slots_released_total{operation=%q,model_instance=%q,reason=%q} %d\n", op, in.ID, reason, e.released[op+"/"+reason+"/"+in.ID])
			}
		}
	}
	for _, kind := range []string{"upstream", "queue_wait"} {
		name := "voice_" + kind + "_seconds"
		fmt.Fprintf(w, "# TYPE %s histogram\n", name)
		for _, op := range []string{"stt", "tts"} {
			h := e.durations[op+"/"+kind]
			if h == nil {
				h = &histogram{Buckets: make([]uint64, len(buckets))}
			}
			for i, b := range buckets {
				fmt.Fprintf(w, "%s_bucket{operation=%q,le=%q} %d\n", name, op, fmt.Sprint(b), h.Buckets[i])
			}
			fmt.Fprintf(w, "%s_bucket{operation=%q,le=\"+Inf\"} %d\n%s_count{operation=%q} %d\n%s_sum{operation=%q} %g\n", name, op, h.Count, name, op, h.Count, name, op, h.Sum)
		}
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
