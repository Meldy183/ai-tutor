import { Button, Card, Heading, Text } from '@radix-ui/themes'
import { GraduationCap, Mic, Play, Sparkles, Target, Zap } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AccountMenu, useAuth } from '@/features/auth'
import { useSubjectsQuery } from '@/features/subject-selection'
import {
  useDiagnosticHistoryQuery,
  useStartDiagnosticMutation,
} from '@/features/diagnostic-session'
import { DiagnosticApiError } from '@/entities/diagnostic-session'
import { GeekDialog } from './geek-dialog'
import { GeekResult } from './geek-result'
import styles from './learning-home.module.scss'

type View = {
  subjectId: string
  sessionId: string
  screen: 'dialog' | 'result'
}
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

export function LearningHome({
  requestedSubjectId,
  requestedSessionId,
}: {
  requestedSubjectId?: string
  requestedSessionId?: string
}) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const userId = user?.id ?? ''
  const subjectsQuery = useSubjectsQuery(userId)
  const subjects = subjectsQuery.data ?? []
  // On a fresh visit, show course progress first; resume only after an explicit click.
  const [view, setView] = useState<View | null>(null)
  const [savedSubjectId, setSavedSubjectId] = useState(() =>
    userId ? readSelectedSubject(userId) : '',
  )
  const selected = requestedSubjectId
    ? subjects.find((subject) => subject.id === requestedSubjectId)
    : (subjects.find((subject) => subject.id === savedSubjectId) ??
      subjects.find((subject) => subject.id === view?.subjectId) ??
      subjects[0])
  const history = useDiagnosticHistoryQuery(userId, selected?.id ?? '')
  const start = useStartDiagnosticMutation(userId, selected?.id ?? '')
  const [openedRequest, setOpenedRequest] = useState(false)

  useEffect(() => {
    if (
      openedRequest ||
      !requestedSessionId ||
      !requestedSubjectId ||
      selected?.id !== requestedSubjectId ||
      !history.data
    )
      return
    const item = history.data.find(
      (entry) =>
        entry.id === requestedSessionId && entry.status !== 'cancelled',
    )
    if (item)
      setView({
        subjectId: selected.id,
        sessionId: item.id,
        screen: item.status === 'completed' ? 'result' : 'dialog',
      })
    setOpenedRequest(true)
  }, [
    openedRequest,
    requestedSessionId,
    requestedSubjectId,
    selected,
    history.data,
  ])

  useEffect(() => {
    if (
      view &&
      !subjectsQuery.isPending &&
      subjects.length &&
      !subjects.some((s) => s.id === view.subjectId)
    ) {
      setView(null)
    }
  }, [view, subjects, subjectsQuery.isPending])

  useEffect(() => {
    if (selected) {
      setSavedSubjectId(selected.id)
      saveSelectedSubject(userId, selected.id)
    }
  }, [selected, userId])

  function open(sessionId: string, screen: View['screen']) {
    if (!selected) return
    const next = { subjectId: selected.id, sessionId, screen }
    setView(next)
  }
  function back() {
    setView(null)
  }
  function begin() {
    start.mutate(undefined, {
      onSuccess: (session) => open(session.id, 'dialog'),
    })
  }

  const active = history.data?.find((item) => item.status === 'active')
  const startError =
    start.error instanceof DiagnosticApiError &&
    start.error.code === 'RUBRIC_INVALID'
      ? t('geek.rubricInvalid')
      : start.error instanceof DiagnosticApiError &&
          start.error.code === 'GRAPH_NOT_READY'
        ? t('home.graphNotReady')
        : start.error
          ? t('home.startError')
          : ''
  if (view && selected && view.subjectId === selected.id)
    return view.screen === 'dialog' ? (
      <GeekDialog
        key={view.sessionId}
        userId={userId}
        sessionId={view.sessionId}
        subjectName={selected.name}
        onBack={back}
        onResult={() => open(view.sessionId, 'result')}
      />
    ) : (
      <GeekResult
        key={view.sessionId}
        userId={userId}
        sessionId={view.sessionId}
        subjectName={selected.name}
        onBack={back}
        onAgain={begin}
        onChat={() => open(view.sessionId, 'dialog')}
        starting={start.isPending}
        startError={startError}
      />
    )

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div className={styles.brand}>
          <span className={styles.brandIcon} aria-hidden="true">
            <GraduationCap size={22} />
          </span>
          <Text weight="bold">{t('trainer.title')}</Text>
        </div>
        <AccountMenu />
      </header>
      <main className={styles.content}>
        <Heading as="h1" size="8" id="courses-title">
          {t('home.title')}
        </Heading>
        {subjectsQuery.isPending ? (
          <Text role="status">{t('home.loading')}</Text>
        ) : subjectsQuery.isError ? (
          <Card className={styles.notice}>
            <Text role="alert">{t('home.loadError')}</Text>
            <Button onClick={() => void subjectsQuery.refetch()}>
              {t('trainer.retry')}
            </Button>
          </Card>
        ) : !selected ? (
          <Card className={styles.notice}>
            <Text>{t('home.empty')}</Text>
          </Card>
        ) : (
          <section className={styles.courses} aria-labelledby="courses-title">
            <article className={styles.course} aria-labelledby="courses-title">
              <div className={styles.courseHeader}>
                <Text as="p" size="3" color="gray">
                  {t('home.intro')}
                </Text>
              </div>
              {/* Four numbered steps say what the student does; they are
                  information, not controls. */}
              <ol className={styles.steps} aria-label={t('home.stepsLabel')}>
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
                      {t(`home.steps.${step.key}.title`)}
                    </Text>
                    <Text as="p" className={styles.stepText}>
                      {t(`home.steps.${step.key}.text`)}
                    </Text>
                  </li>
                ))}
              </ol>
              <div className={styles.actions}>
                <div className={styles.actionRow}>
                  {active ? (
                    <CourseAction
                      icon={<Play size={22} fill="currentColor" />}
                      title={t('home.resume')}
                      hint={t('home.resumeHint')}
                      onClick={() => open(active.id, 'dialog')}
                    />
                  ) : (
                    <CourseAction
                      icon={<Zap size={22} fill="currentColor" />}
                      title={t(
                        start.isPending ? 'home.starting' : 'home.start',
                      )}
                      hint={t('home.startHint')}
                      disabled={start.isPending || history.isPending}
                      onClick={begin}
                    />
                  )}
                </div>
                {startError && (
                  <Text as="p" role="alert" color="red">
                    {startError}
                  </Text>
                )}
              </div>
            </article>
            {history.isError && (
              <Card className={styles.notice}>
                <Text role="alert">{t('home.historyError')}</Text>
                <Button onClick={() => void history.refetch()}>
                  {t('trainer.retry')}
                </Button>
              </Card>
            )}
          </section>
        )}
      </main>
    </div>
  )
}

// A large course action: icon, title and a line explaining what happens. The
// title is the accessible name; the explanation describes it.
function CourseAction({
  icon,
  title,
  hint,
  disabled,
  onClick,
}: {
  icon: ReactNode
  title: string
  hint: string
  disabled?: boolean
  onClick: () => void
}) {
  const hintId = useId()
  return (
    <button
      type="button"
      className={styles.courseAction}
      data-kind="primary"
      aria-label={title}
      aria-describedby={hintId}
      disabled={disabled}
      onClick={onClick}
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
