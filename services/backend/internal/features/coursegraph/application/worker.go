package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/material"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/logx"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/security"
	"go.uber.org/zap"
)

type Repository interface {
	Claim(context.Context, string, time.Time) (coursegraph.Job, error)
	Finish(context.Context, coursegraph.Job, json.RawMessage, time.Time) (bool, error)
	Fail(context.Context, coursegraph.Job, string, time.Time) error
	Read(context.Context, string) (coursegraph.Snapshot, error)
	Request(context.Context, string, time.Time) (bool, error)
	RetryMap(ctx context.Context, subjectID, expectedFingerprint string, now time.Time) (bool, error)
	// Published lists the graphs Geek examines by.
	Published(context.Context) ([]PublishedGraph, error)
	// SaveMap stores an imported competency map and queues its build; false
	// when a build is already queued or running.
	SaveMap(ctx context.Context, subjectID, filename, sheet string, graph json.RawMessage, now time.Time) (bool, error)
	// SaveMapIfCurrent rejects a changed fingerprint or an unpublished map atomically.
	SaveMapIfCurrent(ctx context.Context, subjectID, filename, sheet string, graph json.RawMessage, expectedFingerprint string, now time.Time) (bool, error)
	// MapGraph is the graph of the imported competency map.
	MapGraph(context.Context, string) (json.RawMessage, error)
}
type PublishedGraph struct {
	SubjectID string
	Graph     json.RawMessage
}
type Materials interface {
	List(context.Context, string) ([]material.Material, error)
}
type Storage interface {
	Get(context.Context, string) ([]byte, error)
}

// Builder runs a build synchronously; report receives its progress while it
// runs and is not called after Build returns.
type Builder interface {
	Build(ctx context.Context, buildID string, materials []coursegraph.Material, report func(coursegraph.BuildProgress)) (json.RawMessage, error)
}

// Voices synthesizes prepared questions in the background; Prepare queues
// them without waiting.
type Voices interface {
	Prepare(texts []string) int
	TrackPublished(subjectID, graphVersion string, texts []string)
	TrackRestored(subjectID, graphVersion string, texts []string)
}

// Questions prepares the main question and its rephrase of every topic of a
// build before it is published, so Geek asks them without waiting for the
// model; an error means some topic is still unprepared and the build is not
// published (decision of 18.10.2026).
type Questions interface {
	Prepare(ctx context.Context, graph json.RawMessage) (json.RawMessage, error)
}

// LearningMapStager stores the second workbook sheet as an inactive learning
// projection and returns its revision for atomic publication with the graph.
type LearningMapStager func(context.Context, user.User, string, []byte, []coursegraph.MapSheet) (int64, error)
type LearningMapAborter func(context.Context, string, int64) error

// MapReader reads the worksheets of an imported competency map book.
type MapReader func(data []byte) ([]coursegraph.MapSheet, error)

type Worker struct {
	repo             Repository
	materials        Materials
	storage          Storage
	builder          Builder
	voices           Voices
	questions        Questions
	maps             MapReader
	stageLearningMap LearningMapStager
	abortLearningMap LearningMapAborter
	// progress of running builds by subject, shown in snapshots.
	mu            sync.Mutex
	progress      map[string]coursegraph.BuildProgress
	publicationMu sync.Mutex
}

// New builds the graph worker; voices may be nil, then questions are voiced
// only when a student first hears them. maps may be nil, then a competency
// map cannot be imported.
func New(repo Repository, materials Materials, storage Storage, builder Builder, voices Voices, maps MapReader) *Worker {
	return &Worker{repo: repo, materials: materials, storage: storage, builder: builder, voices: voices, maps: maps,
		progress: map[string]coursegraph.BuildProgress{}}
}

// SetQuestions makes builds prepare their questions before publication;
// without it a build is published with the questions course-graph wrote.
func (w *Worker) SetQuestions(q Questions) { w.questions = q }

