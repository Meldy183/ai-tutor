package coursegraph

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A competency map is the second source of a course graph besides the
// materials (decision of 19.10.2026). Its template is the course map of
// competencies: Субкомпетенция → Составляющая → ОР with Bloom level,
// importance, «Что должно войти в тест», Задание and optional criteria.
// The book serves both modes: the first sheet is the simulation's, the
// second the diagnostic's (decision of 19.10.2026, map «Введение в ИИ4»).
//
// For every constituent with at least three outcomes marked TRUE that have a task, the
// outcome with the highest Bloom level (then importance, then map order) is
// a top-level topic and the two simplest of the others (Bloom ascending,
// importance descending, map order) are its prerequisites. An outcome with
// several tasks («Задание 1», «Задание 2»…) keeps them all as variants, each
// with the criteria column of the same position (or the default rubric if
// absent), and the exam picks one of them when it starts.

// MapVersion is the version of a graph built from a competency map.
const MapVersion = "competency-map-v1"

// CanonicalMapVersion is the runtime projection built from canonical course
// vertices/edges and the Geek-specific tasks for that version.
const CanonicalMapVersion = "competency-map-canonical-v1"

// SourceMap marks a graph built from a competency map.
const SourceMap = "competency_map"

// mapColumns are found by the prefix of their header: the template adds
// English glosses in brackets, and the map of «Введение в ИИ4» names the
// competency and the Bloom level otherwise.
var mapColumns = map[string][]string{
	"competency":  {"Субкомпетенция", "Компетенция"},
	"constituent": {"Составляющая"},
	"outcome":     {"Образовательный результат"},
	"bloom":       {"Уровень по Блуму", "Таксономия"},
	"importance":  {"Важность"},
	"include":     {"Что должно войти в тест"},
	"evidence":    {"Свидетельство"},
	// Both graph columns opt in to an explicit graph, without the legacy
	// selection of three outcomes per constituent. Other books are unchanged.
	"topic_id":      {"ID темы"},
	"prerequisites": {"Пререквизиты"},
	"content":       {"Текст темы"},
}

// The tasks and their criteria are every column with these prefixes, paired
// by position: the first task with the first criteria.
const (
	taskPrefix     = "Задание"
	criteriaPrefix = "Критерии оценивания"
)

var requiredMapColumns = []string{"competency", "constituent", "outcome", "bloom", "importance", "include"}

// columnTitle names a column in messages by its first title.
func columnTitle(key string) string {
	switch key {
	case "task":
		return taskPrefix
	case "criteria":
		return criteriaPrefix
	}
	return mapColumns[key][0]
}

// bloomRanks orders the Bloom levels of the template; the nouns of the
// previous ML map are accepted as well.
var bloomRanks = map[string]int{
	"помнить": 1, "знание": 1,
	"понимать": 2, "понимание": 2,
	"применять": 3, "применение": 3,
	"анализировать": 4, "анализ": 4,
	"оценивать": 5, "оценка": 5,
	"создавать": 6, "синтез": 6,
}

// MapSheet is one worksheet of a competency map book.
type MapSheet struct {
	Name string
	Rows [][]string
}

// MapIssue is a problem in a cell of the map; Row counts from 1 as in Excel.
type MapIssue struct {
	Row     int    `json:"row"`
	Column  string `json:"column"`
	Message string `json:"message"`
}

// SkippedCompetency is a competency left out of the graph and why.
type SkippedCompetency struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// MapGraph is the graph of a competency map in the shape of a course-graph
// build, so Geek and the admin read it as they read a build.
type MapGraph struct {
	Graph   json.RawMessage
	Sheet   string
	Topics  int
	Skipped []SkippedCompetency
}

// MapError rejects a map; Issues name the cells.
type MapError struct {
	Code    string
	Message string
	Issues  []MapIssue
}

func (e *MapError) Error() string { return e.Message }

// mapLayout is where the columns of a map sheet are: one column per key and
// the task and criteria columns in their order.
type mapLayout struct {
	header   int
	columns  map[string]int
	tasks    []int
	criteria []int
}

