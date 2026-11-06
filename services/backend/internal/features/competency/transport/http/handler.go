package competencyhttp

import (
	"net/http"
	"strconv"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/httpx"
)

type ImportResult struct {
	Revision              int64           `json:"revision"`
	ImportedAt            int64           `json:"imported_at"`
	CompetencyCount       int             `json:"competency_count"`
	ConstituentCount      int             `json:"constituent_count"`
	OutcomeCount          int             `json:"outcome_count"`
	TaskCount             int             `json:"task_count"`
	UnparsedTaskCellCount int             `json:"unparsed_task_cell_count"`
	Warnings              []ImportWarning `json:"warnings"`
}

type ImportWarning struct {
	Row         int    `json:"row"`
	ColumnIndex int    `json:"column_index"`
	Column      string `json:"column"`
	Code        string `json:"code"`
}

type Handler struct {
	service        *application.Service
	maxUploadBytes int64
}

func New(service *application.Service, maxUploadBytes int64) *Handler {
	return &Handler{service: service, maxUploadBytes: maxUploadBytes}
}

// ImportSubject handles POST /admin/subjects/{subject_id}/competency-map/import.
// @Summary Заменить карту предмета
// @Description Доступно только admin. Совместимый импорт CSV и однолистовой XLSX атомарно заменяет учебную карту. Двухлистовую XLSX-карту нужно загрузить через единый импорт графа курса; независимая публикация её учебного листа отклоняется.
// @ID importSubjectCompetencyMap
// @Tags Учебная база
// @Security accessCookie
// @Accept mpfd
// @Produce json
// @Param subject_id path string true "ID предмета"
// @Param X-Expected-Map-Revision header integer false "Ожидаемая ревизия карты"
// @Param file formData file true "Карта компетенций CSV или XLSX, до 25 МиБ"
// @Success 200 {object} ImportResult
// @Failure 401 {object} fault.Error
// @Failure 403 {object} fault.Error
// @Failure 404 {object} fault.Error
// @Failure 409 {object} fault.Error "COMPETENCY_MAP_CHANGED или UNIFIED_COMPETENCY_MAP_REQUIRED"
// @Failure 413 {object} fault.Error
// @Failure 415 {object} fault.Error
// @Failure 422 {object} fault.Error
// @Failure 503 {object} fault.Error
// @Router /learn/admin/subjects/{subject_id}/competency-map/import [post]
func (h *Handler) ImportSubject(w http.ResponseWriter, r *http.Request) {
	h.importForSubject(w, r, r.PathValue("subject_id"))
}

func (h *Handler) importForSubject(w http.ResponseWriter, r *http.Request, subjectID string) {
	actor, ok := httpx.Principal[user.User](r)
	if !ok {
		httpx.Error(r.Context(), w, fault.New(fault.Unauthorized, "UNAUTHORIZED", "Требуется действующая сессия."))
		return
	}
	if err := h.service.Authorize(actor); err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	var expectedRevision *int64
	if raw := r.Header.Get("X-Expected-Map-Revision"); raw != "" {
		revision, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 0 {
			httpx.Error(r.Context(), w, fault.Validation("X-Expected-Map-Revision", "Ожидается неотрицательная ревизия."))
			return
		}
		expectedRevision = &revision
	}
	data, media, err := httpx.Upload(r, "file", h.maxUploadBytes)
	if err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	result, err := h.service.ImportForSubject(r.Context(), actor, subjectID, data, media, expectedRevision)
	if err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ImportResult{
		Revision: result.Revision, ImportedAt: result.ImportedAt,
		CompetencyCount: result.CompetencyCount, ConstituentCount: result.ConstituentCount,
		OutcomeCount: result.OutcomeCount, TaskCount: result.TaskCount,
		UnparsedTaskCellCount: result.UnparsedTaskCells,
		Warnings:              warnings(result.Warnings),
	})
}

func warnings(source []competencymap.ImportWarning) []ImportWarning {
	items := make([]ImportWarning, len(source))
	for i, warning := range source {
		items[i] = ImportWarning{Row: warning.Row, ColumnIndex: warning.ColumnIndex, Column: warning.Column, Code: warning.Code}
	}
	return items
}
