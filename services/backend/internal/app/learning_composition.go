package app

import (
	"context"
	"net/http"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/training"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	assessmentapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/assessment/application"
	assessmentmodel "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/assessment/infrastructure/modelapi"
	assessmentpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/assessment/infrastructure/postgres"
	assessmenthttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/assessment/transport/http"
	authapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/auth/application"
	competencyhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/transport/http"
	diagnosticapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/diagnostic/application"
	diagnostichttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/diagnostic/transport/http"
	diagnosticfeedbackapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/diagnosticfeedback/application"
	diagnosticfeedbackmodel "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/diagnosticfeedback/infrastructure/modelapi"
	diagnosticfeedbackpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/diagnosticfeedback/infrastructure/postgres"
	diagnosticfeedbackhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/diagnosticfeedback/transport/http"
	learningapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/learning/application"
	learningmodel "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/learning/infrastructure/modelapi"
	learningpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/learning/infrastructure/postgres"
	learninghttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/learning/transport/http"
	subjecthttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/subject/transport/http"
	taskaudioapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskaudio/application"
	taskaudiopg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskaudio/infrastructure/postgres"
	taskaudiohttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskaudio/transport/http"
	taskbankapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskbank/application"
	taskbankpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskbank/infrastructure/postgres"
	taskbankhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskbank/transport/http"
	taskgenapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskgen/application"
	taskgenmodel "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskgen/infrastructure/modelapi"
	taskgenpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskgen/infrastructure/postgres"
	taskgenhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskgen/transport/http"
	trainingapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/training/application"
	trainingmodel "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/training/infrastructure/modelapi"
	trainingpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/training/infrastructure/postgres"
	trainingsnapshot "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/training/infrastructure/snapshot"
	traininghttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/training/transport/http"
	variantgenhttp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/variantgen/transport/http"
	voiceapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/voice/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/llmgateway"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/security"
	pb "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/llmgateway/v1"
)

func (a *App) learningModelHTTP() *http.Client {
	return &http.Client{Transport: llmgateway.Transport{Gateway: a.gatewayClient, Complexity: pb.Complexity_COMPLEXITY_HIGH}}
}

func learningTaskItem(task training.Task) variant.VariantTask {
	voiceInstruction, referenceAnswer, criteria := task.VoiceInstruction, task.ReferenceAnswer, task.Criteria
	taxonomy, level, importance, content := task.TaxonomyCode, task.ALDLevelCode, task.Importance, task.EducationalContent
	return variant.VariantTask{ID: task.SourceTaskID, Role: "training", Task: variant.TaskProfile{
		ID: task.SourceTaskID, Question: task.Question, Options: task.Options,
		VoiceInstruction: &voiceInstruction, AudioAssetID: task.AudioAssetID,
		ReferenceAnswer: &referenceAnswer, Criteria: &criteria,
		Competency:  variant.CompetencyProfile{ID: task.CompetencyID, Name: task.CompetencyName},
		Constituent: variant.ConstituentProfile{ID: task.ConstituentID, Name: task.ConstituentName},
		Outcome:     variant.OutcomeProfile{ID: task.OutcomeID, Name: task.OutcomeName, TaxonomyCode: &taxonomy, ALDLevelCode: &level, Importance: &importance, EducationalContent: &content},
	}}
}

type trainingAssessment struct{ service *assessmentapp.Service }

func (g trainingAssessment) EvaluateTraining(ctx context.Context, owner, transcriptionID string, exercise training.Exercise) (assessmentapp.Evaluation, error) {
	return g.service.EvaluateTrusted(ctx, owner, transcriptionID, learningTaskItem(exercise.Task))
}

type trainingFinalAssessment struct{ service *assessmentapp.Service }

func (g trainingFinalAssessment) GradeFinal(ctx context.Context, exercise training.Exercise, answer string) (assessmentapp.Evaluation, error) {
	return g.service.EvaluateTrustedText(ctx, learningTaskItem(exercise.Task), answer)
}

