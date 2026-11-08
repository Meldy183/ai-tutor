// Package application runs the Geek oral exam: it starts diagnostics, accepts
// answers, asks the model for questions and evaluations and voices Geek turns.
package application

import (
	"context"
	"errors"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/resultsummary"
)

var (
	ErrNotFound     = errors.New("geek session not found")
	ErrActiveExists = errors.New("active geek session already exists")
	ErrConflict     = errors.New("geek session changed concurrently")
)

// GraphSource returns the current graph of a course; ok is false when the
// course has no graph yet.
type GraphSource interface {
	Current(ctx context.Context, subjectID string) (g exam.Graph, ok bool, err error)
}

type RawMaterial struct {
	Filename, Revision string
	Data               []byte
	Found              bool
}
type RawMaterials interface {
	CurrentRaw(context.Context, string) (RawMaterial, error)
}

// Request is a stored idempotent request and the session it produced.
type Request struct {
	OwnerID     string
	Key         string
	Fingerprint string
	SessionID   string
	Created     bool
	Response    exam.Session
	CreatedAt   int64
}

type Store interface {
	SubjectExists(ctx context.Context, subjectID string) (bool, error)
	Session(ctx context.Context, ownerID, id string) (exam.Session, error)
	ActiveSession(ctx context.Context, ownerID, subjectID string) (exam.Session, bool, error)
	// Sessions lists the owner's diagnostics of a course, newest first.
	Sessions(ctx context.Context, ownerID, subjectID string) ([]exam.Session, error)
	Request(ctx context.Context, ownerID, key string) (Request, bool, error)
	// Create inserts a session; ErrActiveExists if the owner already has an
	// active one on the course.
	Create(ctx context.Context, s exam.Session, r Request) error
	// Update saves s if its stored version is still previous; otherwise
	// ErrConflict. A non-nil request is saved in the same transaction.
	Update(ctx context.Context, s exam.Session, previous int, r *Request) error
	// SaveRequest records a request that did not change a session; a request
	// already stored under the key is kept.
	SaveRequest(ctx context.Context, r Request) error
}

// Summarizer writes the overall conclusion of a completed exam.
type Summarizer interface {
	Summarize(ctx context.Context, report resultsummary.Report) (string, error)
}

// SummaryStore keeps the conclusion once it is written, so the student always
// reads the same text. The first saved text of a session stays.
type SummaryStore interface {
	ResultSummary(ctx context.Context, ownerID, sessionID string) (string, bool, error)
	SaveResultSummary(ctx context.Context, ownerID, sessionID, summary string) error
}

// Transcriptions reads a transcription owned by the student.
type Transcriptions interface {
	Text(ctx context.Context, ownerID, id string) (text string, ok bool, err error)
}

type QuestionRequest struct {
	PreferredName string
	// Reasons are the grader's grounds for the score observed so far; a
	// followup asks only about what they leave missing.
	Reasons           []string
	AddressPreference exam.AddressPreference
	Hint              bool          // requested context, not a ready-made answer
	Kind              exam.TurnKind // question, followup or rephrase
	Node              exam.Node
	Dialogue          []exam.Turn
	// Ahead marks a main question or its rephrase prepared before any
	// session (decision of 18.10.2026); nobody waits for it, while a question
	// asked during a session keeps the student waiting.
	Ahead bool
}

type EvaluationRequest struct {
	PreferredName        string
	ClarificationPending bool
	AddressPreference    exam.AddressPreference
	HintUsed             bool          // distinguish the student's reasoning from supplied context
	Kind                 exam.TurnKind // the step that was answered
	Node                 exam.Node
	// MaxScore is the topic's scale: exam.RootMaxScore at the top level,
	// exam.NestedMaxScore below it.
	MaxScore int
	// Prerequisites are the titles of the topic's direct and transitive
	// prerequisites that are still unchecked. Geek may ask them later, so the
	// explanation must not give them away.
	Prerequisites []string
	Dialogue      []exam.Turn // turns on the topic, ending with the answer
}

// Assessment contains only grading; feedback follows the exam engine's decision.
type Assessment struct {
	ClarificationPending bool
	AddressPreference    exam.AddressPreference
	Clarification        string // from the separate support dialogue, never grading prose
	exam.Evaluation
}

// NameInterpreter identifies explicit name changes independently of grading.
type NameInterpreter interface {
	InterpretName(context.Context, string, string) (NameIntent, error)
}

type NameIntent struct {
	Name      string
	HasAnswer bool
}

// FeedbackRequest describes the actual outcome, not a model's prediction of it.
// Next is meaningful while the topic stays open; Result contains accumulated
// knowledge, including earlier answers when the last reply was "don't know".
type FeedbackRequest struct {
	EvaluationRequest
	Verdict exam.Verdict
	Closed  bool
	Next    exam.TurnKind
	Result  exam.NodeState
}

// Examiner is the language model behind Geek.
type Examiner interface {
	Question(ctx context.Context, r QuestionRequest) (string, error)
	Evaluate(ctx context.Context, r EvaluationRequest) (Assessment, error)
	Feedback(ctx context.Context, r FeedbackRequest) (string, error)
}

type Synthesizer interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// AudioStore caches synthesized Geek turns.
type AudioStore interface {
	Get(ctx context.Context, key string) (data []byte, ok bool, err error)
	Put(ctx context.Context, key string, data []byte) error
}

type IDGenerator interface {
	New(prefix string) (string, error)
}
