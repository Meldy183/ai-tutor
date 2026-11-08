package variantgenhttp

import (
	"errors"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/httpx"
	"net/http"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type DiagnosticOutline struct {
	Revision         int64               `json:"revision"`
	AlgorithmVersion string              `json:"algorithm_version"`
	Competencies     []OutlineCompetency `json:"competencies"`
}
type OutlineCompetency struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	DisplayName  string               `json:"display_name,omitempty" binding:"optional"`
	Constituents []OutlineConstituent `json:"constituents"`
}
type OutlineConstituent struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	DisplayName string           `json:"display_name,omitempty" binding:"optional"`
	Outcomes    []OutlineOutcome `json:"outcomes"`
}
type OutlineOutcome struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role" enums:"main,basic"`
}

// Preview returns only the blocks and outcomes selected for diagnostics.
// @Summary Предпросмотр блоков диагностики
// @Description Применяет отбор constituent-map-v2 к текущей карте без сохранения варианта и запуска диагностики. Возвращает только названия и ID выбранных компетенций, составляющих и ОР с ролью main/basic. Короткие display_name компетенций и составляющих готовятся через LLM gateway и кэшируются; исходные name сохранены. Вопросы, эталоны, criteria и ОС не выдаются. Пустой отбор возвращается пустым списком.
// @ID previewDiagnosticOutline
// @Tags Варианты
// @Security accessCookie
// @Produce json
// @Param subject_id path string true "ID предмета"
// @Success 200 {object} DiagnosticOutline
// @Failure 401 {object} fault.Error
// @Failure 404 {object} fault.Error
// @Failure 503 {object} fault.Error
// @Router /learn/subjects/{subject_id}/diagnostic-outline [get]
func (h *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.Preview(r.Context(), r.PathValue("subject_id"))
	if err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	outline := outlineDTO(value)
	names := []string{}
	for _, competency := range outline.Competencies {
		names = append(names, competency.Name)
		for _, topic := range competency.Constituents {
			names = append(names, topic.Name)
		}
	}
	if budget := h.service.GraphLabelsBudget(len(names)); budget > 0 {
		// A large map may require more model batches than the default server
		// write timeout allows. The labeler bounds each phase independently.
		err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(budget))
		if err != nil && !errors.Is(err, http.ErrNotSupported) {
			httpx.Error(r.Context(), w, fault.New(fault.Unavailable, "GRAPH_LABELS_UNAVAILABLE", "Не удалось подготовить короткие названия тем. Попробуйте ещё раз."))
			return
		}
	}
	labels, err := h.service.GraphLabels(r.Context(), names)
	if err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	for i := range outline.Competencies {
		competency := &outline.Competencies[i]
		competency.DisplayName = labels[competency.Name]
		for j := range competency.Constituents {
			topic := &competency.Constituents[j]
			topic.DisplayName = labels[topic.Name]
		}
	}
	httpx.JSON(w, http.StatusOK, outline)
}

func outlineDTO(value variant.Variant) DiagnosticOutline {
	result := DiagnosticOutline{Revision: value.MapRevision, AlgorithmVersion: value.AlgorithmVersion, Competencies: make([]OutlineCompetency, 0, len(value.Competencies))}
	competencyPositions := map[string]int{}
	for _, selection := range value.Competencies {
		index, found := competencyPositions[selection.Competency.ID]
		if !found {
			index = len(result.Competencies)
			competencyPositions[selection.Competency.ID] = index
			result.Competencies = append(result.Competencies, OutlineCompetency{ID: selection.Competency.ID, Name: selection.Competency.Name, Constituents: []OutlineConstituent{}})
		}
		competency := &result.Competencies[index]
		positions := map[string]int{}
		for i, topic := range competency.Constituents {
			positions[topic.ID] = i
		}
		for _, task := range selection.Tasks {
			topic := task.Task.Constituent
			position, found := positions[topic.ID]
			if !found {
				position = len(competency.Constituents)
				positions[topic.ID] = position
				competency.Constituents = append(competency.Constituents, OutlineConstituent{ID: topic.ID, Name: topic.Name, Outcomes: []OutlineOutcome{}})
			}
			competency.Constituents[position].Outcomes = append(competency.Constituents[position].Outcomes, OutlineOutcome{ID: task.Task.Outcome.ID, Name: task.Task.Outcome.Name, Role: task.Role})
		}
	}
	return result
}
