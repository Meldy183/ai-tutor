package exam

// TaskScore is accepted evidence within one constituent, on the task's scale.
type TaskScore struct {
	Score, Maximum int
	Main           bool
}

// ConstituentMastered credits the complete constituent, including unasked
// outcomes: a full main answer or at least 90% of the asked tasks' points.
func ConstituentMastered(tasks []TaskScore) bool {
	score, maximum := 0, 0
	for _, task := range tasks {
		if task.Maximum <= 0 {
			continue
		}
		if task.Main && task.Score >= task.Maximum {
			return true
		}
		score += task.Score
		maximum += task.Maximum
	}
	return maximum > 0 && score*100 >= maximum*90
}

// MasteredConstituents exposes the same constituent result to consumers of
// an exam snapshot. Material graphs without constituent IDs have no entries.
func (e *Exam) MasteredConstituents() map[string]bool {
	required := e.Graph.required()
	groups := map[string][]TaskScore{}
	for _, node := range e.Graph.Nodes {
		if node.ConstituentID == "" {
			continue
		}
		state := e.Nodes[node.ID]
		if state == nil || !scored(state) {
			continue
		}
		maximum := maxScore(required, node.ID)
		groups[node.ConstituentID] = append(groups[node.ConstituentID], TaskScore{Score: *state.Score, Maximum: maximum, Main: maximum == RootMaxScore})
	}
	out := map[string]bool{}
	for id, scores := range groups {
		out[id] = ConstituentMastered(scores)
	}
	return out
}
