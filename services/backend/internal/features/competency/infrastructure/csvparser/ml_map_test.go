package csvparser

import (
	_ "embed"
	"strings"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

func TestLegacyBloomVerbsCollapseIntoFourTaxonomyLevels(t *testing.T) {
	for _, tc := range []struct{ verb, want string }{
		{"Помнить", "knowledge"},
		{"Понимать", "understanding"},
		{"Применять", "application"},
		{"Анализировать", "analysis"},
		{"Оценивать", "analysis"},
		{"Создавать", "analysis"},
	} {
		got, ok := taxonomyCode(tc.verb)
		if !ok || got != tc.want {
			t.Errorf("taxonomyCode(%q) = %q, %t; want %q", tc.verb, got, ok, tc.want)
		}
	}
}

//go:embed testdata/ml_map.csv
var originalMLMap []byte

func TestOriginalMLMapProvidesMarkdownPythonTasks(t *testing.T) {
	parsed, err := Parse(originalMLMap)
	if err != nil {
		t.Fatal(err)
	}
	var codeTasks int
	for _, task := range parsed.Tasks {
		if !strings.HasPrefix(task.Question, "код.") {
			continue
		}
		codeTasks++
		if !strings.Contains(task.Question, "```python\nimport numpy as np\n") || !strings.HasSuffix(task.Question, "\n```") {
			t.Errorf("source task must contain a Markdown code block: %q", task.Question)
		}
		if !strings.Contains(task.Question, "\n    y_pred = X @ w + b\n") {
			t.Errorf("Python indentation was lost: %q", task.Question)
		}
	}
	if codeTasks != 2 {
		t.Fatalf("code course_map_tasks = %d, want 2", codeTasks)
	}
}

func TestOriginalMLMapProvidesReadableFormulaTasks(t *testing.T) {
	parsed, err := Parse(originalMLMap)
	if err != nil {
		t.Fatal(err)
	}
	var standardized int
	for _, task := range parsed.Tasks {
		if strings.Contains(task.Question, "\\hat") || strings.Contains(task.Question, "\\begin") {
			t.Errorf("task still contains duplicated LaTeX export: %q", task.Question)
		}
		if strings.HasPrefix(task.Question, "Все признаки стандартизированы.") {
			standardized++
			if strings.Count(task.Question, "ŷ") != 1 {
				t.Errorf("expected one readable prediction formula: %q", task.Question)
			}
		}
	}
	if standardized != 3 {
		t.Fatalf("standardized course_map_tasks = %d, want 3", standardized)
	}
}

func TestOriginalMLMapHasTypedMetadataAndKeepsSourceCoordinates(t *testing.T) {
	parsed, err := Parse(originalMLMap)
	if err != nil {
		t.Fatalf("parse original map: %#v", err)
	}
	if parsed.SourceFormat != "ml-map" {
		t.Fatalf("source format = %q, want ml-map", parsed.SourceFormat)
	}
	if got := [4]int{len(parsed.Competencies), len(parsed.Constituents), len(parsed.Outcomes), len(parsed.Tasks)}; got != [4]int{13, 17, 101, 13} {
		t.Fatalf("map counts = course_map_competencies/course_map_constituents/course_map_outcomes/course_map_tasks %v, want [13 17 101 13]", got)
	}
	if parsed.UnparsedTaskCells != 41 {
		t.Fatalf("unparsed nonempty task cells = %d, want 41", parsed.UnparsedTaskCells)
	}

	var withContent, included, excluded int
	for _, outcome := range parsed.Outcomes {
		if outcome.EducationalContent != nil {
			withContent++
		}
		if outcome.IncludeInTest == nil || outcome.TaxonomyCode == "" || outcome.ALDLevelCode == "" || outcome.Importance == nil {
			t.Errorf("outcome %q is missing typed ML properties: %#v", outcome.Name, outcome)
			continue
		}
		if *outcome.IncludeInTest {
			included++
		} else {
			excluded++
		}
	}
	if withContent != 18 || included != 18 || excluded != 83 {
		t.Fatalf("typed metadata counts: educational content=%d include_in_test true=%d false=%d; want 18/18/83", withContent, included, excluded)
	}
	for _, constituent := range parsed.Constituents {
		if constituent.TopicLevelCode == "" || len(constituent.Sections) == 0 {
			t.Errorf("constituent %q is missing topic level or RPD section", constituent.Name)
		}
	}

	// The same RPD section has a different set of curriculum competency codes
	// for these two course_map_constituents. Keep codes on the constituent-section link.
	var rawPrep, featureSelection []string
	for _, constituent := range parsed.Constituents {
		switch {
		case strings.HasPrefix(constituent.Name, "Умеет подготовить сырые данные"):
			rawPrep = codesForSection(constituent.Sections, "Р.6")
		case strings.HasPrefix(constituent.Name, "Умеет отобрать признаки"):
			featureSelection = codesForSection(constituent.Sections, "Р.6")
		}
	}
	if strings.Join(rawPrep, ",") != "ОПК-2,ОПК-8,ПК-2" {
		t.Errorf("R.6 codes for raw data preparation = %v", rawPrep)
	}
	if strings.Join(featureSelection, ",") != "ОПК-8,ПК-2" {
		t.Errorf("R.6 codes for feature selection = %v", featureSelection)
	}

	first := parsed.Tasks[0]
	if first.Question == "" || first.VoiceInstruction == "" || first.ReferenceAnswer == "" {
		t.Fatalf("first complete task is missing question/instruction/reference answer: %#v", first)
	}
	if len(first.Options) != 4 || first.Options[0] != "классификация" || first.Options[3] != "ранжирование" {
		t.Fatalf("first task options = %v, want the four source options in order", first.Options)
	}
	if !strings.Contains(first.ReferenceAnswer, "целевая переменная") {
		t.Errorf("reference answer was not extracted from Ответ: %q", first.ReferenceAnswer)
	}
	if first.SourceRowIndex < 1 || first.SourceRowIndex > len(parsed.SourceRows) {
		t.Fatalf("task logical row index %d is outside %d source rows", first.SourceRowIndex, len(parsed.SourceRows))
	}
	if first.SourceColumnIndex != 11 {
		t.Fatalf("task source column = %d, want 11 (1-based)", first.SourceColumnIndex)
	}
	row := parsed.SourceRows[first.SourceRowIndex-1]
	if row.Index != first.SourceRowIndex || row.Line != 6 {
		t.Fatalf("task logical/physical source coordinates = %d/%d, want logical %d and physical line 6", row.Index, row.Line, first.SourceRowIndex)
	}
	if !strings.HasPrefix(row.Cells[first.SourceColumnIndex-1], first.Question) {
		t.Fatal("task logical column does not point back to its original question cell")
	}
	if row.Line == row.Index {
		t.Fatal("fixture should demonstrate that logical record index and physical CSV line differ")
	}
	for i, sourceRow := range parsed.SourceRows {
		if sourceRow.Index != i+1 || sourceRow.Line < 1 {
			t.Errorf("source row[%d] has index/physical line %d/%d", i, sourceRow.Index, sourceRow.Line)
		}
	}
}

func codesForSection(sections []competencymap.CurriculumSection, code string) []string {
	for _, section := range sections {
		if section.Code == code {
			return section.CompetencyCodes
		}
	}
	return nil
}

func TestMLMapRejectsConflictingOutcomeMetadata(t *testing.T) {
	data := ";Компетенция;Уровень темы;Составляющая;Образовательный результат;Что должно войти в тест;Таксономия;Уровень ALDs;Важность;Раздел РПД · компетенции РПД;Задание1;ОС\n" +
		";К;Базовый;С;ОР;TRUE;Знание;базовый;4;\"Р.1 Введение\nОПК-2\";;\n" +
		";К;; ;ОР;FALSE;Знание;базовый;4;\"Р.1 Введение\nОПК-2\";;\n"
	_, err := Parse([]byte(data))
	failure, ok := err.(*fault.Error)
	if !ok || failure.Code != "CSV_INVALID" || len(failure.Details) == 0 {
		t.Fatalf("conflicting duplicate metadata should be a CSV_INVALID error: %#v", err)
	}
	if failure.Details[0].Path != "row:4" || !strings.Contains(failure.Details[0].Message, "Что должно войти в тест") {
		t.Fatalf("conflict should identify the later row and conflicting column: %#v", failure.Details[0])
	}
}

// The map may plan minutes for theory and practice per outcome; both columns
// are optional and empty cells stay unset.
func TestMLMapReadsOptionalPlanningMinutes(t *testing.T) {
	header := ";Компетенция;Уровень темы;Составляющая;Образовательный результат;Что должно войти в тест;Таксономия;Уровень ALDs;Важность;Раздел РПД · компетенции РПД;Задание1;ОС;Время на теорию;Время на практику\n"
	data := header +
		";К;Базовый;С;ОР1;TRUE;Знание;базовый;4;;;;20;25,0\n" +
		";К;;С;ОР2;TRUE;Знание;базовый;3;;;;;\n"
	parsed, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	first, second := parsed.Outcomes[0], parsed.Outcomes[1]
	if first.TheoryMinutes == nil || *first.TheoryMinutes != 20 || first.PracticeMinutes == nil || *first.PracticeMinutes != 25 {
		t.Fatalf("minutes: %+v", first)
	}
	if second.TheoryMinutes != nil || second.PracticeMinutes != nil {
		t.Fatalf("empty minutes must stay unset: %+v", second)
	}
	for _, bad := range []string{"-1", "601", "полчаса", "12.5"} {
		_, err := Parse([]byte(header + ";К;Базовый;С;ОР1;TRUE;Знание;базовый;4;;;;" + bad + ";10\n"))
		failure, ok := err.(*fault.Error)
		if !ok || failure.Code != "CSV_INVALID" || !strings.Contains(failure.Details[0].Message, "Время на теорию") {
			t.Fatalf("%q must be rejected: %#v", bad, err)
		}
	}
}

func TestMLMapParsesOptionsWithInternalSemicolons(t *testing.T) {
	header := ";Компетенция;Уровень темы;Составляющая;Образовательный результат;Что должно войти в тест;Таксономия;Уровень ALDs;Важность;Раздел РПД · компетенции РПД;Задание1;ОС\n"
	taskCell := "\"Таблица автосалона\n\n\n1. Объект: автомобиль 2018 года; Таргет: марка; Срок: 2 - 3 дня\n2. Объект: автомобиль; Таргет: цена продажи; Признаки: пробег\n3. Объект: салон 100; Таргет: год\n4. Объект: цена; Таргет: ID\n\n\nНазовите объект\n\n\n2\""
	data := header + ";К;Базовый;С;ОР;TRUE;Знание;базовый;4;\"Р.1 Введение\nОПК-2\";" + taskCell + ";ОС\n"
	parsed, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(parsed.Tasks))
	}
	options := parsed.Tasks[0].Options
	if len(options) != 4 {
		t.Fatalf("expected 4 options, got %d: %#v", len(options), options)
	}
	expected := []string{
		"Объект: автомобиль 2018 года; Таргет: марка; Срок: 2 - 3 дня",
		"Объект: автомобиль; Таргет: цена продажи; Признаки: пробег",
		"Объект: салон 100; Таргет: год",
		"Объект: цена; Таргет: ID",
	}
	for i, want := range expected {
		if options[i] != want {
			t.Errorf("option[%d] = %q, want %q", i, options[i], want)
		}
	}
}

