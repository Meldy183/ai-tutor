package taskbankhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskbank/application"
)

type recordingRepository struct {
	filter application.SearchFilter
	calls  int
}

func (r *recordingRepository) Search(_ context.Context, filter application.SearchFilter) ([]application.TaskSummary, error) {
	r.calls++
	r.filter = filter
	return []application.TaskSummary{{
		ID: "task-1", Question: "Вопрос", Origin: "authored", Options: []string{"A", "B"},
		VoiceInstruction: stringPointer("Объясните выбор"), OutcomeID: "outcome-1",
	}}, nil
}

func (r *recordingRepository) Profile(context.Context, string, string) (application.TaskProfile, error) {
	return application.TaskProfile{}, nil
}

func TestSearchParsesAllSupportedQueryFilters(t *testing.T) {
	repository := &recordingRepository{}
	handler := New(application.New(repository))
	request := httptest.NewRequest(http.MethodGet,
		"/course_map_tasks?subject_id=subject%3Ab&outcome_id=o1&competency_id=c1&constituent_id=s1&taxonomy=analysis&ald_level=advanced&topic_level=intermediate&importance=5&include_in_test=false&origin=ai_generated&section=R.6&curriculum_competency=ОПК-8&limit=75", nil)
	response := httptest.NewRecorder()
	handler.Search(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.calls != 1 {
		t.Fatalf("search calls=%d", repository.calls)
	}
	if repository.filter.SubjectID != "subject:b" || repository.filter.OutcomeID != "o1" || repository.filter.CompetencyID != "c1" || repository.filter.ConstituentID != "s1" || repository.filter.TaxonomyCode != "analysis" || repository.filter.ALDLevelCode != "advanced" || repository.filter.TopicLevelCode != "intermediate" || repository.filter.Importance != 5 || repository.filter.IncludeInTest == nil || *repository.filter.IncludeInTest || repository.filter.Origin != "ai_generated" || repository.filter.SectionCode != "R.6" || repository.filter.CurriculumCompetencyCode != "ОПК-8" || repository.filter.Limit != 75 {
		t.Fatalf("parsed search filters = %#v", repository.filter)
	}
	var responseItems []TaskSummary
	if err := json.Unmarshal(response.Body.Bytes(), &responseItems); err != nil {
		t.Fatal(err)
	}
	if len(responseItems) != 1 || responseItems[0].ID != "task-1" || !reflect.DeepEqual(responseItems[0].Options, []string{"A", "B"}) {
		t.Fatalf("search DTO response = %#v", responseItems)
	}
	if strings.Contains(response.Body.String(), "reference_answer") || strings.Contains(response.Body.String(), "criteria") {
		t.Fatalf("student-facing search leaked evaluation material: %s", response.Body.String())
	}
}

func TestSearchRejectsInvalidTypedQueryFiltersBeforeRepositoryCall(t *testing.T) {
	for _, query := range []string{
		"subject_id=subject%3Atest&include_in_test=no", "subject_id=subject%3Atest&importance=0", "subject_id=subject%3Atest&importance=6", "subject_id=subject%3Atest&importance=invalid", "subject_id=subject%3Atest&limit=0", "subject_id=subject%3Atest&limit=101", "subject_id=subject%3Atest&limit=abc",
		"limit=", "limit=1&limit=2", "origin=authored&origin=ai_generated", "unknown=value", "%zz", "outcome_id=%ff", "outcome_id=%00",
	} {
		t.Run(query, func(t *testing.T) {
			repository := &recordingRepository{}
			handler := New(application.New(repository))
			response := httptest.NewRecorder()
			handler.Search(response, httptest.NewRequest(http.MethodGet, "/course_map_tasks?"+query, nil))
			if response.Code != 422 || repository.calls != 0 {
				t.Fatalf("query %q: status=%d repository calls=%d body=%s", query, response.Code, repository.calls, response.Body.String())
			}
		})
	}
}

func TestSearchUsesDefaultAndPassesBoundaryLimits(t *testing.T) {
	repository := &recordingRepository{}
	handler := New(application.New(repository))
	for _, tc := range []struct {
		input    string
		expected int32
	}{{"subject_id=subject%3Atest", 50}, {"subject_id=subject%3Atest&limit=1", 1}, {"subject_id=subject%3Atest&limit=100", 100}} {
		request := httptest.NewRequest(http.MethodGet, "/course_map_tasks?"+tc.input, nil)
		response := httptest.NewRecorder()
		handler.Search(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("query %q status=%d body=%s", tc.input, response.Code, response.Body.String())
		}
		limit := repository.filter.Limit
		if limit != tc.expected {
			t.Errorf("query %q produced limit %d, want %d", tc.input, limit, tc.expected)
		}
	}
}

func TestSearchRequiresNonblankSubjectID(t *testing.T) {
	for _, query := range []string{"", "subject_id=%20%20"} {
		repository := &recordingRepository{}
		response := httptest.NewRecorder()
		New(application.New(repository)).Search(response, httptest.NewRequest(http.MethodGet, "/course_map_tasks?"+query, nil))
		if response.Code != http.StatusUnprocessableEntity || repository.calls != 0 {
			t.Fatalf("query %q: status=%d calls=%d body=%s", query, response.Code, repository.calls, response.Body.String())
		}
	}
}

func TestProfileRequiresNonblankSubjectID(t *testing.T) {
	for _, query := range []string{"", "?subject_id=%20%20"} {
		repository := &recordingRepository{}
		response := httptest.NewRecorder()
		New(application.New(repository)).Profile(response, httptest.NewRequest(http.MethodGet, "/course_map_tasks/task-1"+query, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("query %q: status=%d body=%s", query, response.Code, response.Body.String())
		}
	}
}

func stringPointer(value string) *string { return &value }

func TestSearchRejectsInvalidImportanceRanges(t *testing.T) {
	for _, query := range []string{"subject_id=subject%3Atest&importance_min=0", "subject_id=subject%3Atest&importance_max=6", "subject_id=subject%3Atest&importance_min=no", "subject_id=subject%3Atest&importance_min=4&importance_max=2"} {
		repository := &recordingRepository{}
		response := httptest.NewRecorder()
		New(application.New(repository)).Search(response, httptest.NewRequest("GET", "/course_map_tasks?"+query, nil))
		if response.Code != 422 || repository.calls != 0 {
			t.Fatalf("%s: %d %s", query, response.Code, response.Body.String())
		}
	}
}
