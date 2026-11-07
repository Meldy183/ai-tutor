// Package application implements competency-map import and read policy.
package application

import (
	"context"
	"strings"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type Repository interface {
	Replace(ctx context.Context, subjectID, actorID string, parsed competencymap.Map, importedAt int64, expectedRevision *int64) (competencymap.ImportResult, error)
	Read(ctx context.Context, subjectID string) (competencymap.Snapshot, error)
}

type Parser interface {
	Parse(data []byte) (competencymap.Map, error)
}

type XLSXSheetCounter interface {
	XLSXSheetCount(data []byte) (int, error)
}

type Service struct {
	repo   Repository
	parser Parser
	now    func() time.Time
}

func New(repo Repository, parser Parser, now func() time.Time) *Service {
	return &Service{repo: repo, parser: parser, now: now}
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

func (s *Service) ImportForSubject(ctx context.Context, actor user.User, subjectID string, data []byte, media string, expectedRevision *int64) (competencymap.ImportResult, error) {
	if err := s.Authorize(actor); err != nil {
		return competencymap.ImportResult{}, err
	}
	media = strings.TrimSpace(strings.Split(media, ";")[0])
	if media != "text/csv" && media != "application/csv" && media != "application/vnd.ms-excel" &&
		media != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		return competencymap.ImportResult{}, fault.New(fault.Unsupported, "UNSUPPORTED_MEDIA_TYPE", "Ожидается файл CSV или XLSX.")
	}
	if media == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		if counter, ok := s.parser.(XLSXSheetCounter); ok {
			count, countErr := counter.XLSXSheetCount(data)
			if countErr != nil {
				return competencymap.ImportResult{}, countErr
			}
			if count > 1 {
				return competencymap.ImportResult{}, fault.New(fault.Conflict, "UNIFIED_COMPETENCY_MAP_REQUIRED", "Двухлистовую карту загрузите один раз через импорт графа курса: так обе части карты будут опубликованы вместе.")
			}
		}
	}
	parsed, err := s.parser.Parse(data)
	if err != nil {
		return competencymap.ImportResult{}, err
	}
	return s.repo.Replace(ctx, subjectID, actor.ID, parsed, s.now().Unix(), expectedRevision)
}
