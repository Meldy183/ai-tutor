package coursegraph

import (
	"encoding/json"
	"slices"
	"testing"
)

func unifiedRows() [][]string {
	return [][]string{
		{"№", "Компетенция", "Составляющая", "Образовательный результат", "Что должно войти в тест", "Уровень по Блуму", "Важность (1–5)", "Учебный текст",
			"Задание диагностики 1", "Критерии к заданию диагностики 1", "Задание диагностики 2",
			"Задание экзамена 1", "Критерии к заданию экзамена 1", "Задание экзамена 2"},
		{"1", "К1", "С1", "ОР простой", "TRUE", "Помнить", "4", "Текст 1", "Д1", "Кд1", "Д2", "Э1", "Эталон: э1", "Э2"},
		{"2", "", "", "ОР средний", "TRUE", "Понимать", "3", "Текст 2", "Д3", "", "", "Э3", "Эталон: э3", ""},
		{},
		{"3", "", "", "ОР сложный", "TRUE", "Анализировать", "5", "Текст 3", "Д4", "", "", "Э4", "", ""},
	}
}

func TestSplitUnifiedSheetProjectsBothModesRowForRow(t *testing.T) {
	sheet := MapSheet{Name: "Карта", Rows: unifiedRows()}
	if !IsUnifiedSheet(sheet) {
		t.Fatal("unified sheet not recognised")
	}
	exam, learning, ok := SplitUnifiedSheet(sheet)
	if !ok || len(exam.Rows) != len(sheet.Rows) || len(learning.Rows) != len(sheet.Rows) {
		t.Fatalf("projection rows: exam=%d learning=%d", len(exam.Rows), len(learning.Rows))
	}
	col := func(rows [][]string, title string) int { return slices.Index(rows[0], title) }
	at := func(s MapSheet, row int, title string) string { return s.Rows[row][col(s.Rows, title)] }

	// The exam sees only its own tasks, each with the criteria of its number.
	if at(exam, 1, "Задание 1") != "Э1" || at(exam, 1, "Критерии оценивания 1") != "Эталон: э1" || at(exam, 1, "Критерии оценивания 2") != "" {
		t.Fatalf("exam row: %v", exam.Rows[1])
	}
	if slices.ContainsFunc(exam.Rows[1], func(v string) bool { return v == "Д1" || v == "Кд1" }) {
		t.Fatal("a diagnostic task leaked into the exam")
	}
	if at(exam, 1, "Текст темы") != "Текст 1" {
		t.Fatalf("exam text = %q", at(exam, 1, "Текст темы"))
	}

	// The diagnostic sees only its own tasks with their criteria, and the
	// same learning text.
	if at(learning, 1, "Задание 1") != "Д1" || at(learning, 1, "Критерии 1") != "Кд1" || at(learning, 1, "Задание 2") != "Д2" {
		t.Fatalf("learning row: %v", learning.Rows[1])
	}
	if slices.ContainsFunc(learning.Rows[1], func(v string) bool { return v == "Э1" }) {
		t.Fatal("an exam task leaked into the diagnostic")
	}
	if at(learning, 1, "ОС") != "Текст 1" {
		t.Fatalf("learning text = %q", at(learning, 1, "ОС"))
	}
	// Levels absent from the book stay empty: the modes do not use them.
	if at(learning, 1, "Уровень темы") != "" || at(learning, 1, "Уровень ALDs") != "" {
		t.Fatalf("levels were invented: %v", learning.Rows[1])
	}

	// Messages name the columns as the book does.
	for projected, book := range map[string]string{"Важность": "Важность (1–5)", "Задание 1": "Задание диагностики 1", "Критерии 1": "Критерии к заданию диагностики 1", "ОС": "Учебный текст"} {
		if got := learning.BookTitle(projected); got != book {
			t.Fatalf("learning %q → %q, want %q", projected, got, book)
		}
	}
	for projected, book := range map[string]string{"Уровень по Блуму": "Уровень по Блуму", "Субкомпетенция": "Компетенция", "Задание 2": "Задание экзамена 2"} {
		if got := exam.BookTitle(projected); got != book {
			t.Fatalf("exam %q → %q, want %q", projected, got, book)
		}
	}
	if exam.Number != 1 || learning.Number != 1 {
		t.Fatalf("both projections come from sheet 1: %d %d", exam.Number, learning.Number)
	}
}

func TestUnifiedSheetBuildsTheExamGraph(t *testing.T) {
	exam, _, _ := SplitUnifiedSheet(MapSheet{Name: "Карта", Rows: unifiedRows()})
	built, err := BuildMapGraph([]MapSheet{exam})
	if err != nil {
		t.Fatal(err)
	}
	var graph struct {
		Topics []struct {
			Role     string `json:"role"`
			Row      int    `json:"row"`
			Variants []struct {
				Question string `json:"question"`
			} `json:"variants"`
		} `json:"topics"`
	}
	if err := json.Unmarshal(built.Graph, &graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.Topics) != 3 {
		t.Fatalf("topics: %+v", graph.Topics)
	}
	for _, topic := range graph.Topics {
		if topic.Role == "main" && (topic.Row != 5 || topic.Variants[0].Question != "Э4") {
			t.Fatalf("main topic keeps the row of the book and its exam task: %+v", topic)
		}
	}
}

func TestPairedFirstSheetIsNotUnified(t *testing.T) {
	sheet := MapSheet{Rows: [][]string{{"Компетенция", "Составляющая", "Образовательный результат", "Задание 1", "Критерии оценивания к заданию 1"}}}
	if IsUnifiedSheet(sheet) {
		t.Fatal("a sheet of the paired book read as unified")
	}
}