// mapHeader finds the header row (the first non-empty one) and its columns.
func mapHeader(rows [][]string) (mapLayout, bool) {
	for i, row := range rows {
		if blank(row) {
			continue
		}
		layout := mapLayout{header: i, columns: map[string]int{}}
		for key, prefixes := range mapColumns {
		find:
			for _, prefix := range prefixes {
				for j, title := range row {
					if strings.HasPrefix(strings.TrimSpace(title), prefix) {
						layout.columns[key] = j
						break find
					}
				}
			}
		}
		for j, title := range row {
			switch title = strings.TrimSpace(title); {
			case strings.HasPrefix(title, taskPrefix):
				layout.tasks = append(layout.tasks, j)
			case strings.HasPrefix(title, criteriaPrefix):
				layout.criteria = append(layout.criteria, j)
			}
		}
		for _, key := range requiredMapColumns {
			if _, ok := layout.columns[key]; !ok {
				return layout, false
			}
		}
		return layout, len(layout.tasks) > 0
	}
	return mapLayout{}, false
}

// mapTask is a task of an outcome with the criteria of its position.
type mapTask struct {
	task, criteria string
	position       int
}

type mapOutcome struct {
	row                   int
	competency, outcome   string
	constituent, evidence string
	tasks                 []mapTask
	bloom, importance     int
	order                 int
	id, content           string
	requires              []string
}

// BuildMapGraph builds the Geek course graph from the first sheet. Paired-map
// structure is validated by the importer before it stages the learning sheet.
func BuildMapGraph(sheets []MapSheet) (MapGraph, error) {
	if len(sheets) == 0 {
		return MapGraph{}, &MapError{Code: "COMPETENCY_MAP_INVALID", Message: "В книге нет листов."}
	}
	sheet, rows := sheets[0].Name, sheets[0].Rows
	layout, valid := mapHeader(rows)
	if !valid {
		return MapGraph{}, &MapError{Code: "COMPETENCY_MAP_INVALID",
			Message: fmt.Sprintf("На первом листе «%s» нет обязательных колонок карты: Субкомпетенция (или Компетенция), Составляющая, Образовательный результат, Уровень по Блуму (или Таксономия), Важность, Что должно войти в тест, Задание.", sheet)}
	}
	_, explicit := layout.columns["topic_id"]
	_, dependencies := layout.columns["prerequisites"]
	if explicit != dependencies {
		return MapGraph{}, &MapError{Code: "COMPETENCY_MAP_INVALID", Message: "Для явного графа нужны обе колонки: «ID темы» и «Пререквизиты»."}
	}
	outcomes, constituents, issues := readMapOutcomes(rows)
	if len(issues) > 0 {
		return MapGraph{}, &MapError{Code: "COMPETENCY_MAP_INVALID",
			Message: fmt.Sprintf("Карта на листе «%s» содержит ошибки.", sheet), Issues: issues}
	}
	var graph json.RawMessage
	var topics int
	var skipped []SkippedCompetency
	var err error
	if explicit {
		graph, topics, err = explicitMapGraph(outcomes)
	} else {
		graph, topics, skipped, err = mapGraph(constituents, outcomes)
	}
	if err != nil {
		return MapGraph{}, err
	}
	if topics == 0 {
		if explicit {
			return MapGraph{}, &MapError{Code: "COMPETENCY_MAP_EMPTY", Message: "В явной карте нет включённых тем с заданием."}
		}
		return MapGraph{}, &MapError{Code: "COMPETENCY_MAP_EMPTY",
			Message: "Ни одна составляющая не набрала трёх ОР с TRUE и заданием; граф не из чего собрать.", Issues: skippedIssues(skipped)}
	}
	graph, err = withAssessmentWarnings(graph, outcomes)
	if err != nil {
		return MapGraph{}, err
	}
	return MapGraph{Graph: graph, Sheet: sheet, Topics: topics, Skipped: skipped}, nil
}

