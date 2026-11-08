package application

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

const (
	closingText = "Спасибо! Диагностика завершена, итог уже готов."
	// speechLimit is the TTS limit of one synthesis request.
	speechLimit = 500
	maxKeyLen   = 200
	maxTextLen  = 4000
)

type Config struct {
	// MaxQuestions stops a diagnostic after that many questions; 0 means no limit.
	MaxQuestions int
}

// AudioPart selects what of a Geek turn is voiced.
type AudioPart string

const (
	AudioWhole      AudioPart = ""
	AudioReply      AudioPart = "reply"
	AudioQuestion   AudioPart = "question"
	AudioIntro      AudioPart = "intro"
	AudioTransition AudioPart = "transition"
)

type Service struct {
	graphs   GraphSource
	raw      RawMaterials
	store    Store
	texts    Transcriptions
	examiner Examiner
	// summarizer and summaries write and keep the result's overall conclusion.
	summarizer Summarizer
	summaries  SummaryStore
	ids        IDGenerator
	now        func() time.Time
	cfg        Config
	speech     *SpeechCache
	// rng shuffles top-level topics of equal coverage for each new diagnostic.
	rng func() *rand.Rand
}

func (s *Service) SetRawMaterials(raw RawMaterials) { s.raw = raw }

func (s *Service) RawMaterial(ctx context.Context, ownerID, sessionID, materialID string) (RawMaterial, error) {
	session, err := s.store.Session(ctx, ownerID, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return RawMaterial{}, fault.New(fault.NotFound, "NOT_FOUND", "Диагностика не найдена.")
		}
		return RawMaterial{}, err
	}
	if session.Exam == nil {
		return RawMaterial{}, fault.New(fault.NotFound, "NOT_FOUND", "Диагностика не найдена.")
	}
	var expected *string
	linked := false
	for _, node := range session.Exam.Graph.Nodes {
		for _, source := range node.Sources {
			if source.MaterialID == materialID {
				linked = true
				expected = source.MaterialRevision
				break
			}
		}
		if linked {
			break
		}
	}
	if !linked {
		return RawMaterial{}, fault.New(fault.NotFound, "NOT_FOUND", "Материал не найден.")
	}
	if expected == nil || *expected == "" {
		return RawMaterial{}, fault.New(fault.Gone, "SOURCE_UNAVAILABLE", "Исходный материал больше недоступен.")
	}
	if s.raw == nil {
		return RawMaterial{}, fault.New(fault.Gone, "SOURCE_UNAVAILABLE", "Исходный материал больше недоступен.")
	}
	current, err := s.raw.CurrentRaw(ctx, materialID)
	if err != nil {
		return RawMaterial{}, err
	}
	if !current.Found || current.Revision == "" || current.Revision != *expected {
		return RawMaterial{}, fault.New(fault.Gone, "SOURCE_UNAVAILABLE", "Исходный материал больше недоступен.")
	}
	return current, nil
}

// New builds the Geek service. speech voices turns; the app shares one
// instance with the warmer, so a question missed by both is synthesized once.
func New(graphs GraphSource, store Store, texts Transcriptions, examiner Examiner, speech *SpeechCache, ids IDGenerator, now func() time.Time, cfg Config) *Service {
	if now == nil {
		now = time.Now
	}
	rng := func() *rand.Rand { return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) }
	return &Service{graphs: graphs, store: store, texts: texts, examiner: examiner, ids: ids, now: now, cfg: cfg, speech: speech, rng: rng}
}

// Answer is the student's reply: exactly one of a confirmed transcription, a
// text or the "don't know" button.
type Answer struct {
	TurnID          string
	TranscriptionID string
	Text            string
	DontKnow        bool
}

var errSessionNotFound = fault.New(fault.NotFound, "SESSION_NOT_FOUND", "Диагностика не найдена.")

func validKey(key string) error {
	if strings.TrimSpace(key) == "" || len(key) > maxKeyLen {
		return fault.Validation("Idempotency-Key", "Нужен ключ повтора запроса до 200 символов.")
	}
	return nil
}

