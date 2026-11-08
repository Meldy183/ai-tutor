package http

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	_ "github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/httpx"
)

// MaxMapBytes bounds an imported competency map book.
const MaxMapBytes = 25 << 20

type Handler struct{ service *application.Worker }

// RetryMap resumes preparation of the saved competency map after failure.
// @Summary Повторить подготовку сохранённой карты
// @Description Доступно только admin после окончательного сбоя подготовки карты. Ставит сохранённую карту в очередь без повторного импорта и без изменения заданий. Fingerprint проверяется атомарно. Материалы PDF не требуются; опубликованный граф сохраняется до успешной подготовки.
// @ID retryCourseGraphMap
// @Tags Материалы
// @Security accessCookie
// @Produce json
// @Param subject_id path string true "ID предмета"
// @Param X-Expected-Map-Fingerprint header string true "Fingerprint сохранённой карты из последнего snapshot"
// @Success 202 {object} coursegraph.Snapshot
// @Failure 401 {object} fault.Error
// @Failure 403 {object} fault.Error
// @Failure 404 {object} fault.Error
// @Failure 409 {object} fault.Error
// @Failure 503 {object} fault.Error
// @Router /exam/admin/subjects/{subject_id}/graph/competency-map/retry [post]
func (h *Handler) RetryMap(w http.ResponseWriter, r *http.Request) {
	actor, ok := httpx.Principal[user.User](r)
	if !ok {
		httpx.Error(r.Context(), w, fault.New(fault.Unauthorized, "UNAUTHORIZED", "Требуется сессия."))
		return
	}
	result, err := h.service.RetryMap(r.Context(), actor, r.PathValue("subject_id"), r.Header.Get("X-Expected-Map-Fingerprint"))
	if err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, result)
}

// SkipMissingCriteria excludes fallback-graded tasks after admin confirmation.
// @Summary Исключить задания карты без критериев
// @Description Доступно только admin. Исключает задания с общей рубрикой, пустые темы, составляющие, где осталось меньше трёх тем, и связанные с ними рёбра из сохранённой карты и ставит пересборку в очередь. Исходный XLSX не меняется. Повтор без новых исключений не запускает сборку. Активные диагностики отменяются при публикации изменённого графа. Если вопросов не останется, операция отклоняется без изменения карты.
// @Param X-Expected-Map-Fingerprint header string true "Fingerprint карты из подтверждённого snapshot; несовпадение или неопубликованная карта — 409"
// @ID skipCourseGraphMissingCriteria
// @Tags Материалы
// @Security accessCookie
// @Produce json
// @Param subject_id path string true "ID предмета"
// @Success 202 {object} coursegraph.Snapshot
// @Failure 401 {object} fault.Error
// @Failure 403 {object} fault.Error
// @Failure 404 {object} fault.Error
// @Failure 409 {object} fault.Error
// @Failure 422 {object} fault.Error
// @Failure 503 {object} fault.Error
// @Router /exam/admin/subjects/{subject_id}/graph/competency-map/skip-missing-criteria [post]
func (h *Handler) SkipMissingCriteria(w http.ResponseWriter, r *http.Request) {
	actor, ok := httpx.Principal[user.User](r)
	if !ok {
		httpx.Error(r.Context(), w, fault.New(fault.Unauthorized, "UNAUTHORIZED", "Требуется сессия."))
		return
	}
	result, err := h.service.SkipMissingCriteria(r.Context(), actor, r.PathValue("subject_id"), r.Header.Get("X-Expected-Map-Fingerprint"))
	if err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, result)
}

func New(service *application.Worker) *Handler { return &Handler{service} }

// Read returns the latest successful graph and current build status.
// @Summary Граф курса и состояние построения
// @Description Доступно только admin. Сборку запускает POST .../graph/build. stale — источник опубликованного Geek-графа изменился; inputs_ready — все материалы обработаны и сборку можно запустить. graph_source, graph_fingerprint и graph_map_revision описывают опубликованную версию Geek; status, build_source и error_message — последнюю подготовку. map и map.learning_revision описывают последнюю сохранённую карту. Для двухлистовой карты учебная версия активируется при сохранении карты; опубликованный Geek-граф остаётся доступен до успешной подготовки новой версии.
// @ID getCourseGraph
// @Tags Материалы
// @Security accessCookie
// @Produce json
// @Param subject_id path string true "ID предмета"
// @Success 200 {object} coursegraph.Snapshot
// @Failure 401 {object} fault.Error
// @Failure 403 {object} fault.Error
// @Failure 404 {object} fault.Error
// @Failure 503 {object} fault.Error
// @Router /exam/admin/subjects/{subject_id}/graph [get]
func (h *Handler) Read(w http.ResponseWriter, r *http.Request) {
	actor, ok := httpx.Principal[user.User](r)
	if !ok {
		httpx.Error(r.Context(), w, fault.New(fault.Unauthorized, "UNAUTHORIZED", "Требуется сессия."))
		return
	}
	result, err := h.service.Read(r.Context(), actor, r.PathValue("subject_id"))
	if err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

// Build starts a build of the course graph from the current materials.
// @Summary Собрать граф курса
// @Description Доступно только admin. Кнопка «Собрать граф»: ставит сборку из текущих материалов курса. Опубликованный граф остаётся у Geek до успешной сборки; успешная сборка с другим графом переводит активные диагностики курса в cancelled.
// @ID buildCourseGraph
// @Tags Материалы
// @Security accessCookie
// @Produce json
// @Param subject_id path string true "ID предмета"
// @Success 202 {object} coursegraph.Snapshot
// @Failure 401 {object} fault.Error
// @Failure 403 {object} fault.Error
// @Failure 404 {object} fault.Error
// @Failure 409 {object} fault.Error "GRAPH_BUILD_IN_PROGRESS или COMPETENCY_MAP_CHANGED"
// @Failure 422 {object} fault.Error "GRAPH_INPUTS_NOT_READY: материалы ещё обрабатываются, не обработаны или их нет"
// @Failure 503 {object} fault.Error
// @Router /exam/admin/subjects/{subject_id}/graph/build [post]
func (h *Handler) Build(w http.ResponseWriter, r *http.Request) {
	actor, ok := httpx.Principal[user.User](r)
	if !ok {
		httpx.Error(r.Context(), w, fault.New(fault.Unauthorized, "UNAUTHORIZED", "Требуется сессия."))
		return
	}
	result, err := h.service.Request(r.Context(), actor, r.PathValue("subject_id"))
	if err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, result)
}

