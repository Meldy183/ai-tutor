// Package application implements competency-map import and read policy.
package application

import (
	"context"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type Repository interface {
	Read(ctx context.Context, subjectID string) (competencymap.Snapshot, error)
}

// Service reads the competency map of a course and authorizes its import,
// which the course graph runs for both sheets at once.
type Service struct {
	repo Repository
}

func New(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) ReadSubject(ctx context.Context, subjectID string) (competencymap.Snapshot, error) {
	return s.repo.Read(ctx, subjectID)
}

// Authorize allows transports to check import access before reading uploads.
func (s *Service) Authorize(actor user.User) error {
	if actor.Role != user.Admin {
		return fault.New(fault.Forbidden, "FORBIDDEN", "Операция доступна только admin.")
	}
	return nil
}
