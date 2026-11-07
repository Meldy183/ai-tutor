package csvparser

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type Parser struct{}

func (*Parser) Parse(data []byte) (competencymap.Map, error) { return Parse(data) }
func (*Parser) XLSXSheetCount(data []byte) (int, error)      { return XLSXSheetCount(data) }

var taskColumn = regexp.MustCompile(`^(Задание|Критерии) ?([1-9][0-9]*)$`)
var topicColumn = regexp.MustCompile(`^Тем [1-9][0-9]*$`)

func csvError(row int, column, message string) *fault.Error {
	e := fault.New(fault.Invalid, "CSV_INVALID", "Карта компетенций содержит ошибку.")
	e.Details = []fault.Detail{{Path: fmt.Sprintf("row:%d", row), Code: "CSV_INVALID", Message: column + ": " + message}}
	return e
}
func blankRow(row []string) bool {
	for _, s := range row {
		if strings.TrimSpace(s) != "" {
			return false
		}
	}
	return true
}
func mapKey(parts ...string) string { b, _ := json.Marshal(parts); return string(b) }
func sourceRow(row []string, line, index int) competencymap.SourceRow {
	return competencymap.SourceRow{Index: index, Line: line, Cells: append([]string(nil), row...)}
}
func explanationRow(row []string, columns map[string]int) bool {
	// Explain-only row has labels rather than actual competency identifiers.
	competency := strings.ToLower(strings.TrimSpace(row[columns["Ком"]]))
	constituent := strings.ToLower(strings.TrimSpace(row[columns["Сост"]]))
	outcome := strings.ToLower(strings.TrimSpace(row[columns["ОР"]]))
	if competency != "компетенция" || (constituent != "составляющая" && constituent != "составляющая рпд") || outcome != "образовательный результат" {
		return false
	}
	for h, i := range columns {
		match := taskColumn.FindStringSubmatch(h)
		if match == nil {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(row[i]))
		if value == "" {
			continue
		}
		if match[1] == "Задание" {
			if value != "текст задания" && value != "текст вопроса" && value != "задание" && value != "вопрос" {
				return false
			}
		} else if value != "критерии оценивания" && value != "критерии оценки" && value != "текст критериев" && value != "критерии" {
			return false
		}
	}
	return true
}

func Parse(data []byte) (competencymap.Map, error) {
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return parseXLSX(data)
	}
	return parseCSV(data, func(cell string) string { return strings.ReplaceAll(cell, "\x00", "\r") })
}