// The diagnostic sheet of the map «Введение в ИИ4»: a numbered first column,
// a header with outer spaces, columns of the simulation the parser does not
// know, Bloom verbs, the topic level on the first row of a competency only,
// and the lecture in «Раздел РПД» with the authors' notes in ОС.
func TestMLMapReadsTheDiagnosticSheetOfMap4(t *testing.T) {
	task := "\"Через что агент действует на среду?\n\n\n- сенсоры\n- исполнительные механизмы\n\n\nВыберите один ответ.\n\n\nисполнительные механизмы\""
	data := "№;Компетенция;Уровень темы;Составляющая ;Образовательный результат;Что должно войти в тест;Таксономия;Уровень ALDs;Важность;Раздел РПД · компетенции РПД;Задание 1;ОС;;Уровень по DOK;Свидетельство (продукт работы);Соответствие целям дисциплины\n" +
		"7;К1. Агент;средний;1.2 PEAS;Даёт определение агента;TRUE;Понимать;базовый;4.0;Нед. 3, лекция «Agents». Агент воспринимает среду сенсорами.;" + task + ";;;1.0;Ответ;✓\n" +
		"8;;;;Составляет PEAS;TRUE;Применять;средний;5.0;Нед. 3, лекция «Agents».;;перегенерить задание;;2.0;Описание;✓\n" +
		"9;;;1.3 Среда;Определяет свойства среды;TRUE;Анализировать;средний;3.0;\"Р.1 Введение\nОПК-2\";;Учебный контекст;;3.0;Таблица;\n" +
		"10;К6. ИНС;продвинутый;6.1 Перцептрон;Строит перцептрон;TRUE;Помнить;базовый;4;;;;;1.0;Схема;✓\n" +
		"11;;;;Проектирует сеть;TRUE;Создавать;продвинутый;3;;;;;3.0;Схема;\n"
	parsed, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Constituents) != 3 || len(parsed.Outcomes) != 5 || len(parsed.Tasks) != 1 {
		t.Fatalf("map = %+v", parsed)
	}
	// The level of the competency holds for its course_map_constituents without one.
	for i, want := range []string{"intermediate", "intermediate", "advanced"} {
		if got := parsed.Constituents[i].TopicLevelCode; got != want {
			t.Fatalf("constituent %d level = %q, want %q", i, got, want)
		}
	}
	for i, want := range []string{"understanding", "application", "analysis", "knowledge", "analysis"} {
		if got := parsed.Outcomes[i].TaxonomyCode; got != want {
			t.Fatalf("outcome %d taxonomy = %q, want %q", i, got, want)
		}
	}
	// The lecture becomes the educational content; the notes in ОС do not.
	content := func(i int) string {
		if parsed.Outcomes[i].EducationalContent == nil {
			return ""
		}
		return *parsed.Outcomes[i].EducationalContent
	}
	if content(0) != "Нед. 3, лекция «Agents». Агент воспринимает среду сенсорами." || content(1) != "Нед. 3, лекция «Agents»." {
		t.Fatalf("contents = %q, %q", content(0), content(1))
	}
	// A section of the curriculum is still read as one, with ОС as before.
	if content(2) != "Учебный контекст" || len(parsed.Constituents[1].Sections) != 1 || parsed.Constituents[1].Sections[0].Code != "Р.1" {
		t.Fatalf("section = %+v, content %q", parsed.Constituents[1], content(2))
	}
	if parsed.Tasks[0].ReferenceAnswer != "исполнительные механизмы" || len(parsed.Tasks[0].Options) != 2 {
		t.Fatalf("task = %+v", parsed.Tasks[0])
	}
}

