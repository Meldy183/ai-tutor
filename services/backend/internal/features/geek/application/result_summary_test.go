package application

import (
	"context"
	"errors"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/resultsummary"
)

type fakeSummarizer struct {
	calls  int
	err    error
	report resultsummary.Report
}

func (f *fakeSummarizer) Summarize(_ context.Context, report resultsummary.Report) (string, error) {
	f.calls++
	f.report = report
	return "Вывод", f.err
}

type fakeSummaries struct{ texts map[string]string }

func (f *fakeSummaries) ResultSummary(_ context.Context, _, id string) (string, bool, error) {
	text, ok := f.texts[id]
	return text, ok, nil
}

func (f *fakeSummaries) SaveResultSummary(_ context.Context, _, id, summary string) error {
	if _, ok := f.texts[id]; !ok {
		f.texts[id] = summary
	}
	return nil
}

func TestSummaryReportSortsTopicsByTheirPoints(t *testing.T) {
	score := func(v int) *int { return &v }
	report := summaryReport(exam.Result{Nodes: []exam.NodeResult{
		{Title: "Полный", Status: exam.StatusIndependent, Score: score(2), Maximum: 2, Reply: "верно"},
		{Title: "С помощью", Status: exam.StatusAssisted, Score: score(1), Maximum: 2},
		{Title: "Пробел", Status: exam.StatusGap, Score: score(0), Maximum: 1},
		{Title: "Неясно", Status: exam.StatusInsufficient},
		{Title: "Засчитано", Status: exam.StatusCredited},
		{Title: "Не спрошено", Status: exam.StatusUnchecked},
	}})
	if len(report.Strengths) != 1 || report.Strengths[0].Topic != "Полный" || report.Strengths[0].Note != "верно" {
		t.Fatalf("strengths = %+v", report.Strengths)
	}
	if len(report.Partial) != 1 || report.Partial[0].Topic != "С помощью" {
		t.Fatalf("partial = %+v", report.Partial)
	}
	if len(report.Gaps) != 1 || report.Gaps[0].Topic != "Пробел" {
		t.Fatalf("gaps = %+v", report.Gaps)
	}
	if len(report.Unverified) != 1 || report.Unverified[0] != "Неясно" {
		t.Fatalf("unverified = %+v", report.Unverified)
	}
}

func TestResultSummaryIsWrittenOnceAfterTheExam(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, err := f.service.Start(ctx, "student", "subject", "start")
	if err != nil {
		t.Fatal(err)
	}
	summarizer, summaries := &fakeSummarizer{}, &fakeSummaries{texts: map[string]string{}}
	f.service.SetSummaries(summarizer, summaries)
	if _, err := f.service.ResultSummary(ctx, "student", s.ID); code(err) != "SESSION_NOT_COMPLETED" {
		t.Fatalf("active session: %v", err)
	}
	for i := 0; s.Status == exam.SessionActive && i < 60; i++ {
		s = f.answer(t, s, Answer{DontKnow: true})
	}
	if s.Status != exam.SessionCompleted {
		t.Fatalf("status = %s", s.Status)
	}
	summarizer.err = errors.New("model down")
	if _, err := f.service.ResultSummary(ctx, "student", s.ID); code(err) != "MODEL_UNAVAILABLE" {
		t.Fatalf("model failure: %v", err)
	}
	if len(summaries.texts) != 0 {
		t.Fatal("a failed call must save nothing")
	}
	summarizer.err = nil
	for range 2 {
		text, err := f.service.ResultSummary(ctx, "student", s.ID)
		if err != nil || text != "Вывод" {
			t.Fatalf("summary = %q, %v", text, err)
		}
	}
	if summarizer.calls != 2 || len(summarizer.report.Gaps) == 0 {
		t.Fatalf("calls = %d, report = %+v", summarizer.calls, summarizer.report)
	}
	if _, err := f.service.ResultSummary(ctx, "other", s.ID); code(err) == "" {
		t.Fatal("another student must not read the summary")
	}
}
