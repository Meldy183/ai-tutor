package csvparser

import (
	"os"
	"testing"
)

// The teacher's "Введение в ML" workbook stores importance as spreadsheet
// numbers ("4.0") and 0/1 booleans; it must import like the CSV export.
func TestParseIntroToMLWorkbook(t *testing.T) {
	data, err := os.ReadFile("testdata/ml_map_intro.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Competencies) != 13 || len(m.Constituents) != 17 || len(m.Outcomes) != 101 || len(m.Tasks) != 13 || m.UnparsedTaskCells != 41 {
		t.Fatalf("course_map_competencies=%d course_map_constituents=%d course_map_outcomes=%d course_map_tasks=%d unparsed=%d",
			len(m.Competencies), len(m.Constituents), len(m.Outcomes), len(m.Tasks), m.UnparsedTaskCells)
	}
	included := 0
	for _, outcome := range m.Outcomes {
		if outcome.IncludeInTest != nil && *outcome.IncludeInTest {
			included++
		}
		if outcome.Importance == nil || *outcome.Importance < 1 || *outcome.Importance > 5 {
			t.Fatalf("outcome %q importance = %v", outcome.Name, outcome.Importance)
		}
	}
	if included != 18 {
		t.Fatalf("course_map_outcomes included in test = %d, want 18", included)
	}
	// Row 6 holds a table task cut after its header: the first cell lacks the
	// instruction and answer, the next cells are leftover table fragments.
	codes := map[string]string{}
	for _, warning := range m.Warnings {
		if warning.Row == 6 {
			codes[warning.Column] = warning.Code
		}
	}
	want := map[string]string{
		"Задание 1": "TASK_INVALID_BLOCK_COUNT",
		"Задание 2": "TASK_INVALID_BLOCK_COUNT",
		"Задание 3": "TASK_INVALID_BLOCK_COUNT",
	}
	for column, code := range want {
		if codes[column] != code {
			t.Fatalf("row 6 %s code = %q, want %q (all: %v)", column, codes[column], code, codes)
		}
	}
}

func TestTaskFragmentCodeNamesTheMissingPart(t *testing.T) {
	for raw, want := range map[string]string{
		"---:": "TASK_INVALID_BLOCK_COUNT",
		"Вопрос\n\n\n[]\n\n\n \n\n\nОтвет":            "TASK_EMPTY_PART",
		"Вопрос\n\n\nне список\n\n\nГолос\n\n\nОтвет": "TASK_INVALID_OPTIONS",
	} {
		if got := taskFragmentCode(raw); got != want {
			t.Errorf("taskFragmentCode(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestImportanceValueAcceptsWholeSpreadsheetNumbers(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		ok    bool
	}{
		{"4", 4, true}, {"4.0", 4, true}, {"5,0", 5, true}, {"1.00", 1, true},
		{"4.5", 0, false}, {"0", 0, false}, {"6.0", 0, false}, {"высокая", 0, false},
	} {
		got, ok := importanceValue(tc.value)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("importanceValue(%q) = %d, %v; want %d, %v", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}
