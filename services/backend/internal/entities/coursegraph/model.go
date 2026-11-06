package coursegraph

import "encoding/json"

// Job is a claimed build; Source is SourceMaterials or SourceMap.
type Job struct {
	SubjectID, Fingerprint, Token string
	Source                        string
	Attempts                      int
}

// SourceMaterials marks a build of the course materials; SourceMap (in
// competency_map.go) marks a build of an imported competency map.
const SourceMaterials = "materials"

type Material struct {
	ID               string          `json:"id"`
	Markdown         string          `json:"markdown"`
	Week             *int32          `json:"week"`
	Kind             *string         `json:"kind"`
	Filename         string          `json:"filename"`
	KindLabel        string          `json:"kind_label"`
	MaterialRevision string          `json:"material_revision"`
	Topics           json.RawMessage `json:"topics,omitempty"`
}

// Where the graph of a snapshot comes from: a build from the course
// materials, a build from a competency map, or nothing.
const (
	SourceBuilt = "built"
	SourceNone  = "none"
)

// Snapshot is the published graph of a course and the state of its build.
// Status is that of the last requested build; InputsReady means every
// material of the course is processed, so a build can start.
type Snapshot struct {
	Status       string          `json:"status" enums:"not_requested,pending,processing,ready,failed"`
	Attempts     int32           `json:"attempts"`
	ErrorMessage string          `json:"error_message"`
	Graph        json.RawMessage `json:"graph" swaggertype:"object" extensions:"x-nullable"`
	GraphSource  string          `json:"graph_source" enums:"built,competency_map,none"`
	// GraphFingerprint identifies the currently published Geek graph.
	GraphFingerprint string `json:"graph_fingerprint"`
	// GraphMapRevision is the learning-map revision used to build the published Geek graph.
	GraphMapRevision *int64 `json:"graph_map_revision" extensions:"x-nullable"`
	// BuildSource is what the last requested build reads: materials or
	// competency_map; null before the first request.
	BuildSource *string `json:"build_source" enums:"materials,competency_map" extensions:"x-nullable"`
	// Map is the imported competency map, if any.
	Map         *MapFile `json:"map" extensions:"x-nullable"`
	UpdatedAt   int64    `json:"updated_at"`
	Stale       bool     `json:"stale"`
	InputsReady bool     `json:"inputs_ready"`
	// Progress is where a running build is; only while Status is processing.
	// It lives in API memory, so after a restart it returns with the next poll.
	Progress *BuildProgress `json:"progress" extensions:"x-nullable"`
}

// MapFile is the competency map imported last and the sheet it was read from.
type MapFile struct {
	Fingerprint      string `json:"fingerprint"`
	Filename         string `json:"filename"`
	Sheet            string `json:"sheet"`
	LearningRevision *int64 `json:"learning_revision" extensions:"x-nullable"`
}

// BuildProgress is stage StageIndex of StageCount of a running build and the
// model batches of that stage finished so far; a stage without work has
// Total 0.
type BuildProgress struct {
	Stage            string `json:"stage" enums:"topics,synonyms,prerequisites,attach,questions,prepare_questions,retry_questions,publish"`
	StageIndex       int    `json:"stage_index"`
	StageCount       int    `json:"stage_count"`
	Done             int    `json:"done"`
	Total            int    `json:"total"`
	RemainingSeconds *int   `json:"remaining_seconds,omitempty" binding:"optional"`
}