// Start returns the student's active diagnostic on the course or starts a new
// one with its first question; created reports a new session.
func (s *Service) Start(ctx context.Context, ownerID, subjectID, key string) (exam.Session, bool, error) {
	if err := validKey(key); err != nil {
		return exam.Session{}, false, err
	}
	if strings.TrimSpace(subjectID) == "" {
		return exam.Session{}, false, fault.Validation("subject_id", "Укажите курс.")
	}
	fingerprint := "start:" + subjectID
	if r, ok, err := s.replay(ctx, ownerID, key, fingerprint); err != nil || ok {
		return r.Response, r.Created, err
	}
	if exists, err := s.store.SubjectExists(ctx, subjectID); err != nil {
		return exam.Session{}, false, err
	} else if !exists {
		return exam.Session{}, false, fault.New(fault.NotFound, "SUBJECT_NOT_FOUND", "Курс не найден.")
	}
	if active, ok, err := s.store.ActiveSession(ctx, ownerID, subjectID); err != nil {
		return exam.Session{}, false, err
	} else if ok {
		if active, err = s.refresh(ctx, active); err != nil {
			return exam.Session{}, false, err
		}
		if active.Status == exam.SessionActive {
			return s.resume(ctx, ownerID, key, fingerprint, active)
		}
	}
	graph, ok, err := s.graphs.Current(ctx, subjectID)
	if err != nil {
		return exam.Session{}, false, err
	}
	if !ok {
		return exam.Session{}, false, fault.New(fault.Conflict, "GRAPH_NOT_READY", "Граф курса ещё не собран или готовится.")
	}
	id, err := s.ids.New("diagnostic")
	if err != nil {
		return exam.Session{}, false, err
	}
	limit := s.cfg.MaxQuestions
	if graph.Source == coursegraph.SourceMap {
		// A competency map is examined in full, as the variant of v0.0.2 was
		// (decision of 19.10.2026).
		limit = 0
	}
	ex, step := exam.NewExam(graph, limit, s.rng())
	session := exam.Session{ID: id, OwnerID: ownerID, SubjectID: subjectID, Status: exam.SessionActive, StartedAt: s.now().Unix(), Version: 1, Exam: ex, Turns: []exam.Turn{}}
	if err := s.say(ctx, &session, step, "", false); err != nil {
		return exam.Session{}, false, err
	}
	request := Request{OwnerID: ownerID, Key: key, Fingerprint: fingerprint, SessionID: id, Created: true, Response: session, CreatedAt: s.now().Unix()}
	switch err := s.store.Create(ctx, session, request); {
	case errors.Is(err, ErrActiveExists):
		// A concurrent start won; resume its session.
		active, ok, err := s.store.ActiveSession(ctx, ownerID, subjectID)
		if err != nil || !ok {
			return exam.Session{}, false, errors.Join(ErrConflict, err)
		}
		return s.resume(ctx, ownerID, key, fingerprint, active)
	case err != nil:
		return exam.Session{}, false, err
	}
	return session, true, nil
}

// resume records the start request that returned an existing session, so its
// key keeps returning that result, and returns what is stored under the key.
func (s *Service) resume(ctx context.Context, ownerID, key, fingerprint string, active exam.Session) (exam.Session, bool, error) {
	request := Request{OwnerID: ownerID, Key: key, Fingerprint: fingerprint, SessionID: active.ID, Response: active, CreatedAt: s.now().Unix()}
	if err := s.store.SaveRequest(ctx, request); err != nil {
		return exam.Session{}, false, err
	}
	r, _, err := s.replay(ctx, ownerID, key, fingerprint)
	return r.Response, r.Created, err
}

// Get reads a diagnostic of its owner.
func (s *Service) Get(ctx context.Context, ownerID, id string) (exam.Session, error) {
	session, err := s.load(ctx, ownerID, id)
	if err != nil {
		return exam.Session{}, err
	}
	return s.refresh(ctx, session)
}

// History lists the student's diagnostics of a course, newest first; an active
// one over a rebuilt graph is cancelled on the way.
func (s *Service) History(ctx context.Context, ownerID, subjectID string) ([]exam.Session, error) {
	if strings.TrimSpace(subjectID) == "" {
		return nil, fault.Validation("subject_id", "Укажите курс.")
	}
	sessions, err := s.store.Sessions(ctx, ownerID, subjectID)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		if sessions[i], err = s.refresh(ctx, sessions[i]); err != nil {
			return nil, err
		}
	}
	return sessions, nil
}

