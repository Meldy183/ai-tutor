package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestRestoreBarrierRecognizesOnlyCurrentMarker(t *testing.T) {
	current := &kgo.Record{Key: []byte(restoreBarrierKey), Value: []byte("current")}
	if !isRestoreBarrier(current, "current") {
		t.Fatal("current restore marker was not recognized")
	}
	if isRestoreBarrier(current, "previous") {
		t.Fatal("an older restore marker completed the current recovery")
	}
	if isRestoreBarrier(&kgo.Record{Key: []byte("job-id"), Value: []byte("current")}, "current") {
		t.Fatal("ordinary job state was recognized as a restore marker")
	}
}

func TestUpstreamReadFailureRetainsUncertainSlot(t *testing.T) {
	if !uncertainUpstreamOutcome(&UpstreamFailure{Code: "upstream_response_error", Err: io.ErrUnexpectedEOF}) {
		t.Fatal("response read failure was treated as a confirmed upstream completion")
	}
	if uncertainUpstreamOutcome(&UpstreamFailure{Code: "upstream_transport_error", BeforeSend: true}) {
		t.Fatal("connection failure before sending was treated as an uncertain generation")
	}
	if !uncertainUpstreamOutcome(&UpstreamFailure{Code: "upstream_transport_error"}) {
		t.Fatal("transport failure after an unknown send outcome was treated as certain")
	}
}

func TestOnlyVerifiedDisconnectAbortMakesSentCallCertain(t *testing.T) {
	timeout := &UpstreamFailure{Code: "upstream_transport_error", Err: context.DeadlineExceeded}
	if uncertainUpstreamOutcomeForInstance(InstanceConfig{}, timeout) != true {
		t.Fatal("unverified upstream timeout did not retain its slot")
	}
	if uncertainUpstreamOutcomeForInstance(InstanceConfig{AbortOnDisconnect: true}, timeout) {
		t.Fatal("verified abort-on-disconnect timeout retained its slot")
	}
	// The model closed the connection: the call ended without the gateway's timer.
	for _, closedByModel := range []*UpstreamFailure{
		{Code: "upstream_response_error", Err: io.ErrUnexpectedEOF},
		{Code: "upstream_transport_error", Err: io.EOF},
	} {
		if !uncertainUpstreamOutcomeForInstance(InstanceConfig{}, closedByModel) {
			t.Fatalf("%+v: unverified upstream did not retain its slot", closedByModel)
		}
		if uncertainUpstreamOutcomeForInstance(InstanceConfig{AbortOnDisconnect: true}, closedByModel) {
			t.Fatalf("%+v: verified abort-on-disconnect upstream retained its slot", closedByModel)
		}
	}
}

