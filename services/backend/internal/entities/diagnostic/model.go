// Package diagnostic contains process-local diagnostic session state.
package diagnostic

import "encoding/json"

type LearningState struct {
	SubjectID           string `json:"subject_id"`
	SubjectName         string `json:"subject_name"`
	DiagnosticSessionID string `json:"diagnostic_session_id,omitempty" binding:"optional"`
	ActiveSessionID     string `json:"active_session_id,omitempty" binding:"optional"`
	DiagnosticStatus    string `json:"diagnostic_status"`
	DiagnosticCompleted bool   `json:"diagnostic_completed"`
	TrainingAvailable   bool   `json:"training_available"`
}

const (
	StatusActive    = "active"
	StatusCompleted = "completed"
)

type Session struct {
	ID                string
	OwnerID           string
	VariantID         string
	Status            string
	CurrentCompetency int
	CurrentTask       int
	Variant           VariantSnapshot
	// Traversal is the state of the shared exam traversal over the variant,
	// owned by the diagnostic feature; CurrentCompetency and CurrentTask point
	// at its current task, CurrentPosition is the 1-based position of the
	// current constituent in the traversal order.
	Traversal          json.RawMessage `json:",omitempty"`
	CurrentPosition    int
	Answers            []Answer
	SkippedBasics      []TaskSnapshot
	AcceptedRequests   map[string]AcceptedRequest
	StartRequestDigest string
	InFlight           *Reservation
}

func (s Session) Current() *TaskSnapshot {
	if s.Status != StatusActive || s.CurrentCompetency < 0 || s.CurrentCompetency >= len(s.Variant.Competencies) {
		return nil
	}
	competency := s.Variant.Competencies[s.CurrentCompetency]
	if s.CurrentTask < 0 || s.CurrentTask >= len(competency.Tasks) {
		return nil
	}
	task := competency.Tasks[s.CurrentTask]
	return &task
}

func (s Session) Progress() Progress {
	progress := Progress{
		SessionID: s.ID, Status: s.Status, Current: s.Current(),
		Completed: len(s.Answers), Skipped: len(s.SkippedBasics), Total: s.Variant.TaskCount(),
		CompetencyCount: len(s.Variant.Competencies),
	}
	// The number of questions depends on answers, the number of competencies
	// does not, so clients show progress by competency: the position of the
	// current one in the order of the traversal.
	if progress.Current != nil {
		progress.CurrentCompetency = s.CurrentCompetency + 1
		if s.CurrentPosition > 0 {
			progress.CurrentCompetency = s.CurrentPosition
		}
		progress.CurrentStep = s.CurrentTask
	}
	return progress
}

type VariantSnapshot struct {
	ID                      string
	SubjectID               string
	SubjectNameSnapshot     string
	MapRevision             int64
	AlgorithmVersion        string
	IncludedCompetencyCount int
	SkippedCompetencies     []SkippedCompetency
	Competencies            []Competency
}

// TaskCount includes only positions present in the saved variant.
func (v VariantSnapshot) TaskCount() int {
	total := 0
	for _, competency := range v.Competencies {
		total += len(competency.Tasks)
	}
	return total
}

type Competency struct {
	ID       string
	Name     string
	Position int
	Tasks    []TaskSnapshot
}

type TaskSnapshot struct {
	ID                 string
	SourceTaskID       string
	Role               string
	CompetencyID       string
	CompetencyName     string
	ConstituentID      string
	ConstituentName    string
	OutcomeID          string
	OutcomeName        string
	Question           string
	Options            []string
	VoiceInstruction   string
	AudioAssetID       *string
	ReferenceAnswer    string
	Criteria           string
	TaxonomyCode       string
	ALDLevelCode       string
	Importance         int16
	IncludeInTest      bool
	EducationalContent string
}

type Answer struct {
	Skipped          bool
	VariantTaskID    string
	SourceTaskID     string
	CompetencyID     string
	OutcomeID        string
	Role             string
	Task             TaskSnapshot
	TranscriptionID  string
	Text             string
	GraderScore      int
	GraderMaxScore   int
	Score            int
	Verdict          string
	CriterionResults []CriterionResult
	Feedback         string
	CreatedAt        int64
}

type CriterionResult struct {
	Key         string
	Satisfied   bool
	Explanation string
}

type SkippedCompetency struct {
	ID   string
	Name string
	Code string
}

type AcceptedRequest struct {
	Fingerprint string
	Response    Progress
}

type Transition struct {
	CurrentCompetency int
	CurrentTask       int
	Status            string
	SkippedBasics     []TaskSnapshot
	Traversal         json.RawMessage
	CurrentPosition   int
}

type Reservation struct {
	Key               string
	Fingerprint       string
	VariantTaskID     string
	Token             string
	TranscriptionID   string
	TranscriptionText string
}

type Progress struct {
	AnswerSkipped bool
	Text          string
	SessionID     string
	Status        string
	Completed     int
	Skipped       int
	Total         int
	// CompetencyCount is fixed for the session; CurrentCompetency is 1-based
	// and CurrentStep is 0 for the main task, 1–2 for basics.
	CompetencyCount   int
	CurrentCompetency int
	CurrentStep       int
	Current           *TaskSnapshot
	Score             *int
	GraderScore       *int
	GraderMaxScore    *int
	Verdict           string
	CriterionResults  []CriterionResult
	Feedback          string
}

type Result struct {
	SessionID               string
	Status                  string
	VariantID               string
	MapRevision             int64
	IncludedCompetencyCount int
	SkippedCompetencies     []SkippedCompetency
	CompletedTasks          int
	TotalTasks              int
	DiagnosticScore         int
	MaximumScore            int
	Answers                 []Answer
	UntestedBasics          []TaskSnapshot
}