func (w *Worker) SetLearningMapStager(stager LearningMapStager)    { w.stageLearningMap = stager }
func (w *Worker) SetLearningMapAborter(aborter LearningMapAborter) { w.abortLearningMap = aborter }

func (w *Worker) setProgress(subjectID string, p *coursegraph.BuildProgress) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p == nil {
		delete(w.progress, subjectID)
		return
	}
	w.progress[subjectID] = *p
}

func (w *Worker) progressOf(subjectID string) (coursegraph.BuildProgress, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.progress[subjectID]
	return p, ok
}

// PrepareVoices queues the questions of every published graph for synthesis.
// The queue lives in memory, so after a restart the questions of a graph
// built before it would otherwise be voiced only when a student reaches them.
// Questions already cached cost one cache read.
func (w *Worker) PrepareVoices(ctx context.Context) error {
	if w.voices == nil {
		return nil
	}
	w.publicationMu.Lock()
	defer w.publicationMu.Unlock()
	graphs, err := w.repo.Published(ctx)
	if err != nil {
		return err
	}
	queued, total := 0, 0
	for _, graph := range graphs {
		questions := TopicQuestions(graph.Graph)
		total += len(questions)
		w.voices.TrackRestored(graph.SubjectID, GraphVersion(graph.Graph), questions)
		queued += w.voices.Prepare(questions)
	}
	logx.From(ctx).Info("published graph questions queued for synthesis", zap.Int("queued", queued), zap.Int("total", total), zap.Int("graphs", len(graphs)))
	return nil
}

// RefreshPublishedAudio restores a course omitted by startup recovery after
// it becomes published again while the backend is running.
func (w *Worker) RefreshPublishedAudio(ctx context.Context, subjectID string) error {
	if w.voices == nil {
		return nil
	}
	w.publicationMu.Lock()
	defer w.publicationMu.Unlock()
	graphs, err := w.repo.Published(ctx)
	if err != nil {
		return err
	}
	for _, graph := range graphs {
		if graph.SubjectID != subjectID {
			continue
		}
		questions := TopicQuestions(graph.Graph)
		w.voices.TrackRestored(subjectID, GraphVersion(graph.Graph), questions)
		w.voices.Prepare(questions)
		break
	}
	return nil
}

// GraphVersion follows the hash used by Geek for a stored graph.
func GraphVersion(graph json.RawMessage) string {
	sum := sha256.Sum256(graph)
	return "pg:" + hex.EncodeToString(sum[:8])
}

// TopicQuestions are the prepared texts of a built graph that Geek asks as
// is: the question of each topic and, with it, its rephrase, or those of each
// variant of a topic of a competency map.
func TopicQuestions(graph json.RawMessage) []string {
	type asked struct {
		Question string `json:"question"`
		Rephrase string `json:"rephrase"`
	}
	var shape struct {
		Topics []struct {
			asked
			Variants []asked `json:"variants"`
		} `json:"topics"`
	}
	out := []string{}
	if json.Unmarshal(graph, &shape) != nil {
		return out
	}
	for _, t := range shape.Topics {
		texts := t.Variants
		if len(texts) == 0 {
			texts = []asked{t.asked}
		}
		for _, a := range texts {
			q := strings.TrimSpace(a.Question)
			if q == "" {
				continue
			}
			out = append(out, q)
			if r := strings.TrimSpace(a.Rephrase); r != "" {
				out = append(out, r)
			}
		}
	}
	return out
}

func (w *Worker) Read(ctx context.Context, actor user.User, subjectID string) (coursegraph.Snapshot, error) {
	if actor.Role != user.Admin {
		return coursegraph.Snapshot{}, fault.New(fault.Forbidden, "FORBIDDEN", "Доступно только admin.")
	}
	return w.snapshot(ctx, subjectID)
}