// CompetencyMapStructure extracts the authored structure of Geek's map sheet
// in the shared comparison shape used to pair it with the learning sheet.
func CompetencyMapStructureKeys(sheet MapSheet) ([]string, error) {
	_, valid := mapHeader(sheet.Rows)
	if !valid {
		return nil, &MapError{Code: "COMPETENCY_MAP_INVALID", Message: fmt.Sprintf("На листе «%s» нет обязательных колонок карты.", sheet.Name)}
	}
	_, groups, issues := readMapOutcomes(sheet.Rows)
	if len(issues) > 0 {
		return nil, &MapError{Code: "COMPETENCY_MAP_INVALID", Message: fmt.Sprintf("Карта на листе «%s» содержит ошибки.", sheet.Name), Issues: issues}
	}
	keys := []string{}
	competencyNames := map[string]struct{}{}
	constituentNames := map[string]struct{}{}
	for _, group := range groups {
		competency := normalizeMapKey(group.competency)
		constituent := normalizeMapKey(group.constituent)
		if _, ok := competencyNames[competency]; !ok {
			competencyNames[competency] = struct{}{}
			keys = append(keys, "компетенция/"+competency)
		}
		constituentKey := "составляющая/" + competency + "/" + constituent
		if _, ok := constituentNames[constituentKey]; !ok {
			constituentNames[constituentKey] = struct{}{}
			keys = append(keys, constituentKey)
		}
	}
	layout, _ := mapHeader(sheet.Rows)
	competency, constituent := "", ""
	for i := layout.header + 1; i < len(sheet.Rows); i++ {
		row := sheet.Rows[i]
		if blank(row) {
			continue
		}
		cell := func(key string) string {
			index, ok := layout.columns[key]
			if !ok || index >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[index])
		}
		if value := cell("competency"); value != "" {
			if value != competency {
				constituent = ""
			}
			competency = value
		}
		if value := cell("constituent"); value != "" {
			constituent = value
		}
		if outcome := cell("outcome"); outcome != "" {
			keys = append(keys, "ор/"+normalizeMapKey(competency)+"/"+normalizeMapKey(constituent)+"/"+normalizeMapKey(outcome))
		}
	}
	slices.Sort(keys)
	return keys, nil
}

