package app

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
	"net/http"
)

// bloomOfTaxonomy names the Bloom level of the Geek sheet for a taxonomy of
// the learning sheet.
var bloomOfTaxonomy = map[string]string{"Знание": "Помнить", "Понимание": "Понимать", "Применение": "Применять", "Анализ": "Анализировать"}

// courseMapBook turns a learning map (CSV) into the two-sheet workbook a
// course map is imported as: the Geek sheet with the same structure first,
// the learning map second.
func courseMapBook(t *testing.T, learningCSV []byte) []byte {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(learningCSV)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	column := map[string]int{}
	for i, title := range rows[0] {
		column[strings.TrimSpace(title)] = i
	}
	cell := func(row []string, title string) string {
		i, ok := column[title]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	book := excelize.NewFile()
	defer book.Close()
	if err := book.SetSheetName(book.GetSheetName(0), "Карта компетенций"); err != nil {
		t.Fatal(err)
	}
	if _, err := book.NewSheet("Учебная карта"); err != nil {
		t.Fatal(err)
	}
	geek := [][]interface{}{{"№", "Субкомпетенция (Proficiency)", "Составляющая (Observe)", "Образовательный результат (Observe Behaviour)", "Уровень по Блуму", "Важность (1–5)", "Что должно войти в тест", "Задание (устная инструкция)", "Критерии оценивания"}}
	for i, row := range rows[1:] {
		outcome := cell(row, "Образовательный результат")
		if outcome == "" {
			continue
		}
		task := strings.SplitN(cell(row, "Задание 1"), "\n", 2)[0]
		if task == "" {
			task = "Расскажите про «" + outcome + "»."
		}
		bloom := bloomOfTaxonomy[cell(row, "Таксономия")]
		if bloom == "" {
			bloom = "Понимать"
		}
		importance := cell(row, "Важность")
		if importance == "" {
			importance = "3"
		}
		geek = append(geek, []interface{}{fmt.Sprint(i + 1), cell(row, "Компетенция"), cell(row, "Составляющая"), outcome, bloom, importance,
			cell(row, "Что должно войти в тест"), task, "Эталон: " + outcome + "."})
	}
	for i, row := range geek {
		if err := book.SetSheetRow("Карта компетенций", fmt.Sprintf("A%d", i+1), &row); err != nil {
			t.Fatal(err)
		}
	}
	for i, row := range rows {
		values := make([]interface{}, len(row))
		for j, value := range row {
			values[j] = value
		}
		if err := book.SetSheetRow("Учебная карта", fmt.Sprintf("A%d", i+1), &values); err != nil {
			t.Fatal(err)
		}
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// importCourseMap imports a learning map with its Geek sheet through the one
// import route; the learning revision must still be the expected one.
func importCourseMap(t *testing.T, f *fixture, subjectID string, learningCSV []byte, expectedRevision int64, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return uploadWithHeader(f, "/admin/subjects/"+subjectID+"/graph/competency-map", "file", "map.xlsx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", courseMapBook(t, learningCSV), cookie,
		"X-Expected-Learning-Revision", fmt.Sprint(expectedRevision))
}
