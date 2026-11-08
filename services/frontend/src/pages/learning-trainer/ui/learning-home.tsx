import { Button, Card, Heading, Text } from '@radix-ui/themes'
import {
  GraduationCap,
  Mic,
  RotateCcw,
  Sparkles,
  Target,
  Zap,
} from 'lucide-react'
import { useCallback, useEffect, useId, useState } from 'react'
import type { ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/features/auth'
import {
  useLearningStateQuery,
  useSubjectsQuery,
} from '@/features/learning-subject-selection'
import { subjectQueryKeys, type Subject } from '@/entities/learning-subject'
import { LearningPlanEntry } from './learning-plan'
import { DiagnosticTrainer } from './diagnostic-trainer'
import { LearningRoadmap } from './learning-roadmap'
import {
  competencyMapQueryKeys,
  useCompetencyMapOutlineQuery,
} from '@/features/import-learning-competency-map'
import type { CompetencyMapOutline } from '@/entities/learning-competency-map'
import { AccountMenu } from '@/features/auth'
import styles from './learning-home.module.scss'

type Intent = {
  apiBase: string
  userId: string
  subjectId: string
  sessionId: string
  mode: 'diagnostic' | 'training'
  diagnosticId?: string
}
const apiBase =
  (import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(/\/$/, '') + '/learn'
const selectedSubjectKey = 'ai-tutor:selected-subject'
function readSelectedSubject(userId: string) {
  try {
    return localStorage.getItem(`${selectedSubjectKey}:${userId}`) ?? ''
  } catch {
    return ''
  }
}
function saveSelectedSubject(userId: string, subjectId: string) {
  try {
    localStorage.setItem(`${selectedSubjectKey}:${userId}`, subjectId)
  } catch {
    // The selection still works for this page when storage is unavailable.
  }
}
function saveIntent(intent: Intent) {
  try {
    sessionStorage.setItem('ai-tutor:learning-home', JSON.stringify(intent))
  } catch {
    // The current page can continue from React state without persisted intent.
  }
}
function clearIntent() {
  try {
    sessionStorage.removeItem('ai-tutor:learning-home')
  } catch {
    // Clearing persisted state is best-effort; the current page still resets.
  }
}

export function LearningHome({
  requestedSubjectId,
  requestedResume,
}: {
  requestedSubjectId?: string
  requestedResume?: 'diagnostic' | 'training'
}) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const userId = user?.id ?? ''
  const subjectsQuery = useSubjectsQuery(userId)
  const [savedSubjectId, setSavedSubjectId] = useState(() =>
    userId ? readSelectedSubject(userId) : '',
  )
  // A persisted intent is only request metadata for the current visit. On a
  // fresh visit, show course progress first and resume only after an explicit click.
  const [intent, setIntent] = useState<Intent | null>(null)
  const [renderedRoadmap, setRenderedRoadmap] = useState<{
    subjectId: string
    outline: CompetencyMapOutline
  } | null>(null)
  const handleRoadmapReady = useCallback(
    (subjectId: string, outline: CompetencyMapOutline, ready: boolean) => {
      setRenderedRoadmap((previous) => {
        const matches =
          previous?.subjectId === subjectId && previous.outline === outline
        if (!ready) return matches ? null : previous
        return matches ? previous : { subjectId, outline }
      })
    },
    [],
  )
  const subjects = subjectsQuery.data ?? []
  // A direct course URL wins; otherwise reuse the shared selection across modes.
  const catalogSelection = requestedSubjectId
    ? subjects.find((subject) => subject.id === requestedSubjectId)
    : (subjects.find((subject) => subject.id === savedSubjectId) ??
      (savedSubjectId
        ? undefined
        : intent
          ? subjects.find((subject) => subject.id === intent.subjectId)
          : subjects[0]))
  const learningSubjectId =
    catalogSelection?.id ??
    requestedSubjectId ??
    (savedSubjectId || intent?.subjectId || '')
  const learning = useLearningStateQuery(userId, learningSubjectId)
  const hasLearningHistory = Boolean(
    learning.data?.active_session_id ||
    learning.data?.training_available ||
    learning.data?.diagnostic_completed,
  )
  const historicalSubject: Subject | undefined =
    !catalogSelection && learning.data && hasLearningHistory
      ? {
          id: learning.data.subject_id,
          name: learning.data.subject_name,
          ready: false,
          learning_ready: false,
          exam_ready: false,
        }
      : undefined
  const selected = catalogSelection ?? historicalSubject

  const data = learning.data
  const [openedRequest, setOpenedRequest] = useState(false)
  useEffect(() => {
    if (
      openedRequest ||
      !requestedResume ||
      !requestedSubjectId ||
      selected?.id !== requestedSubjectId ||
      !data
    )
      return
    const sessionId =
      requestedResume === 'diagnostic'
        ? data.active_session_id
        : data.diagnostic_session_id
    if (
      sessionId &&
      (requestedResume === 'diagnostic' || data.training_available)
    ) {
      const next: Intent = {
        apiBase,
        userId,
        subjectId: selected.id,
        sessionId: requestedResume === 'diagnostic' ? sessionId : '',
        mode: requestedResume,
        ...(requestedResume === 'training' ? { diagnosticId: sessionId } : {}),
      }
      saveIntent(next)
      setIntent(next)
    }
    setOpenedRequest(true)
  }, [
    openedRequest,
    requestedResume,
    requestedSubjectId,
    selected,
    data,
    userId,
  ])
  // Diagnostic and training are one process for the student: the course
  // offers a single action that resumes where they stopped.
  const stage = !data
    ? undefined
    : data.active_session_id
      ? 'diagnostic'
      : data.training_available && data.diagnostic_session_id
        ? 'training'
        : data.diagnostic_completed
          ? 'completed'
          : 'start'
  const showRoadmap = Boolean(
    selected?.learning_ready &&
    intent?.subjectId !== selected.id &&
    (stage === 'start' || stage === 'completed' || stage === 'training'),
  )
  const outline = useCompetencyMapOutlineQuery(
    userId,
    showRoadmap ? (selected?.id ?? '') : '',
  )
  const canStartDiagnostic =
    showRoadmap &&
    outline.isSuccess &&
    outline.fetchStatus === 'idle' &&
    outline.data.competencies.length > 0 &&
    renderedRoadmap?.subjectId === selected?.id &&
    renderedRoadmap?.outline === outline.data

  useEffect(() => {
    if (selected) {
      setSavedSubjectId(selected.id)
      saveSelectedSubject(userId, selected.id)
    }
  }, [selected, userId])

  function openDiagnostic(
    subject: Subject,
    sessionId: string | undefined,
    startNew: boolean,
  ) {
    if (!userId) return
    if (startNew) {
      const current = queryClient.getQueryState<CompetencyMapOutline>(
        competencyMapQueryKeys.outline(userId, subject.id),
      )
      if (
        !canStartDiagnostic ||
        current?.status !== 'success' ||
        current.fetchStatus !== 'idle' ||
        !current.data?.competencies.length
      )
        return
    }
    const next: Intent = {
      apiBase,
      userId,
      subjectId: subject.id,
      sessionId: sessionId ?? '',
      mode: 'diagnostic',
    }
    if (startNew) next.sessionId = ''
    saveIntent(next)
    setIntent(next)
  }
  function back() {
    clearIntent()
    setIntent(null)
    if (selected)
      void queryClient.invalidateQueries({
        queryKey: subjectQueryKeys.learningState(userId, selected.id),
      })
  }
  function openTraining(
    diagnosticId = learning.data?.diagnostic_session_id ?? '',
  ) {
    if (!selected || !diagnosticId) return
    const next: Intent = {
      apiBase,
      userId,
      subjectId: selected.id,
      sessionId: '',
      mode: 'training',
      diagnosticId,
    }
    saveIntent(next)
    setIntent(next)
  }
  if (
    intent?.mode === 'training' &&
    selected &&
    selected.id === intent.subjectId &&
    userId
  )
    return (
      <LearningPlanEntry
        key={intent.diagnosticId}
        userId={userId}
        diagnosticId={
          intent.diagnosticId ?? learning.data?.diagnostic_session_id ?? ''
        }
        subjectName={selected.name}
        onBack={back}
      />
    )
  if (
    intent?.mode === 'diagnostic' &&
    selected &&
    selected.id === intent.subjectId
  )
    return (
      <DiagnosticTrainer
        subjectId={selected.id}
        subjectName={selected.name}
        onBack={back}
        onTraining={(diagnosticId) => {
          void queryClient.invalidateQueries({
            queryKey: subjectQueryKeys.learningState(userId, selected.id),
          })
          openTraining(diagnosticId)
        }}
        initialSessionId={intent.sessionId || undefined}
        startNew={!intent.sessionId}
      />
    )

  function resume() {
    if (!selected || !data) return
    if (stage === 'diagnostic')
      openDiagnostic(selected, data.active_session_id ?? undefined, false)
    else if (stage === 'training') openTraining()
    else openDiagnostic(selected, undefined, true)
  }
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div className={styles.brand}>
          <span className={styles.brandIcon} aria-hidden="true">
            <GraduationCap size={22} />
          </span>
          <Text weight="bold">{t('learningTrainer.title')}</Text>
        </div>
        <AccountMenu />
      </header>
      <main className={styles.content}>
        <Heading as="h1" size="8" id="courses-title">
          {t('learningHome.title')}
        </Heading>
        {subjectsQuery.isPending ? (
          <Text role="status">{t('learningHome.loading')}</Text>
        ) : subjectsQuery.isError ? (
          <Card className={styles.notice}>
            <Text role="alert">{t('learningHome.loadError')}</Text>
            <Button onClick={() => void subjectsQuery.refetch()}>
              {t('learningTrainer.retry')}
            </Button>
          </Card>
        ) : !selected && learningSubjectId && learning.isPending ? (
          <Text role="status">{t('learningHome.learningLoading')}</Text>
        ) : !selected && learningSubjectId && learning.isError ? (
          <Card className={styles.notice}>
            <Text role="alert">{t('learningHome.learningError')}</Text>
            <Button onClick={() => void learning.refetch()}>
              {t('learningTrainer.retry')}
            </Button>
          </Card>
        ) : !selected ? (
          <Card className={styles.notice}>
            <Text>{t('learningHome.empty')}</Text>
          </Card>
        ) : (
          <section className={styles.courses} aria-labelledby="courses-title">
            <article className={styles.course} aria-labelledby="courses-title">
              <div className={styles.courseHeader}>
                <Text as="p" size="3" color="gray">
                  {t('learningHome.intro')}
                </Text>
              </div>
              {/* Four coloured, numbered steps say what the student does: a voice
                  check, its gaps, a personal plan and exam preparation. They
                  are information, not controls. */}
              <ol
                className={styles.steps}
                aria-label={t('learningHome.stepsLabel')}
              >
                {[
                  { key: 'check', icon: <Mic size={20} /> },
                  { key: 'gaps', icon: <Target size={20} /> },
                  { key: 'plan', icon: <Sparkles size={20} /> },
                  { key: 'exam', icon: <GraduationCap size={20} /> },
                ].map((step, index) => (
                  <li key={step.key} className={styles.step} data-step={index}>
                    <span className={styles.stepNumber} aria-hidden="true">
                      {String(index + 1).padStart(2, '0')}
                    </span>
                    <span className={styles.stepIcon} aria-hidden="true">
                      {step.icon}
                    </span>
                    <Text as="p" className={styles.stepTitle}>
                      {t(`learningHome.steps.${step.key}.title`)}
                    </Text>
                    <Text as="p" className={styles.stepText}>
                      {t(`learningHome.steps.${step.key}.text`)}
                    </Text>
                  </li>
                ))}
              </ol>
              {showRoadmap && selected && (
                <LearningRoadmap
                  key={selected.id}
                  query={outline}
                  onReady={handleRoadmapReady}
                  subjectId={selected.id}
                  subjectName={selected.name}
                />
              )}
              {learning.isPending ? (
                <Text role="status">{t('learningHome.learningLoading')}</Text>
              ) : learning.isError ? (
                <Card className={styles.notice}>
                  <Text role="alert">{t('learningHome.learningError')}</Text>
                  <Button onClick={() => void learning.refetch()}>
                    {t('learningTrainer.retry')}
                  </Button>
                </Card>
              ) : data ? (
                <div className={styles.actions}>
                  {stage !== 'start' && (
                    <Text as="p" className={styles.status}>
                      {t(
                        stage === 'diagnostic'
                          ? 'learningHome.statusDiagnostic'
                          : stage === 'completed'
                            ? 'learningHome.statusCompleted'
                            : 'learningHome.statusTraining',
                      )}
                    </Text>
                  )}
                  <div className={styles.actionRow}>
                    {(stage !== 'completed' || selected.learning_ready) && (
                      <CourseAction
                        kind="primary"
                        icon={<Zap size={22} fill="currentColor" />}
                        title={t(
                          stage === 'start'
                            ? 'learningHome.startLearning'
                            : stage === 'completed'
                              ? 'learningHome.restart'
                              : 'learningHome.continueLearning',
                        )}
                        hint={t(
                          stage === 'start'
                            ? 'learningHome.startLearningHint'
                            : stage === 'diagnostic'
                              ? 'learningHome.continueDiagnosticHint'
                              : stage === 'completed'
                                ? 'learningHome.restartHint'
                                : 'learningHome.continueTrainingHint',
                        )}
                        disabled={
                          (stage === 'start' || stage === 'completed') &&
                          !canStartDiagnostic
                        }
                        onClick={resume}
                      />
                    )}
                    {stage === 'training' && selected.learning_ready && (
                      <CourseAction
                        kind="secondary"
                        icon={<RotateCcw size={22} />}
                        title={t('learningHome.restart')}
                        hint={t('learningHome.restartHint')}
                        disabled={!canStartDiagnostic}
                        onClick={() =>
                          openDiagnostic(selected, undefined, true)
                        }
                      />
                    )}
                  </div>
                </div>
              ) : null}
            </article>
          </section>
        )}
      </main>
    </div>
  )
}

// A large course action: icon, title and a line explaining what happens. The
// title is the accessible name; the explanation describes it.
function CourseAction({
  kind,
  icon,
  title,
  hint,
  onClick,
  disabled = false,
}: {
  kind: 'primary' | 'secondary'
  icon: ReactNode
  title: string
  hint: string
  onClick: () => void
  disabled?: boolean
}) {
  const hintId = useId()
  return (
    <button
      type="button"
      className={styles.courseAction}
      data-kind={kind}
      aria-label={title}
      aria-describedby={hintId}
      onClick={onClick}
      disabled={disabled}
    >
      <span className={styles.courseActionIcon} aria-hidden="true">
        {icon}
      </span>
      <span className={styles.courseActionText}>
        <span className={styles.courseActionTitle}>{title}</span>
        <span id={hintId} className={styles.courseActionHint}>
          {hint}
        </span>
      </span>
    </button>
  )
}
