package diagnostic

type TrainingTarget struct {
	Kind   string
	Answer Answer
}

// TrainingTargets includes every non-maximal answer of an open constituent.
func TrainingTargets(answers []Answer) []TrainingTarget {
	targets := []TrainingTarget{}
	for _, finding := range OpenFindings(answers) {
		kind := "partial_competency"
		if finding.Answer.Score == 0 {
			kind = "confirmed_gap"
		}
		targets = append(targets, TrainingTarget{Kind: kind, Answer: finding.Answer})
	}
	return targets
}
