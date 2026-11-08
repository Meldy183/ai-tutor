package application

import (
	"errors"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type fixedChooser struct {
	calls int
	index int
}

func (c *fixedChooser) Choose(count int) (int, error) {
	c.calls++
	if c.index >= count {
		return 0, errors.New("bad test chooser index")
	}
	return c.index, nil
}

type fixedIDs struct{ n int }

func (g *fixedIDs) New(prefix string) (string, error) {
	g.n++
	return prefix + "-" + string(rune('0'+g.n)), nil
}

func TestLearningBloomNormalizationAndTiesMatchLegacy(t *testing.T) {
	chooser := &fixedChooser{index: 1}
	service := New(nil, chooser, &fixedIDs{}, func() time.Time { return time.Unix(100, 0) })
	candidates := []variant.CandidateOutcome{
		candidate("c1", "Comp", "low-a", "Low A", "understanding", 3, 10, "low-a-task", 1),
		candidate("c1", "Comp", "low-b", "Low B", "understanding", 2, 20, "low-b-task", 2),
		// A higher importance does not outweigh a lower Bloom rank.
		candidate("c1", "Comp", "important", "Important", "application", 5, 30, "important-task", 3),
		candidate("c1", "Comp", "main", "Main", "analysis", 2, 40, "main-task-1", 4),
		candidate("c1", "Comp", "main", "Main", "analysis", 2, 40, "main-task-2", 4),
		candidate("c1", "Comp", "rival", "Rival", "analysis", 1, 50, "rival-task", 5),
	}
	got, err := service.build("owner", "subject:test", "Тест", 7, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if got.MapRevision != 7 || got.SubjectID != "subject:test" || got.SubjectNameSnapshot != "Тест" || got.IncludedCompetencyCount != 1 || got.CreatedAt != 100 || got.AlgorithmVersion != AlgorithmVersion {
		t.Fatalf("variant metadata: %+v", got)
	}
	course_map_tasks := got.Competencies[0].Tasks
	if len(course_map_tasks) != 3 || course_map_tasks[0].Role != "main" || course_map_tasks[1].Role != "basic" || course_map_tasks[2].Role != "basic" {
		t.Fatalf("course_map_tasks: %+v", course_map_tasks)
	}
	if course_map_tasks[0].Task.Outcome.ID != "main" {
		t.Fatalf("main outcome = %s, want the analysis outcome with the higher importance", course_map_tasks[0].Task.Outcome.ID)
	}
	if course_map_tasks[0].Task.ID != "main-task-2" {
		t.Fatalf("random chooser did not select pool member: %s", course_map_tasks[0].Task.ID)
	}
	if course_map_tasks[1].Task.Outcome.ID != "low-a" || course_map_tasks[2].Task.Outcome.ID != "low-b" {
		t.Fatalf("basics: %s, %s; want the simplest course_map_outcomes", course_map_tasks[1].Task.Outcome.ID, course_map_tasks[2].Task.Outcome.ID)
	}
	if chooser.calls != 1 {
		t.Fatalf("chooser called %d times, want once for multi-task pool", chooser.calls)
	}
}

func TestBuildAllowsBasicsWithTheMainBloomRank(t *testing.T) {
	service := New(nil, &fixedChooser{}, &fixedIDs{}, time.Now)
	got, err := service.build("owner", "subject:test", "Тест", 1, []variant.CandidateOutcome{
		candidate("c1", "Comp", "app-3", "App 3", "application", 3, 1, "t1", 1),
		candidate("c1", "Comp", "app-5", "App 5", "application", 5, 2, "t2", 2),
		candidate("c1", "Comp", "app-4", "App 4", "application", 4, 3, "t3", 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	course_map_tasks := got.Competencies[0].Tasks
	want := []string{"app-5", "app-4", "app-3"}
	if len(course_map_tasks) != 3 {
		t.Fatalf("course_map_tasks: %+v", course_map_tasks)
	}
	for i, id := range want {
		if course_map_tasks[i].Task.Outcome.ID != id {
			t.Fatalf("task %d = %s, want %s", i, course_map_tasks[i].Task.Outcome.ID, id)
		}
	}
}

func TestBuildSkipsCompetencyWithFewerThanThreeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []variant.CandidateOutcome
	}{
		{"only main", []variant.CandidateOutcome{candidate("c1", "Comp", "main", "Main", "application", 5, 1, "t1", 1)}},
		{"one basic and analogs", []variant.CandidateOutcome{
			candidate("c1", "Comp", "main", "Main", "analysis", 5, 1, "t1", 1),
			candidate("c1", "Comp", "main", "Main", "analysis", 5, 1, "t1b", 1),
			candidate("c1", "Comp", "low", "Low", "understanding", 2, 2, "t2", 2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := New(nil, &fixedChooser{}, &fixedIDs{}, time.Now)
			_, err := service.build("owner", "subject:test", "Тест", 1, tc.candidates)
			failure, ok := err.(*fault.Error)
			if !ok || failure.Code != "NO_ELIGIBLE_COMPETENCIES" || len(failure.Details) != 1 || failure.Details[0].Code != "INSUFFICIENT_DISTINCT_OUTCOMES" {
				t.Fatalf("want competency skipped with INSUFFICIENT_DISTINCT_OUTCOMES, got %v", err)
			}
		})
	}
}

func TestBuildSkipsOnlyCompetenciesWithoutReadyEnabledOutcomes(t *testing.T) {
	valid := candidate("included", "Included", "ready", "Ready", "application", 4, 1, "t1", 1)
	validLowA := candidate("included", "Included", "low-a", "Low A", "knowledge", 3, 4, "t4", 4)
	validLowB := candidate("included", "Included", "low-b", "Low B", "knowledge", 2, 5, "t5", 5)
	disabled := candidate("disabled", "Disabled", "off", "Off", "analysis", 5, 2, "t2", 2)
	disabled.Profile.Outcome.IncludeInTest = new(bool)
	incomplete := candidate("incomplete", "Incomplete", "missing", "Missing", "analysis", 5, 3, "t3", 3)
	incomplete.Profile.ReferenceAnswer = nil
	service := New(nil, &fixedChooser{}, &fixedIDs{}, time.Now)
	got, err := service.build("owner", "subject:test", "Тест", 1, []variant.CandidateOutcome{valid, validLowA, validLowB, disabled, incomplete})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Competencies) != 1 || len(got.SkippedCompetencies) != 2 {
		t.Fatalf("unexpected variant: %+v", got)
	}
	for _, skipped := range got.SkippedCompetencies {
		if skipped.Code != "NO_READY_OUTCOMES" || skipped.EligibleOutcomes != 0 {
			t.Fatalf("unexpected skip: %+v", skipped)
		}
	}
	_, err = service.build("owner", "subject:test", "Тест", 1, []variant.CandidateOutcome{disabled, incomplete})
	failure, ok := err.(*fault.Error)
	if !ok || failure.Code != "NO_ELIGIBLE_COMPETENCIES" {
		t.Fatalf("unexpected failure: %v", err)
	}
}

func candidate(compID, compName, outcomeID, outcomeName, taxonomy string, importance int16, order int64, taskID string, taskOrder int32) variant.CandidateOutcome {
	include := true
	voice, answer := "Скажите ответ.", "Эталон."
	importanceValue := importance
	return variant.CandidateOutcome{CompetencySourceOrder: 1, SourceOrder: order, HasTask: true, Profile: variant.TaskProfile{
		ID: taskID, Question: "Вопрос?", Options: []string{}, VoiceInstruction: &voice, ReferenceAnswer: &answer, Origin: "authored", SourceRowIndex: &taskOrder,
		Competency:  variant.CompetencyProfile{ID: compID, Name: compName},
		Constituent: variant.ConstituentProfile{ID: compID + "-s", Name: "Составляющая"},
		Outcome:     variant.OutcomeProfile{ID: outcomeID, Name: outcomeName, IncludeInTest: &include, TaxonomyCode: &taxonomy, Importance: &importanceValue},
	}}
}

func TestBuildGroupsOutcomesByConstituent(t *testing.T) {
	var candidates []variant.CandidateOutcome
	for _, constituent := range []string{"s1", "s2"} {
		for i, bloom := range []string{"knowledge", "understanding", "application"} {
			id := constituent + string(rune('1'+i))
			c := candidate("c1", "Comp", id, id, bloom, 3, int64(len(candidates)+1), id, int32(i+1))
			c.Profile.Constituent = variant.ConstituentProfile{ID: constituent, Name: constituent}
			candidates = append(candidates, c)
		}
	}
	got, err := New(nil, &fixedChooser{}, &fixedIDs{}, time.Now).build("owner", "subject", "Subject", 1, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Competencies) != 2 {
		t.Fatalf("got %d blocks, want 2", len(got.Competencies))
	}
	for _, block := range got.Competencies {
		for _, task := range block.Tasks {
			if task.Task.Constituent.ID != block.Tasks[0].Task.Constituent.ID {
				t.Fatal("mixed constituents")
			}
		}
	}
}

func TestBuildSkipsShortConstituentsWithinOneCompetency(t *testing.T) {
	var candidates []variant.CandidateOutcome
	for i, constituent := range []string{"s1", "s1", "s2", "s2"} {
		id := string(rune('a' + i))
		c := candidate("c1", "Comp", id, id, "knowledge", 3, int64(i+1), id, int32(i+1))
		c.Profile.Constituent = variant.ConstituentProfile{ID: constituent, Name: constituent}
		candidates = append(candidates, c)
	}
	_, err := New(nil, &fixedChooser{}, &fixedIDs{}, time.Now).build("owner", "subject", "Subject", 1, candidates)
	failure, ok := err.(*fault.Error)
	if !ok || failure.Code != "NO_ELIGIBLE_COMPETENCIES" || len(failure.Details) != 2 {
		t.Fatalf("error = %v", err)
	}
}

func TestPreviewSelectsExactlyThePersistedOutcomesWithoutChoosingTasksOrIDs(t *testing.T) {
	chooser := &fixedChooser{index: 1}
	ids := &fixedIDs{}
	service := New(nil, chooser, ids, time.Now)
	candidates := []variant.CandidateOutcome{
		candidate("c1", "Comp", "low-a", "Low A", "knowledge", 3, 10, "low-a-task", 1),
		candidate("c1", "Comp", "low-b", "Low B", "understanding", 2, 20, "low-b-task", 2),
		candidate("c1", "Comp", "unused", "Unused", "application", 5, 30, "unused-task", 3),
		candidate("c1", "Comp", "main", "Main", "analysis", 2, 40, "main-task-1", 4),
		candidate("c1", "Comp", "main", "Main", "analysis", 2, 40, "main-task-2", 4),
		candidate("short", "Not enough", "only", "Only", "knowledge", 1, 50, "only-task", 5),
	}
	preview, err := service.buildSelected("", "subject", "Subject", 7, candidates, true)
	if err != nil {
		t.Fatal(err)
	}
	if chooser.calls != 0 || ids.n != 0 {
		t.Fatalf("preview used randomness or IDs: %d %d", chooser.calls, ids.n)
	}
	created, err := service.build("user", "subject", "Subject", 7, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Competencies) != 1 || len(preview.SkippedCompetencies) != 1 {
		t.Fatalf("preview includes an ineligible competency: %+v", preview)
	}
	for i, task := range preview.Competencies[0].Tasks {
		actual := created.Competencies[0].Tasks[i]
		if task.Role != actual.Role || task.Task.Outcome.ID != actual.Task.Outcome.ID || task.Task.Constituent.ID != actual.Task.Constituent.ID {
			t.Fatalf("preview differs from saved selection: %+v vs %+v", task, actual)
		}
	}
}
