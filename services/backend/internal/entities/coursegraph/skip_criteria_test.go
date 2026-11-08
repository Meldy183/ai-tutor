package coursegraph

import (
	"encoding/json"
	"testing"
)

func TestSkipMissingCriteriaKeepsAuthoredVariantsAndRemovesDanglingEdges(t *testing.T) {
	topics := []mapTopic{
		{ID: "gone", Provides: []string{"gone-provides"}, Variants: []mapVariant{{Question: "skip", AssessmentGuidance: DefaultAssessmentGuidance}}},
		{ID: "mixed", Requires: []string{"gone-provides"}, Variants: []mapVariant{{Question: "skip", AssessmentGuidance: DefaultAssessmentGuidance}, {TaskID: "mixed:exam:2", Question: "keep", Content: "reference", AssessmentGuidance: "author rubric"}}},
	}
	raw, _ := json.Marshal(map[string]any{"version": MapVersion, "topics": topics, "prerequisites": []mapEdge{{Source: "gone", Target: "mixed"}}, "hierarchy": []mapEdge{{Source: "mixed", Target: "gone"}}, "assessment_warnings": []MapIssue{{Row: 3}}, "extra": "kept"})
	out, err := SkipMissingCriteria(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Topics                   []mapTopic
		Prerequisites, Hierarchy []mapEdge
		Skipped                  int `json:"assessment_skipped"`
		Extra                    string
		Warnings                 []MapIssue `json:"assessment_warnings"`
	}
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Topics) != 1 || got.Topics[0].Question != "keep" || len(got.Topics[0].Variants) != 1 || got.Topics[0].AssessmentGuidance != "author rubric" || len(got.Topics[0].Requires) != 0 || len(got.Prerequisites) != 0 || len(got.Hierarchy) != 0 || got.Skipped != 2 || got.Extra != "kept" || len(got.Warnings) != 0 {
		t.Fatalf("filtered graph: %s", out)
	}
	if got.Topics[0].Variants[0].TaskID != "mixed:exam:2" {
		t.Fatalf("filtering lost the retained task ID: %s", out)
	}
	again, err := SkipMissingCriteria(out)
	if err != nil || string(again) != string(out) {
		t.Fatalf("repeat changed graph: %s %v", again, err)
	}
}

func TestSkipMissingCriteriaRejectsEmptyExam(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"version": MapVersion, "topics": []mapTopic{{ID: "only", Variants: []mapVariant{{AssessmentGuidance: DefaultAssessmentGuidance}}}}})
	_, err := SkipMissingCriteria(raw)
	if err == nil {
		t.Fatal("accepted empty exam")
	}
}
