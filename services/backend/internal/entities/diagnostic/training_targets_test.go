package diagnostic

import "testing"

func TestTrainingTargetsExcludeUntestedAndPerfectAndClassifyFailedBasics(t *testing.T) {
	answers := []Answer{{CompetencyID: "a", Role: "main", Score: 1, Task: TaskSnapshot{OutcomeID: "main-a"}}, {CompetencyID: "a", Role: "basic", Score: 0, Task: TaskSnapshot{OutcomeID: "basic-a"}}, {CompetencyID: "b", Role: "main", Score: 0, Task: TaskSnapshot{OutcomeID: "main-b"}}, {CompetencyID: "c", Role: "main", Score: 2, Task: TaskSnapshot{OutcomeID: "main-c"}}}
	got := TrainingTargets(answers)
	if len(got) != 3 || got[0].Answer.Task.OutcomeID != "main-a" || got[1].Kind != "confirmed_gap" || got[1].Answer.Task.OutcomeID != "basic-a" || got[2].Answer.Task.OutcomeID != "main-b" {
		t.Fatalf("targets: %+v", got)
	}
}

func TestTrainingTargetsIncludeUserSkippedAnswersAsZero(t *testing.T) {
	answers := []Answer{{CompetencyID: "a", Role: "main", Skipped: true, Task: TaskSnapshot{OutcomeID: "main-a"}}, {CompetencyID: "a", Role: "basic", Skipped: true, Task: TaskSnapshot{OutcomeID: "basic-a"}}}
	if got := TrainingTargets(answers); len(got) != 2 || got[0].Answer.Task.OutcomeID != "main-a" || got[1].Answer.Task.OutcomeID != "basic-a" {
		t.Fatalf("skipped basic should be a zero-score gap: %+v", got)
	}
	if got := TrainingTargets(answers[:1]); len(got) != 1 || got[0].Kind != "confirmed_gap" || got[0].Answer.Task.OutcomeID != "main-a" {
		t.Fatalf("skipped main should be a zero-score target: %+v", got)
	}
}

func TestTrainingTargetsKeepConstituentsSeparate(t *testing.T) {
	answers := []Answer{
		{CompetencyID: "c", Role: "main", Score: 0, Task: TaskSnapshot{ConstituentID: "s1", OutcomeID: "a"}},
		{CompetencyID: "c", Role: "basic", Score: 0, Task: TaskSnapshot{ConstituentID: "s1", OutcomeID: "b"}},
		{CompetencyID: "c", Role: "main", Score: 2, Task: TaskSnapshot{ConstituentID: "s2", OutcomeID: "c"}},
	}
	got := TrainingTargets(answers)
	if len(got) != 2 || got[0].Answer.Task.OutcomeID != "a" || got[1].Answer.Task.OutcomeID != "b" {
		t.Fatalf("targets = %+v", got)
	}
}
