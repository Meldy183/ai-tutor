package competencymap

import (
	"fmt"
	"sort"
	"strings"
)

// ValidatePair verifies that the two workbook sheets describe the same course
// structure. Their task data is intentionally independent, so tasks and
// assessment criteria are not compared here.
func ValidatePair(geek, learning Map) error {
	geekKeys, _ := structureKeyList(geek)
	return ValidateStructureKeys(geekKeys, learning)
}

// ValidateStructureKeys checks a Geek sheet's structural keys against the
// learning sheet. Keys are produced by the Geek map parser in the same entity
// layer and keep the two parsers independent.
func ValidateStructureKeys(geekKeys []string, learning Map) error {
	left := make(map[string]struct{}, len(geekKeys))
	for _, key := range geekKeys {
		left[key] = struct{}{}
	}
	rightKeys, rightDuplicates := structureKeyList(learning)
	right := make(map[string]struct{}, len(rightKeys))
	for _, key := range rightKeys {
		right[key] = struct{}{}
	}
	leftDuplicates := duplicates(geekKeys)
	if len(leftDuplicates) > 0 || len(rightDuplicates) > 0 {
		return fmt.Errorf("нельзя однозначно сопоставить повторяющуюся структуру карты: лист Geek: %s; лист обучения: %s", joinKeys(leftDuplicates), joinKeys(rightDuplicates))
	}
	var missing, extra []string
	for key := range left {
		if _, ok := right[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range right {
		if _, ok := left[key]; !ok {
			extra = append(extra, key)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	return fmt.Errorf("листы карты компетенций описывают разную структуру курса: только на листе Geek: %s; только на листе обучения: %s", joinKeys(missing), joinKeys(extra))
}

func StructureKeys(m Map) []string {
	keys, _ := structureKeyList(m)
	return keys
}

func structureKeyList(m Map) ([]string, []string) {
	competencies := map[string]string{}
	for _, competency := range m.Competencies {
		competencies[competency.Key] = cleanKey(competency.Name)
	}
	constituents := map[string]string{}
	for _, constituent := range m.Constituents {
		constituents[constituent.Key] = cleanKey(constituent.Name)
	}
	keys := map[string]struct{}{}
	var duplicates []string
	add := func(key string) {
		if _, exists := keys[key]; exists {
			duplicates = append(duplicates, key)
			return
		}
		keys[key] = struct{}{}
	}
	for _, competency := range m.Competencies {
		add("компетенция/" + cleanKey(competency.Name))
	}
	for _, constituent := range m.Constituents {
		add("составляющая/" + competencies[constituent.CompetencyKey] + "/" + cleanKey(constituent.Name))
	}
	for _, outcome := range m.Outcomes {
		constituent := ""
		for _, candidate := range m.Constituents {
			if candidate.Key == outcome.ConstituentKey {
				constituent = cleanKey(candidate.Name)
				break
			}
		}
		competency := ""
		for _, candidate := range m.Constituents {
			if candidate.Key == outcome.ConstituentKey {
				competency = competencies[candidate.CompetencyKey]
				break
			}
		}
		add("ор/" + competency + "/" + constituent + "/" + cleanKey(outcome.Name))
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, duplicates
}

func duplicates(keys []string) []string {
	seen := map[string]struct{}{}
	var duplicates []string
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			duplicates = append(duplicates, key)
			continue
		}
		seen[key] = struct{}{}
	}
	return duplicates
}

func cleanKey(value string) string { return strings.Join(strings.Fields(strings.ToLower(value)), " ") }

func joinKeys(keys []string) string {
	if len(keys) == 0 {
		return "нет"
	}
	sort.Strings(keys)
	return strings.Join(keys, "; ")
}
