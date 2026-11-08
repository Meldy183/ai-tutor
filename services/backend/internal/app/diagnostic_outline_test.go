package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	variantgenhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/variantgen/transport/http"
)

func TestDiagnosticOutlineMatchesVariantWithoutCreatingSessions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	student, _, _ := f.register(t, "outline@example.invalid")
	path := "/learn/subjects/subject:test/diagnostic-outline"
	if got := f.request(http.MethodGet, path, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("guest: %d", got.Code)
	}
	empty := f.request(http.MethodGet, path, "", student)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"competencies":[]`) {
		t.Fatalf("empty: %d %s", empty.Code, empty.Body.String())
	}
	data, err := os.ReadFile("testdata/learning-ready-map.csv")
	if err != nil {
		t.Fatal(err)
	}
	imported := importCourseMap(t, f, "subject:test", data, 0, f.admin(t))
	if imported.Code != http.StatusAccepted {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	preview := f.request(http.MethodGet, path, "", student)
	var outline variantgenhttp.DiagnosticOutline
	if preview.Code != http.StatusOK || json.Unmarshal(preview.Body.Bytes(), &outline) != nil || len(outline.Competencies) == 0 {
		t.Fatalf("outline: %d %s", preview.Code, preview.Body.String())
	}
	for _, competency := range outline.Competencies {
		if competency.DisplayName != "Короткая тема" {
			t.Fatalf("missing competency caption: %+v", competency)
		}
		for _, topic := range competency.Constituents {
			if topic.DisplayName != "Короткая тема" {
				t.Fatalf("missing constituent caption: %+v", topic)
			}
		}
	}
	var cacheCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM learning_graph_labels`).Scan(&cacheCount); err != nil || cacheCount == 0 {
		t.Fatalf("labels not cached: %d %v", cacheCount, err)
	}
	again := f.request(http.MethodGet, path, "", student)
	if again.Code != http.StatusOK || again.Body.String() != preview.Body.String() {
		t.Fatalf("cached outline changed: %s", again.Body.String())
	}
	for _, forbidden := range []string{`"question"`, `"reference_answer"`, `"criteria"`, `"educational_content"`, `"tasks"`} {
		if strings.Contains(preview.Body.String(), forbidden) {
			t.Fatalf("outline exposes %s", forbidden)
		}
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM learning_variants)+(SELECT count(*) FROM learning_diagnostic_sessions)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview wrote sessions: count=%d error=%v", count, err)
	}
	r := httptest.NewRequest(http.MethodPost, "/learn/variants", strings.NewReader(`{"subject_id":"subject:test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "outline-variant")
	r.AddCookie(student)
	w := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(w, r)
	var saved variantgenhttp.Variant
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &saved) != nil {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if outline.Revision != saved.MapRevision || outline.AlgorithmVersion != saved.AlgorithmVersion {
		t.Fatalf("preview metadata differs from variant")
	}
	selected := map[string]string{}
	for _, competency := range outline.Competencies {
		for _, topic := range competency.Constituents {
			for _, outcome := range topic.Outcomes {
				selected[outcome.ID] = topic.ID + ":" + outcome.Role
			}
		}
	}
	if len(selected) != saved.TaskCount {
		t.Fatalf("outcome count: got %d want %d", len(selected), saved.TaskCount)
	}
	for _, competency := range saved.Competencies {
		for _, task := range append([]variantgenhttp.VariantTask{competency.Main}, competency.Basic...) {
			if selected[task.OutcomeID] != task.ConstituentID+":"+task.Role {
				t.Fatalf("different selected outcome: %+v", task)
			}
		}
	}
	missing := f.request(http.MethodGet, "/learn/subjects/missing/diagnostic-outline", "", student)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing: %d %s", missing.Code, missing.Body.String())
	}
}
