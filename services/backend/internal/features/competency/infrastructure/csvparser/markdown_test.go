package csvparser

import (
	"bytes"
	"encoding/csv"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestMapFormatsPreserveAuthoredMarkdown(t *testing.T) {
	question := "Объясните `x`.\n\n```python\nfor step in range(2):\n    print(\"a\\nb\")\n```\n\n| Шаг | Loss |\n|---|---:|\n| 1 | 0.5 |"
	rows := [][]string{
		{"", "Компетенция", "Уровень темы", "Составляющая", "Образовательный результат", "Что должно войти в тест", "Таксономия", "Уровень ALDs", "Важность", "Раздел РПД · компетенции РПД", "Задание1", "ОС"},
		{"", "К", "Базовый", "С", "ОР", "TRUE", "Знание", "базовый", "4", "Р.1 Введение\nОПК-2", question + "\n\n\n1. **Первый**; с пояснением\n2. Второй\n\n\nОбъясните код.\n\n\nпример.", "ОС"},
	}
	var csvData bytes.Buffer
	writer := csv.NewWriter(&csvData)
	writer.Comma = ';'
	if err := writer.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	workbook := excelize.NewFile()
	defer workbook.Close()
	for row, values := range rows {
		for column, value := range values {
			cell, err := excelize.CoordinatesToCellName(column+1, row+1)
			if err != nil {
				t.Fatal(err)
			}
			if err := workbook.SetCellValue("Sheet1", cell, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	xlsxData, err := workbook.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"CSV": csvData.Bytes(), "XLSX": xlsxData.Bytes()} {
		t.Run(name, func(t *testing.T) {
			parsed, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.Tasks) != 1 {
				t.Fatalf("course_map_tasks = %d, want 1", len(parsed.Tasks))
			}
			if options := parsed.Tasks[0].Options; len(options) != 2 || options[0] != "**Первый**; с пояснением" || options[1] != "Второй" {
				t.Fatalf("numbered options changed: %#v", options)
			}
			if got := parsed.Tasks[0].Question; got != question {
				t.Fatalf("Markdown changed: got %q, want %q", got, question)
			}
		})
	}
}
