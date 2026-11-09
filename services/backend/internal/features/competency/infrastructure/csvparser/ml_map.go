package csvparser

import (
	"encoding/csv"
	"errors"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
)

var rpdSection = regexp.MustCompile(`^Р\s*\.\s*(\d+(?:\.\d+)*)\s*(.*)$`)
var rpdCompetency = regexp.MustCompile(`(?:ОПК|ПК)-[0-9]+`)

// parseMLMap accepts the ML spreadsheet export. Its
// task columns contain both complete assignments and fragments of assignments.
// Every source cell is archived; only self-contained assignments become tasks.
// Columns it does not know (№, DOK, ALDs notes, criteria of the simulation…)
// are archived but not read; the outer spaces of a header are ignored.
// «Критерии N» are the criteria of «Задание N». The topic level, the ALDs
// level and the curriculum section are optional: the modes do not use them.
func parseMLMap(reader *csv.Reader, headers []string, headerLine int, restoreCell func(string) string) (competencymap.Map, error) {
	result := competencymap.Map{SourceFormat: "ml-map", SourceHeaders: append([]string(nil), headers...)}
	headers = append([]string(nil), headers...)
	for i := range headers {
		headers[i] = strings.TrimSpace(headers[i])
	}
	columns := make(map[string]int, len(headers))
	taskColumns := []int{}
	criteriaByNumber := map[string]int{}
	taskNumbers := map[int]string{}
	cellOf := func(row []string, name string) string {
		if index, ok := columns[name]; ok {
			return strings.TrimSpace(row[index])
		}
		return ""
	}
	allowed := map[string]bool{
		"": true, "Компетенция": true, "Уровень темы": true,
		"Составляющая": true, "Образовательный результат": true,
		"Что должно войти в тест": true, "Таксономия": true,
		"Уровень ALDs": true, "Важность": true,
		"Раздел РПД · компетенции РПД": true, "ОС": true,
		// Optional planning columns, minutes per outcome.
		theoryMinutesColumn: true, practiceMinutesColumn: true,
	}
	for i, header := range headers {
		match := taskColumn.FindStringSubmatch(header)
		if match == nil && (!allowed[header] || header == "" && i != 0) {
			// An unknown column, or an unnamed one after the first.
			continue
		}
		if _, exists := columns[header]; exists {
			return result, csvError(headerLine, header, "Повторный заголовок.")
		}
		columns[header] = i
		if match != nil {
			if match[1] == "Критерии" {
				criteriaByNumber[match[2]] = i
				continue
			}
			taskColumns = append(taskColumns, i)
			taskNumbers[i] = match[2]
		}
	}
	for _, header := range []string{"Компетенция", "Составляющая", "Образовательный результат", "Что должно войти в тест", "Таксономия", "Важность"} {
		if _, exists := columns[header]; !exists {
			return result, csvError(headerLine, header, "Обязательная колонка карты отсутствует.")
		}
	}
	if len(taskColumns) == 0 {
		return result, csvError(headerLine, "Задание 1", "Нужна хотя бы одна колонка заданий.")
	}
	for number, i := range criteriaByNumber {
		if !slices.Contains(slices.Collect(maps.Values(taskNumbers)), number) {
			return result, csvError(headerLine, headers[i], "Критерии без колонки «Задание "+number+"».")
		}
	}
	seenCompetencies := map[string]bool{}
	constituentIndexes := map[string]int{}
	seenOutcomes := map[string]int{}
	sectionTitles := map[string]string{}
	competency, constituent := "", ""
	// A topic level given once for a competency holds for its constituents
	// without their own.
	topicLevel, competencyLevel := "", ""
	firstData := true
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, csvReadError(err, headerLine)
		}
		line, _ := reader.FieldPos(0)
		for i := range row {
			row[i] = restoreCell(row[i])
		}
		sourceIndex := len(result.SourceRows) + 1
		result.SourceRows = append(result.SourceRows, sourceRow(row, line, sourceIndex))
		if blankRow(row) {
			continue
		}
		if len(row) != len(headers) {
			return result, csvError(line, "CSV", "Число полей ("+strconv.Itoa(len(row))+") не совпадает с заголовками ("+strconv.Itoa(len(headers))+").")
		}
		if index, exists := columns[""]; exists && strings.TrimSpace(row[index]) != "" {
			return result, csvError(line, "CSV", "Безымянный первый столбец должен быть пустым.")
		}
		if firstData && mlExplanationRow(row, columns, taskColumns) {
			firstData = false
			continue
		}
		firstData = false
		outcome := strings.TrimSpace(row[columns["Образовательный результат"]])
		if outcome == "" {
			for _, i := range taskColumns {
				if strings.TrimSpace(row[i]) != "" {
					return result, csvError(line, headers[i], "Задание без образовательного результата.")
				}
			}
			// The final "ПРОВЕРИТЬ: НЕТ В РПД" section contains notes, not
			// outcomes. It remains available in competency_map_source_rows.
			continue
		}
		if value := strings.TrimSpace(row[columns["Компетенция"]]); value != "" {
			if value != competency {
				constituent = ""
				topicLevel = ""
				competencyLevel = ""
			}
			competency = value
		}
		if value := strings.TrimSpace(row[columns["Составляющая"]]); value != "" {
			if value != constituent {
				topicLevel = ""
			}
			constituent = value
		}
		if value := cellOf(row, "Уровень темы"); value != "" {
			canonical, ok := levelCode(value)
			if !ok {
				return result, csvError(line, "Уровень темы", "Ожидается базовый, средний или продвинутый уровень.")
			}
			if topicLevel != "" && topicLevel != canonical {
				return result, csvError(line, "Уровень темы", "Внутри составляющей указаны разные уровни.")
			}
			topicLevel = canonical
			if competencyLevel == "" {
				competencyLevel = canonical
			}
		}
		if competency == "" || constituent == "" {
			return result, csvError(line, "Компетенция/Составляющая", "Для ОР отсутствует родительская запись.")
		}
		cKey := mapKey(competency)
		sKey := mapKey(competency, constituent)
		oKey := mapKey(competency, constituent, outcome)
		if !seenCompetencies[cKey] {
			result.Competencies = append(result.Competencies, competencymap.Competency{Key: cKey, Name: competency})
			seenCompetencies[cKey] = true
		}
		constituentIndex, constituentExists := constituentIndexes[sKey]
		if !constituentExists {
			// Only a new constituent takes the level of its competency; one
			// met before keeps its own.
			if topicLevel == "" {
				topicLevel = competencyLevel
			}
			constituentIndex = len(result.Constituents)
			constituentIndexes[sKey] = constituentIndex
			result.Constituents = append(result.Constituents, competencymap.Constituent{Key: sKey, CompetencyKey: cKey, Name: constituent, TopicLevelCode: topicLevel})
		} else if topicLevel != "" && result.Constituents[constituentIndex].TopicLevelCode != topicLevel {
			return result, csvError(line, "Уровень темы", "Внутри составляющей указаны разные уровни.")
		}
		// A cell that does not start as a section of the curriculum («Р.N …»)
		// holds the text of the lecture: it becomes the educational content
		// of the outcome instead of the ОС column, which then holds the
		// authors' notes (decision of 19.10.2026, map «Введение в ИИ4»). A
		// cell that does start so is a section, and its errors stay errors.
		lecture := ""
		if rawSection := cellOf(row, "Раздел РПД · компетенции РПД"); rawSection != "" {
			if !curriculumSectionHeading(rawSection) {
				lecture = rawSection
			} else {
				section, ok := parseCurriculumSection(rawSection)
				if !ok {
					return result, csvError(line, "Раздел РПД · компетенции РПД", "Ожидается раздел РПД и список компетенций через точку с запятой.")
				}
				if title, exists := sectionTitles[section.Code]; exists && title != section.Title {
					return result, csvError(line, "Раздел РПД · компетенции РПД", "Для одного кода раздела указаны разные названия.")
				}
				sectionTitles[section.Code] = section.Title
				if !mergeCurriculumSection(&result.Constituents[constituentIndex], section) {
					return result, csvError(line, "Раздел РПД · компетенции РПД", "Для одной составляющей раздел указан с разными наборами компетенций.")
				}
			}
		}
		index, exists := seenOutcomes[oKey]
		if !exists {
			index = len(result.Outcomes)
			seenOutcomes[oKey] = index
			result.Outcomes = append(result.Outcomes, competencymap.Outcome{Key: oKey, ConstituentKey: sKey, Name: outcome})
		}
		properties, err := parseOutcomeProperties(row, columns, line)
		if err != nil {
			return result, err
		}
		if lecture != "" {
			properties.educationalContent = &lecture
		}
		if err := mergeOutcomeProperties(&result.Outcomes[index], properties, line); err != nil {
			return result, err
		}
		result.Outcomes[index].SourceRowIndexes = append(result.Outcomes[index].SourceRowIndexes, sourceIndex)
		for _, i := range taskColumns {
			if task, ok := parseMLTask(row[i]); ok {
				task.OutcomeKey = oKey
				if criteria, ok := criteriaByNumber[taskNumbers[i]]; ok {
					task.Criteria = strings.TrimSpace(row[criteria])
				}
				// The provenance keeps the header as written; only its
				// recognition ignores the outer spaces.
				task.Column = result.SourceHeaders[i]
				task.Row = line
				task.SourceRowIndex = sourceIndex
				task.SourceColumnIndex = i + 1
				result.Tasks = append(result.Tasks, task)
			} else if strings.TrimSpace(row[i]) != "" {
				result.UnparsedTaskCells++
				result.Warnings = append(result.Warnings, competencymap.ImportWarning{Row: line, ColumnIndex: i + 1, Column: result.SourceHeaders[i], Code: taskFragmentCode(row[i])})
			}
		}
	}
	if len(result.Outcomes) == 0 {
		return result, csvError(headerLine+1, "Образовательный результат", "Карта не содержит ОР.")
	}
	return result, nil
}

