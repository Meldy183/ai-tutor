// Package gateway implements the voice gateway's gRPC contract.
package gateway

import (
	"context"
	"errors"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/diagnostic"
	"mime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AskarKasimov/ai-tutor/services/audio"
	pb "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/voicegateway/v1"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/execution"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/scheduler"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type Provider interface {
	Transcribe(context.Context, string, []byte, string) (string, error)
	Synthesize(context.Context, string, string, string) ([]byte, error)
}
type Server struct {
	pb.UnimplementedVoiceGatewayServer
	engine   *execution.Engine
	provider Provider
	voices   map[string]string
	timeout  time.Duration
}

func New(engine *execution.Engine, provider Provider, voices map[string]string, timeout time.Duration) *Server {
	return &Server{engine: engine, provider: provider, voices: voices, timeout: timeout}
}
func (s *Server) DescribeVoice(ctx context.Context, req *pb.DescribeVoiceRequest) (*pb.DescribeVoiceResponse, error) {
	if !s.engine.Ready() {
		return nil, status.Error(codes.Unavailable, "voice gateway not ready")
	}
	revision, ok := s.voices[req.GetVoiceProfile()]
	if !ok {
		return nil, status.Error(codes.NotFound, "unknown voice profile")
	}
	return &pb.DescribeVoiceResponse{Revision: revision}, nil
}
func key(ctx context.Context) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	keys := md.Get("idempotency-key")
	if len(keys) > 1 || len(keys) == 1 && (len(keys[0]) > 128 || strings.TrimSpace(keys[0]) == "") {
		return "", status.Error(codes.InvalidArgument, "invalid idempotency key")
	}
	if len(keys) == 1 {
		return keys[0], nil
	}
	return "", nil
}
func announce(ctx context.Context) func(string) {
	return func(id string) { _ = grpc.SendHeader(ctx, metadata.Pairs("x-request-id", id)) }
}
func (s *Server) Transcribe(ctx context.Context, req *pb.TranscribeRequest) (*pb.TranscribeResponse, error) {
	if req == nil || len(req.Audio) == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid audio size")
	}
	if len(req.Audio) > 25<<20 {
		return nil, status.Error(codes.OutOfRange, "audio exceeds 25 MiB")
	}
	media, _, err := mime.ParseMediaType(req.MediaType)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid media type")
	}
	switch media {
	case "audio/wav", "audio/x-wav", "audio/wave":
		info, valid := audio.InspectWAV(req.Audio)
		if !valid || info.SampleRate < 8000 || info.SampleRate > 192000 {
			return nil, status.Error(codes.InvalidArgument, "invalid WAV")
		}
	case "audio/ogg", "application/ogg", "audio/webm", "video/webm":
	default:
		return nil, status.Error(codes.Unimplemented, "unsupported audio format")
	}
	k, err := key(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	input, _ := proto.Marshal(req)
	r, id := s.engine.Submit(ctx, "stt", "", k, input, announce(ctx), func(ctx context.Context, instance string) scheduler.Result {
		text, err := s.provider.Transcribe(ctx, instance, req.Audio, req.MediaType)
		if err == nil && (!utf8.ValidString(text) || strings.ContainsRune(text, 0)) {
			err = status.Error(codes.Internal, "invalid provider response")
		}
		if err != nil {
			return scheduler.Result{Err: failure(err)}
		}
		data, err := proto.Marshal(&pb.TranscribeResponse{Text: text})
		return scheduler.Result{Value: data, Err: err}
	})
	if r.Err != nil {
		return nil, mapError(r.Err)
	}
	var response pb.TranscribeResponse
	if err := proto.Unmarshal(r.Value.([]byte), &response); err != nil {
		return nil, status.Error(codes.DataLoss, "invalid stored result")
	}
	response.RequestId = id
	return &response, nil
}
func (s *Server) Synthesize(ctx context.Context, req *pb.SynthesizeRequest) (*pb.SynthesizeResponse, error) {
	if req == nil || strings.TrimSpace(req.Text) == "" || !utf8.ValidString(req.Text) || strings.ContainsRune(req.Text, 0) || utf8.RuneCountInString(req.Text) > 500 {
		return nil, status.Error(codes.InvalidArgument, "invalid synthesis text")
	}
	revision, ok := s.voices[req.VoiceProfile]
	if !ok {
		return nil, status.Error(codes.NotFound, "unknown voice profile")
	}
	if req.VoiceRevision != revision {
		return nil, status.Error(codes.FailedPrecondition, "voice revision changed; refresh backend voice configuration")
	}
	k, err := key(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	input, _ := proto.Marshal(req)
	r, id := s.engine.Submit(ctx, "tts", req.VoiceProfile, k, input, announce(ctx), func(ctx context.Context, instance string) scheduler.Result {
		data, err := s.provider.Synthesize(ctx, instance, req.VoiceProfile, req.Text)
		if err == nil && (!audio.ValidWAV(data) || len(data) > 64<<20) {
			err = status.Error(codes.Internal, "invalid provider response")
		}
		if err != nil {
			return scheduler.Result{Err: failure(err)}
		}
		payload, err := proto.Marshal(&pb.SynthesizeResponse{Audio: data, MediaType: "audio/wav"})
		return scheduler.Result{Value: payload, Err: err}
	})
	if r.Err != nil {
		return nil, mapError(r.Err)
	}
	var response pb.SynthesizeResponse
	if err := proto.Unmarshal(r.Value.([]byte), &response); err != nil {
		return nil, status.Error(codes.DataLoss, "invalid stored result")
	}
	response.RequestId = id
	return &response, nil
}

// failure prepares a provider error for the engine. Context and diagnostic
// errors pass unmapped: the engine decides from them whether the slot stays
// reserved, and mapping would erase that distinction.
func failure(err error) error {
	var upstream *diagnostic.Error
	if errors.As(err, &upstream) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return mapError(err)
}
func mapError(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "request timed out")
	}
	if errors.Is(err, scheduler.ErrQueueFull) {
		return status.Error(codes.ResourceExhausted, "voice queue full")
	}
	if errors.Is(err, scheduler.ErrNoCompatibleInstance) {
		return status.Error(codes.Unavailable, "no compatible model")
	}
	if status.Code(err) != codes.Unknown {
		return err
	}
	var upstream *diagnostic.Error
	if errors.As(err, &upstream) {
		return status.Error(upstream.Code, "voice model failed")
	}
	return status.Error(codes.Unavailable, "voice model unavailable")
}