const map4Header = "Компетенция;Уровень темы;Составляющая;Образовательный результат;Что должно войти в тест;Таксономия;Уровень ALDs;Важность;Раздел РПД · компетенции РПД;ОС;Задание 1\n"

// A constituent met again keeps its own level; only a new one without a level
// takes that of its competency.
func TestMLMapKeepsTheLevelOfAConstituentMetAgain(t *testing.T) {
	data := map4Header +
		"К;базовый;С1;О1;TRUE;Знание;базовый;3;;;\n" +
		";продвинутый;С2;О2;TRUE;Знание;базовый;3;;;\n" +
		";;С1;О3;TRUE;Знание;базовый;3;;;\n" +
		";;С2;О4;TRUE;Знание;базовый;3;;;\n" +
		";;С3;О5;TRUE;Знание;базовый;3;;;\n"
	parsed, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"basic", "advanced", "basic"} {
		if got := parsed.Constituents[i].TopicLevelCode; got != want {
			t.Fatalf("constituent %d level = %q, want %q", i, got, want)
		}
	}
}

// Only a cell that does not start as «Р.N …» is a lecture; a section with a
// wrong competency code is still an error.
func TestMLMapRejectsAWrongSectionInsteadOfTakingItForALecture(t *testing.T) {
	_, err := Parse([]byte(map4Header + "К;базовый;С;О;TRUE;Знание;базовый;3;\"Р.1 Введение\nОПК-X\";;\n"))
	failure, ok := err.(*fault.Error)
	if !ok || failure.Code != "CSV_INVALID" || !strings.Contains(failure.Details[0].Message, "Раздел РПД") {
		t.Fatalf("wrong section: %#v", err)
	}
}

// The provenance of a task keeps its header as written, outer spaces too.
func TestMLMapKeepsTheHeaderOfATaskAsWritten(t *testing.T) {
	header := strings.Replace(map4Header, "Задание 1", " Задание 1 ", 1)
	task := "\"Вопрос?\n\n\n- да\n- нет\n\n\nОтветьте.\n\n\nда\""
	parsed, err := Parse([]byte(header + "К;базовый;С;О;TRUE;Знание;базовый;3;;;" + task + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tasks) != 1 || parsed.Tasks[0].Column != " Задание 1 " || parsed.SourceHeaders[parsed.Tasks[0].SourceColumnIndex-1] != parsed.Tasks[0].Column {
		t.Fatalf("task = %+v, headers %q", parsed.Tasks, parsed.SourceHeaders)
	}
}
