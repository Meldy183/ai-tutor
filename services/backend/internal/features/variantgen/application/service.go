package application

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

const AlgorithmVersion = "constituent-map-v2"

var bloomRanks = map[string]int{"knowledge": 1, "understanding": 2, "application": 3, "analysis": 4}

type Service struct {
	repo    Repository
	labels  GraphLabeler
	chooser TaskChooser
	ids     IDGenerator
	now     func() time.Time
}

type taskPool struct {
	outcome    variant.OutcomeProfile
	tasks      []variant.TaskProfile
	order      int64
	rank       int
	importance int
}

func New(repo Repository, chooser TaskChooser, ids IDGenerator, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repo: repo, chooser: chooser, ids: ids, now: now}
}

func (s *Service) Create(ctx context.Context, ownerID, subjectID, key string) (variant.Variant, error) {
	if strings.TrimSpace(key) == "" || len(key) > 128 || strings.TrimSpace(key) != key || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return variant.Variant{}, fault.Validation("Idempotency-Key", "Укажите ключ длиной от 1 до 128 символов без пробелов по краям и управляющих символов.")
	}
	if strings.TrimSpace(subjectID) == "" {
		return variant.Variant{}, fault.Validation("subject_id", "Укажите предмет.")
	}
	return s.repo.Create(ctx, ownerID, subjectID, key, func(id, name string, revision int64, candidates []variant.CandidateOutcome) (variant.Variant, error) {
		return s.build(ownerID, id, name, revision, candidates)
	})
}

// Preview uses the same eligibility, ordering and outcome selection as Create,
// without saving a variant or starting a diagnostic session.
func (s *Service) Preview(ctx context.Context, subjectID string) (variant.Variant, error) {
	return s.repo.Preview(ctx, subjectID, func(id, name string, revision int64, candidates []variant.CandidateOutcome) (variant.Variant, error) {
		result, err := s.buildSelected("", id, name, revision, candidates, true)
		if f, ok := err.(*fault.Error); ok && f.Code == "NO_ELIGIBLE_COMPETENCIES" {
			return variant.Variant{SubjectID: id, SubjectNameSnapshot: name, MapRevision: revision, AlgorithmVersion: AlgorithmVersion, Competencies: []variant.CompetencySelection{}}, nil
		}
		return result, err
	})
}

func (s *Service) build(ownerID, subjectID, subjectName string, revision int64, candidates []variant.CandidateOutcome) (variant.Variant, error) {
	return s.buildSelected(ownerID, subjectID, subjectName, revision, candidates, false)
}