// snapshot shows the admin the graph Geek uses, or none.
func (w *Worker) snapshot(ctx context.Context, subjectID string) (coursegraph.Snapshot, error) {
	snapshot, err := w.repo.Read(ctx, subjectID)
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	if p, ok := w.progressOf(subjectID); ok && snapshot.Status == "processing" {
		snapshot.Progress = &p
	}
	source := snapshot.GraphSource
	snapshot.GraphSource = coursegraph.SourceNone
	if len(snapshot.Graph) == 0 || string(snapshot.Graph) == "null" {
		snapshot.Graph = nil
		return snapshot, nil
	}
	if source == coursegraph.SourceMap {
		snapshot.GraphSource = coursegraph.SourceMap
		return snapshot, nil
	}
	// Geek reads a graph of materials only while the course has materials
	// (ReadPublishedCourseGraph).
	items, err := w.materials.List(ctx, subjectID)
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	if len(items) == 0 {
		snapshot.Graph = nil
		return snapshot, nil
	}
	snapshot.GraphSource = coursegraph.SourceBuilt
	return snapshot, nil
}

// ImportMap builds the course graph from a competency map instead of the
// materials (the admin's "import competency map" button): the map is read
// and checked at once, its questions are prepared and published by the
// worker like those of a build.
func (w *Worker) ImportMap(ctx context.Context, actor user.User, subjectID, filename string, data []byte) (coursegraph.Snapshot, error) {
	if actor.Role != user.Admin {
		return coursegraph.Snapshot{}, fault.New(fault.Forbidden, "FORBIDDEN", "Доступно только admin.")
	}
	if w.maps == nil {
		return coursegraph.Snapshot{}, fault.New(fault.Unavailable, "COMPETENCY_MAP_UNAVAILABLE", "Импорт карты компетенций не настроен.")
	}
	current, err := w.repo.Read(ctx, subjectID)
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	if current.Status == "pending" || current.Status == "processing" {
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "GRAPH_BUILD_IN_PROGRESS", "Граф курса уже собирается.")
	}
	sheets, err := w.maps(data)
	if err != nil {
		return coursegraph.Snapshot{}, fault.New(fault.Invalid, "COMPETENCY_MAP_INVALID", "Не удалось прочитать книгу Excel (.xlsx).")
	}
	built, err := coursegraph.BuildMapGraph(sheets)
	var mapErr *coursegraph.MapError
	if errors.As(err, &mapErr) {
		return coursegraph.Snapshot{}, mapFault(mapErr)
	}
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	var stagedRevision int64
	if len(sheets) > 1 && w.stageLearningMap != nil {
		revision, stageErr := w.stageLearningMap(ctx, actor, subjectID, data, sheets)
		if stageErr != nil {
			return coursegraph.Snapshot{}, stageErr
		}
		var graph map[string]json.RawMessage
		if err := json.Unmarshal(built.Graph, &graph); err != nil {
			return coursegraph.Snapshot{}, err
		}
		version, _ := json.Marshal(revision)
		graph["learning_revision"] = version
		built.Graph, err = json.Marshal(graph)
		if err != nil {
			if w.abortLearningMap != nil {
				_ = w.abortLearningMap(ctx, subjectID, revision)
			}
			return coursegraph.Snapshot{}, err
		}
		stagedRevision = revision
	}
	var assessment struct {
		Warnings []coursegraph.MapIssue `json:"assessment_warnings"`
	}
	if err := json.Unmarshal(built.Graph, &assessment); err != nil {
		return coursegraph.Snapshot{}, err
	}
	queued, err := w.repo.SaveMap(ctx, subjectID, filename, built.Sheet, built.Graph, time.Now())
	if err != nil {
		if stagedRevision > 0 && w.abortLearningMap != nil {
			if abortErr := w.abortLearningMap(ctx, subjectID, stagedRevision); abortErr != nil {
				return coursegraph.Snapshot{}, errors.Join(err, abortErr)
			}
		}
		return coursegraph.Snapshot{}, err
	}
	if !queued {
		if stagedRevision > 0 && w.abortLearningMap != nil {
			if err := w.abortLearningMap(ctx, subjectID, stagedRevision); err != nil {
				return coursegraph.Snapshot{}, err
			}
		}
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "GRAPH_BUILD_IN_PROGRESS", "Граф курса уже собирается.")
	}
	for _, warning := range assessment.Warnings {
		logx.From(ctx).Warn("course graph uses default assessment criteria", zap.String("subject_id", subjectID), zap.String("filename", filename),
			zap.String("sheet", built.Sheet), zap.Int("row", warning.Row), zap.String("column", warning.Column), zap.String("warning", warning.Message))
	}
	return w.snapshot(ctx, subjectID)
}

