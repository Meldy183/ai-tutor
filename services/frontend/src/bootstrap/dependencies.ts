import * as auth from '@/entities/user'
import * as competencyMap from '@/entities/competency-map'
import * as courseGraph from '@/entities/course-graph'
import * as subjects from '@/entities/subject'
import * as diagnostic from '@/entities/diagnostic-session'
import * as learningDiagnostic from '@/entities/learning-diagnostic-session'
import * as learningMap from '@/entities/learning-competency-map'
import * as learningSubjects from '@/entities/learning-subject'
import * as learningPlan from '@/entities/learning-plan'
import type { AuthDependencies } from '@/features/auth'
import type { CompetencyMapDependencies } from '@/features/import-competency-map'
import type { CourseGraphBuildDependencies } from '@/features/course-graph-build'
import type { DiagnosticDependencies } from '@/features/diagnostic-session'
import type { SubjectSelectionDependencies } from '@/features/subject-selection'
import type { DiagnosticDependencies as LearningDiagnosticDependencies } from '@/features/learning-diagnostic-session'
import type { CompetencyMapDependencies as LearningMapDependencies } from '@/features/import-learning-competency-map'
import type { SubjectSelectionDependencies as LearningSubjectDependencies } from '@/features/learning-subject-selection'
import type { LearningPlanDependencies } from '@/features/learning-plan'
import {
  createDiagnosticIdentity,
  diagnosticSessionStorage,
} from '@/features/learning-diagnostic-session'
import { transcribeRecording } from '@/shared/api'

export type AppDependencies = {
  auth: AuthDependencies['auth']
  competencyMap: CompetencyMapDependencies['competencyMap']
  courseGraph: CourseGraphBuildDependencies['courseGraph']
  subjects: SubjectSelectionDependencies['subjects']
  diagnostic: DiagnosticDependencies['diagnostic']
  learningMap: LearningMapDependencies['competencyMap']
  learningSubjects: LearningSubjectDependencies['subjects']
  learningApiBase: LearningDiagnosticDependencies['apiBase']
  createLearningDiagnosticIdentity: LearningDiagnosticDependencies['createDiagnosticIdentity']
  learningDiagnosticStorage: LearningDiagnosticDependencies['diagnosticStorage']
  learningDiagnostic: LearningDiagnosticDependencies['diagnostic']
  learningPlan: LearningPlanDependencies['learningPlan']
}

export function createAppDependencies(): AppDependencies {
  const learningApiBase = `${(import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(/\/$/, '')}/learn`
  return {
    learningApiBase,
    createLearningDiagnosticIdentity: createDiagnosticIdentity,
    learningDiagnosticStorage: diagnosticSessionStorage,
    auth: {
      readCurrentUser: auth.readCurrentUser,
      authenticate: auth.authenticate,
      logout: auth.logout,
    },
    competencyMap: {
      readCourseMaterialKinds: competencyMap.readCourseMaterialKinds,
      readSubjectMaterials: competencyMap.readSubjectMaterials,
      uploadCourseMaterials: competencyMap.uploadCourseMaterials,
      replaceCourseMaterialFile: competencyMap.replaceCourseMaterialFile,
      updateCourseMaterialMetadata: competencyMap.updateCourseMaterialMetadata,
      deleteCourseMaterial: competencyMap.deleteCourseMaterial,
    },
    courseGraph: {
      readLatestCourseGraphBuild: courseGraph.readLatestCourseGraphBuild,
      startCourseGraphBuild: courseGraph.startCourseGraphBuild,
      importCompetencyMap: courseGraph.importCompetencyMap,
      skipMissingCriteria: courseGraph.skipMissingCriteria,
      retryCompetencyMap: courseGraph.retryCompetencyMap,
      readCourseGraph: courseGraph.readCourseGraph,
    },
    subjects: {
      listSubjects: subjects.listSubjects,
      createSubject: subjects.createSubject,
      readAudioPreparation: subjects.readAudioPreparation,
    },
    learningMap: {
      readOutline: learningMap.readCompetencyMapOutline,
      read: learningMap.readCompetencyMap,
    },
    learningSubjects: {
      listSubjects: learningSubjects.listSubjects,
      createSubject: learningSubjects.createSubject,
      readLearningState: learningSubjects.readLearningState,
    },
    learningDiagnostic: {
      transcribeRecording,
      createVariant: learningDiagnostic.createVariant,
      startDiagnostic: learningDiagnostic.startDiagnostic,
      readDiagnostic: learningDiagnostic.readDiagnostic,
      readDiagnosticResult: learningDiagnostic.readDiagnosticResult,
      readDiagnosticFeedback: learningDiagnostic.readDiagnosticFeedback,
      submitDiagnostic: learningDiagnostic.submitDiagnostic,
      createSubmission: learningDiagnostic.createSubmission,
      createSkipSubmission: learningDiagnostic.createSkipSubmission,
      readDiagnosticAudio: learningDiagnostic.readDiagnosticAudio,
      regenerateDiagnosticAudio: learningDiagnostic.regenerateDiagnosticAudio,
      fetchDiagnosticAudioFile: learningDiagnostic.fetchDiagnosticAudioFile,
    },
    learningPlan: {
      previewAnswer: transcribeRecording,
      findLearningPlan: learningPlan.findLearningPlan,
      createLearningPlan: learningPlan.createLearningPlan,
      readLearningPlan: learningPlan.readLearningPlan,
      readLearningMessages: learningPlan.readLearningMessages,
      openLesson: learningPlan.openLesson,
      answerLesson: learningPlan.answerLesson,
      retryLessonAnswer: learningPlan.retryLessonAnswer,
    },
    diagnostic: {
      transcribeRecording,
      startDiagnostic: diagnostic.startDiagnostic,
      readDiagnostic: diagnostic.readDiagnostic,
      listDiagnostics: diagnostic.listDiagnostics,
      answerDiagnostic: diagnostic.answerDiagnostic,
      readDiagnosticResult: diagnostic.readDiagnosticResult,
      readDiagnosticResultSummary: diagnostic.readDiagnosticResultSummary,
      fetchTurnAudio: diagnostic.fetchTurnAudio,
    },
  }
}
