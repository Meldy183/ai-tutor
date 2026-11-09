package coursegraph

import (
	"regexp"
	"strconv"
	"strings"
)

// A unified competency map keeps both modes on one sheet: every outcome is
// one row with its common properties, the tasks of the exam and the tasks of
// the diagnostic, each with their criteria. The questions of the modes are
// different and stay apart (owner's decision); the learning text is the same
// for both.
//
// SplitUnifiedSheet projects such a sheet onto the two sheets the mode
// readers know, row for row, so the row numbers of their messages stay those
// of the book. Prototype on top of the paired book: the projection lets the
// importer, storage, traversal and scale of the paired map stay as they are.

var (
	examTaskColumn           = regexp.MustCompile(`^Задание экзамена\s*(\d+)`)
	examCriteriaColumn       = regexp.MustCompile(`^Критерии к заданию экзамена\s*(\d+)`)
	diagnosticTaskColumn     = regexp.MustCompile(`^Задание диагностики\s*(\d+)`)
	diagnosticCriteriaColumn = regexp.MustCompile(`^Критерии к заданию диагностики\s*(\d+)`)
)

// unifiedColumns are the common columns, found by the prefix of their header.
var unifiedColumns = []struct {
	key      string
	prefixes []string
}{
	{"competency", []string{"Компетенция", "Субкомпетенция"}},
	{"topic_level", []string{"Уровень темы"}},
	{"constituent", []string{"Составляющая"}},
	{"outcome", []string{"Образовательный результат"}},
	{"evidence", []string{"Свидетельство"}},
	{"include", []string{"Что должно войти в тест"}},
	{"bloom", []string{"Таксономия", "Уровень по Блуму"}},
	{"ald", []string{"Уровень ALDs"}},
	{"importance", []string{"Важность"}},
	{"text", []string{"Учебный текст"}},
	{"theory", []string{"Время на теорию"}},
	{"practice", []string{"Время на практику"}},
}

// taskPair is a task column and the column of its criteria, -1 if none.
type taskPair struct{ task, criteria int }

type unifiedLayout struct {
	header     int
	columns    map[string]int
	exam       []taskPair
	diagnostic []taskPair
}

// pairTasks orders the task columns by appearance and gives each the criteria
// column of the same number.
func pairTasks(tasks []int, numbers map[int]string, criteria map[string]int) []taskPair {
	out := make([]taskPair, len(tasks))
	for i, task := range tasks {
		out[i] = taskPair{task: task, criteria: -1}
		if column, ok := criteria[numbers[task]]; ok {
			out[i].criteria = column
		}
	}
	return out
}

// unifiedHeader finds the header of a unified sheet: the first non-empty row,
// holding at least one task column of the exam or of the diagnostic.
func unifiedHeader(rows [][]string) (unifiedLayout, bool) {
	for i, row := range rows {
		if blank(row) {
			continue
		}
		layout := unifiedLayout{header: i, columns: map[string]int{}}
		var examTasks, diagnosticTasks []int
		numbers := map[int]string{}
		examCriteria, diagnosticCriteria := map[string]int{}, map[string]int{}
		for j, title := range row {
			title = strings.TrimSpace(title)
			switch {
			case examTaskColumn.MatchString(title):
				examTasks = append(examTasks, j)
				numbers[j] = examTaskColumn.FindStringSubmatch(title)[1]
				continue
			case examCriteriaColumn.MatchString(title):
				examCriteria[examCriteriaColumn.FindStringSubmatch(title)[1]] = j
				continue
			case diagnosticTaskColumn.MatchString(title):
				diagnosticTasks = append(diagnosticTasks, j)
				numbers[j] = diagnosticTaskColumn.FindStringSubmatch(title)[1]
				continue
			case diagnosticCriteriaColumn.MatchString(title):
				diagnosticCriteria[diagnosticCriteriaColumn.FindStringSubmatch(title)[1]] = j
				continue
			}
			for _, column := range unifiedColumns {
				if _, found := layout.columns[column.key]; found {
					continue
				}
				for _, prefix := range column.prefixes {
					if strings.HasPrefix(title, prefix) {
						layout.columns[column.key] = j
						break
					}
				}
			}
		}
		layout.exam = pairTasks(examTasks, numbers, examCriteria)
		layout.diagnostic = pairTasks(diagnosticTasks, numbers, diagnosticCriteria)
		return layout, len(layout.exam)+len(layout.diagnostic) > 0
	}
	return unifiedLayout{}, false
}

// IsUnifiedSheet reports whether the sheet is a unified map.
func IsUnifiedSheet(sheet MapSheet) bool {
	_, ok := unifiedHeader(sheet.Rows)
	return ok
}