func normalizeMapKey(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func skippedIssues(skipped []SkippedCompetency) []MapIssue {
	out := make([]MapIssue, len(skipped))
	for i, s := range skipped {
		out[i] = MapIssue{Column: columnTitle("constituent"), Message: s.Name + ": " + s.Reason}
	}
	return out
}

// readMapOutcomes reads the outcomes that may enter the graph and every
// constituent of the map in its order; the competency and constituent are
// filled on the first row of their group only.
type mapGroup struct{ competency, constituent string }

func readMapOutcomes(rows [][]string) ([]mapOutcome, []mapGroup, []MapIssue) {
	layout, _ := mapHeader(rows)
	header := layout.header
	at := func(row []string, i int) string {
		if i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	cell := func(row []string, key string) string {
		i, ok := layout.columns[key]
		if !ok {
			return ""
		}
		return at(row, i)
	}
	var (
		out                     []mapOutcome
		constituents            []mapGroup
		issues                  []MapIssue
		competency, constituent string
	)
	issue := func(row int, key, message string) {
		issues = append(issues, MapIssue{Row: row, Column: columnTitle(key), Message: message})
	}
	for i := header + 1; i < len(rows); i++ {
		row, line := rows[i], i+1
		if blank(row) {
			continue
		}
		if v := cell(row, "competency"); v != "" {
			if v != competency {
				constituent = ""
			}
			competency = v
		}
		if v := cell(row, "constituent"); v != "" {
			constituent = v
		}
		outcome := cell(row, "outcome")
		if outcome == "" {
			if _, explicit := layout.columns["topic_id"]; explicit && strings.EqualFold(cell(row, "include"), "TRUE") {
				issue(line, "outcome", "У включённой темы нет образовательного результата (названия темы).")
			}
			continue
		}
		if competency == "" {
			issue(line, "competency", "У ОР нет субкомпетенции выше.")
			continue
		}
		if constituent == "" {
			issue(line, "constituent", "У ОР нет составляющей выше.")
			continue
		}
		group := mapGroup{competency, constituent}
		if !slices.Contains(constituents, group) {
			constituents = append(constituents, group)
		}
		include := strings.ToUpper(cell(row, "include"))
		switch include {
		case "TRUE", "FALSE":
		case "":
			include = "FALSE"
		default:
			issue(line, "include", "Ожидается TRUE или FALSE.")
			continue
		}
		var tasks []mapTask
		for k, i := range layout.tasks {
			task, criteria := at(row, i), ""
			if k < len(layout.criteria) {
				criteria = at(row, layout.criteria[k])
			}
			if task != "" {
				tasks = append(tasks, mapTask{task: task, criteria: criteria, position: k + 1})
			}
		}
		// Only outcomes for the test with a task can be asked.
		if include != "TRUE" || len(tasks) == 0 {
			if _, explicit := layout.columns["topic_id"]; explicit && include == "TRUE" && len(tasks) == 0 {
				issue(line, "task", "У включённой темы нет задания.")
			}
			continue
		}
		bloom, ok := bloomRanks[strings.ToLower(cell(row, "bloom"))]
		if !ok {
			issue(line, "bloom", "Неизвестный уровень по Блуму: ожидается Помнить, Понимать, Применять, Анализировать, Оценивать или Создавать.")
			continue
		}
		importance, ok := importanceValue(cell(row, "importance"))
		if !ok {
			issue(line, "importance", "Ожидается целое число от 1 до 5.")
			continue
		}
		o := mapOutcome{row: line, competency: competency, constituent: constituent, outcome: outcome,
			evidence: cell(row, "evidence"), tasks: tasks, bloom: bloom, importance: importance, order: len(out)}
		if _, explicit := layout.columns["topic_id"]; explicit {
			o.id = cell(row, "topic_id")
			o.content = cell(row, "content")
			if o.id == "" {
				issue(line, "topic_id", "У включённой темы нет ID.")
				continue
			}
			for _, id := range strings.FieldsFunc(cell(row, "prerequisites"), func(r rune) bool { return r == ';' || r == ',' || r == '\n' || r == '\r' }) {
				if id = strings.TrimSpace(id); id != "" && !slices.Contains(o.requires, id) {
					o.requires = append(o.requires, id)
				}
			}
		}
		out = append(out, o)
	}
	return out, constituents, issues
}

func importanceValue(value string) (int, bool) {
	number, err := strconv.ParseFloat(strings.Replace(value, ",", ".", 1), 64)
	if err != nil || number != float64(int(number)) || number < 1 || number > 5 {
		return 0, false
	}
	return int(number), true
}

func blank(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

type mapTopic struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Text               string `json:"text"`
	Question           string `json:"question"`
	AssessmentGuidance string `json:"assessment_guidance,omitempty"`
	// Variants are all the tasks of the outcome; the topic's own question,
	// text and guidance are those of the first one.
	Variants         []mapVariant `json:"variants"`
	Provides         []string     `json:"provides"`
	Requires         []string     `json:"requires"`
	ParentTitle      string       `json:"parent_title"`
	ConstituentTitle string       `json:"constituent_title"`
	CompetencyID     string       `json:"competency_id,omitempty"`
	ConstituentID    string       `json:"constituent_id,omitempty"`
	Role             string       `json:"role"`
	Row              int          `json:"row"`
}

// mapVariant has the shape of exam.Variant.
type mapVariant struct {
	Question           string `json:"question"`
	Content            string `json:"content"`
	AssessmentGuidance string `json:"assessment_guidance,omitempty"`
}

type mapEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Reason string `json:"reason,omitempty"`
}

func mapGraph(order []mapGroup, outcomes []mapOutcome) (json.RawMessage, int, []SkippedCompetency, error) {
	byConstituent := map[mapGroup][]mapOutcome{}
	for _, o := range outcomes {
		key := mapGroup{o.competency, o.constituent}
		byConstituent[key] = append(byConstituent[key], o)
	}
	topics := []mapTopic{}
	prerequisites, hierarchy := []mapEdge{}, []mapEdge{}
	skipped := []SkippedCompetency{}
	pools := make([]Pool, len(order))
	byKey := map[string]mapOutcome{}
	for i, group := range order {
		pool := Pool{Key: strconv.Itoa(i), Name: group.constituent}
		for _, o := range byConstituent[group] {
			key := topicID(o)
			byKey[key] = o
			pool.Candidates = append(pool.Candidates, Candidate{Key: key, Name: o.outcome, Rank: o.bloom, Importance: o.importance, Order: o.order})
		}
		pools[i] = pool
	}
	trees, skips := SelectTrees(pools)
	for _, skip := range skips {
		skipped = append(skipped, SkippedCompetency{Name: skip.Name,
			Reason: fmt.Sprintf("ОР с TRUE и заданием — %d, нужно не меньше трёх: основное и два базовых.", skip.Eligible)})
	}
	for _, tree := range trees {
		name := tree.Name
		main := byKey[tree.Root.Key]
		mainID := topicID(main)
		// Study order: the basic tasks come before the main one.
		requires := make([]string, 0, len(tree.Children))
		for _, child := range tree.Children {
			basic := byKey[child.Key]
			topics = append(topics, newMapTopic(basic, "basic", nil))
			prerequisites = append(prerequisites, mapEdge{Source: topicID(basic), Target: mainID, Reason: "Базовое задание составляющей «" + name + "»."})
			hierarchy = append(hierarchy, mapEdge{Source: mainID, Target: topicID(basic)})
			requires = append(requires, topicID(basic))
		}
		topics = append(topics, newMapTopic(main, "main", requires))
	}
	data, err := json.Marshal(struct {
		Version       string     `json:"version"`
		Source        string     `json:"source"`
		Topics        []mapTopic `json:"topics"`
		Prerequisites []mapEdge  `json:"prerequisites"`
		Hierarchy     []mapEdge  `json:"hierarchy"`
		Quality       struct {
			Acyclic bool `json:"acyclic"`
			Topics  int  `json:"topics"`
		} `json:"quality"`
		Skipped []SkippedCompetency `json:"skipped"`
	}{Version: MapVersion, Source: SourceMap, Topics: topics, Prerequisites: prerequisites, Hierarchy: hierarchy,
		Quality: struct {
			Acyclic bool `json:"acyclic"`
			Topics  int  `json:"topics"`
		}{Acyclic: true, Topics: len(topics)}, Skipped: skipped})
	return data, len(topics), skipped, err
}

func topicID(o mapOutcome) string {
	if o.id != "" {
		return o.id
	}
	return "map-row-" + strconv.Itoa(o.row)
}

func newMapTopic(o mapOutcome, role string, requires []string) mapTopic {
	variants := make([]mapVariant, len(o.tasks))
	for i, t := range o.tasks {
		reference, guidance := splitCriteria(t.criteria)
		if t.criteria == "" {
			guidance = DefaultAssessmentGuidance
		}
		text := o.content
		if text == "" {
			text = "Образовательный результат: " + o.outcome
		}
		if o.content == "" && o.evidence != "" {
			text += "\nСвидетельство: " + o.evidence
		}
		if reference != "" {
			text += "\n" + reference
		}
		variants[i] = mapVariant{Question: t.task, Content: text, AssessmentGuidance: guidance}
	}
	if requires == nil {
		requires = []string{}
	}
	first := variants[0]
	return mapTopic{ID: topicID(o), Title: o.outcome, Text: first.Content, Question: first.Question, AssessmentGuidance: first.AssessmentGuidance,
		Variants: variants, Provides: []string{o.outcome}, Requires: requires, ParentTitle: o.competency, ConstituentTitle: o.constituent, Role: role, Row: o.row}
}

// scoringLine starts the scoring part of the criteria: «Балл (макс. 1)» or an
// analytic rubric with its own К1, К2….
var scoringLine = regexp.MustCompile(`(?i)^(Балл|Аналитическая рубрика|[КK]\d+(?:\s|:)|[+−-]?\d+(?:[.,]\d+)?\s*(?:балл|[—–−-]))`)

// splitCriteria keeps the reference answer (Эталон, Допустимо) as the text of
// the topic and turns the scoring of the map into the teacher's criteria for
// the grader. The grader scores on the topic's scale; the criteria tell what a
// full answer is (decision of 08.11.2026).
func splitCriteria(criteria string) (string, string) {
	var reference, scoring []string
	for _, line := range strings.Split(strings.ReplaceAll(criteria, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case len(scoring) > 0 || scoringLine.MatchString(line):
			scoring = append(scoring, line)
		default:
			reference = append(reference, line)
		}
	}
	guidance := ""
	if len(scoring) > 0 {
		guidance = "Критерии задания из карты компетенций (ориентир, что считать полным ответом):\n" + strings.Join(scoring, "\n")
	}
	return strings.Join(reference, "\n"), guidance
}