func (s *Service) buildSelected(ownerID, subjectID, subjectName string, revision int64, candidates []variant.CandidateOutcome, preview bool) (variant.Variant, error) {
	if len(candidates) == 0 {
		return variant.Variant{}, noEligible([]variant.SkippedCompetency{{Code: "EMPTY_MAP"}})
	}
	type compPool struct {
		profile     variant.CompetencyProfile
		order       int64
		constituent variant.ConstituentProfile
		outcomes    map[string]*taskPool
	}
	competencies := map[string]*compPool{}
	for _, candidate := range candidates {
		p := candidate.Profile
		if p.Competency.ID == "" {
			continue
		}
		key := p.Competency.ID + "\x00" + p.Constituent.ID
		comp := competencies[key]
		if comp == nil {
			comp = &compPool{profile: p.Competency, constituent: p.Constituent, order: candidate.SourceOrder, outcomes: map[string]*taskPool{}}
			competencies[key] = comp
		} else if candidate.SourceOrder < comp.order {
			comp.order = candidate.SourceOrder
		}
		if !candidate.HasTask || p.Outcome.ID == "" || p.Outcome.IncludeInTest == nil || !*p.Outcome.IncludeInTest || p.Outcome.TaxonomyCode == nil || p.Outcome.Importance == nil || *p.Outcome.Importance < 1 || *p.Outcome.Importance > 5 {
			continue
		}
		rank, ok := bloomRanks[*p.Outcome.TaxonomyCode]
		if !ok || !ready(p) {
			continue
		}
		outcome := comp.outcomes[p.Outcome.ID]
		if outcome == nil {
			outcome = &taskPool{outcome: p.Outcome, order: candidate.SourceOrder, rank: rank, importance: int(*p.Outcome.Importance), tasks: []variant.TaskProfile{}}
			comp.outcomes[p.Outcome.ID] = outcome
		} else if candidate.SourceOrder < outcome.order {
			outcome.order = candidate.SourceOrder
		}
		outcome.tasks = append(outcome.tasks, p)
	}
	ordered := make([]*compPool, 0, len(competencies))
	for _, comp := range competencies {
		ordered = append(ordered, comp)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].order != ordered[j].order {
			return ordered[i].order < ordered[j].order
		}
		if ordered[i].profile.Name != ordered[j].profile.Name {
			return ordered[i].profile.Name < ordered[j].profile.Name
		}
		if ordered[i].profile.ID != ordered[j].profile.ID {
			return ordered[i].profile.ID < ordered[j].profile.ID
		}
		if ordered[i].constituent.Name != ordered[j].constituent.Name {
			return ordered[i].constituent.Name < ordered[j].constituent.Name
		}
		return ordered[i].constituent.ID < ordered[j].constituent.ID
	})
	result := variant.Variant{OwnerID: ownerID, SubjectID: subjectID, SubjectNameSnapshot: subjectName, MapRevision: revision, AlgorithmVersion: AlgorithmVersion,
		SkippedCompetencies: []variant.SkippedCompetency{}, Competencies: []variant.CompetencySelection{}, CreatedAt: s.now().Unix()}
	pools := make([]coursegraph.Pool, len(ordered))
	byKey := map[string]*taskPool{}
	for i, comp := range ordered {
		pool := coursegraph.Pool{Key: comp.constituent.ID, Name: comp.profile.Name + " / " + comp.constituent.Name}
		for _, outcome := range comp.outcomes {
			byKey[outcome.outcome.ID] = outcome
			pool.Candidates = append(pool.Candidates, coursegraph.Candidate{Key: outcome.outcome.ID, Name: outcome.outcome.Name,
				Rank: outcome.rank, Importance: outcome.importance, Order: int(outcome.order)})
		}
		pools[i] = pool
	}
	trees, skips := coursegraph.SelectTrees(pools)
	for _, skip := range skips {
		comp := ordered[skip.Pool]
		code := "INSUFFICIENT_DISTINCT_OUTCOMES"
		if skip.Eligible == 0 {
			code = "NO_READY_OUTCOMES"
		}
		result.SkippedCompetencies = append(result.SkippedCompetencies, variant.SkippedCompetency{CompetencyID: comp.profile.ID, CompetencyName: skip.Name, Code: code, EligibleOutcomes: skip.Eligible})
	}
	for _, tree := range trees {
		comp := ordered[tree.Pool]
		picks := []*taskPool{byKey[tree.Root.Key]}
		for _, child := range tree.Children {
			picks = append(picks, byKey[child.Key])
		}
		selection := variant.CompetencySelection{Position: len(result.Competencies) + 1, Competency: comp.profile, Tasks: make([]variant.VariantTask, 0, len(picks))}
		for i, pick := range picks {
			idx := 0
			if !preview && len(pick.tasks) > 1 {
				var err error
				idx, err = s.chooser.Choose(len(pick.tasks))
				if err != nil {
					return variant.Variant{}, err
				}
				if idx < 0 || idx >= len(pick.tasks) {
					return variant.Variant{}, fmt.Errorf("task chooser returned invalid index %d", idx)
				}
			}
			taskID := pick.outcome.ID
			if !preview {
				var err error
				taskID, err = s.ids.New("variant-task")
				if err != nil {
					return variant.Variant{}, err
				}
			}
			role := "basic"
			if i == 0 {
				role = "main"
			}
			selection.Tasks = append(selection.Tasks, variant.VariantTask{ID: taskID, Role: role, Task: pick.tasks[idx]})
		}
		result.Competencies = append(result.Competencies, selection)
		result.TaskCount += len(selection.Tasks)
	}
	result.IncludedCompetencyCount = len(result.Competencies)
	if result.IncludedCompetencyCount == 0 {
		return variant.Variant{}, noEligible(result.SkippedCompetencies)
	}
	if preview {
		return result, nil
	}
	variantID, err := s.ids.New("variant")
	if err != nil {
		return variant.Variant{}, err
	}
	result.ID = variantID
	return result, nil
}

func ready(p variant.TaskProfile) bool {
	return p.VoiceInstruction != nil && p.ReferenceAnswer != nil && variant.ValidTaskContent(p.Question, p.Options, *p.VoiceInstruction, *p.ReferenceAnswer)
}

func noEligible(skipped []variant.SkippedCompetency) *fault.Error {
	e := fault.New(fault.Conflict, "NO_ELIGIBLE_COMPETENCIES", "Не удалось собрать вариант: нет составляющих, подходящих по карте.")
	for _, item := range skipped {
		if item.Code == "EMPTY_MAP" {
			e.Details = append(e.Details, fault.Detail{Code: "EMPTY_MAP", Message: "Активная карта компетенций пуста."})
			continue
		}
		message := fmt.Sprintf("Составляющая «%s»: подходящих ОР — %d", item.CompetencyName, item.EligibleOutcomes)
		e.Details = append(e.Details, fault.Detail{Path: "competencies/" + item.CompetencyID, Code: item.Code, Message: message})
	}
	return e
}

func EncodeCursor(cursor Cursor) string {
	raw := strconv.FormatInt(cursor.CreatedAt, 10) + ":" + cursor.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
func DecodeCursor(raw string) (*Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fault.Validation("cursor", "Некорректный курсор.")
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, fault.Validation("cursor", "Некорректный курсор.")
	}
	createdAt, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || createdAt < 0 {
		return nil, fault.Validation("cursor", "Некорректный курсор.")
	}
	return &Cursor{CreatedAt: createdAt, ID: parts[1]}, nil
}

func (s *Service) Get(ctx context.Context, ownerID, id string) (variant.Variant, error) {
	return s.repo.Get(ctx, ownerID, id)
}
func (s *Service) Task(ctx context.Context, ownerID, variantID, taskID string) (variant.VariantTask, error) {
	return s.repo.Task(ctx, ownerID, variantID, taskID)
}
func (s *Service) List(ctx context.Context, ownerID, subjectID string, limit int, cursor string) ([]variant.Variant, string, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		return nil, "", fault.Validation("limit", "Лимит должен быть от 1 до 100.")
	}
	decoded, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(subjectID) == "" {
		return nil, "", fault.Validation("subject_id", "Укажите предмет.")
	}
	items, next, err := s.repo.List(ctx, ownerID, subjectID, limit, decoded)
	if err != nil {
		return nil, "", err
	}
	if next == nil {
		return items, "", nil
	}
	return items, EncodeCursor(*next), nil
}