// Answer accepts the reply to the current Geek turn and returns the session
// with the next turn. The model is called before anything is saved, so a
// failure leaves the session unchanged and the request can be retried.
func (s *Service) Answer(ctx context.Context, ownerID, id, key string, a Answer) (exam.Session, error) {
	if err := validKey(key); err != nil {
		return exam.Session{}, err
	}
	fingerprint, err := answerFingerprint(id, a)
	if err != nil {
		return exam.Session{}, err
	}
	if r, ok, err := s.replay(ctx, ownerID, key, fingerprint); err != nil || ok {
		return r.Response, err
	}
	session, err := s.load(ctx, ownerID, id)
	if err != nil {
		return exam.Session{}, err
	}
	if session, err = s.refresh(ctx, session); err != nil {
		return exam.Session{}, err
	}
	current, ok := session.CurrentTurn()
	if !ok {
		return exam.Session{}, fault.New(fault.Conflict, "SESSION_NOT_ACTIVE", "Диагностика уже завершена.")
	}
	if current.ID != a.TurnID {
		return exam.Session{}, fault.New(fault.Conflict, "TURN_NOT_CURRENT", "Ответ относится не к текущему вопросу.")
	}
	step := *session.Exam.Current
	node, _ := session.Exam.Graph.Node(step.NodeID)
	text, source, err := s.answerText(ctx, ownerID, a)
	if err != nil {
		return exam.Session{}, err
	}
	previous := session.Version
	if err := s.append(&session, exam.SpeakerStudent, exam.KindAnswer, &step.NodeID, &source, text); err != nil {
		return exam.Session{}, err
	}
	evaluate := EvaluationRequest{PreferredName: session.PreferredName, ClarificationPending: session.ClarificationPending, AddressPreference: exam.AddressNeutral, HintUsed: session.Exam.Nodes[node.ID].HintUsed, Kind: step.Kind, Node: node, MaxScore: session.Exam.Graph.MaxScore(node.ID), Prerequisites: unaskedPrerequisites(session.Exam, node.ID), Dialogue: session.Dialogue(node.ID)}
	assessment := Assessment{Evaluation: exam.Evaluation{Verdict: exam.VerdictDontKnow}}
	if source != exam.SourceDontKnow {
		if assessment, err = s.examiner.Evaluate(ctx, evaluate); err != nil {
			return exam.Session{}, modelError(err)
		}
		if err := checkEvaluation(assessment.Evaluation, session); err != nil {
			return exam.Session{}, modelError(err)
		}
	}
	session.ClarificationPending = assessment.ClarificationPending
	if !assessment.AddressPreference.Valid() || (assessment.Verdict == exam.VerdictAddressCorrection && assessment.AddressPreference == "") {
		return exam.Session{}, modelError(errors.New("invalid address preference"))
	}
	session.AddressPreference = exam.AddressNeutral
	evaluate.AddressPreference = exam.AddressNeutral
	if assessment.Verdict == exam.VerdictInsufficient || assessment.Verdict == exam.VerdictPause || assessment.Verdict == exam.VerdictAddressCorrection {
		assessment.Verdict = exam.VerdictClarifyQuestion
		assessment.Clarification = RepeatUnclearSpeech
	}
	if assessment.Verdict.Conversational() {
		// Keep the actual exam step, scores, hint allowance and question count.
		session.Turns[len(session.Turns)-1].Meta = true
		message := assessment.Clarification
		switch assessment.Verdict {
		case exam.VerdictAddressCorrection:
			message = addressAcknowledgement(session.AddressPreference)
		case exam.VerdictPause:
			message = pauseAcknowledgement
		}
		if strings.TrimSpace(message) == "" || utf8.RuneCountInString(message) > 600 {
			return exam.Session{}, modelError(errors.New("invalid conversational reply"))
		}
		if err := s.append(&session, exam.SpeakerGeek, step.Kind, &step.NodeID, nil, message); err != nil {
			return exam.Session{}, err
		}
		session.Turns[len(session.Turns)-1].Meta = true
	} else {
		next := session.Exam.Apply(assessment.Evaluation)
		closed := next.NodeID != step.NodeID
		feedback := FeedbackRequest{EvaluationRequest: evaluate, Verdict: assessment.Verdict,
			Closed: closed, Next: next.Kind, Result: *session.Exam.Nodes[node.ID]}
		// Apply may have credited prerequisites or closed the topic. Only protect
		// prerequisites that can still be asked after that decision.
		feedback.Prerequisites = unaskedPrerequisites(session.Exam, node.ID)
		reply, err := s.examiner.Feedback(ctx, feedback)
		if err != nil {
			return exam.Session{}, modelError(err)
		}
		if err := s.say(ctx, &session, next, reply, closed); err != nil {
			return exam.Session{}, err
		}
	}
	session.Version++
	request := Request{OwnerID: ownerID, Key: key, Fingerprint: fingerprint, SessionID: id, Response: session, CreatedAt: s.now().Unix()}
	if err := s.store.Update(ctx, session, previous, &request); errors.Is(err, ErrConflict) {
		// A retry of the same request may have been accepted meanwhile.
		if r, ok, err := s.replay(ctx, ownerID, key, fingerprint); err != nil || ok {
			return r.Response, err
		}
		return exam.Session{}, fault.New(fault.Conflict, "TURN_NOT_CURRENT", "Диагностика изменилась; обновите её.")
	} else if err != nil {
		return exam.Session{}, err
	}
	return session, nil
}

