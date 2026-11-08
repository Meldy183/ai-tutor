package coursegraph

import (
	"encoding/json"
	"testing"
)

func TestSkipMissingCriteriaKeepsAuthoredVariantsAndRemovesDanglingEdges(t *testing.T) {
	topics := []mapTopic{
		{ID: "gone", Provides: []string{"gone-provides"}, Variants: []mapVariant{{Question: "skip", AssessmentGuidance: DefaultAssessmentGuidance}}},
		{ID: "mixed", Requires: []string{"gone-provides"}, Variants: []mapVariant{{Question: "skip", AssessmentGuidance: DefaultAssessmentGuidance}, {TaskID: "mixed:exam:2", Question: "keep", Content: "reference", AssessmentGuidance: "author rubric"}}},
		{ID: "second", Variants: []mapVariant{{Question: "second", AssessmentGuidance: "author rubric"}}},
		{ID: "third", Variants: []mapVariant{{Question: "third", AssessmentGuidance: "author rubric"}}},
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
	if len(got.Topics) != 3 || got.Topics[0].Question != "keep" || len(got.Topics[0].Variants) != 1 || got.Topics[0].AssessmentGuidance != "author rubric" || len(got.Topics[0].Requires) != 0 || len(got.Prerequisites) != 0 || len(got.Hierarchy) != 0 || got.Skipped != 2 || got.Extra != "kept" || len(got.Warnings) != 0 {
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

func TestSkipMissingCriteriaRemovesConstituentLeftWithoutAFullTree(t *testing.T) {
	for name, excluded := range map[string]string{"main": "a-main", "basic": "a-basic-1"} {
		t.Run(name, func(t *testing.T) {
			topic := func(id, constituent string) mapTopic {
				guidance := "author rubric"
				if id == excluded {
					guidance = DefaultAssessmentGuidance
				}
				return mapTopic{ID: id, ParentTitle: "К1", ConstituentTitle: constituent, Provides: []string{id + "-provides"},
					Variants: []mapVariant{{TaskID: id + ":exam:1", Question: id, AssessmentGuidance: guidance}}}
			}
			edges := []mapEdge{{Source: "a-basic-1", Target: "a-main"}, {Source: "a-basic-2", Target: "a-main"}, {Source: "b-basic-1", Target: "b-main"}, {Source: "b-basic-2", Target: "b-main"}}
			raw, _ := json.Marshal(map[string]any{
				"version": MapVersion, "prerequisites": edges, "hierarchy": edges, "bank": []string{"a-main", "b-main"},
				"topics": []mapTopic{topic("a-main", "1.1"), topic("a-basic-1", "1.1"), topic("a-basic-2", "1.1"), topic("b-main", "1.2"), topic("b-basic-1", "1.2"), topic("b-basic-2", "1.2")},
			})
			out, err := SkipMissingCriteria(raw)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Topics                   []mapTopic
				Prerequisites, Hierarchy []mapEdge
				Bank                     []string
				Skipped                  int `json:"assessment_skipped"`
			}
			if err = json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Topics) != TreeSize || got.Topics[0].ID != "b-main" || len(got.Prerequisites) != 2 || len(got.Hierarchy) != 2 {
				t.Fatalf("constituent without a full tree stayed in the check: %s", out)
			}
			if got.Skipped != 1 || len(got.Bank) != 2 {
				t.Fatalf("counted only tasks without criteria and kept the bank: %s", out)
			}
		})
	}
}

func TestSkipMissingCriteriaRejectsEmptyExam(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"version": MapVersion, "topics": []mapTopic{{ID: "only", Variants: []mapVariant{{AssessmentGuidance: DefaultAssessmentGuidance}}}}})
	_, err := SkipMissingCriteria(raw)
	if err == nil {
		t.Fatal("accepted empty exam")
	}
}