// RetryMap restarts preparation of the saved map without importing or filtering it.
func (w *Worker) RetryMap(ctx context.Context, actor user.User, subjectID, expectedFingerprint string) (coursegraph.Snapshot, error) {
	if actor.Role != user.Admin {
		return coursegraph.Snapshot{}, fault.New(fault.Forbidden, "FORBIDDEN", "Доступно только admin.")
	}
	current, err := w.repo.Read(ctx, subjectID)
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	if current.Map == nil || expectedFingerprint == "" || current.Map.Fingerprint != expectedFingerprint {
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "COMPETENCY_MAP_CHANGED", "Карта изменилась. Обновите страницу и повторите подготовку.")
	}
	if current.Status != "failed" || current.BuildSource == nil || *current.BuildSource != coursegraph.SourceMap {
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "GRAPH_RETRY_NOT_AVAILABLE", "Повтор доступен только после ошибки подготовки карты.")
	}
	queued, err := w.repo.RetryMap(ctx, subjectID, expectedFingerprint, time.Now())
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	if !queued {
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "GRAPH_RETRY_NOT_AVAILABLE", "Состояние подготовки изменилось. Обновите страницу.")
	}
	return w.snapshot(ctx, subjectID)
}

// SkipMissingCriteria queues a replacement map after the admin's confirmation.
func (w *Worker) SkipMissingCriteria(ctx context.Context, actor user.User, subjectID, expectedFingerprint string) (coursegraph.Snapshot, error) {
	if actor.Role != user.Admin {
		return coursegraph.Snapshot{}, fault.New(fault.Forbidden, "FORBIDDEN", "Доступно только admin.")
	}
	current, err := w.repo.Read(ctx, subjectID)
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	if current.Status == "pending" || current.Status == "processing" {
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "GRAPH_BUILD_IN_PROGRESS", "Граф курса уже собирается.")
	}
	if current.Map == nil || current.GraphSource != coursegraph.SourceMap {
		return coursegraph.Snapshot{}, fault.New(fault.Invalid, "COMPETENCY_MAP_INVALID", "Сначала импортируйте карту для экзамена.")
	}
	if current.Stale || current.Status != "ready" || expectedFingerprint == "" || current.Map.Fingerprint != expectedFingerprint {
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "COMPETENCY_MAP_CHANGED", "Карта изменилась или ещё не опубликована. Обновите граф и подтвердите исключение заново.")
	}
	source, err := w.repo.MapGraph(ctx, subjectID)
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	graph, err := coursegraph.SkipMissingCriteria(source)
	var mapErr *coursegraph.MapError
	if errors.As(err, &mapErr) {
		return coursegraph.Snapshot{}, mapFault(mapErr)
	}
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	if string(graph) == string(source) {
		return w.snapshot(ctx, subjectID)
	}
	queued, err := w.repo.SaveMapIfCurrent(ctx, subjectID, current.Map.Filename, current.Map.Sheet, graph, expectedFingerprint, time.Now())
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	if !queued {
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "GRAPH_BUILD_IN_PROGRESS", "Граф курса уже собирается.")
	}
	return w.snapshot(ctx, subjectID)
}