// Result is the outcome of a completed diagnostic.
func (s *Service) Result(ctx context.Context, ownerID, id string) (exam.Result, error) {
	session, err := s.Get(ctx, ownerID, id)
	if err != nil {
		return exam.Result{}, err
	}
	if session.Status != exam.SessionCompleted {
		return exam.Result{}, fault.New(fault.Conflict, "SESSION_NOT_COMPLETED", "Диагностика ещё не завершена.")
	}
	return session.Result(), nil
}

// TurnAudio voices a Geek turn: the whole turn, or only the reply to the
// previous answer or only the question after it. The whole turn is cached per
// turn; a part is cached by its text, so a prepared question that every
// student hears is synthesized once.
func (s *Service) TurnAudio(ctx context.Context, ownerID, id, turnID string, part AudioPart, chunk ...int) ([]byte, error) {
	session, err := s.load(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	turn, ok := session.Turn(turnID)
	if !ok || turn.Speaker != exam.SpeakerGeek {
		return nil, fault.New(fault.NotFound, "TURN_NOT_FOUND", "Реплика Geek не найдена.")
	}
	if len(chunk) > 1 || (len(chunk) == 1 && part != AudioReply) {
		return nil, fault.Validation("chunk", "Номер блока допустим только для reply.")
	}
	switch part {
	case AudioIntro:
		if turn.AudioIntro == "" {
			return nil, fault.New(fault.NotFound, "TURN_PART_NOT_FOUND", "У реплики Geek нет вводной.")
		}
		return s.speech.Part(ctx, turn.AudioIntro)
	case AudioTransition:
		text := NextQuestionTransition(turn)
		if text == "" {
			return nil, fault.New(fault.NotFound, "TURN_PART_NOT_FOUND", "У реплики Geek нет перехода.")
		}
		return s.speech.Part(ctx, text)
	case AudioWhole:
		// Prepared questions and fixed conversational replies use the same
		// content-keyed cache as the background warmer. Personalized replies
		// and feedback-plus-question turns remain private to the session.
		if turn.Reply == "" && !turn.Meta && turn.Kind != exam.KindClosing {
			return s.speech.Part(ctx, turn.Text)
		}
		if turn.Meta {
			for _, text := range ConversationSpeech() {
				if turn.Text == text {
					return s.speech.Part(ctx, turn.Text)
				}
			}
		}
		return s.speech.Get(ctx, "geek/v2/"+session.ID+"/"+turn.ID+".wav", turn.Text)
	case AudioReply, AudioQuestion:
		reply, question := turn.Parts()
		text := question
		if part == AudioReply {
			text = reply
			if len(chunk) == 1 {
				parts := ReplySpeechParts(reply)
				if chunk[0] < 0 || chunk[0] >= len(parts) {
					return nil, fault.Validation("chunk", "Блок озвучки не найден.")
				}
				text = parts[chunk[0]]
			}
		}
		if strings.TrimSpace(text) == "" {
			return nil, fault.New(fault.NotFound, "TURN_PART_NOT_FOUND", "У реплики Geek нет этой части.")
		}
		return s.speech.Part(ctx, text)
	}
	return nil, fault.Validation("part", "Часть реплики: intro, reply, transition или question.")
}

func (s *Service) load(ctx context.Context, ownerID, id string) (exam.Session, error) {
	session, err := s.store.Session(ctx, ownerID, id)
	if errors.Is(err, ErrNotFound) {
		return exam.Session{}, errSessionNotFound
	}
	return session, err
}

// replay returns the stored result of a repeated request.
func (s *Service) replay(ctx context.Context, ownerID, key, fingerprint string) (Request, bool, error) {
	r, ok, err := s.store.Request(ctx, ownerID, key)
	if err != nil || !ok {
		return Request{}, false, err
	}
	if r.Fingerprint != fingerprint {
		return Request{}, true, fault.New(fault.Invalid, "IDEMPOTENCY_KEY_REUSED", "Ключ повтора уже использован для другого запроса.")
	}
	return r, true, nil
}

// refresh cancels an active diagnostic whose course graph was rebuilt.
func (s *Service) refresh(ctx context.Context, session exam.Session) (exam.Session, error) {
	if session.Status != exam.SessionActive {
		return session, nil
	}
	graph, ok, err := s.graphs.Current(ctx, session.SubjectID)
	if err != nil || !ok || graph.Version == session.Exam.Graph.Version {
		return session, err
	}
	previous := session.Version
	session.Status = exam.SessionCancelled
	session.Version++
	if err := s.store.Update(ctx, session, previous, nil); err != nil && !errors.Is(err, ErrConflict) {
		return exam.Session{}, err
	}
	return s.store.Session(ctx, session.OwnerID, session.ID)
}

// unaskedPrerequisites are the titles of the direct and transitive
// prerequisites of a topic that are still unchecked: the descent may reach
// any of them.
func unaskedPrerequisites(e *exam.Exam, id string) []string {
	titles := []string{}
	for _, p := range e.Graph.Closure(id) {
		if n, ok := e.Graph.Node(p); ok && e.Nodes[p].Status == exam.StatusUnchecked {
			titles = append(titles, n.Title)
		}
	}
	return titles
}

// say appends the Geek turn for a step, led by the reply to the previous
// answer; recap marks a reply on the previous, closed topic. The closing
// completes the session.
func (s *Service) say(ctx context.Context, session *exam.Session, step exam.Step, reply string, recap bool) error {
	if step.Kind == exam.KindClosing {
		now := s.now().Unix()
		session.Status, session.CompletedAt = exam.SessionCompleted, &now
		return s.appendLed(session, exam.KindClosing, nil, reply, recap, closingText)
	}
	node, _ := session.Exam.Graph.Node(step.NodeID)
	var text string
	if step.Kind == exam.KindQuestion && node.MainQuestion() != "" {
		text = node.Questions[0]
	} else if step.Kind == exam.KindRephrase && !step.Hint && node.PreparedRephrase() != "" {
		text = node.PreparedRephrase()
	} else {
		var err error
		text, err = s.examiner.Question(ctx, QuestionRequest{PreferredName: session.PreferredName, Reasons: session.Exam.Nodes[node.ID].Reasons, AddressPreference: session.AddressPreference, Hint: step.Hint, Kind: step.Kind, Node: node, Dialogue: session.Dialogue(node.ID)})
		if err != nil {
			return modelError(err)
		}
		if text = strings.TrimSpace(text); text == "" {
			return modelError(errors.New("empty question"))
		}
	}
	nodeID := step.NodeID
	return s.appendLed(session, step.Kind, &nodeID, reply, recap, text)
}

// appendLed appends a Geek turn whose text starts with the reply, if any.
func (s *Service) appendLed(session *exam.Session, kind exam.TurnKind, nodeID *string, reply string, recap bool, text string) error {
	if reply != "" {
		text = reply + "\n\n" + text
	}
	if err := s.append(session, exam.SpeakerGeek, kind, nodeID, nil, text); err != nil {
		return err
	}
	session.Turns[len(session.Turns)-1].Reply = reply
	if recap && reply != "" {
		session.Turns[len(session.Turns)-1].Recap = reply + "\n\n"
		index := (len(session.Turns) / 2) % len(feedbackLeadIns)
		for i := len(session.Turns) - 2; i >= 0; i-- {
			if session.Turns[i].AudioIntro == feedbackLeadIns[index] {
				index = (index + 1) % len(feedbackLeadIns)
				break
			}
			if session.Turns[i].AudioIntro != "" {
				break
			}
		}
		session.Turns[len(session.Turns)-1].AudioIntro = feedbackLeadIns[index]
	}
	return nil
}

func (s *Service) append(session *exam.Session, speaker exam.Speaker, kind exam.TurnKind, nodeID *string, source *exam.Source, text string) error {
	id, err := s.ids.New("turn")
	if err != nil {
		return err
	}
	session.Turns = append(session.Turns, exam.Turn{ID: id, Line: len(session.Turns) + 1, Speaker: speaker, Kind: kind, NodeID: nodeID, Source: source, Text: text, CreatedAt: s.now().Unix()})
	return nil
}

func (s *Service) answerText(ctx context.Context, ownerID string, a Answer) (string, exam.Source, error) {
	switch {
	case a.DontKnow:
		return "Не знаю.", exam.SourceDontKnow, nil
	case a.TranscriptionID != "":
		text, ok, err := s.texts.Text(ctx, ownerID, a.TranscriptionID)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", fault.New(fault.NotFound, "TRANSCRIPTION_NOT_FOUND", "Расшифровка не найдена.")
		}
		return text, exam.SourceVoice, nil
	}
	return strings.TrimSpace(a.Text), exam.SourceText, nil
}

