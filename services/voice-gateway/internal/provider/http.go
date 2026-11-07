// Package provider implements the existing GigaAM and VoxCPM HTTP APIs.
package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/diagnostic"
	"google.golang.org/grpc/codes"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/AskarKasimov/ai-tutor/services/audio"
)

const (
	// Short-form GigaAM receives chunks of at most 20 seconds. Longform
	// instances bypass splitting and receive the original recording.
	gigaAMChunkSeconds = 20
	// Bound splitting allocations and sequential upstream calls per request;
	// the preflight check and the splitter must enforce the same budget.
	gigaAMMaxParts = 100
)

func transportFailure(err error, beforeSend bool) error {
	code := codes.Unavailable
	if errors.Is(err, context.DeadlineExceeded) {
		code = codes.DeadlineExceeded
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		code = codes.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		code = codes.Canceled
	}
	stage, reason := "upstream", "transport_failure"
	if beforeSend {
		stage, reason = "connect", "connection_failed"
	}
	return &diagnostic.Error{Code: code, Uncertain: !beforeSend, Stage: stage, Reason: reason}
}
func statusCode(err error) codes.Code {
	if errors.Is(err, context.DeadlineExceeded) {
		return codes.DeadlineExceeded
	}
	return codes.Canceled
}
func responseFailure(resp *http.Response, stt bool) error {
	code := codes.Internal
	switch resp.StatusCode {
	case 401, 403, 429, 503:
		code = codes.Unavailable
	case 408, 504:
		code = codes.DeadlineExceeded
	case 413:
		if stt {
			code = codes.OutOfRange
		}
	case 415:
		if stt {
			code = codes.Unimplemented
		}
	case 422:
		if stt {
			code = codes.InvalidArgument
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			var body struct {
				Detail string `json:"detail"`
			}
			if json.Unmarshal(data, &body) == nil && strings.HasPrefix(body.Detail, "Unsupported ASR model:") {
				code = codes.Unavailable
			}
		}
	}
	states := resp.Header.Values("X-Voice-Execution-State")
	confirmed := len(states) == 1 && (states[0] == "not-started" || states[0] == "finished")
	return &diagnostic.Error{Code: code, Uncertain: !confirmed, Stage: "upstream", Reason: "http_response", HTTPStatus: resp.StatusCode}
}

type STT struct {
	Token    string
	URL      string
	Model    string
	Longform bool
	Timeout  time.Duration
}
type TTS struct {
	Token    string
	URL      string
	Timeout  time.Duration
	Profiles map[string]Options
}
type Options struct {
	Seed               int64   `json:"seed"`
	CFGValue           float64 `json:"cfg_value"`
	InferenceTimesteps int     `json:"inference_timesteps"`
	UseReference       bool    `json:"use_reference"`
	UsePrompt          bool    `json:"use_prompt"`
}
type HTTP struct {
	Client *http.Client
	STT    map[string]STT
	TTS    map[string]TTS
}

func (p *HTTP) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

// A connection assigned by the standard transport may already be sending the
// request when cancellation arrives. Before GotConn, no HTTP bytes can reach
// the model. Custom transports without this trace contract stay conservative.
func (p *HTTP) do(req *http.Request) (*http.Response, error) {
	// Never forward credentials or payloads to a redirect target. Copy the
	// client so injected/shared clients keep their own redirect policy.
	client := *p.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	_, tracked := transport.(*http.Transport)
	var connected atomic.Bool
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	response, err := client.Do(req)
	if err != nil {
		return nil, transportFailure(err, tracked && !connected.Load())
	}
	return response, nil
}

func (p *HTTP) Transcribe(ctx context.Context, instance string, data []byte, media string) (string, error) {
	cfg, ok := p.STT[instance]
	if !ok {
		return "", errors.New("unknown STT instance")
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	parts := [][]byte{data}
	base, _, _ := mime.ParseMediaType(media)
	if !cfg.Longform && (base == "audio/wav" || base == "audio/x-wav" || base == "audio/wave") {
		if audio.TooManyParts(data, gigaAMChunkSeconds, gigaAMMaxParts) {
			return "", &diagnostic.Error{Code: codes.InvalidArgument, Stage: "validation", Reason: "too_many_audio_parts"}
		}
		if split, ok := audio.SplitWAV(data, gigaAMChunkSeconds, gigaAMMaxParts); ok {
			parts = split
		}
	}
	texts := make([]string, 0, len(parts))
	for index, part := range parts {
		partCtx := context.WithValue(ctx, partKey{}, index)
		text, err := p.transcribeOnce(partCtx, cfg, part, media)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) != "" {
			texts = append(texts, strings.TrimSpace(text))
		}
	}
	return strings.Join(texts, " "), nil
}
func (p *HTTP) transcribeOnce(ctx context.Context, cfg STT, data []byte, media string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="audio"`)
	header.Set("Content-Type", media)
	part, err := writer.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err = part.Write(data); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	target, err := url.Parse(cfg.URL)
	if err != nil {
		return "", err
	}
	if cfg.Model != "" {
		query := target.Query()
		query.Set("model", cfg.Model)
		target.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	setReceiverHeaders(req, cfg.Token)
	if err := ctx.Err(); err != nil {
		return "", &diagnostic.Error{Code: statusCode(err), Stage: "connect", Reason: "cancelled_before_send"}
	}
	response, err := p.do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", responseFailure(response, true)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil {
		return "", responseReadFailure(err)
	}
	var parsed struct {
		Text *string `json:"text"`
	}
	if len(payload) > 8*1024*1024 || !utf8.Valid(payload) || json.Unmarshal(payload, &parsed) != nil || parsed.Text == nil || strings.ContainsRune(*parsed.Text, 0) {
		return "", &diagnostic.Error{Code: codes.Internal, Stage: "upstream", Reason: "invalid_response"}
	}
	return *parsed.Text, nil
}
func (p *HTTP) Synthesize(ctx context.Context, instance, profile, text string) ([]byte, error) {
	cfg, ok := p.TTS[instance]
	if !ok {
		return nil, errors.New("unknown TTS instance")
	}
	opts, ok := cfg.Profiles[profile]
	if !ok {
		return nil, errors.New("unknown voice profile")
	}
	if !strings.HasSuffix(text, "!!!") {
		text += "!!!"
	}
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
		Options
	}{Text: text, Options: opts})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	setReceiverHeaders(req, cfg.Token)
	if err := ctx.Err(); err != nil {
		return nil, &diagnostic.Error{Code: statusCode(err), Stage: "connect", Reason: "cancelled_before_send"}
	}
	response, err := p.do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseFailure(response, false)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024*1024+1))
	if err != nil {
		return nil, responseReadFailure(err)
	}
	if len(data) > 64*1024*1024 || !audio.ValidWAV(data) {
		return nil, &diagnostic.Error{Code: codes.Internal, Stage: "upstream", Reason: "invalid_response"}
	}
	return data, nil
}

func responseReadFailure(err error) error {
	d := diagnostic.Info(transportFailure(err, false))
	d.Uncertain = true
	d.Stage = "upstream_read"
	d.Reason = "response_body_interrupted"
	d.HTTPStatus = 200
	return &d
}

// Each chunk is a separate receiver job, stable for the lifetime of a gateway job.
type partKey struct{}

func setReceiverHeaders(req *http.Request, token string) {
	if token == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	id := diagnostic.ID(req.Context())
	if id == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return
		}
		id = hex.EncodeToString(raw[:])
	}
	index, _ := req.Context().Value(partKey{}).(int)
	req.Header.Set("Idempotency-Key", fmt.Sprintf("%s-%d", id, index))
}
