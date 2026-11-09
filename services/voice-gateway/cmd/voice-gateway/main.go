package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	pb "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/voicegateway/v1"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/config"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/execution"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/gateway"
	"github.com/AskarKasimov/ai-tutor/services/voice-gateway/internal/journal"
	"google.golang.org/grpc"
)

// failureDrain limits the wait for active RPCs after a failure: their results
// can no longer be committed, so a longer wait gains nothing.
const failureDrain = 10 * time.Second

func drainWindow(failure error, planned time.Duration) time.Duration {
	if failure != nil {
		return failureDrain
	}
	return planned
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "voice-gateway"))
	if err := run(); err != nil {
		slog.Error("voice gateway stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
func commandLine(arguments []string) (string, []string, error) {
	flags := flag.NewFlagSet("voice-gateway", flag.ContinueOnError)
	path := flags.String("config", "config/dev.yaml", "runtime YAML configuration")
	if err := flags.Parse(arguments); err != nil {
		return "", nil, err
	}
	args := flags.Args()
	if len(args) == 0 || len(args) == 1 && args[0] == "healthcheck" || len(args) == 3 && args[0] == "release-held" && args[2] == "--upstream-stopped" {
		return *path, args, nil
	}
	return "", nil, errors.New("usage: voice-gateway [--config PATH] [healthcheck | release-held INSTANCE --upstream-stopped]")
}

func run() error {
	path, args, err := commandLine(os.Args[1:])
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	var level slog.Level
	if err = level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			a.Key = "ts"
		}
		return a
	}})).With("service", "voice-gateway")
	slog.SetDefault(logger)
	if len(args) > 0 && args[0] == "healthcheck" {
		_, port, _ := net.SplitHostPort(cfg.HTTPAddress)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+port+"/health/ready", nil)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return errors.New("voice gateway is not ready")
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return errors.New("voice gateway is not ready")
		}
		return nil
	}
	if len(args) > 0 && args[0] == "release-held" {
		socket := filepath.Join(cfg.DataDir, "admin.sock")
		payload, _ := json.Marshal(map[string]any{"instance": args[1], "upstream_stopped": true})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://unix/admin/release-held", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}}
		defer client.CloseIdleConnections()
		response, err := client.Do(req)
		if err != nil {
			return errors.New("voice gateway unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != 204 {
			return errors.New("held slots could not be released")
		}
		return nil
	}
	blobs, err := journal.OpenBlobs(cfg.DataDir, cfg.MaxPayloadBytes)
	if err != nil {
		return err
	}
	defer blobs.Close()
	store, err := journal.New(cfg.Brokers, cfg.STTTopic, cfg.TTSTopic)
	if err != nil {
		return err
	}
	defer store.Close()
	queues, err := cfg.Queues()
	if err != nil {
		return err
	}
	engine := execution.New(store, blobs, queues, cfg.Retention.Duration, cfg.AbortOnDisconnect, logger)
	restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = engine.Restore(restoreCtx)
	restoreCancel()
	if err != nil {
		return err
	}
	provider := cfg.Provider()
	provider.Client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	voices := map[string]string{}
	for id, v := range cfg.Voices {
		voices[id] = v.Revision()
	}
	rpc := grpc.NewServer(grpc.MaxRecvMsgSize(26<<20), grpc.MaxSendMsgSize(65<<20), grpc.UnaryInterceptor(gateway.Interceptor(cfg.MaxConnections, engine, logger)))
	pb.RegisterVoiceGatewayServer(rpc, gateway.New(engine, provider, voices, cfg.RequestTimeout.Duration))
	listener, err := net.Listen("tcp", cfg.GRPCAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", engine.Metrics)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if !engine.Ready() || store.Ping(ctx) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	})
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("POST /admin/release-held", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Instance string `json:"instance"`
			Stopped  bool   `json:"upstream_stopped"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || input.Instance == "" || !input.Stopped {
			w.WriteHeader(400)
			return
		}
		if err := engine.ReleaseHeld(input.Instance); err != nil {
			w.WriteHeader(409)
			return
		}
		w.WriteHeader(204)
	})
	socket := filepath.Join(cfg.DataDir, "admin.sock")
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	adminListener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer func() { _ = adminListener.Close(); _ = os.Remove(socket) }()
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	adminServer := &http.Server{Handler: adminMux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
	httpServer := &http.Server{Addr: cfg.HTTPAddress, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go engine.Maintain(ctx, cfg.HoldTimeout)
	failures := make(chan error, 3)
	go func() { failures <- rpc.Serve(listener) }()
	go func() { failures <- httpServer.ListenAndServe() }()
	go func() { failures <- adminServer.Serve(adminListener) }()
	logger.Info("voice gateway started", slog.String("grpc_address", cfg.GRPCAddress), slog.String("http_address", cfg.HTTPAddress))
	select {
	case <-ctx.Done():
	case <-engine.Failed():
		err = errors.New("voice durable state unavailable")
		cancel()
	case err = <-failures:
		cancel()
	}
	engine.Stop()
	done := make(chan struct{})
	go func() { rpc.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(drainWindow(err, cfg.ShutdownDrain.Duration)):
		rpc.Stop()
		<-done
	}
	engine.Wait()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = httpServer.Shutdown(shutdown)
	_ = adminServer.Shutdown(shutdown)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}