func parseCSV(data []byte, restoreCell func(string) string) (competencymap.Map, error) {
	var result competencymap.Map
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return result, csvError(1, "CSV", "Ожидается UTF-8 без NUL.")
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	// encoding/csv normalizes CRLF, including inside quoted fields. Protect CR
	// with a byte forbidden in the input, then restore it in every parsed field.
	protected := []byte(text)
	quoted := false
	for i, b := range protected {
		if b == '"' {
			quoted = !quoted
		}
		if quoted && b == '\r' {
			protected[i] = 0
		}
	}
	text = string(protected)
	// A quoted header is legal; try both supported delimiters, selecting by the
	// exact mandatory header fields rather than punctuation inside task text.
	var reader *csv.Reader
	var headers []string
	var headerLine int
	format := ""
	for _, sep := range []rune{',', ';'} {
		candidate := csv.NewReader(strings.NewReader(text))
		candidate.Comma = sep
		candidate.FieldsPerRecord = -1
		for {
			row, err := candidate.Read()
			if err != nil {
				break
			}
			for i := range row {
				row[i] = restoreCell(row[i])
			}
			if blankRow(row) {
				continue
			}
			found := map[string]bool{}
			for _, h := range row {
				found[strings.TrimSpace(h)] = true
			}
			if found["Ком"] && found["Сост"] && found["ОР"] {
				reader = candidate
				headers = row
				headerLine, _ = candidate.FieldPos(0)
				format = "paired"
			} else if found["Компетенция"] && found["Составляющая"] && found["Образовательный результат"] {
				reader = candidate
				headers = row
				headerLine, _ = candidate.FieldPos(0)
				format = "ml-map"
			}
			break
		}
		if reader != nil {
			break
		}
	}
	if reader == nil {
		return result, csvError(1, "CSV", "Первая непустая строка должна содержать Ком/Сост/ОР или Компетенция/Составляющая/Образовательный результат.")
	}
	if format == "ml-map" {
		return parseMLMap(reader, headers, headerLine, restoreCell)
	}
	result.SourceFormat = format
	result.SourceHeaders = append([]string(nil), headers...)
	columns := map[string]int{}
	questions := map[int]int{}
	criteria := map[int]int{}
	firstTask := len(headers)
	for i, h := range headers {
		if _, ok := columns[h]; ok {
			return result, csvError(headerLine, h, "Повторный заголовок.")
		}
		columns[h] = i
		if match := taskColumn.FindStringSubmatch(h); match != nil {
			n, err := strconv.Atoi(match[2])
			if err != nil {
				return result, csvError(headerLine, h, "Некорректный номер пары.")
			}
			firstTask = min(firstTask, i)
			if match[1] == "Задание" {
				if _, exists := questions[n]; exists {
					return result, csvError(headerLine, h, "Повторная колонка задания с тем же номером.")
				}
				questions[n] = i
			} else {
				if _, exists := criteria[n]; exists {
					return result, csvError(headerLine, h, "Повторная колонка критериев с тем же номером.")
				}
				criteria[n] = i
			}
		} else if strings.HasPrefix(h, "Задание") || strings.HasPrefix(h, "Критерии") {
			return result, csvError(headerLine, h, "Ожидается название с положительным целым номером.")
		}
	}
	allowed := map[string]bool{"Ком": true, "Сост": true, "ОР": true, "Что должно войти в тест": true, "Уровень ОР": true, "Таксономия": true, "Важность темы": true, "Важность": true, "Уровень ALDs": true, "ОС": true}
	for i, h := range headers {
		if taskColumn.MatchString(h) {
			continue
		}
		if i >= firstTask {
			return result, csvError(headerLine, h, "Колонки карты должны находиться слева от заданий.")
		}
		if !allowed[h] && !topicColumn.MatchString(h) {
			return result, csvError(headerLine, h, "Неизвестная колонка карты.")
		}
	}
	for n, i := range questions {
		if _, ok := criteria[n]; !ok {
			return result, csvError(headerLine, headers[i], "Нет парной колонки Критерии "+strconv.Itoa(n)+".")
		}
	}
	for n, i := range criteria {
		if _, ok := questions[n]; !ok {
			return result, csvError(headerLine, headers[i], "Нет парной колонки Задание "+strconv.Itoa(n)+".")
		}
	}
	if len(questions) == 0 {
		return result, csvError(headerLine, "Задание 1", "Нужна хотя бы одна пара заданий и критериев.")
	}
	cSeen := map[string]bool{}
	sSeen := map[string]bool{}
	oSeen := map[string]int{}
	inherited := map[string]string{}
	firstData := true
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			line := headerLine + 1
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				line = pe.Line
			}
			return result, csvError(line, "CSV", "Некорректное экранирование или структура строки.")
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
			return result, csvError(line, "CSV", "Число полей не совпадает с заголовками.")
		}
		if explanationRow(row, columns) {
			if !firstData {
				return result, csvError(line, "Ком", "Служебная строка допустима только один раз сразу после заголовков.")
			}
			firstData = false
			continue
		}
		firstData = false
		for _, h := range []string{"Ком", "Сост", "ОР"} {
			value := strings.TrimSpace(row[columns[h]])
			if value == "" {
				value = inherited[h]
			}
			if value == "" {
				return result, csvError(line, h, "Обязательное значение отсутствует и не может быть унаследовано.")
			}
			inherited[h] = value
		}
		cKey := mapKey(inherited["Ком"])
		sKey := mapKey(inherited["Ком"], inherited["Сост"])
		oKey := mapKey(inherited["Ком"], inherited["Сост"], inherited["ОР"])
		if !cSeen[cKey] {
			result.Competencies = append(result.Competencies, competencymap.Competency{Key: cKey, Name: inherited["Ком"]})
			cSeen[cKey] = true
		}
		if !sSeen[sKey] {
			result.Constituents = append(result.Constituents, competencymap.Constituent{Key: sKey, CompetencyKey: cKey, Name: inherited["Сост"]})
			sSeen[sKey] = true
		}
		index, exists := oSeen[oKey]
		if !exists {
			index = len(result.Outcomes)
			oSeen[oKey] = index
			result.Outcomes = append(result.Outcomes, competencymap.Outcome{Key: oKey, ConstituentKey: sKey, Name: inherited["ОР"]})
		}
		propertyColumns := columns
		if column, ok := columns["Важность"]; ok {
			switch strings.ToLower(strings.TrimSpace(row[column])) {
			case "высокая", "средняя", "низкая":
				// Legacy labels have no defined mapping to the numerical 1–5 scale.
				// Retain them in the linked source, reporting rather than inventing a value.
				propertyColumns = make(map[string]int, len(columns))
				for name, index := range columns {
					propertyColumns[name] = index
				}
				delete(propertyColumns, "Важность")
				result.Warnings = append(result.Warnings, competencymap.ImportWarning{
					Row: line, ColumnIndex: column + 1, Column: "Важность", Code: "LEGACY_IMPORTANCE_UNPARSED",
				})
			}
		}
		properties, err := parsePresentOutcomeProperties(row, propertyColumns, line)
		if err != nil {
			return result, err
		}
		if err := mergeOutcomeProperties(&result.Outcomes[index], properties, line); err != nil {
			return result, err
		}
		result.Outcomes[index].SourceRowIndexes = append(result.Outcomes[index].SourceRowIndexes, sourceIndex)
		// Preserve source column order, even when N is not sequential.
		for i, h := range headers {
			match := taskColumn.FindStringSubmatch(h)
			if match == nil || match[1] != "Задание" {
				continue
			}
			n, _ := strconv.Atoi(match[2])
			ci := criteria[n]
			q, c := row[i], row[ci]
			qEmpty, cEmpty := strings.TrimSpace(q) == "", strings.TrimSpace(c) == ""
			if qEmpty && cEmpty {
				continue
			}
			if qEmpty {
				return result, csvError(line, h, "Критерии заполнены без задания.")
			}
			if cEmpty {
				return result, csvError(line, headers[ci], "Задание заполнено без критериев.")
			}
			result.Tasks = append(result.Tasks, competencymap.Task{OutcomeKey: oKey, Question: q, Criteria: c, Column: h, Row: line, SourceRowIndex: sourceIndex, SourceColumnIndex: i + 1})
		}
	}
	if len(result.Tasks) == 0 {
		return result, csvError(headerLine+1, "Задание 1", "Карта не содержит заданий.")
	}
	return result, nil
}