// mapFault names the cells of a rejected map.
func mapFault(e *coursegraph.MapError) *fault.Error {
	f := fault.New(fault.Invalid, e.Code, e.Message)
	for _, issue := range e.Issues {
		message := issue.Message
		if issue.Row > 0 {
			message = fmt.Sprintf("Строка %d, «%s»: %s", issue.Row, issue.Column, issue.Message)
		}
		f.Details = append(f.Details, fault.Detail{Path: "file", Code: "COMPETENCY_MAP_CELL", Message: message})
	}
	return f
}

// Request starts a build of the course graph from the current materials (the
// admin's "build graph" button).
func (w *Worker) Request(ctx context.Context, actor user.User, subjectID string) (coursegraph.Snapshot, error) {
	if actor.Role != user.Admin {
		return coursegraph.Snapshot{}, fault.New(fault.Forbidden, "FORBIDDEN", "Доступно только admin.")
	}
	current, err := w.repo.Read(ctx, subjectID)
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	if current.Status == "pending" || current.Status == "processing" {
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "GRAPH_BUILD_IN_PROGRESS", "Граф курса уже собирается.")
	}
	items, err := w.materials.List(ctx, subjectID)
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	missing := []string{}
	allProcessed := len(items) > 0
	for _, item := range items {
		if item.Status != material.StatusParsed || item.Cleaning == nil || item.Cleaning.Status != "ready" {
			allProcessed = false
		}
		if item.Week == nil || *item.Week <= 0 || item.Kind == nil {
			missing = append(missing, item.ID)
		}
	}
	if allProcessed && len(missing) > 0 {
		f := fault.New(fault.Invalid, "MATERIAL_METADATA_INCOMPLETE", "Классифицируйте неделю и вид у каждого материала перед сборкой графа.")
		for _, id := range missing {
			f.Details = append(f.Details, fault.Detail{Path: "materials", Code: "MATERIAL_METADATA_INCOMPLETE", Message: id})
		}
		return coursegraph.Snapshot{}, f
	}
	queued, err := w.repo.Request(ctx, subjectID, time.Now())
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	snapshot, err := w.snapshot(ctx, subjectID)
	switch {
	case err != nil:
		return coursegraph.Snapshot{}, err
	case queued:
		return snapshot, nil
	case snapshot.Status == "pending" || snapshot.Status == "processing":
		return coursegraph.Snapshot{}, fault.New(fault.Conflict, "GRAPH_BUILD_IN_PROGRESS", "Граф курса уже собирается.")
	default:
		return coursegraph.Snapshot{}, fault.New(fault.Invalid, "GRAPH_INPUTS_NOT_READY",
			"Граф можно собрать, когда все материалы курса обработаны.")
	}
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		jobCtx := logx.StartTrace(ctx)
		worked, err := w.ProcessOne(jobCtx)
		if err != nil && ctx.Err() == nil {
			logx.From(jobCtx).Error("course graph job failed", zap.Error(err))
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	token, err := security.Token()
	if err != nil {
		return false, err
	}
	job, err := w.repo.Claim(ctx, token, time.Now())
	if err != nil || job.SubjectID == "" {
		return false, err
	}
	if owner, ok := w.repo.(interface {
		Renew(context.Context, coursegraph.Job, time.Time) (bool, error)
		Release(context.Context, coursegraph.Job, time.Time) error
	}); ok {
		err = runWithLease(ctx, job, owner.Renew, owner.Release, func(taskCtx context.Context) error { return w.process(taskCtx, job) })
	} else {
		err = w.process(ctx, job)
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, errGraphLeaseLost) {
			return true, nil
		}
		failureCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		message := "Не удалось построить граф; предыдущий граф сохранён."
		if errors.Is(err, errQuestionsNotPrepared) {
			message = "Граф собран, но вопросы к темам не подготовлены (LLM недоступна); предыдущий граф сохранён."
		}
		if failure := w.repo.Fail(failureCtx, job, message, time.Now()); failure != nil {
			return true, fmt.Errorf("%v; record failure: %w", err, failure)
		}
	}
	return true, err
}

