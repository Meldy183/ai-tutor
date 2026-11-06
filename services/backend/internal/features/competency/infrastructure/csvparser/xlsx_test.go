package csvparser

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

func TestXLSXMapPreservesBlankRowsAndParsesSourceCoordinates(t *testing.T) {
	workbook := excelize.NewFile()
	sheet := workbook.GetSheetName(0)
	if err := workbook.SetCellValue(sheet, "A1", "Заметки"); err != nil {
		t.Fatal(err)
	}

	reader := csv.NewReader(bytes.NewReader(originalMLMap))
	reader.FieldsPerRecord = -1
	for rowIndex := 1; ; rowIndex++ {
		row, err := reader.Read()
		if err != nil {
			break
		}
		if rowIndex == 1 && len(row) > 0 {
			row[0] = strings.TrimPrefix(row[0], "\ufeff")
		}
		for columnIndex, value := range row {
			cell, err := excelize.CoordinatesToCellName(columnIndex+1, rowIndex+2)
			if err != nil {
				t.Fatal(err)
			}
			if value != "" {
				if err := workbook.SetCellValue(sheet, cell, value); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := workbook.SetCellValue(sheet, "A2", ""); err != nil {
		t.Fatal(err)
	}
	if err := workbook.SetCellValue(sheet, "N2", ""); err != nil {
		t.Fatal(err)
	}
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	if err := workbook.Close(); err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(buffer.Bytes())
	if err != nil {
		t.Fatalf("parse generated XLSX: %#v", err)
	}
	if len(parsed.Outcomes) != 101 || len(parsed.Tasks) != 13 || parsed.UnparsedTaskCells != 41 {
		t.Fatalf("parsed XLSX counts: learning_outcomes=%d learning_tasks=%d fragments=%d", len(parsed.Outcomes), len(parsed.Tasks), parsed.UnparsedTaskCells)
	}
	firstTask := parsed.Tasks[0]
	if firstTask.SourceColumnIndex != 11 || firstTask.SourceRowIndex <= 0 {
		t.Fatalf("source coordinate: row=%d column=%d", firstTask.SourceRowIndex, firstTask.SourceColumnIndex)
	}
	if !strings.HasPrefix(parsed.SourceRows[firstTask.SourceRowIndex-1].Cells[firstTask.SourceColumnIndex-1], firstTask.Question) {
		t.Fatal("XLSX source coordinates do not point to original task cell")
	}
}

// book writes sheets of rows into a workbook.
func book(t *testing.T, sheets map[string][][]any, order []string) []byte {
	t.Helper()
	workbook := excelize.NewFile()
	for i, name := range order {
		if i == 0 {
			if err := workbook.SetSheetName(workbook.GetSheetName(0), name); err != nil {
				t.Fatal(err)
			}
		} else if _, err := workbook.NewSheet(name); err != nil {
			t.Fatal(err)
		}
		for r, row := range sheets[name] {
			for c, value := range row {
				cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
				if err := workbook.SetCellValue(name, cell, value); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	workbook.Close()
	return buffer.Bytes()
}

// One book serves both modes of the stand: the simulation reads the first
// sheet and the diagnostic the second (decision of 19.10.2026).
func TestLearningStillReadsSecondSheet(t *testing.T) {
	headers := []any{"Компетенция", "Составляющая", "Образовательный результат", "Уровень темы", "Что должно войти в тест", "Таксономия", "Уровень ALDs", "Важность", "Раздел РПД · компетенции РПД", "ОС", "Задание1"}
	simulation := [][]any{headers, {"К", "С", "Для симуляции", "Базовый", true, "Знание", "Базовый", 3, "", "", ""}}
	diagnostic := [][]any{headers, {"К", "С", "Для диагностики", "Базовый", true, "Знание", "Базовый", 3, "", "", ""}}
	legend := [][]any{{"Поле", "Пояснение"}}
	data := book(t, map[string][][]any{"Симуляция": simulation, "Диагностика": diagnostic, "Легенда": legend}, []string{"Симуляция", "Диагностика", "Легенда"})
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Outcomes) != 1 || parsed.Outcomes[0].Name != "Для диагностики" {
		t.Fatalf("learning_outcomes = %+v", parsed.Outcomes)
	}
	// A book of one sheet is read as before.
	if parsed, err := Parse(book(t, map[string][][]any{"Карта": simulation}, []string{"Карта"})); err != nil || parsed.Outcomes[0].Name != "Для симуляции" {
		t.Fatalf("one sheet: %+v %v", parsed.Outcomes, err)
	}
	_, err = Parse(book(t, map[string][][]any{"Карта": diagnostic, "Легенда": legend}, []string{"Карта", "Легенда"}))
	failure, ok := err.(*fault.Error)
	if !ok || failure.Code != "CSV_INVALID" || !strings.Contains(failure.Details[0].Message, "втором листе «Легенда»") {
		t.Fatalf("second sheet without the map: %#v", err)
	}
}

func TestParseXLSXSheetSelectsEitherCourseMapSheet(t *testing.T) {
	headers := []any{"Компетенция", "Уровень темы", "Составляющая", "Образовательный результат", "Что должно войти в тест", "Таксономия", "Уровень ALDs", "Важность", "Раздел РПД · компетенции РПД", "ОС", "Задание 1"}
	bookData := book(t, map[string][][]any{
		"Экзамен":  {headers, {"К1", "средний", "С1", "Устный ОР", true, "Понимать", "средний", 4, "", "", "Объясните концепцию своими словами."}},
		"Обучение": {headers, {"К1", "средний", "С1", "Учебный ОР", true, "Понимать", "средний", 4, "", "", "Выберите правильный ответ."}},
	}, []string{"Экзамен", "Обучение"})

	for index, want := range []string{"Устный ОР", "Учебный ОР"} {
		parsed, err := ParseXLSXSheet(bookData, index)
		if err != nil {
			t.Fatalf("parse sheet %d: %v", index+1, err)
		}
		if len(parsed.Outcomes) != 1 || parsed.Outcomes[0].Name != want {
			t.Fatalf("sheet %d outcomes = %+v, want %q", index+1, parsed.Outcomes, want)
		}
	}
	if _, err := ParseXLSXSheet(bookData, 2); err == nil {
		t.Fatal("out-of-range sheet index accepted")
	}
}

func TestXLSXNativeBooleanValues(t *testing.T) {
	for _, value := range []bool{true, false} {
		workbook := excelize.NewFile()
		sheet := workbook.GetSheetName(0)
		headers := []string{"Компетенция", "Составляющая", "Образовательный результат", "Уровень темы", "Что должно войти в тест", "Таксономия", "Уровень ALDs", "Важность", "Раздел РПД · компетенции РПД", "ОС", "Задание1"}
		row := []any{"К", "С", "О", "Базовый", value, "Знание", "Базовый", 3, "", "", ""}
		for i, header := range headers {
			cell, _ := excelize.CoordinatesToCellName(i+1, 1)
			if err := workbook.SetCellValue(sheet, cell, header); err != nil {
				t.Fatal(err)
			}
			cell, _ = excelize.CoordinatesToCellName(i+1, 2)
			if err := workbook.SetCellValue(sheet, cell, row[i]); err != nil {
				t.Fatal(err)
			}
		}
		buffer, err := workbook.WriteToBuffer()
		if err != nil {
			t.Fatal(err)
		}
		workbook.Close()
		parsed, err := Parse(buffer.Bytes())
		if err != nil {
			t.Fatalf("boolean %v: %v", value, err)
		}
		if len(parsed.Outcomes) != 1 || parsed.Outcomes[0].IncludeInTest == nil || *parsed.Outcomes[0].IncludeInTest != value {
			t.Fatalf("boolean %v: %#v", value, parsed.Outcomes)
		}
	}
}