// projection builds one mode's sheet: the common columns it reads, then its
// own tasks and criteria, under the titles its reader knows.
type projection struct {
	header []string
	titles map[string]string
	keys   []string // common column of each header position, "" for none
	tasks  []taskPair
}

func newProjection(source []string, layout unifiedLayout, common [][2]string, tasks []taskPair, taskTitle, criteriaTitle string) projection {
	p := projection{titles: map[string]string{}, tasks: tasks}
	title := func(i int) string {
		if i < 0 || i >= len(source) {
			return ""
		}
		return strings.TrimSpace(source[i])
	}
	for _, c := range common {
		p.header = append(p.header, c[1])
		p.keys = append(p.keys, c[0])
		if i, ok := layout.columns[c[0]]; ok {
			p.titles[c[1]] = title(i)
		}
	}
	for k, pair := range tasks {
		number := strconv.Itoa(k + 1)
		p.header = append(p.header, taskTitle+number, criteriaTitle+number)
		p.titles[taskTitle+number] = title(pair.task)
		if pair.criteria >= 0 {
			p.titles[criteriaTitle+number] = title(pair.criteria)
		}
	}
	return p
}

func (p projection) row(row []string, layout unifiedLayout) []string {
	cell := func(i int) string {
		if i < 0 || i >= len(row) {
			return ""
		}
		return row[i]
	}
	out := make([]string, 0, len(p.header))
	for _, key := range p.keys {
		value := ""
		if i, ok := layout.columns[key]; ok {
			value = strings.TrimSpace(cell(i))
		}
		out = append(out, value)
	}
	for _, pair := range p.tasks {
		out = append(out, cell(pair.task), cell(pair.criteria))
	}
	return out
}

// SplitUnifiedSheet returns the Geek and the learning projections of a
// unified sheet; ok is false when the sheet is not one.
func SplitUnifiedSheet(sheet MapSheet) (exam, learning MapSheet, ok bool) {
	layout, ok := unifiedHeader(sheet.Rows)
	if !ok {
		return MapSheet{}, MapSheet{}, false
	}
	source := sheet.Rows[layout.header]
	examProjection := newProjection(source, layout, [][2]string{
		{"competency", "Компетенция"}, {"constituent", "Составляющая"}, {"outcome", "Образовательный результат"},
		{"evidence", "Свидетельство"}, {"include", "Что должно войти в тест"}, {"bloom", "Таксономия"},
		{"importance", "Важность"}, {"text", "Текст темы"},
	}, layout.exam, "Задание ", "Критерии оценивания ")
	learningProjection := newProjection(source, layout, [][2]string{
		{"", ""}, {"competency", "Компетенция"}, {"topic_level", "Уровень темы"}, {"constituent", "Составляющая"},
		{"outcome", "Образовательный результат"}, {"include", "Что должно войти в тест"}, {"bloom", "Таксономия"},
		{"ald", "Уровень ALDs"}, {"importance", "Важность"}, {"text", "ОС"},
		{"theory", "Время на теорию"}, {"practice", "Время на практику"},
	}, layout.diagnostic, "Задание ", "Критерии ")

	// The exam reader names the columns it reports by their first titles.
	for _, alias := range [][2]string{{columnTitle("competency"), "Компетенция"}, {columnTitle("bloom"), "Таксономия"}} {
		if title, ok := examProjection.titles[alias[1]]; ok {
			examProjection.titles[alias[0]] = title
		}
	}
	number := sheet.Number
	if number == 0 {
		number = 1
	}
	exam = MapSheet{Name: sheet.Name, Rows: make([][]string, len(sheet.Rows)), Number: number, Titles: examProjection.titles}
	learning = MapSheet{Name: sheet.Name, Rows: make([][]string, len(sheet.Rows)), Number: number, Titles: learningProjection.titles}
	exam.Rows[layout.header] = examProjection.header
	learning.Rows[layout.header] = learningProjection.header
	for i := layout.header + 1; i < len(sheet.Rows); i++ {
		if blank(sheet.Rows[i]) {
			continue
		}
		exam.Rows[i] = examProjection.row(sheet.Rows[i], layout)
		learning.Rows[i] = learningProjection.row(sheet.Rows[i], layout)
	}
	return exam, learning, true
}

// BookTitle is the title a column of a projected sheet has in the book.
func (s MapSheet) BookTitle(column string) string {
	if title, ok := s.Titles[strings.TrimSpace(column)]; ok && title != "" {
		return title
	}
	return column
}
