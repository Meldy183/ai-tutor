package competencyhttp

import (
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/application"
)

// Handler reads the active competency map. A map is imported only together
// with its Geek sheet through the course graph (decision of 08.11.2026).
type Handler struct {
	service *application.Service
}

func New(service *application.Service) *Handler {
	return &Handler{service: service}
}