func (a *App) registerLearningRoutes(mux *http.ServeMux, auth *authapp.Service, voice *voiceapp.Service, subjects *subjecthttp.Handler, competency *competencyhttp.Handler, variants *variantgenhttp.Handler) {
	grader := assessmentmodel.New(a.learningModelHTTP(), "http://gateway.invalid/v1", "", a.cfg.LLMTimeout)
	assessment := assessmentapp.New(assessmentpg.New(a.pool), a.variantRepository, grader)
	assessmentHandlers := assessmenthttp.New(assessment)
	transcriptions := assessmentpg.New(a.pool)
	taskAudio := taskaudioapp.NewService(taskaudiopg.New(a.pool), a.audioStorage).WithPrioritizer(a.audioWorker)
	taskAudioHandlers := taskaudiohttp.New(taskAudio)
	diagnostic := diagnosticapp.New(a.diagnosticStore, a.variantRepository, voice, assessment, security.IDGenerator{}, a.now).
		WithAudioReader(taskAudio).WithAudioRegenerator(a.audioWorker).WithTranscriptions(transcriptions)
	diagnosticHandlers := diagnostichttp.New(diagnostic, a.cfg.MaxUploadBytes)
	feedbackModel := diagnosticfeedbackmodel.New(a.learningModelHTTP(), "http://gateway.invalid/v1", "", a.cfg.LLMTimeout)
	feedback := diagnosticfeedbackapp.New(diagnostic, feedbackModel, diagnosticfeedbackpg.NewStore(a.pool), a.now).WithTaskFinder(diagnosticfeedbackpg.NewTaskFinder(a.pool))
	feedbackHandlers := diagnosticfeedbackhttp.New(feedback)
	taskBank := taskbankhttp.New(taskbankapp.New(taskbankpg.New(a.pool)))
	taskGenRepository := taskgenpg.New(a.pool)
	taskGenerator := taskgenmodel.New(a.learningModelHTTP(), "http://gateway.invalid/v1", "", a.cfg.LLMTimeout)
	taskGen := taskgenhttp.New(taskgenapp.New(taskGenRepository, taskGenerator, "learning-taskgen", func() int64 { return a.now().Unix() }), taskgenapp.NewMaterialService(taskGenRepository, func() int64 { return a.now().Unix() }))
	trainingRepository := trainingpg.New(a.pool).WithLease(a.cfg.HTTPWriteTimeout())
	trainingService := trainingapp.New(trainingRepository, a.diagnosticStore, voice, trainingAssessment{assessment}, trainingsnapshot.Provider{}, security.IDGenerator{}, a.now).
		WithAudio(taskAudio).WithAudioRegenerator(a.audioWorker).WithBank(trainingRepository)
	chatRepository := trainingpg.NewChatRepository(a.pool).WithLease(a.cfg.HTTPWriteTimeout())
	trainingService = trainingService.WithCheckedNext(chatRepository)
	var tutor trainingapp.Tutor
	if a.cfg.LearningTutorEnabled {
		tutor = trainingmodel.NewTutor(a.learningModelHTTP(), "http://gateway.invalid/v1", "", a.cfg.LLMTimeout)
	}
	chatService := trainingapp.NewChatService(chatRepository, voice, tutor, trainingFinalAssessment{assessment}, security.IDGenerator{}, a.now).
		WithContextLimits(training.ContextLimits{TaskMaxChars: a.cfg.TutorTaskMaxChars, HistoryMaxChars: a.cfg.TutorHistoryMaxChars, SummaryMaxChars: a.cfg.TutorSummaryMaxChars})
	chatHandlers := traininghttp.NewChatHandler(chatService, a.cfg.MaxUploadBytes)
	trainingHandlers := traininghttp.New(trainingService, a.cfg.MaxUploadBytes)
	var teacher learningapp.Teacher
	if a.cfg.LearningTutorEnabled {
		teacher = learningmodel.New(a.learningModelHTTP(), "http://gateway.invalid/v1", "", a.cfg.LLMTimeout)
	}
	planService := learningapp.New(learningpg.New(a.pool), a.diagnosticStore, voice, teacher, security.IDGenerator{}, a.now).WithTranscriptions(transcriptions)
	planHandlers := learninghttp.New(planService, a.cfg.MaxUploadBytes)

	protectRoute := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, protect(auth, h)) }
	protectRoute("POST /admin/subjects", subjects.CreateLearning)
	protectRoute("GET /subjects", subjects.ListLearning)
	protectRoute("GET /subjects/{subject_id}/competency-map", competency.ReadSubject)
	protectRoute("GET /subjects/{subject_id}/diagnostic-outline", variants.Preview)
	protectRoute("POST /variants", variants.Create)
	protectRoute("GET /variants", variants.List)
	protectRoute("GET /variants/{id}", variants.Read)
	protectRoute("GET /variants/{id}/tasks/{task_id}", variants.ReadTask)
	protectRoute("POST /assessments/evaluate", assessmentHandlers.Evaluate)
	protectRoute("POST /assessments/overall-feedback", feedbackHandlers.SynthesizeAnswersFeedback)
	protectRoute("POST /diagnostic-sessions", diagnosticHandlers.Start)
	protectRoute("GET /diagnostic-sessions/{id}", diagnosticHandlers.Read)
	protectRoute("GET /subjects/{subject_id}/learning-state", diagnosticHandlers.LearningState)
	protectRoute("GET /diagnostic-sessions/{id}/current/audio", diagnosticHandlers.CurrentAudio)
	protectRoute("POST /diagnostic-sessions/{id}/current/audio/regenerate", diagnosticHandlers.RegenerateCurrentAudio)
	protectRoute("GET /task-audio/{id}/file", taskAudioHandlers.File)
	protectRoute("POST /diagnostic-sessions/{id}/answers/transcription", diagnosticHandlers.AnswerTranscription)
	protectRoute("POST /diagnostic-sessions/{id}/answers", diagnosticHandlers.Answer)
	protectRoute("POST /diagnostic-sessions/{id}/skip", diagnosticHandlers.Skip)
	protectRoute("GET /diagnostic-sessions/{id}/result", diagnosticHandlers.Result)
	protectRoute("GET /diagnostic-sessions/{id}/feedback", feedbackHandlers.GetFeedback)
	protectRoute("GET /diagnostic-sessions/{id}/training/preview", trainingHandlers.Preview)
	protectRoute("POST /diagnostic-sessions/{id}/training", trainingHandlers.Start)
	protectRoute("GET /diagnostic-sessions/{id}/training", trainingHandlers.ByDiagnostic)
	protectRoute("GET /training-sessions/{id}", trainingHandlers.Get)
	protectRoute("POST /training-sessions/{id}/answers", trainingHandlers.Answer)
	protectRoute("POST /training-sessions/{id}/skip", trainingHandlers.Skip)
	protectRoute("POST /training-sessions/{id}/answers/reset", trainingHandlers.ResetAnswer)
	protectRoute("POST /training-sessions/{id}/exercises/{exercise_id}/next", trainingHandlers.NextChecked)
	protectRoute("GET /training-sessions/{id}/history", trainingHandlers.History)
	protectRoute("GET /training-sessions/{id}/current/audio", trainingHandlers.Audio)
	protectRoute("POST /training-sessions/{id}/current/audio/regenerate", trainingHandlers.RegenerateAudio)
	protectRoute("GET /training-sessions/{id}/exercises/{exercise_id}/chat", chatHandlers.Read)
	protectRoute("POST /training-sessions/{id}/exercises/{exercise_id}/chat/turns", chatHandlers.Send)
	protectRoute("POST /training-sessions/{id}/exercises/{exercise_id}/chat/turns/{turn_id}/retry", chatHandlers.Retry)
	protectRoute("POST /training-sessions/{id}/exercises/{exercise_id}/chat/turns/{turn_id}/cancel", chatHandlers.Cancel)
	protectRoute("GET /tasks", taskBank.Search)
	protectRoute("GET /tasks/{id}", taskBank.Profile)
	protectRoute("POST /tasks/generate", taskGen.Generate)
	protectRoute("POST /admin/materials", taskGen.ImportMaterial)
	protectRoute("POST /diagnostic-sessions/{id}/learning-plan", planHandlers.Create)
	protectRoute("GET /diagnostic-sessions/{id}/learning-plan", planHandlers.ByDiagnostic)
	protectRoute("GET /learning-plans/{id}", planHandlers.Get)
	protectRoute("GET /learning-plans/{id}/items/{index}/messages", planHandlers.Messages)
	protectRoute("POST /learning-plans/{id}/open", planHandlers.Open)
	protectRoute("POST /learning-plans/{id}/answers/transcription", planHandlers.AnswerTranscription)
	protectRoute("POST /learning-plans/{id}/answers", planHandlers.Answer)
	protectRoute("POST /learning-plans/{id}/answers/retry", planHandlers.Retry)
}
