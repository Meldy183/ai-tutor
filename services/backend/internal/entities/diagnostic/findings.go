package diagnostic

import "strings"

// OutcomeFinding describes only an accepted answer. FullStudy is inferred only
// from a main and two distinct basics in the same known constituent, all zero.
// This describes observed evidence; unasked outcomes can still enter learning.
type OutcomeFinding struct {
	Answer    Answer
	Mastered  bool
	FullStudy bool
}

func HasLearningGaps(answers []Answer) bool {
	return len(OpenFindings(answers)) > 0
}

func OutcomeFindings(answers []Answer) []OutcomeFinding {
	open := OpenConstituents(answers)
	type evidence struct {
		main    string
		basics  map[string]bool
		allZero bool
	}
	groups := map[string]*evidence{}
	for _, a := range answers {
		if a.Role != "main" && a.Role != "basic" {
			continue
		}
		key := a.CompetencyID + "\x00" + a.Task.ConstituentID
		g := groups[key]
		if g == nil {
			g = &evidence{basics: map[string]bool{}, allZero: true}
			groups[key] = g
		}
		if a.Score != 0 || a.Skipped {
			g.allZero = false
		}
		id := a.Task.OutcomeID
		if a.Role == "main" {
			g.main = id
		} else if id != "" {
			g.basics[id] = true
		}
	}
	findings := []OutcomeFinding{}
	seen := map[string]bool{}
	for _, a := range answers {
		if a.Role != "main" && a.Role != "basic" {
			continue
		}
		id := a.Task.OutcomeID
		key := a.CompetencyID + "\x00" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		full := AnswerMaximum(a)
		g := groups[a.CompetencyID+"\x00"+a.Task.ConstituentID]
		findings = append(findings, OutcomeFinding{Answer: a, Mastered: !open[ConstituentKey(a.CompetencyID, a.Task.ConstituentID)] || a.Score >= full && !a.Skipped,
			FullStudy: a.Task.ConstituentID != "" && strings.TrimSpace(a.Task.ConstituentName) != "" && g.main != "" && !g.basics[g.main] && len(g.basics) == 2 && g.allZero})
	}
	return findings
}
