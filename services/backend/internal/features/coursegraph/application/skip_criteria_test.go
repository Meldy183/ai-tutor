package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type skipRepo struct {
	requestRepo
	saved json.RawMessage
}

func (r *skipRepo) SaveMapIfCurrent(_ context.Context, _, _, _ string, graph json.RawMessage, _ string, _ time.Time) (bool, error) {
	r.saved = graph
	return true, nil
}

func TestSkipMissingCriteriaRequiresAdminAndKeepsAuthoredQuestion(t *testing.T) {
	fallback, _ := json.Marshal(coursegraph.DefaultAssessmentGuidance)
	graph := json.RawMessage(`{"version":"competency-map-v1","topics":[{"id":"a","variants":[{"question":"skip","assessment_guidance":` + string(fallback) + `},{"question":"keep","assessment_guidance":"author"}]},{"id":"b","variants":[{"question":"b","assessment_guidance":"author"}]},{"id":"c","variants":[{"question":"c","assessment_guidance":"author"}]}],"prerequisites":[],"hierarchy":[]}`)
	repo := &skipRepo{requestRepo: requestRepo{snapshot: coursegraph.Snapshot{Status: "ready", GraphSource: coursegraph.SourceMap, Map: &coursegraph.MapFile{Filename: "map.xlsx", Sheet: "sheet", Fingerprint: "map-a"}}, mapGraph: graph}}
	w := New(repo, nil, nil, nil, nil, nil)
	if _, err := w.SkipMissingCriteria(context.Background(), user.User{Role: user.Student}, "s", "map-a"); err == nil || repo.saved != nil {
		t.Fatal("non-admin was allowed to alter graph")
	}
	if _, err := w.SkipMissingCriteria(context.Background(), user.User{Role: user.Admin}, "s", "map-a"); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Topics []struct {
			Question string
			Variants []struct{ Question string }
		}
		Skipped int `json:"assessment_skipped"`
	}
	if err := json.Unmarshal(repo.saved, &result); err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || len(result.Topics) != 3 || result.Topics[0].Question != "keep" || len(result.Topics[0].Variants) != 1 {
		t.Fatalf("wrong graph: %s", repo.saved)
	}
}

func TestSkipMissingCriteriaRejectsUnpublishedOrChangedConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, status, expected string
		stale                  bool
	}{
		{"failed replacement B with published A", "failed", "map-b", true},
		{"stale published A", "ready", "map-b", true},
		{"another tab published B", "ready", "map-a", false},
		{"missing confirmation fingerprint", "ready", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &skipRepo{requestRepo: requestRepo{snapshot: coursegraph.Snapshot{
				Status: tc.status, Stale: tc.stale, GraphSource: coursegraph.SourceMap,
				Map:   &coursegraph.MapFile{Fingerprint: "map-b"},
				Graph: json.RawMessage(`{"topics":[{"id":"published-a"}]}`),
			}, mapGraph: json.RawMessage(`{"topics":[{"id":"imported-b"}]}`)}}
			_, err := New(repo, nil, nil, nil, nil, nil).SkipMissingCriteria(context.Background(), user.User{Role: user.Admin}, "s", tc.expected)
			var f *fault.Error
			if !errors.As(err, &f) || f.Kind != fault.Conflict || f.Code != "COMPETENCY_MAP_CHANGED" || repo.saved != nil {
				t.Fatalf("expected conflict without writes, got %v, saved %s", err, repo.saved)
			}
		})
	}
}

type retryRepo struct {
	requestRepo
	confirmed string
}

func (r *retryRepo) RetryMap(_ context.Context, _, fingerprint string, _ time.Time) (bool, error) {
	r.confirmed = fingerprint
	return true, nil
}

func TestRetryMapRequiresAdminFailedMapAndCurrentFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name, status, source, expected, code string
		role                                 user.Role
	}{
		{"saved B after failed exclusion", "failed", coursegraph.SourceMap, "b", "", user.Admin},
		{"student", "failed", coursegraph.SourceMap, "b", "FORBIDDEN", user.Student},
		{"missing fingerprint", "failed", coursegraph.SourceMap, "", "COMPETENCY_MAP_CHANGED", user.Admin},
		{"changed fingerprint", "failed", coursegraph.SourceMap, "a", "COMPETENCY_MAP_CHANGED", user.Admin},
		{"already queued", "pending", coursegraph.SourceMap, "b", "GRAPH_RETRY_NOT_AVAILABLE", user.Admin},
		{"running", "processing", coursegraph.SourceMap, "b", "GRAPH_RETRY_NOT_AVAILABLE", user.Admin},
		{"ready", "ready", coursegraph.SourceMap, "b", "GRAPH_RETRY_NOT_AVAILABLE", user.Admin},
		{"materials failed", "failed", coursegraph.SourceMaterials, "b", "GRAPH_RETRY_NOT_AVAILABLE", user.Admin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.source
			repo := &retryRepo{requestRepo: requestRepo{snapshot: coursegraph.Snapshot{Status: tc.status, Stale: true, BuildSource: &source, Map: &coursegraph.MapFile{Fingerprint: "b"}}}}
			_, err := New(repo, nil, nil, nil, nil, nil).RetryMap(context.Background(), user.User{Role: tc.role}, "s", tc.expected)
			if tc.code == "" {
				if err != nil || repo.confirmed != "b" {
					t.Fatalf("retry B: confirmed=%q err=%v", repo.confirmed, err)
				}
			} else if !isFault(err, tc.code) || repo.confirmed != "" {
				t.Fatalf("expected %s without queueing: confirmed=%q err=%v", tc.code, repo.confirmed, err)
			}
		})
	}
}