func answerFingerprint(sessionID string, a Answer) (string, error) {
	set := 0
	if a.TranscriptionID != "" {
		set++
	}
	if a.Text != "" {
		set++
	}
	if a.DontKnow {
		set++
	}
	switch {
	case strings.TrimSpace(a.TurnID) == "":
		return "", fault.Validation("turn_id", "Укажите текущую реплику.")
	case set != 1:
		return "", fault.Validation("body", "Нужно ровно одно из transcription_id, text или dont_know.")
	case a.Text != "" && (strings.TrimSpace(a.Text) == "" || utf8.RuneCountInString(a.Text) > maxTextLen || strings.IndexByte(a.Text, 0) >= 0):
		return "", fault.Validation("text", "Ответ должен содержать 1–4000 символов.")
	}
	return fmt.Sprintf("answer:%s:%s:%s:%s:%t", sessionID, a.TurnID, a.TranscriptionID, a.Text, a.DontKnow), nil
}

func checkEvaluation(e exam.Evaluation, session exam.Session) error {
	switch e.Verdict {
	case exam.VerdictAnswered, exam.VerdictDontKnow, exam.VerdictInsufficient, exam.VerdictHintRequested, exam.VerdictAddressCorrection, exam.VerdictClarifyQuestion, exam.VerdictPause:
	default:
		return fmt.Errorf("unknown verdict %q", e.Verdict)
	}
	if session.Exam.Current != nil && e.Verdict == exam.VerdictAnswered {
		if maximum := session.Exam.Graph.MaxScore(session.Exam.Current.NodeID); e.Score < 0 || e.Score > maximum {
			return fmt.Errorf("score %d out of 0..%d", e.Score, maximum)
		}
	}
	for _, line := range e.Evidence {
		if line < 1 || line > len(session.Turns) {
			return fmt.Errorf("evidence line %d out of range", line)
		}
	}
	return nil
}

func modelError(err error) error {
	var f *fault.Error
	if errors.As(err, &f) {
		return err
	}
	return errors.Join(fault.New(fault.Unavailable, "MODEL_UNAVAILABLE", "Модель временно недоступна; повторите ответ."), err)
}
