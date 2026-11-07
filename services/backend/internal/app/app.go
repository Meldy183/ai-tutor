// Package app is the composition root: it wires ports to adapters and exposes
// the HTTP router. Business policies belong to the feature application layers.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	voicegatewayv1 "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/voicegateway/v1"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	llmgatewayv1 "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/llmgateway/v1"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/audioasset"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	coursegraphentity "github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	authapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/auth/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/auth/infrastructure/argon2"
	authpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/auth/infrastructure/postgres"
	authhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/auth/transport/http"
	competencyapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/csvparser"
	competencypg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/postgres"
	competencyhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/transport/http"
	graphapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/application"
	graphapi "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/infrastructure/courseapi"
	graphpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/infrastructure/postgres"
	graphxlsx "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/infrastructure/xlsx"
	graphhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/transport/http"
	diagnosticpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/diagnostic/infrastructure/postgres"
	geekapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/application"
	geekgraph "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/infrastructure/graph"
	geekllm "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/infrastructure/llm"
	geekpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/infrastructure/postgres"
	geekstorage "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/infrastructure/storage"
	geekhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/transport/http"
	materialsapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/materials/application"
	materialsocr "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/materials/infrastructure/ocr"
	materialspg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/materials/infrastructure/postgres"
	materialstranslator "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/materials/infrastructure/translator"
	materialshttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/materials/transport/http"
	subjectapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/subject/application"
	subjectpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/subject/infrastructure/postgres"
	subjecthttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/subject/transport/http"
	taskaudioapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskaudio/application"
	taskaudiopg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskaudio/infrastructure/postgres"
	taskaudios3 "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskaudio/infrastructure/s3"
	variantgenapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/variantgen/application"
	variantgenlabels "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/variantgen/infrastructure/labels"
	variantgenpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/variantgen/infrastructure/postgres"
	variantgenrandom "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/variantgen/infrastructure/random"
	variantgenhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/variantgen/transport/http"
	voiceapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/voice/application"
	voicegateway "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/voice/infrastructure/gateway"
	voicepg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/voice/infrastructure/postgres"
	voicehttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/voice/transport/http"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/httpx"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/logx"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/s3"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/security"
)

type App struct {
	voiceClient      voicegatewayv1.VoiceGatewayClient
	voiceConn        *grpc.ClientConn
	voiceSynth       *voicegateway.Synthesizer
	cfg              Config
	pool             *pgxpool.Pool
	client           *http.Client
	now              func() time.Time
	hasher           authapp.PasswordHasher
	logger           *zap.Logger
	storage          *s3.Client
	materialsStorage materialsapp.S3Storage
	ocrWorker        *materialsapp.OCRWorker
	cleaningWorker   *materialsapp.CleaningWorker
	graphWorker      *graphapp.Worker
	// examiner is the LLM gateway over gRPC; the app owns its connection and
	// closes it in Close.
	examiner   geekapp.Examiner
	summarizer geekapp.Summarizer
	gateway    io.Closer
	// speech voices Geek turns; one instance serves students and the warmer,
	// so concurrent misses of one question are synthesized once.
	speech *geekapp.SpeechCache
	// warmer voices prepared questions before students ask for them.
	warmer            *geekapp.Warmer
	variantRepository *variantgenpg.Repository
	diagnosticStore   *diagnosticpg.Store
	audioWorker       interface {
		Run(context.Context) error
		ProcessOne(context.Context) (bool, error)
		Regenerate(context.Context, string) (audioasset.Asset, error)
		Prioritize(context.Context, string) error
	}
	audioStorage  taskaudioapp.Storage
	gatewayClient llmgatewayv1.GatewayClient
}

// materialWorkers is how many materials each processing stage handles at
// once; it matches the two topic extractions course-graph runs at a time.
const materialWorkers = 2

func (a *App) recognizer() *voicegateway.Recognizer {
	return voicegateway.NewRecognizer(a.voiceClient, a.cfg.VoiceTimeout)
}
func (a *App) synthesizer() *voicegateway.Synthesizer {
	return a.voiceSynth
}

