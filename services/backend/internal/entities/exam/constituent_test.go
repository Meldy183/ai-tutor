package exam

import "testing"

func TestConstituentMastery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scores []TaskScore
		want   bool
	}{
		{"full main credits unasked outcomes", []TaskScore{{2, 2, true}}, true},
		{"partial main with full basics", []TaskScore{{1, 2, true}, {1, 1, false}, {1, 1, false}}, false},
		{"ninety percent", []TaskScore{{1, 2, true}, {8, 8, false}}, true},
		{"below ninety", []TaskScore{{1, 2, true}, {7, 8, false}}, false},
		{"no evidence", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConstituentMastered(tc.scores); got != tc.want {
				t.Fatalf("mastered=%v want %v", got, tc.want)
			}
		})
	}
}