func levelCode(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "базовый":
		return "basic", true
	case "средний":
		return "intermediate", true
	case "продвинутый":
		return "advanced", true
	default:
		return "", false
	}
}

func taxonomyCode(value string) (string, bool) {
	// The nouns of the ML map and the Bloom verbs of the course template;
	// the two highest levels of Bloom count as analysis, the highest here
	// (decision of 19.10.2026).
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "знание", "помнить":
		return "knowledge", true
	case "понимание", "понимать":
		return "understanding", true
	case "применение", "применять":
		return "application", true
	case "анализ", "анализировать", "оценивать", "создавать":
		return "analysis", true
	default:
		return "", false
	}
}

// curriculumSectionHeading reports whether the cell starts as a section of
// the curriculum, «Р.N Название».
func curriculumSectionHeading(value string) bool {
	first, _, _ := strings.Cut(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	return rpdSection.MatchString(strings.TrimSpace(first))
}

func parseCurriculumSection(value string) (competencymap.CurriculumSection, bool) {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	match := rpdSection.FindStringSubmatch(strings.TrimSpace(lines[0]))
	if match == nil {
		return competencymap.CurriculumSection{}, false
	}
	section := competencymap.CurriculumSection{Code: "Р." + match[1], Title: strings.TrimSpace(match[2]), CompetencyCodes: []string{}}
	for _, line := range lines[1:] {
		for _, field := range strings.FieldsFunc(line, func(r rune) bool { return r == ';' || r == ',' }) {
			code := strings.TrimSpace(field)
			if code == "" {
				continue
			}
			if !rpdCompetency.MatchString(code) || rpdCompetency.FindString(code) != code {
				return competencymap.CurriculumSection{}, false
			}
			section.CompetencyCodes = appendUnique(section.CompetencyCodes, code)
		}
	}
	return section, true
}

func mergeCurriculumSection(constituent *competencymap.Constituent, incoming competencymap.CurriculumSection) bool {
	for i := range constituent.Sections {
		current := &constituent.Sections[i]
		if current.Code != incoming.Code {
			continue
		}
		if current.Title != incoming.Title {
			return false
		}
		if len(current.CompetencyCodes) == 0 {
			current.CompetencyCodes = append(current.CompetencyCodes, incoming.CompetencyCodes...)
			return true
		}
		return len(incoming.CompetencyCodes) == 0 || sameCodes(current.CompetencyCodes, incoming.CompetencyCodes)
	}
	constituent.Sections = append(constituent.Sections, incoming)
	return true
}

func sameCodes(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

const (
	theoryMinutesColumn   = "Время на теорию"
	practiceMinutesColumn = "Время на практику"
)

type outcomeProperties struct {
	includeInTest      *bool
	taxonomyCode       string
	aldLevelCode       string
	importance         *int
	educationalContent *string
	theoryMinutes      *int
	practiceMinutes    *int
}

func parseOutcomeProperties(row []string, columns map[string]int, line int) (outcomeProperties, error) {
	properties, err := parsePresentOutcomeProperties(row, columns, line)
	if err != nil {
		return properties, err
	}
	if properties.includeInTest == nil || properties.taxonomyCode == "" || properties.importance == nil {
		return properties, csvError(line, "Карта", "Для образовательного результата нужны включение в тест, таксономия и важность.")
	}
	return properties, nil
}

// Paired maps may contain only some profile columns. Never read an absent column as column zero.
func parsePresentOutcomeProperties(row []string, columns map[string]int, line int) (outcomeProperties, error) {
	var properties outcomeProperties
	cell := func(name string) string {
		if index, ok := columns[name]; ok {
			return strings.TrimSpace(row[index])
		}
		return ""
	}
	if value := cell("Что должно войти в тест"); value != "" {
		var parsed bool
		switch strings.ToUpper(value) {
		case "TRUE":
			parsed = true
		case "FALSE":
			parsed = false
		default:
			return properties, csvError(line, "Что должно войти в тест", "Ожидается TRUE или FALSE.")
		}
		properties.includeInTest = &parsed
	}
	if value := cell("Таксономия"); value != "" {
		code, ok := taxonomyCode(value)
		if !ok {
			return properties, csvError(line, "Таксономия", "Неизвестный уровень таксономии.")
		}
		properties.taxonomyCode = code
	}
	if value := cell("Уровень ALDs"); value != "" {
		code, ok := levelCode(value)
		if !ok {
			return properties, csvError(line, "Уровень ALDs", "Неизвестный уровень ALDs.")
		}
		properties.aldLevelCode = code
	}
	if value := cell("Важность"); value != "" {
		parsed, ok := importanceValue(value)
		if !ok {
			return properties, csvError(line, "Важность", "Ожидается целое число от 1 до 5.")
		}
		properties.importance = &parsed
	}
	if value := cell("ОС"); value != "" {
		properties.educationalContent = &value
	}
	for _, column := range []struct {
		name string
		dst  **int
	}{{theoryMinutesColumn, &properties.theoryMinutes}, {practiceMinutesColumn, &properties.practiceMinutes}} {
		if value := cell(column.name); value != "" {
			parsed, ok := minutesValue(value)
			if !ok {
				return properties, csvError(line, column.name, "Ожидается целое число минут от 0 до 600.")
			}
			*column.dst = &parsed
		}
	}
	return properties, nil
}

// minutesValue accepts whole minutes 0–600, including spreadsheet numerics
// such as "15.0" or "15,0".
func minutesValue(value string) (int, bool) {
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed, parsed >= 0 && parsed <= 600
	}
	number, err := strconv.ParseFloat(strings.Replace(value, ",", ".", 1), 64)
	if err != nil || number != math.Trunc(number) || number < 0 || number > 600 {
		return 0, false
	}
	return int(number), true
}

// importanceValue accepts whole numbers 1–5, including spreadsheet numerics
// written with a fractional zero such as "4.0" or "4,0".
func importanceValue(value string) (int, bool) {
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed, parsed >= 1 && parsed <= 5
	}
	number, err := strconv.ParseFloat(strings.Replace(value, ",", ".", 1), 64)
	if err != nil || number != math.Trunc(number) || number < 1 || number > 5 {
		return 0, false
	}
	return int(number), true
}