func TestValidatePoolRejectsUnsupportedParametersAndExcessContext(t *testing.T) {
	instance := schedulerInstance("model-a")
	instance.ContextWindow = 32
	instance.SupportedParameters = []string{"temperature", "response_format"}
	instance.SupportedResponseFormats = []string{"text"}
	s := schedulerFixture([]InstanceConfig{instance}, []string{"model-a"})

	unsupported, err := ParseRequest([]byte(`{"messages":[{"role":"user","content":"x"}],"complexity":"low","top_p":0.5}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.validatePool(unsupported); err == nil || !strings.Contains(err.Error(), "No instance") {
		t.Fatalf("validatePool(unsupported) error = %v, want unsupported_parameter", err)
	}

	tooLong, err := ParseRequest([]byte(`{"messages":[{"role":"user","content":"this request is much too long for the configured context"}],"complexity":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.validatePool(tooLong); err == nil || !strings.Contains(err.Error(), "exceeds the context window") {
		t.Fatalf("validatePool(tooLong) error = %v, want context_length_exceeded", err)
	}
}

func TestSupportsRejectsOutputBudgetThatOverflowsContext(t *testing.T) {
	instance := schedulerInstance("model-a")
	instance.SupportedParameters = []string{"max_tokens", "max_completion_tokens"}
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		t.Run(field, func(t *testing.T) {
			request, err := ParseRequest([]byte(`{"messages":[{"role":"user","content":"x"}],"complexity":"low","` + field + `":9223372036854775807}`))
			if err != nil {
				t.Fatalf("ParseRequest() error = %v", err)
			}
			if supports(instance, request) {
				t.Fatalf("supports() accepted MaxInt64 %s for context window %d", field, instance.ContextWindow)
			}
		})
	}
}

func TestSupportsChecksResponseFormatCapabilities(t *testing.T) {
	request, err := ParseRequest([]byte(`{"messages":[{"role":"user","content":"x"}],"complexity":"low","response_format":{"type":"json_object"}}`))
	if err != nil {
		t.Fatal(err)
	}
	instance := schedulerInstance("model-a")
	instance.SupportedParameters = []string{"response_format"}
	instance.SupportedResponseFormats = []string{"text"}
	if supports(instance, request) {
		t.Fatal("instance without json_object support accepted request")
	}
	instance.SupportedResponseFormats = append(instance.SupportedResponseFormats, "json_object")
	if !supports(instance, request) {
		t.Fatal("instance with json_object support rejected request")
	}
}

func TestStateKeyAndStableJobIDScopeIdempotency(t *testing.T) {
	first := &durableJob{ID: "generated", ClientID: "internal", IdempotencyKey: "key-1"}
	same := &durableJob{ID: "different-generated-id", ClientID: "internal", IdempotencyKey: "key-1"}
	different := &durableJob{ID: "generated", ClientID: "other", IdempotencyKey: "key-1"}
	if string(stateKey(first)) != string(stateKey(same)) {
		t.Fatal("state key changed for the same client idempotency key")
	}
	if string(stateKey(first)) == string(stateKey(different)) {
		t.Fatal("state key collided across client identities")
	}
	if stableJobID("internal", "key-1") != stableJobID("internal", "key-1") || stableJobID("internal", "key-1") == stableJobID("internal", "key-2") {
		t.Fatal("stable job ID is not deterministic and key-scoped")
	}
	if got := string(stateKey(&durableJob{ID: "job-id"})); got != "job-id" {
		t.Fatalf("state key without idempotency key = %q, want job-id", got)
	}
}

func TestHandlerHealthReadinessMetricsAndBodyLimit(t *testing.T) {
	s := &Service{
		cfg:         Config{MaxConnections: 1, MaxBodyBytes: 4},
		connections: make(chan struct{}, 1),
		started:     time.Now(),
	}
	for _, tt := range []struct {
		name string
		path string
		want int
	}{
		{name: "live", path: "/health/live", want: http.StatusOK},
		{name: "not ready", path: "/health/ready", want: http.StatusServiceUnavailable},
		{name: "unknown route", path: "/not-found", want: http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d", response.Code, tt.want)
			}
		})
	}

	metrics := httptest.NewRecorder()
	s.Handler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "gateway_queue_depth") {
		t.Fatalf("metrics response = (%d, %q), want Prometheus metrics", metrics.Code, metrics.Body.String())
	}

	s.ready = true
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"too":"large"}`))
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", response.Code)
	}
}

func TestCancellationStateFailureDisablesReadiness(t *testing.T) {
	cfg := Config{KafkaBrokers: []string{"127.0.0.1:1"}, MaxConnections: 1}
	s, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job := &durableJob{ID: "cancel-state-failure", ClientID: "internal", Status: "queued", SubmittedAt: time.Now(), Deadline: time.Now().Add(time.Minute)}
	s.mu.Lock()
	s.jobs[job.ID] = job
	s.cancelled[job.ID] = true
	s.ready = true
	s.brokerAvailable = true
	s.mu.Unlock()
	s.persistCancellation(job.ID, "request_canceled")
	s.mu.Lock()
	uncertain := s.stateUncertain
	ephemeral := s.ephemeral[job.ID]
	s.mu.Unlock()
	if !uncertain || s.Ready() || ephemeral.status != 503 || !strings.Contains(string(ephemeral.body), "state_unavailable") {
		t.Fatalf("uncertain=%v ready=%v ephemeral=%+v", uncertain, s.Ready(), ephemeral)
	}
}
