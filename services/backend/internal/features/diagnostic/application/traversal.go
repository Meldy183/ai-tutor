package application

import (
	"encoding/json"
	"math/rand/v2"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
)

// newTraversal starts the learning diagnostic on the shared exam traversal
// (decision of 08.11.2026): every constituent of the variant is a top-level
// topic, its main task, whose prerequisites are its basic tasks. The
// diagnostic takes one answer per task, so it never rephrases or asks a
// followup. rng shuffles constituents of equal coverage as Geek does; nil
// keeps the variant order.
func newTraversal(v diagnostic.VariantSnapshot, rng *rand.Rand) (diagnostic.Transition, error) {
	graph := exam.Graph{Version: v.ID}
	for _, competency := range v.Competencies {
		if len(competency.Tasks) == 0 {
			continue
		}
		main := competency.Tasks[0]
		requires := []string{}
		for _, basic := range competency.Tasks[1:] {
			graph.Nodes = append(graph.Nodes, exam.Node{ID: basic.OutcomeID, Title: basic.OutcomeName, Prerequisites: []string{}})
			requires = append(requires, basic.OutcomeID)
		}
		graph.Nodes = append(graph.Nodes, exam.Node{ID: main.OutcomeID, Title: main.OutcomeName, Prerequisites: requires})
	}
	traversal, step := exam.NewExam(graph, 0, rng)
	traversal.SingleAnswer = true
	return transitionTo(v, traversal, step, diagnostic.StatusActive)
}

// advance applies an accepted answer to the traversal: a skipped task counts
// as "don't know", a graded one by its score on the scale of its role.
// Basics credited by a full main answer are returned as untested.
func advance(s diagnostic.Session, answer diagnostic.Answer) (diagnostic.Transition, error) {
	var traversal exam.Exam
	if err := json.Unmarshal(s.Traversal, &traversal); err != nil {
		return diagnostic.Transition{}, err
	}
	before := map[string]exam.Status{}
	for id, state := range traversal.Nodes {
		before[id] = state.Status
	}
	evaluation := exam.Evaluation{Verdict: exam.VerdictAnswered, Score: answer.Score}
	if answer.Skipped {
		evaluation = exam.Evaluation{Verdict: exam.VerdictDontKnow}
	}
	step := traversal.Apply(evaluation)
	transition, err := transitionTo(s.Variant, &traversal, step, s.Status)
	if err != nil {
		return diagnostic.Transition{}, err
	}
	for _, competency := range s.Variant.Competencies {
		for _, task := range competency.Tasks {
			if state := traversal.Nodes[task.OutcomeID]; state != nil && state.Status == exam.StatusCredited && before[task.OutcomeID] != exam.StatusCredited {
				transition.SkippedBasics = append(transition.SkippedBasics, task)
			}
		}
	}
	return transition, nil
}

// transitionTo points the session at the task of the traversal's step.
func transitionTo(v diagnostic.VariantSnapshot, traversal *exam.Exam, step exam.Step, status string) (diagnostic.Transition, error) {
	data, err := json.Marshal(traversal)
	if err != nil {
		return diagnostic.Transition{}, err
	}
	transition := diagnostic.Transition{Status: status, Traversal: data, CurrentPosition: len(traversal.Done) + 1}
	if step.Kind == exam.KindClosing {
		transition.Status = diagnostic.StatusCompleted
		transition.CurrentCompetency = len(v.Competencies)
		return transition, nil
	}
	for i, competency := range v.Competencies {
		for j, task := range competency.Tasks {
			if task.OutcomeID == step.NodeID {
				transition.CurrentCompetency, transition.CurrentTask = i, j
			}
		}
	}
	return transition, nil
}
