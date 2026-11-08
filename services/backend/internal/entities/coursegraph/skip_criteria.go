package coursegraph

import (
	"encoding/json"
	"slices"
)

// SkipMissingCriteria removes the variants that received our fallback rubric.
// A constituent left with fewer than TreeSize topics leaves the check whole, so
// a basic topic never takes the place of a removed main one. The full bank and
// the selection of the other constituents are unchanged; dangling references
// are removed.
func SkipMissingCriteria(data json.RawMessage) (json.RawMessage, error) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil {
		return nil, err
	}
	var version string
	_ = json.Unmarshal(shape["version"], &version)
	if version != MapVersion {
		return nil, &MapError{Code: "COMPETENCY_MAP_INVALID", Message: "Исключение доступно только для графа карты компетенций."}
	}
	var topics []mapTopic
	if err := json.Unmarshal(shape["topics"], &topics); err != nil {
		return nil, err
	}
	filtered := []mapTopic{}
	remaining := map[string]int{}
	skipped := 0
	for _, topic := range topics {
		variants := []mapVariant{}
		for _, v := range topic.Variants {
			if v.AssessmentGuidance == DefaultAssessmentGuidance {
				skipped++
				continue
			}
			variants = append(variants, v)
		}
		if len(variants) == 0 {
			continue
		}
		topic.Variants = variants
		topic.Question, topic.Text, topic.AssessmentGuidance = variants[0].Question, variants[0].Content, variants[0].AssessmentGuidance
		filtered = append(filtered, topic)
		remaining[constituentKey(topic)]++
	}
	if skipped == 0 {
		return data, nil
	}
	kept := []mapTopic{}
	ids, available := map[string]bool{}, map[string]bool{}
	for _, topic := range filtered {
		if remaining[constituentKey(topic)] < TreeSize {
			continue
		}
		kept = append(kept, topic)
		ids[topic.ID] = true
		available[topic.ID] = true
		for _, p := range topic.Provides {
			available[p] = true
		}
	}
	if len(kept) == 0 {
		return nil, &MapError{Code: "COMPETENCY_MAP_EMPTY", Message: "После исключения заданий без критериев в экзамене не останется вопросов. Заполните критерии и импортируйте карту снова."}
	}
	for i := range kept {
		kept[i].Requires = slices.DeleteFunc(kept[i].Requires, func(id string) bool { return !available[id] })
	}
	shape["topics"], _ = json.Marshal(kept)
	for _, key := range []string{"prerequisites", "hierarchy"} {
		var edges []mapEdge
		if err := json.Unmarshal(shape[key], &edges); err != nil {
			return nil, err
		}
		edges = slices.DeleteFunc(edges, func(e mapEdge) bool { return !ids[e.Source] || !ids[e.Target] })
		shape[key], _ = json.Marshal(edges)
	}
	if raw := shape["quality"]; raw != nil {
		var quality map[string]json.RawMessage
		if err := json.Unmarshal(raw, &quality); err != nil {
			return nil, err
		}
		quality["topics"], _ = json.Marshal(len(kept))
		shape["quality"], _ = json.Marshal(quality)
	}
	delete(shape, "assessment_warnings")
	shape["assessment_skipped"], _ = json.Marshal(skipped)
	return json.Marshal(shape)
}

func constituentKey(topic mapTopic) string {
	return topic.ParentTitle + "\x00" + topic.ConstituentTitle
}
