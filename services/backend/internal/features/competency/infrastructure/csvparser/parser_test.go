package csvparser

import (
	"strings"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

const mapCSV = "Ком,Сост,ОР,Тем 1,Уровень ОР,Задание 1,Критерии 1,Задание 2,Критерии 2\n" +
	"К1,С1,ОР1,Тема,Основной,Вопрос 1,\"Критерий, один\nНе менять текст\",Вопрос 2,Критерий 2\n" +
	",,,,Базовый,Вопрос 3,Критерий 3,,\n" +
	",,ОР2,,,Вопрос 4,Критерий 4,,\n"

func TestCSVInheritanceAndOpaqueCriteria(t *testing.T) {
	m, err := Parse([]byte("\ufeff\n" + mapCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Competencies) != 1 || len(m.Constituents) != 1 || len(m.Outcomes) != 2 || len(m.Tasks) != 4 {
		t.Fatalf("wrong counts: %+v", m)
	}
	if m.Tasks[0].Criteria != "Критерий, один\nНе менять текст" {
		t.Fatalf("criteria changed: %q", m.Tasks[0].Criteria)
	}
	if m.Tasks[2].OutcomeKey != m.Tasks[0].OutcomeKey {
		t.Fatal("merged cells not inherited")
	}
	if m.SourceRows[0].Cells[3] != "Тема" {
		t.Fatal("original source cell was not preserved")
	}
}

func TestCSVSemicolonAndExplanationRow(t *testing.T) {
	csv := "Ком;Сост;ОР;Задание 1;Критерии 1\r\nКомпетенция;Составляющая;Образовательный результат;Текст задания;Критерии оценивания\r\nК1;С1;ОР1;Вопрос;\"  Всё как есть\r\nстрока 2  \"\r\n"
	m, err := Parse([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tasks) != 1 || m.Tasks[0].Criteria != "  Всё как есть\r\nстрока 2  " {
		t.Fatalf("semicolon import: %+v", m)
	}
}

func TestCSVEmptyPairsAreIgnored(t *testing.T) {
	m, err := Parse([]byte("Ком,Сост,ОР,Задание 1,Критерии 1\nК,С,ОР без задания,,\n,,ОР с заданием,Вопрос,Критерий\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Outcomes) != 2 || len(m.Tasks) != 1 {
		t.Fatalf("empty pairs not ignored: %+v", m)
	}
}

func TestCSVDescriptiveNamesAreData(t *testing.T) {
	m, err := Parse([]byte("Ком,Сост,ОР,Задание 1,Критерии 1\nКомпетенция 1,Составляющая 1,Образовательный результат 1,Первый вопрос,Первые критерии\n,,,Второй вопрос,Вторые критерии\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tasks) != 2 || m.Tasks[0].Question != "Первый вопрос" {
		t.Fatal("real data skipped as explanation")
	}
}

func TestCSVInvalidRows(t *testing.T) {
	cases := []struct{ name, csv, column string }{
		{"no criteria header", "Ком,Сост,ОР,Задание 1\nК,С,О,В\n", "Задание 1"},
		{"orphan criteria", "Ком,Сост,ОР,Критерии 1\nК,С,О,К\n", "Критерии 1"},
		{"missing inheritance", "Ком,Сост,ОР,Задание 1,Критерии 1\n,С,О,В,К\n", "Ком"},
		{"missing criterion", "Ком,Сост,ОР,Задание 1,Критерии 1\nК,С,О,В,\n", "Критерии 1"},
		{"orphan criterion cell", "Ком,Сост,ОР,Задание 1,Критерии 1\nК,С,О,,К\n", "Задание 1"},
		{"second explanation", "Ком,Сост,ОР,Задание 1,Критерии 1\nКомпетенция,Составляющая,Образовательный результат,Текст задания,Критерии оценивания\nКомпетенция,Составляющая,Образовательный результат,Текст задания,Критерии оценивания\nК,С,О,В,К\n", "Ком"},
		{"duplicate header", "Ком,Сост,ОР,Задание 1,Критерии 1,Задание 1\nК,С,О,В,К,В\n", "Задание 1"},
		{"map after course_map_tasks", "Ком,Сост,Задание 1,Критерии 1,ОР\nК,С,В,К,О\n", "ОР"},
		{"no course_map_tasks", "Ком,Сост,ОР,Задание 1,Критерии 1\nК,С,О,,\n", "Задание 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.csv))
			e, ok := err.(*fault.Error)
			if !ok || e.Code != "CSV_INVALID" || len(e.Details) == 0 || !strings.HasPrefix(e.Details[0].Path, "row:") || !strings.Contains(e.Details[0].Message, tc.column) {
				t.Fatalf("wrong CSV error: %#v", err)
			}
		})
	}
	for _, data := range [][]byte{[]byte("Ком,Сост,ОР,Задание 1,Критерии 1\nК,С,О,\"broken,К\n"), {0xff, 0xfe}, []byte(""), []byte("Ком,Сост,ОР,Задание 1,Критерии 1\nК,С,О,В,К,extra\n")} {
		if _, err := Parse(data); err == nil {
			t.Fatal("invalid CSV accepted")
		}
	}
}

func TestCSVInheritanceIsIndependentAcrossColumns(t *testing.T) {
	data := "Ком,Сост,ОР,Что должно войти в тест,Таксономия,Важность темы,Важность,Задание 3,Критерии 3\nК1,С1,О1,TRUE,Знание,тема,3,В1,К1\nК2,,,,,,,В2,К2\n"
	parsed, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Competencies) != 2 || len(parsed.Constituents) != 2 || len(parsed.Outcomes) != 2 || parsed.Constituents[1].Name != "С1" || parsed.Outcomes[1].Name != "О1" {
		t.Fatalf("independent inheritance: %+v", parsed)
	}
	for _, cell := range []string{"TRUE", "Знание", "тема", "3"} {
		if !strings.Contains(strings.Join(parsed.SourceRows[0].Cells, ","), cell) {
			t.Fatalf("missing original cell %s", cell)
		}
	}
}

func TestCSVPhysicalRowsIncludeBlankAndMultilineRecords(t *testing.T) {
	data := "\nКом,Сост,ОР,Задание 1,Критерии 1\n\nК,С,О,В,\"Критерий\nстрока\"\n,,,Следующий,\n"
	_, err := Parse([]byte(data))
	failure, ok := err.(*fault.Error)
	if !ok || len(failure.Details) != 1 || failure.Details[0].Path != "row:6" {
		t.Fatalf("physical row: %#v", err)
	}
	parsed, err := Parse([]byte("\nКом,Сост,ОР,Задание 1,Критерии 1\n\nК,С,О,В,К\n"))
	if err != nil || len(parsed.Tasks) != 1 || parsed.Tasks[0].Row != 4 {
		t.Fatalf("task source row: %+v %v", parsed, err)
	}
}

func TestPairedCSVNormalizesPresentProperties(t *testing.T) {
	parsed, err := Parse([]byte("Ком,Сост,ОР,Что должно войти в тест,Уровень ALDs,Таксономия,Важность,Задание 1,Критерии 1\nК,С,О,TRUE,Базовый,Знание,3,В,К\n,,,TRUE,Базовый,Знание,3,В2,К2\n"))
	if err != nil {
		t.Fatal(err)
	}
	o := parsed.Outcomes[0]
	if o.IncludeInTest == nil || !*o.IncludeInTest || o.TaxonomyCode != "knowledge" || o.ALDLevelCode != "basic" || o.Importance == nil || *o.Importance != 3 {
		t.Fatalf("properties lost: %#v", o)
	}
}
func TestPairedCSVLinksEveryOutcomeRow(t *testing.T) {
	parsed, err := Parse([]byte("Ком,Сост,ОР,Задание 1,Критерии 1\nК,С,О,В,К\n,,,В2,К2\n,,Без задания,,\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Outcomes[0].SourceRowIndexes) != 2 || len(parsed.Outcomes[1].SourceRowIndexes) != 1 || parsed.Outcomes[1].SourceRowIndexes[0] != 3 {
		t.Fatalf("source links lost: %#v", parsed.Outcomes)
	}
}
func TestPairedCSVRejectsInvalidOrConflictingProperties(t *testing.T) {
	for _, rows := range []string{"К,С,О,9,В,К\n", "К,С,О,3,В,К\n,,,4,В2,К2\n"} {
		if _, err := Parse([]byte("Ком,Сост,ОР,Важность,Задание 1,Критерии 1\n" + rows)); err == nil {
			t.Fatalf("invalid properties accepted: %s", rows)
		}
	}
}

func TestPairedCSVKeepsLegacyImportanceWithWarning(t *testing.T) {
	parsed, err := Parse([]byte("Ком,Сост,ОР,Важность,Задание 1,Критерии 1\nК,С,О,Высокая,В,К\n"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Outcomes[0].Importance != nil || parsed.SourceRows[0].Cells[3] != "Высокая" || len(parsed.Warnings) != 1 || parsed.Warnings[0].Code != "LEGACY_IMPORTANCE_UNPARSED" {
		t.Fatalf("legacy importance: %#v", parsed)
	}
}