// ImportMap builds the course graph from a competency map.
// @Summary Импортировать карту компетенций
// @Description Доступно только admin. Одна XLSX-книга: первый лист — Geek, второй — учебная диагностика; дополнительные листы не читаются. Содержание листов может различаться. Полные ОР и задания обоих источников сохраняются в общей схеме и одной ревизии. Обучение активируется в транзакции импорта, Geek остаётся на предыдущем графе до успешной подготовки нового. Оба режима выбирают основное и два базовых ОР на составляющую с минимум тремя подходящими ОР; невыбранные ОР остаются для обучения. Авторские ID и пререквизиты не меняют отбор. Все варианты задания сохраняют свой эталон и критерии.
// @ID importCourseGraphCompetencyMap
// @Tags Материалы
// @Security accessCookie
// @Accept multipart/form-data
// @Produce json
// @Param subject_id path string true "ID предмета"
// @Param file formData file true "Карта компетенций .xlsx, до 25 МиБ"
// @Param X-Expected-Learning-Revision header int64 true "Активная ревизия карты из GET /learn/subjects/{subject_id}/competency-map (0 — карты ещё нет): подтверждает замену"
// @Success 202 {object} coursegraph.Snapshot
// @Failure 400 {object} fault.Error
// @Failure 401 {object} fault.Error
// @Failure 403 {object} fault.Error
// @Failure 404 {object} fault.Error
// @Failure 409 {object} fault.Error "GRAPH_BUILD_IN_PROGRESS или COMPETENCY_MAP_CHANGED"
// @Failure 413 {object} fault.Error
// @Failure 422 {object} fault.Error "UNIFIED_COMPETENCY_MAP_REQUIRED, COMPETENCY_MAP_INVALID или COMPETENCY_MAP_EMPTY"
// @Failure 503 {object} fault.Error
// @Router /exam/admin/subjects/{subject_id}/graph/competency-map [post]
func (h *Handler) ImportMap(w http.ResponseWriter, r *http.Request) {
	actor, ok := httpx.Principal[user.User](r)
	if !ok {
		httpx.Error(r.Context(), w, fault.New(fault.Unauthorized, "UNAUTHORIZED", "Требуется сессия."))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxMapBytes+1<<20)
	if err := r.ParseMultipartForm(MaxMapBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(r.Context(), w, fault.New(fault.TooLarge, "PAYLOAD_TOO_LARGE", "Файл карты больше 25 МиБ."))
			return
		}
		httpx.Error(r.Context(), w, fault.New(fault.Invalid, "BAD_REQUEST", "Ожидается один файл карты .xlsx."))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	files := r.MultipartForm.File["file"]
	if len(files) != 1 {
		httpx.Error(r.Context(), w, fault.Validation("file", "Нужно выбрать ровно один файл карты .xlsx."))
		return
	}
	if files[0].Size > MaxMapBytes {
		httpx.Error(r.Context(), w, fault.New(fault.TooLarge, "PAYLOAD_TOO_LARGE", "Файл карты больше 25 МиБ."))
		return
	}
	f, err := files[0].Open()
	if err != nil {
		httpx.Error(r.Context(), w, fault.New(fault.Invalid, "BAD_REQUEST", "Не удалось прочитать файл карты."))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxMapBytes+1))
	if err != nil || len(data) > MaxMapBytes {
		httpx.Error(r.Context(), w, fault.New(fault.TooLarge, "PAYLOAD_TOO_LARGE", "Файл карты больше 25 МиБ."))
		return
	}
	expectedRevision := int64(-1)
	if raw := r.Header.Get("X-Expected-Learning-Revision"); raw != "" {
		expectedRevision, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || expectedRevision < 0 {
			httpx.Error(r.Context(), w, fault.Validation("X-Expected-Learning-Revision", "Укажите номер активной версии учебной карты."))
			return
		}
	}
	result, err := h.service.ImportMap(r.Context(), actor, r.PathValue("subject_id"), files[0].Filename, data, expectedRevision)
	if err != nil {
		httpx.Error(r.Context(), w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, result)
}
