// Package postgres persists diagnostic session start and read state in PostgreSQL.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	db "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool         *pgxpool.Pool
	q            *db.Queries
	leaseSeconds int64
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: db.New(pool), leaseSeconds: 600}
}

func (s *Store) WithLease(lease time.Duration) *Store {
	if lease > 0 {
		s.leaseSeconds = int64(lease / time.Second)
		if lease%time.Second != 0 {
			s.leaseSeconds++
		}
	}
	return s
}

func (s *Store) FindStart(ctx context.Context, ownerID, key, digest string) (diagnostic.Session, bool, error) {
	row, err := s.q.LearningFindDiagnosticSessionByStart(ctx, db.LearningFindDiagnosticSessionByStartParams{OwnerID: ownerID, StartRequestKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return diagnostic.Session{}, false, nil
	}
	if err != nil {
		return diagnostic.Session{}, false, err
	}
	if row.StartRequestDigest != digest {
		return diagnostic.Session{}, false, startKeyConflict()
	}
	value, err := decodeSession(row.SessionData)
	return value, true, err
}

func (s *Store) Create(ctx context.Context, ownerID, key, digest string, value diagnostic.Session) (diagnostic.Session, bool, error) {
	if value.ID == "" || value.OwnerID != ownerID || value.VariantID == "" || key == "" || digest == "" {
		return diagnostic.Session{}, false, fault.Validation("diagnostic_session", "Данные диагностической сессии некорректны.")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return diagnostic.Session{}, false, err
	}
	rows, err := s.q.LearningCreateDiagnosticSession(ctx, db.LearningCreateDiagnosticSessionParams{
		ID: value.ID, OwnerID: ownerID, VariantID: value.VariantID, Status: value.Status,
		CurrentCompetency: int32(value.CurrentCompetency), CurrentTask: int32(value.CurrentTask),
		StartRequestKey: key, StartRequestDigest: digest, SessionData: encoded,
	})
	if err != nil {
		return diagnostic.Session{}, false, err
	}
	if rows == 0 {
		previous, found, err := s.FindStart(ctx, ownerID, key, digest)
		if err != nil {
			return diagnostic.Session{}, false, err
		}
		if found {
			return previous, true, nil
		}
		return diagnostic.Session{}, false, variantNotFound()
	}
	stored, err := s.Get(ctx, ownerID, value.ID)
	return stored, false, err
}

func (s *Store) Get(ctx context.Context, ownerID, id string) (diagnostic.Session, error) {
	data, err := s.q.LearningGetDiagnosticSession(ctx, db.LearningGetDiagnosticSessionParams{OwnerID: ownerID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return diagnostic.Session{}, sessionNotFound()
	}
	if err != nil {
		return diagnostic.Session{}, err
	}
	return decodeSession(data)
}

func (s *Store) LatestCompleted(ctx context.Context, ownerID, subjectID string) (diagnostic.Session, bool, error) {
	data, err := s.q.LearningLatestCompletedDiagnostic(ctx, db.LearningLatestCompletedDiagnosticParams{OwnerID: ownerID, SubjectID: subjectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return diagnostic.Session{}, false, nil
	}
	if err != nil {
		return diagnostic.Session{}, false, err
	}
	value, err := decodeSession(data)
	return value, err == nil, err
}
func (s *Store) LearningState(ctx context.Context, ownerID, subjectID string) (diagnostic.LearningState, error) {
	row, err := s.q.LearningReadSubjectLearningState(ctx, db.LearningReadSubjectLearningStateParams{OwnerID: ownerID, SubjectID: subjectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return diagnostic.LearningState{}, fault.New(fault.NotFound, "SUBJECT_NOT_FOUND", "Предмет не найден.")
	}
	if err != nil {
		return diagnostic.LearningState{}, err
	}
	state := diagnostic.LearningState{SubjectID: row.ID, SubjectName: row.Name, DiagnosticSessionID: row.CompletedID, ActiveSessionID: row.ActiveID, DiagnosticStatus: "not_started", DiagnosticCompleted: row.CompletedID != "", TrainingAvailable: row.CompletedID != ""}
	if row.CompletedID != "" {
		completed, err := s.Get(ctx, ownerID, row.CompletedID)
		if err != nil {
			return diagnostic.LearningState{}, err
		}
		state.TrainingAvailable = diagnostic.HasLearningGaps(completed.Answers)
		state.DiagnosticStatus = "completed"
	} else if row.ActiveID != "" {
		state.DiagnosticStatus = "active"
	}
	return state, nil
}

func (s *Store) Reserve(ctx context.Context, ownerID, id, key, digest, taskID, token string) (*diagnostic.AcceptedRequest, *diagnostic.Reservation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	row, err := q.LearningLockDiagnosticSession(ctx, db.LearningLockDiagnosticSessionParams{OwnerID: ownerID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, sessionNotFound()
	} else if err != nil {
		return nil, nil, err
	}
	value, err := decodeSession(row.SessionData)
	if err != nil {
		return nil, nil, err
	}
	if previous, ok := value.AcceptedRequests[key]; ok {
		if previous.Fingerprint != digest {
			return nil, nil, startKeyConflict()
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
		return &previous, nil, nil
	}
	if value.Status != diagnostic.StatusActive {
		return nil, nil, fault.New(fault.Conflict, "DIAGNOSTIC_SESSION_COMPLETED", "Диагностическая сессия уже завершена.")
	}
	current := value.Current()
	if current == nil || current.ID != taskID {
		return nil, nil, fault.New(fault.Conflict, "DIAGNOSTIC_TASK_NOT_CURRENT", "Укажите текущее задание сессии.")
	}
	if p := value.InFlight; p != nil && p.Token != "" && row.Leased {
		return nil, nil, fault.New(fault.Conflict, "DIAGNOSTIC_ANSWER_IN_PROGRESS", "Ответ для текущего задания уже обрабатывается.")
	}
	// A failed attempt was never accepted, so a new recording or skip replaces it.
	if p := value.InFlight; p != nil && p.Key == key && p.Fingerprint == digest && p.VariantTaskID == taskID {
		p.Token = token
	} else {
		value.InFlight = &diagnostic.Reservation{Key: key, Fingerprint: digest, VariantTaskID: taskID, Token: token}
	}
	if err = s.saveLocked(ctx, q, value); err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	p := *value.InFlight
	return nil, &p, nil
}

func (s *Store) Accept(ctx context.Context, ownerID, id, token, key, digest string, answer diagnostic.Answer, transition diagnostic.Transition) (diagnostic.Progress, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return diagnostic.Progress{}, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	row, err := q.LearningLockDiagnosticSession(ctx, db.LearningLockDiagnosticSessionParams{OwnerID: ownerID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return diagnostic.Progress{}, sessionNotFound()
	} else if err != nil {
		return diagnostic.Progress{}, err
	}
	value, err := decodeSession(row.SessionData)
	if err != nil {
		return diagnostic.Progress{}, err
	}
	if !row.Leased || value.InFlight == nil || value.InFlight.Token != token || value.InFlight.Key != key || value.InFlight.Fingerprint != digest || value.InFlight.VariantTaskID != answer.VariantTaskID {
		return diagnostic.Progress{}, fault.New(fault.Conflict, "DIAGNOSTIC_ANSWER_STALE", "Обработка ответа устарела.")
	}
	value.Answers = append(value.Answers, answer)
	value.CurrentCompetency = transition.CurrentCompetency
	value.CurrentTask = transition.CurrentTask
	value.Traversal = append(json.RawMessage(nil), transition.Traversal...)
	value.CurrentPosition = transition.CurrentPosition
	value.Status = transition.Status
	value.SkippedBasics = append(value.SkippedBasics, transition.SkippedBasics...)
	value.InFlight = nil
	progress := value.Progress()
	progress.Text = answer.Text
	progress.AnswerSkipped = answer.Skipped
	score, gs, gm := answer.Score, answer.GraderScore, answer.GraderMaxScore
	progress.Score = &score
	progress.GraderScore = &gs
	progress.GraderMaxScore = &gm
	progress.Verdict = answer.Verdict
	progress.CriterionResults = append([]diagnostic.CriterionResult(nil), answer.CriterionResults...)
	progress.Feedback = answer.Feedback
	if value.AcceptedRequests == nil {
		value.AcceptedRequests = map[string]diagnostic.AcceptedRequest{}
	}
	value.AcceptedRequests[key] = diagnostic.AcceptedRequest{Fingerprint: digest, Response: progress}
	if err = s.saveLocked(ctx, q, value); err != nil {
		return diagnostic.Progress{}, err
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		return diagnostic.Progress{}, err
	}
	pr, err := json.Marshal(progress)
	if err != nil {
		return diagnostic.Progress{}, err
	}
	order := len(value.Answers)
	if err = q.LearningInsertDiagnosticAnswer(ctx, db.LearningInsertDiagnosticAnswerParams{SessionID: id, AnswerOrder: int32(order), IdempotencyKey: key, RequestDigest: digest, VariantTaskID: answer.VariantTaskID, AnswerData: raw, ProgressData: pr}); err != nil {
		return diagnostic.Progress{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return diagnostic.Progress{}, err
	}
	return progress, nil
}

func (s *Store) Fail(ctx context.Context, ownerID, id, token, transcriptionID, transcriptionText string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	row, err := q.LearningLockDiagnosticSession(ctx, db.LearningLockDiagnosticSessionParams{OwnerID: ownerID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionNotFound()
	} else if err != nil {
		return err
	}
	value, err := decodeSession(row.SessionData)
	if err != nil {
		return err
	}
	if value.InFlight == nil || value.InFlight.Token != token {
		return tx.Commit(ctx)
	}
	if transcriptionID != "" {
		value.InFlight.TranscriptionID = transcriptionID
		value.InFlight.TranscriptionText = transcriptionText
		value.InFlight.Token = ""
	} else {
		value.InFlight = nil
	}
	if err = s.saveLocked(ctx, q, value); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) saveLocked(ctx context.Context, q *db.Queries, value diagnostic.Session) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	leaseSeconds := int64(0)
	if value.InFlight != nil && value.InFlight.Token != "" {
		leaseSeconds = s.leaseSeconds
	}
	err = q.LearningSaveDiagnosticSession(ctx, db.LearningSaveDiagnosticSessionParams{ID: value.ID, SessionData: raw, Status: value.Status, CurrentCompetency: int32(value.CurrentCompetency), CurrentTask: int32(value.CurrentTask), LeaseSeconds: leaseSeconds})
	return err
}

func decodeSession(data []byte) (diagnostic.Session, error) {
	var value diagnostic.Session
	if err := json.Unmarshal(data, &value); err != nil {
		return diagnostic.Session{}, err
	}
	return value, nil
}

func startKeyConflict() error {
	return fault.New(fault.Conflict, "IDEMPOTENCY_KEY_REUSED", "Ключ запуска уже использован с другими параметрами.")
}

func variantNotFound() error {
	return fault.New(fault.NotFound, "VARIANT_NOT_FOUND", "Вариант не найден.")
}

func sessionNotFound() error {
	return fault.New(fault.NotFound, "DIAGNOSTIC_SESSION_NOT_FOUND", "Диагностическая сессия не найдена.")
}