var errGraphLeaseLost = errors.New("course graph lease lost")

// errQuestionsNotPrepared marks a build whose graph is ready but whose
// questions are not, so the admin learns that only that step failed.
var errQuestionsNotPrepared = errors.New("graph questions not prepared")

func runWithLease(ctx context.Context, job coursegraph.Job,
	renew func(context.Context, coursegraph.Job, time.Time) (bool, error),
	release func(context.Context, coursegraph.Job, time.Time) error,
	process func(context.Context) error) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- process(workCtx) }()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if ctx.Err() != nil {
				releaseCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
				_ = release(releaseCtx, job, time.Now())
				stop()
				return ctx.Err()
			}
			return err
		case <-ctx.Done():
			cancel()
			<-done
			releaseCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			_ = release(releaseCtx, job, time.Now())
			stop()
			return ctx.Err()
		case <-ticker.C:
			heartbeatCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			owned, err := renew(heartbeatCtx, job, time.Now())
			stop()
			if err != nil || !owned {
				cancel()
				<-done
				if err != nil {
					return err
				}
				return errGraphLeaseLost
			}
		}
	}
}

func (w *Worker) process(ctx context.Context, job coursegraph.Job) error {
	defer w.setProgress(job.SubjectID, nil)
	if job.Attempts > 3 {
		return fmt.Errorf("graph attempt limit reached")
	}
	var (
		graph json.RawMessage
		err   error
	)
	if job.Source == coursegraph.SourceMap {
		// The map was read and checked on import; only its questions remain.
		if graph, err = w.repo.MapGraph(ctx, job.SubjectID); err != nil {
			return err
		}
		var shape struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(graph, &shape) != nil || (shape.Version != coursegraph.MapVersion && shape.Version != coursegraph.ExplicitMapVersion) {
			return fmt.Errorf("invalid competency map graph")
		}
	} else if graph, err = w.buildMaterials(ctx, job); err != nil {
		return err
	}
	return w.publish(ctx, job, graph)
}

// buildMaterials builds the graph of the course materials with course-graph.
func (w *Worker) buildMaterials(ctx context.Context, job coursegraph.Job) (json.RawMessage, error) {
	items, err := w.materials.List(ctx, job.SubjectID)
	if err != nil {
		return nil, err
	}
	inputs := make([]coursegraph.Material, 0, len(items))
	for _, m := range items {
		if m.Cleaning == nil || m.Cleaning.Status != "ready" {
			return nil, fmt.Errorf("material %s is not ready", m.ID)
		}
		source, err := w.storage.Get(ctx, m.S3ParsedKey)
		if err != nil {
			return nil, err
		}
		report, err := w.storage.Get(ctx, m.Cleaning.S3ReportKey)
		if err != nil {
			return nil, err
		}
		var cached struct {
			Topics        json.RawMessage `json:"topics"`
			TopicsVersion string          `json:"topics_version"`
		}
		if err = json.Unmarshal(report, &cached); err != nil {
			return nil, err
		}
		var kind *string
		if m.Kind != nil {
			value := string(*m.Kind)
			kind = &value
		}
		revision := sha256.Sum256([]byte(m.S3RawKey))
		input := coursegraph.Material{ID: m.ID, Markdown: string(source), Week: m.Week, Kind: kind,
			Filename: m.Filename, KindLabel: m.KindLabel, MaterialRevision: hex.EncodeToString(revision[:])}
		// Older cards carry questions of an earlier policy (topics-v2 could
		// ask to solve or draw): course-graph extracts them again instead.
		if cached.TopicsVersion == "topics-v4" {
			input.Topics = cached.Topics
		}
		inputs = append(inputs, input)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("empty course")
	}
	// The build ID tells course-graph's progress of this call from another:
	// a reclaimed lease keeps the attempt number while the previous call may
	// still run there, so each call gets its own random ID, never the lease
	// token.
	random, err := security.Token()
	if err != nil {
		return nil, err
	}
	buildID := job.SubjectID + "#" + random
	graph, err := w.builder.Build(ctx, buildID, inputs, func(p coursegraph.BuildProgress) { p.StageCount += 2; w.setProgress(job.SubjectID, &p) })
	if err != nil {
		return nil, err
	}
	var shape struct {
		Version       string            `json:"version"`
		Topics        []json.RawMessage `json:"topics"`
		Prerequisites []json.RawMessage `json:"prerequisites"`
		Quality       struct {
			Acyclic bool `json:"acyclic"`
		} `json:"quality"`
	}
	if json.Unmarshal(graph, &shape) != nil || shape.Version != "course-graph-v1" || !shape.Quality.Acyclic || len(shape.Topics) == 0 || shape.Prerequisites == nil {
		return nil, fmt.Errorf("invalid graph result")
	}
	return graph, nil
}