func mergeOutcomeProperties(target *competencymap.Outcome, incoming outcomeProperties, line int) error {
	if target.IncludeInTest != nil && incoming.includeInTest != nil && *target.IncludeInTest != *incoming.includeInTest {
		return csvError(line, "Что должно войти в тест", "Для образовательного результата указаны разные значения.")
	}
	if target.TaxonomyCode != "" && incoming.taxonomyCode != "" && target.TaxonomyCode != incoming.taxonomyCode {
		return csvError(line, "Таксономия", "Для образовательного результата указаны разные значения.")
	}
	if target.ALDLevelCode != "" && incoming.aldLevelCode != "" && target.ALDLevelCode != incoming.aldLevelCode {
		return csvError(line, "Уровень ALDs", "Для образовательного результата указаны разные значения.")
	}
	if target.Importance != nil && incoming.importance != nil && *target.Importance != *incoming.importance {
		return csvError(line, "Важность", "Для образовательного результата указаны разные значения.")
	}
	if target.EducationalContent != nil && incoming.educationalContent != nil && *target.EducationalContent != *incoming.educationalContent {
		return csvError(line, "ОС", "Для образовательного результата указаны разные значения.")
	}
	if target.TheoryMinutes != nil && incoming.theoryMinutes != nil && *target.TheoryMinutes != *incoming.theoryMinutes {
		return csvError(line, theoryMinutesColumn, "Для образовательного результата указаны разные значения.")
	}
	if target.PracticeMinutes != nil && incoming.practiceMinutes != nil && *target.PracticeMinutes != *incoming.practiceMinutes {
		return csvError(line, practiceMinutesColumn, "Для образовательного результата указаны разные значения.")
	}
	if incoming.includeInTest != nil {
		target.IncludeInTest = incoming.includeInTest
	}
	if incoming.taxonomyCode != "" {
		target.TaxonomyCode = incoming.taxonomyCode
	}
	if incoming.aldLevelCode != "" {
		target.ALDLevelCode = incoming.aldLevelCode
	}
	if incoming.importance != nil {
		target.Importance = incoming.importance
	}
	if incoming.educationalContent != nil {
		target.EducationalContent = incoming.educationalContent
	}
	if incoming.theoryMinutes != nil {
		target.TheoryMinutes = incoming.theoryMinutes
	}
	if incoming.practiceMinutes != nil {
		target.PracticeMinutes = incoming.practiceMinutes
	}
	return nil
}

func csvReadError(err error, headerLine int) error {
	line := headerLine + 1
	var parseError *csv.ParseError
	if errors.As(err, &parseError) {
		line = parseError.Line
	}
	return csvError(line, "CSV", "Некорректное экранирование или структура строки.")
}

func mlExplanationRow(row []string, columns map[string]int, taskColumns []int) bool {
	if strings.TrimSpace(row[columns["Компетенция"]]) != "" ||
		strings.TrimSpace(row[columns["Образовательный результат"]]) != "Сценарий применения на разном уровне" ||
		!strings.HasPrefix(strings.TrimSpace(row[columns["Составляющая"]]), "Что человек должен делать") {
		return false
	}
	for _, i := range taskColumns {
		if strings.TrimSpace(row[i]) != "" {
			return false
		}
	}
	return true
}
