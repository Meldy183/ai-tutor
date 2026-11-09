package application

import (
	"context"
	"errors"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/resultsummary"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

// SetSummaries connects the model and the storage of the overall conclusion.
func (s *Service) SetSummaries(summarizer Summarizer, store SummaryStore) {
	s.summarizer, s.summaries = summarizer, store
}

// ResultSummary is the model's overall conclusion of a completed exam, written
// once when the result is first opened. It runs after the dialogue and does not
// change the session; a model failure can be retried.
func (s *Service) ResultSummary(ctx context.Context, ownerID, id string) (string, error) {
	result, err := s.Result(ctx, ownerID, id)
	if err != nil {
		return "", err
	}
	if s.summarizer == nil || s.summaries == nil {
		return "", summaryUnavailable()
	}
	stored, ok, err := s.summaries.ResultSummary(ctx, ownerID, id)
	if err != nil {
		return "", err
	}
	if ok {
		return stored, nil
	}
	report := summaryReport(result)
	if report.Empty() {
		return "", nil
	}
	summary, err := s.summarizer.Summarize(ctx, report)
	if err != nil {
		var f *fault.Error
		if errors.As(err, &f) {
			return "", err
		}
		return "", errors.Join(summaryUnavailable(), err)
	}
	if err := s.summaries.SaveResultSummary(ctx, ownerID, id, summary); err != nil {
		return "", err
	}
	// A concurrent request may have saved its text first: show that one.
	if stored, ok, err := s.summaries.ResultSummary(ctx, ownerID, id); err == nil && ok {
		return stored, nil
	}
	return summary, nil
}

func summaryUnavailable() error {
	return fault.New(fault.Unavailable, "MODEL_UNAVAILABLE", "Модель временно недоступна; повторите запрос.")
}

func summaryReport(result exam.Result) resultsummary.Report {
	report := resultsummary.Report{Strengths: []resultsummary.Finding{}, Partial: []resultsummary.Finding{}, Gaps: []resultsummary.Finding{}, Unverified: []string{}}
	for _, n := range result.Nodes {
		finding := resultsummary.Finding{Topic: n.Title, Note: n.Reply}
		switch {
		case n.Status == exam.StatusInsufficient:
			report.Unverified = append(report.Unverified, n.Title)
		case n.Score == nil:
		case n.Status == exam.StatusGap:
			report.Gaps = append(report.Gaps, finding)
		case *n.Score == n.Maximum:
			report.Strengths = append(report.Strengths, finding)
		default:
			report.Partial = append(report.Partial, finding)
		}
	}
	return report
}