// publish prepares the questions of a graph of either source and publishes
// it unless its source changed meanwhile.
func (w *Worker) publish(ctx context.Context, job coursegraph.Job, graph json.RawMessage) error {
	var err error
	stage, count := 1, 2
	if job.Source != coursegraph.SourceMap {
		stage, count = 6, 7
	}
	report := func(p coursegraph.BuildProgress) {
		p.StageIndex = stage
		p.StageCount = count
		w.setProgress(job.SubjectID, &p)
	}
	report(coursegraph.BuildProgress{Stage: "prepare_questions"})
	if w.questions != nil {
		if q, ok := w.questions.(interface {
			PrepareWithProgress(context.Context, json.RawMessage, func(coursegraph.BuildProgress)) (json.RawMessage, error)
		}); ok {
			graph, err = q.PrepareWithProgress(ctx, graph, report)
		} else {
			graph, err = w.questions.Prepare(ctx, graph)
		}
		if err != nil {
			return fmt.Errorf("%w: %w", errQuestionsNotPrepared, err)
		}
	}
	// The questions are voiced in the background before the graph is
	stage++
	report(coursegraph.BuildProgress{Stage: "publish"})
	// Preserve the existing pre-publication queueing behavior. The progress registry only tracks the version if Finish accepts it.
	questions := TopicQuestions(graph)
	if w.voices != nil {
		logx.From(ctx).Info("course graph questions queued for synthesis", zap.String("subject_id", job.SubjectID), zap.Int("queued", w.voices.Prepare(questions)), zap.Int("total", len(questions)))
	}
	accepted, err := w.finishPublication(ctx, job, graph, questions)
	if err != nil || accepted {
		return err
	}
	// The source changed during the build (materials, or a new map): queue
	// it again so the next claim takes it, without spending an attempt.
	if owner, ok := w.repo.(interface {
		Release(context.Context, coursegraph.Job, time.Time) error
	}); ok {
		return owner.Release(context.WithoutCancel(ctx), job, time.Now())
	}
	return nil
}

// Serialize publication and progress registration with startup recovery.
func (w *Worker) finishPublication(ctx context.Context, job coursegraph.Job, graph json.RawMessage, questions []string) (bool, error) {
	w.publicationMu.Lock()
	defer w.publicationMu.Unlock()
	accepted, err := w.repo.Finish(ctx, job, graph, time.Now())
	if err == nil && accepted && w.voices != nil {
		versionGraph := graph
		if snapshot, readErr := w.repo.Read(ctx, job.SubjectID); readErr == nil && len(snapshot.Graph) > 0 {
			versionGraph = snapshot.Graph
		}
		w.voices.TrackPublished(job.SubjectID, GraphVersion(versionGraph), questions)
		w.voices.Prepare(questions)
		logx.From(ctx).Info("published course graph questions queued for synthesis", zap.String("subject_id", job.SubjectID), zap.Int("total", len(questions)))
	}
	return accepted, err
}
