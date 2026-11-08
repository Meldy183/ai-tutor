package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"go.uber.org/zap"

	geekapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/application"
	geekgraph "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/infrastructure/graph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/logx"
)

// builtQuestionRounds bounds the rounds of preparing a build: a failed node is
// retried in the next round after a pause that starts at builtQuestionPause
// and doubles, about five minutes in all, before the build fails. A failed
// build is built again by course-graph from the start, which costs far more
// than waiting for the gateway.
const (
	builtQuestionRounds = 6
	builtQuestionPause  = 10 * time.Second
)

// builtQuestions prepares the main question and its rephrase of every topic
// of a course-graph build through the Geek examiner and writes them into the
// topics, keeping every other field of the build.
type builtQuestions struct {
	examiner geekapp.Examiner
	logger   *zap.Logger
	// pause overrides builtQuestionPause in tests.
	pause time.Duration
}

func (b builtQuestions) Prepare(ctx context.Context, data json.RawMessage) (json.RawMessage, error) {
	return b.PrepareWithProgress(ctx, data, nil)
}

func (b builtQuestions) PrepareWithProgress(ctx context.Context, data json.RawMessage, report func(coursegraph.BuildProgress)) (json.RawMessage, error) {
	// The model calls are logged under the worker, as Geek's are, within the job's trace.
	logger := b.logger
	if traceID := logx.TraceID(ctx); traceID != "" {
		logger = logger.With(zap.String("trace_id", traceID))
	}
	ctx = logx.With(ctx, logger)
	// The graph is read as Geek reads it, so the prompts get the same topics.
	g, ok, err := geekgraph.Decode(data)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("graph has no topics")
	}
	pause := b.pause
	if pause == 0 {
		pause = builtQuestionPause
	}
	started := time.Now()
	initial := len(g.Nodes) - len(geekapp.Unprepared(g))
	for round := 1; ; round++ {
		var failures error
		g, _, failures = geekapp.PrepareQuestionsWithProgress(ctx, b.examiner, g, 4, func(done, total int) {
			if report != nil {
				p := coursegraph.BuildProgress{Stage: "prepare_questions", Done: done, Total: total}
				if done-initial >= 2 && done < total && round == 1 {
					seconds := max(1, int(time.Since(started).Seconds()*float64(total-done)/float64(done-initial)))
					p.RemainingSeconds = &seconds
				}
				report(p)
				logger.Info("build question preparation progress", zap.Int("done", done), zap.Int("total", total), zap.Int("round", round))
			}
		})
		left := geekapp.Unprepared(g)
		if len(left) == 0 {
			break
		}
		if round == builtQuestionRounds || ctx.Err() != nil {
			return nil, fmt.Errorf("%d topics without prepared questions (%s): %w", len(left), strings.Join(left, ", "), failures)
		}
		logger.Warn("build questions are not all prepared; retrying", zap.Error(failures), zap.Strings("nodes", left), zap.Int("round", round))
		if report != nil {
			report(coursegraph.BuildProgress{Stage: "retry_questions", Done: len(g.Nodes) - len(left), Total: len(g.Nodes)})
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pause << (round - 1)):
		}
	}

	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil {
		return nil, err
	}
	var topics []map[string]json.RawMessage
	if err := json.Unmarshal(shape["topics"], &topics); err != nil {
		return nil, fmt.Errorf("decode topics: %w", err)
	}
	if len(topics) != len(g.Nodes) {
		return nil, fmt.Errorf("graph has %d topics and %d nodes", len(topics), len(g.Nodes))
	}
	for i, topic := range topics {
		var id string
		if err := json.Unmarshal(topic["id"], &id); err != nil || id != g.Nodes[i].ID {
			return nil, fmt.Errorf("topic %d is not node %s", i, g.Nodes[i].ID)
		}
		for field, value := range map[string]string{"question": g.Nodes[i].MainQuestion(), "rephrase": g.Nodes[i].PreparedRephrase()} {
			if topic[field], err = json.Marshal(value); err != nil {
				return nil, err
			}
		}
		if len(g.Nodes[i].Variants) > 0 {
			if topic["variants"], err = json.Marshal(g.Nodes[i].Variants); err != nil {
				return nil, err
			}
		}
	}
	if shape["topics"], err = json.Marshal(topics); err != nil {
		return nil, err
	}
	out, err := json.Marshal(shape)
	if err != nil {
		return nil, err
	}
	logger.Info("build questions prepared", zap.Int("topics", len(topics)), zap.Duration("duration", time.Since(started)))
	return out, nil
}