func New(cfg Config, pool *pgxpool.Pool, logger *zap.Logger) (*App, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL pool is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	a := &App{
		cfg:    cfg,
		pool:   pool,
		now:    time.Now,
		hasher: argon2.New(),
		logger: logger,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	a.variantRepository = variantgenpg.New(pool)
	a.diagnosticStore = diagnosticpg.New(pool).WithLease(cfg.HTTPWriteTimeout())
	storage, err := s3.New(s3.Config{
		Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		PathStyle: cfg.S3PathStyle, CreateBucket: cfg.S3CreateBucket, Timeout: cfg.S3Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("configure S3 storage: %w", err)
	}
	a.storage = storage
	a.materialsStorage = &materialsStorageAdapter{storage: storage, bucket: cfg.S3Bucket}
	audioStorage := taskaudios3.New(storage)
	a.audioStorage = audioStorage
	a.voiceClient, a.voiceConn, err = voicegateway.Dial(cfg.VoiceGatewayAddress)
	if err != nil {
		return nil, err
	}
	a.voiceSynth = voicegateway.NewSynthesizer(a.voiceClient, cfg.VoiceTimeout, cfg.VoiceProfile)
	a.audioWorker = taskaudioapp.NewWorker(taskaudiopg.New(pool), audioStorage, a.synthesizer(), taskaudioapp.WorkerConfig{Concurrency: cfg.AudioWorkers, PollInterval: cfg.AudioPollInterval, VoiceTimeout: cfg.VoiceTimeout, S3Timeout: cfg.S3Timeout, Bucket: cfg.S3Bucket})
	examiner, conn, err := geekllm.Dial(cfg.LLMGatewayAddress, cfg.LLMTimeout)
	if err != nil {
		_ = a.voiceConn.Close()
		return nil, err
	}
	a.examiner, a.summarizer, a.gateway = examiner, examiner, conn
	a.gatewayClient = llmgatewayv1.NewGatewayClient(conn)

	materialRepository := materialspg.New(pool)
	materialsOCR := materialsocr.New(cfg.CourseGraphURL, cfg.CourseGraphTimeout)
	// OCR text is translated to Russian through the same LLM gateway as Geek.
	translator := materialstranslator.NewWithCompleter(materialstranslator.NewGateway(llmgatewayv1.NewGatewayClient(conn), cfg.LLMTimeout), 4)
	a.ocrWorker = materialsapp.NewOCRWorker(materialRepository, a.materialsStorage, materialsOCR, translator)
	a.cleaningWorker = materialsapp.NewCleaningWorker(materialRepository, a.materialsStorage, materialsOCR)
	a.speech = geekapp.NewSpeechCache(a.synthesizer(), geekstorage.NewAudio(storage, cfg.S3Bucket), a.voiceSynth.VoiceName)
	a.warmer = geekapp.NewWarmer(a.speech, logger.With(zap.String("worker", "speech_warmer")))
	a.warmer.Prepare(geekllm.PreparedSpeech())
	// A course graph is built from the materials or from a competency map.
	learningMapRepository := competencypg.New(pool)
	learningMapImport := competencyapp.New(learningMapRepository, &csvparser.Parser{}, a.now)
	graphRepository := &pairedCourseGraphRepository{Repository: graphpg.New(pool), pool: pool, learning: learningMapRepository}
	a.graphWorker = graphapp.New(graphRepository, materialRepository, a.materialsStorage, graphapi.New(cfg.CourseGraphURL), a.warmer, graphxlsx.Read)
	a.graphWorker.SetLearningMapImporter(func(ctx context.Context, actor user.User, subjectID string, data []byte, sheets []coursegraphentity.MapSheet) (*competencymap.Map, []coursegraphentity.ImportWarning, error) {
		if len(sheets) < 2 {
			return nil, nil, fault.New(fault.Invalid, "COMPETENCY_MAP_INVALID", "Для единой карты нужны листы Geek и обучения.")
		}
		geekMapKeys, err := coursegraphentity.CompetencyMapStructureKeys(sheets[0])
		if err != nil {
			return nil, nil, fault.New(fault.Invalid, "COMPETENCY_MAP_INVALID", "Не удалось разобрать лист карты для Geek.")
		}
		learningMap, err := csvparser.ParseXLSXSheet(data, 1)
		if err != nil {
			return nil, nil, learningSheetParseFailure(err)
		}
		if err := competencymap.ValidateStructureKeys(geekMapKeys, learningMap); err != nil {
			return nil, nil, fault.New(fault.Invalid, "COMPETENCY_MAP_STRUCTURE_MISMATCH", err.Error())
		}
		if err := learningMapImport.Authorize(actor); err != nil {
			return nil, nil, err
		}
		warnings := make([]coursegraphentity.ImportWarning, len(learningMap.Warnings))
		for i, warning := range learningMap.Warnings {
			warnings[i] = coursegraphentity.ImportWarning{Row: warning.Row, ColumnIndex: warning.ColumnIndex, Column: warning.Column, Code: warning.Code}
		}
		return &learningMap, warnings, nil
	})
	a.graphWorker.SetQuestions(builtQuestions{examiner: examiner, logger: logger.With(zap.String("worker", "graph_questions"))})

	return a, nil
}

func learningSheetParseFailure(cause error) *fault.Error {
	failure := fault.New(fault.Invalid, "COMPETENCY_MAP_INVALID", "Не удалось разобрать лист карты для обучения.").WithCause(cause)
	var parserFailure *fault.Error
	if errors.As(cause, &parserFailure) {
		failure.Details = make([]fault.Detail, len(parserFailure.Details))
		for i, detail := range parserFailure.Details {
			detail.Path = "sheet:2/" + detail.Path
			failure.Details[i] = detail
		}
	}
	return failure
}

// Close releases the app's connections once requests have finished.
func (a *App) Close() error {
	var errs []error
	if a.gateway != nil {
		errs = append(errs, a.gateway.Close())
	}
	if a.voiceConn != nil {
		errs = append(errs, a.voiceConn.Close())
	}
	return errors.Join(errs...)
}

// RunStorageSetup provisions the S3 bucket in the background, retrying until it
// succeeds or the caller cancels the context.
func (a *App) RunStorageSetup(ctx context.Context) error {
	backoff := time.Second
	for {
		ensureCtx, cancel := context.WithTimeout(ctx, a.cfg.S3Timeout)
		err := a.storage.EnsureBucket(ensureCtx)
		cancel()
		if err == nil {
			return nil
		}
		a.logger.Warn("S3 bucket is not ready", zap.Error(err), zap.Duration("retry_in", backoff))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

// RunBackground runs background workers and setup tasks.
func (a *App) RunBackground(ctx context.Context) error {
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error { return a.RunStorageSetup(ctx) })
	ocrCtx := logx.With(ctx, a.logger.With(zap.String("worker", "material_ocr")))
	cleaningCtx := logx.With(ctx, a.logger.With(zap.String("worker", "material_cleaning")))
	graphCtx := logx.With(ctx, a.logger.With(zap.String("worker", "course_graph")))
	// Several materials move through OCR (and its translation) and cleaning at
	// once, so the LLM gateway gets enough parallel calls to use every model.
	// Jobs are claimed with FOR UPDATE SKIP LOCKED and a lease, so each goes to
	// one worker; course-graph still converts one document at a time and
	// extracts topics of two, so extra workers wait there instead of piling up.
	for i := 0; i < materialWorkers; i++ {
		group.Go(func() error { return a.ocrWorker.Run(ocrCtx) })
		group.Go(func() error { return a.cleaningWorker.Run(cleaningCtx) })
	}
	group.Go(func() error { return a.graphWorker.Run(graphCtx) })
	group.Go(func() error { return a.audioWorker.Run(ctx) })
	warmCtx := logx.With(ctx, a.logger.With(zap.String("worker", "speech_warmer")))
	group.Go(func() error { return a.warmer.Run(warmCtx) })
	group.Go(func() error {
		// The queue does not survive a restart: the questions of published
		// graphs are queued again.
		return recoverPublishedAudio(warmCtx, a.graphWorker.PrepareVoices, a.warmer.MarkRecoveryComplete, time.Second, a.logger)
	})
	return group.Wait()
}

func recoverPublishedAudio(ctx context.Context, prepare func(context.Context) error, complete func(), delay time.Duration, logger *zap.Logger) error {
	for {
		if err := prepare(ctx); err == nil {
			complete()
			return nil
		} else if ctx.Err() != nil {
			return nil
		} else {
			logger.Warn("questions of published graphs are not voiced ahead", zap.Error(err), zap.Duration("retry_in", delay))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = min(2*delay, 30*time.Second)
	}
}

type materialsStorageAdapter struct {
	storage *s3.Client
	bucket  string
}

func (m *materialsStorageAdapter) Put(ctx context.Context, key, contentType string, data []byte) error {
	return m.storage.PutObject(ctx, m.bucket, key, contentType, data)
}

func (m *materialsStorageAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	return m.storage.GetObject(ctx, m.bucket, key)
}

func (m *materialsStorageAdapter) Open(ctx context.Context, key string) (io.ReadCloser, string, int64, error) {
	return m.storage.OpenObject(ctx, m.bucket, key)
}

func (m *materialsStorageAdapter) Delete(ctx context.Context, key string) error {
	return m.storage.DeleteObject(ctx, m.bucket, key)
}

func (a *App) Handler() http.Handler {
	auth := authapp.New(authpg.New(a.pool), a.hasher, a.now)
	authHandlers := authhttp.New(auth, a.now)
	voice := voiceapp.New(voicepg.New(a.pool), a.recognizer(), a.synthesizer(), a.now)
	voiceHandlers := voicehttp.New(voice, a.cfg.MaxUploadBytes)
	subjectService := subjectapp.New(subjectpg.New(a.pool), security.IDGenerator{}, a.now)
	subjectService.SetAudioProgress(a.warmer, subjectpg.NewLearningAudioProgress(a.pool))
	subjectService.SetExamAudioRefresh(a.graphWorker.RefreshPublishedAudio)
	subjectHandlers := subjecthttp.New(subjectService)

	materialsOCR := materialsocr.New(a.cfg.CourseGraphURL, a.cfg.CourseGraphTimeout)
	materialsService := materialsapp.New(materialspg.New(a.pool), a.materialsStorage, materialsOCR, a.now)
	// Course materials have their own per-file limit; BACKEND_MAX_UPLOAD_BYTES is the STT limit.
	materialsHandlers := materialshttp.NewHandler(materialsService, materialshttp.MaxUploadFileBytes)

	// Geek examines a course by its published graph only.
	geekService := geekapp.New(geekgraph.NewStored(a.pool), geekpg.New(a.pool), geekpg.NewTranscriptions(a.pool), a.examiner, a.speech,
		security.IDGenerator{}, a.now, geekapp.Config{MaxQuestions: a.cfg.GeekMaxQuestions})
	geekService.SetRawMaterials(geekgraph.NewRaw(a.pool, a.materialsStorage))
	geekService.SetSummaries(a.summarizer, geekpg.New(a.pool))
	geekHandlers := geekhttp.New(geekService)
	learningSubjects := subjecthttp.New(subjectapp.New(subjectpg.New(a.pool), security.IDGenerator{}, a.now))
	competencyHandlers := competencyhttp.New(competencyapp.New(competencypg.New(a.pool), &csvparser.Parser{}, a.now), a.cfg.MaxUploadBytes)
	variantHandlers := variantgenhttp.New(variantgenapp.New(a.variantRepository, variantgenrandom.Chooser{}, security.IDGenerator{}, a.now).
		WithGraphLabels(variantgenlabels.New(variantgenlabels.NewPostgresStore(a.pool), a.gatewayClient, a.cfg.LLMTimeout)))

	mux := http.NewServeMux()
	examMux := http.NewServeMux()
	mux.HandleFunc("POST /auth/register", authHandlers.Register)
	mux.HandleFunc("POST /auth/login", authHandlers.Login)
	mux.HandleFunc("POST /auth/teacher", authHandlers.LoginTeacher)
	mux.HandleFunc("POST /auth/refresh", authHandlers.Refresh)
	mux.HandleFunc("POST /auth/logout", authHandlers.Logout)
	mux.HandleFunc("GET /auth/me", authHandlers.Me)
	examMux.Handle("GET /admin/material-kinds", protect(auth, http.HandlerFunc(materialsHandlers.ListKinds)))
	examMux.Handle("PATCH /admin/subjects/{subject_id}/materials/{material_id}", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(materialsHandlers.UpdateMaterialMetadata))))
	examMux.Handle("PUT /admin/subjects/{subject_id}/materials/{material_id}/raw", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(materialsHandlers.ReplaceMaterial))))
	examMux.Handle("POST /admin/subjects/{subject_id}/materials", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(materialsHandlers.UploadMaterials))))
	examMux.Handle("GET /admin/subjects/{subject_id}/materials", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(materialsHandlers.ListMaterials))))
	graphHandlers := graphhttp.New(a.graphWorker)
	examMux.Handle("GET /admin/subjects/{subject_id}/graph", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(graphHandlers.Read))))
	examMux.Handle("GET /admin/subjects/{subject_id}/audio-preparation", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(subjectHandlers.AudioPreparation))))
	examMux.Handle("POST /admin/subjects/{subject_id}/graph/build", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(graphHandlers.Build))))
	examMux.Handle("POST /admin/subjects/{subject_id}/graph/competency-map", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(graphHandlers.ImportMap))))
	examMux.Handle("POST /admin/subjects/{subject_id}/graph/competency-map/skip-missing-criteria", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(graphHandlers.SkipMissingCriteria))))
	examMux.Handle("POST /admin/subjects/{subject_id}/graph/competency-map/retry", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(graphHandlers.RetryMap))))
	examMux.Handle("DELETE /admin/subjects/{subject_id}/materials/{material_id}", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(materialsHandlers.DeleteMaterial))))
	examMux.Handle("GET /admin/subjects/{subject_id}/materials/{material_id}/raw", protect(auth, subjectpg.RequireSubject(a.pool, http.HandlerFunc(materialsHandlers.DownloadRaw))))
	examMux.Handle("POST /admin/subjects", protect(auth, http.HandlerFunc(subjectHandlers.Create)))
	mux.Handle("POST /voice/transcriptions", protect(auth, http.HandlerFunc(voiceHandlers.Transcribe)))
	mux.Handle("POST /voice/syntheses", protect(auth, http.HandlerFunc(voiceHandlers.Synthesize)))
	examMux.Handle("GET /subjects", protect(auth, http.HandlerFunc(subjectHandlers.List)))
	examMux.Handle("POST /diagnostic-sessions", protect(auth, http.HandlerFunc(geekHandlers.Start)))
	examMux.Handle("GET /diagnostic-sessions", protect(auth, http.HandlerFunc(geekHandlers.History)))
	examMux.Handle("GET /diagnostic-sessions/{id}", protect(auth, http.HandlerFunc(geekHandlers.Read)))
	examMux.Handle("POST /diagnostic-sessions/{id}/answers", protect(auth, http.HandlerFunc(geekHandlers.Answer)))
	examMux.Handle("GET /diagnostic-sessions/{id}/result", protect(auth, http.HandlerFunc(geekHandlers.Result)))
	examMux.Handle("GET /diagnostic-sessions/{id}/feedback", protect(auth, http.HandlerFunc(geekHandlers.ResultSummary)))
	examMux.Handle("GET /diagnostic-sessions/{id}/materials/{material_id}/raw", protect(auth, http.HandlerFunc(geekHandlers.RawMaterial)))
	examMux.Handle("GET /diagnostic-sessions/{id}/turns/{turn_id}/audio", protect(auth, http.HandlerFunc(geekHandlers.TurnAudio)))
	mux.Handle("/exam/", http.StripPrefix("/exam", examMux))

	learnMux := http.NewServeMux()
	a.registerLearningRoutes(learnMux, auth, voice, learningSubjects, competencyHandlers, variantHandlers)
	mux.Handle("/learn/", http.StripPrefix("/learn", learnMux))
	mux.HandleFunc("GET /health", a.health)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(r.Context(), w, fault.New(fault.NotFound, "NOT_FOUND", "Ресурс не найден."))
	})
	return httpx.RequestLogger(a.logger, a.middleware(mux))
}

func protect(auth *authapp.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The admin panel sends its portal header and its own session cookie.
		access, _ := httpx.SessionCookieNames(r)
		principal, err := auth.Me(r.Context(), httpx.Cookie(r, access))
		if err != nil {
			httpx.Error(r.Context(), w, err)
			return
		}
		next.ServeHTTP(w, httpx.WithPrincipal(r, principal))
	})
}

type HealthResponse struct {
	Status string `json:"status" enums:"ok"`
}

// health handles GET /health.
// @Summary Проверить API и подключение к PostgreSQL
// @ID health
// @Tags Сервис
// @Produce json
// @Success 200 {object} HealthResponse
// @Failure 503 {object} fault.Error
// @Router /health [get]
func (a *App) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := a.pool.Ping(ctx); err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	httpx.JSON(w, 200, HealthResponse{Status: "ok"})
}
