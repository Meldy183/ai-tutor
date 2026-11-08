import { Button, Text } from '@radix-ui/themes'
import {
  ArrowRight,
  Activity,
  GraduationCap,
  Mail,
  MessagesSquare,
} from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AccountMenu, useAuth } from '@/features/auth'
import { useSubjectsQuery } from '@/features/subject-selection'
import { useDiagnosticHistoryQuery } from '@/features/diagnostic-session'
import { useLearningStateQuery } from '@/features/learning-subject-selection'
import styles from './profile-screen.module.scss'

export function ProfileScreen() {
  const { t, i18n } = useTranslation()
  const { user } = useAuth()
  const subjects = useSubjectsQuery(user?.id ?? '')
  const [selectedId, setSelectedId] = useState('')
  const selected =
    subjects.data?.find((subject) => subject.id === selectedId) ??
    subjects.data?.[0]
  const history = useDiagnosticHistoryQuery(user?.id ?? '', selected?.id ?? '')
  const learning = useLearningStateQuery(user?.id ?? '', selected?.id ?? '')
  const name = user?.display_name || user?.email || ''

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <Link to="/" className={styles.brand}>
          <GraduationCap aria-hidden="true" />
          {t('trainer.title')}
        </Link>
        <AccountMenu />
      </header>
      <main className={styles.content}>
        <section className={styles.identity} aria-labelledby="profile-title">
          <span className={styles.avatar} aria-hidden="true">
            {name.charAt(0).toUpperCase()}
          </span>
          <div>
            <h1 id="profile-title">{name}</h1>
            <span className={styles.email}>
              <Mail size={15} aria-hidden="true" />
              {user?.email}
            </span>
          </div>
        </section>
        <div className={styles.heading}>
          <h2>{t('profile.history')}</h2>
          {subjects.data && subjects.data.length > 0 && (
            <label>
              <span className={styles.srOnly}>{t('profile.course')}</span>
              <select
                value={selected?.id ?? ''}
                onChange={(event) => setSelectedId(event.target.value)}
              >
                {subjects.data.map((subject) => (
                  <option key={subject.id} value={subject.id}>
                    {subject.name}
                  </option>
                ))}
              </select>
            </label>
          )}
        </div>
        {subjects.isPending ? (
          <Text role="status">{t('home.loading')}</Text>
        ) : subjects.isError ? (
          <div role="alert">
            {t('home.loadError')}{' '}
            <Button onClick={() => void subjects.refetch()}>
              {t('trainer.retry')}
            </Button>
          </div>
        ) : !selected ? (
          <Text>{t('home.empty')}</Text>
        ) : (
          <>
            {learning.isPending && (
              <Text as="p" role="status">
                {t('profile.learningLoading')}
              </Text>
            )}
            {learning.isError && (
              <div role="alert">
                {t('profile.learningError')}{' '}
                <Button onClick={() => void learning.refetch()}>
                  {t('trainer.retry')}
                </Button>
              </div>
            )}
            {history.isPending && (
              <Text as="p" role="status">
                {t('home.historyLoading')}
              </Text>
            )}
            {history.isError && (
              <div role="alert">
                {t('home.historyError')}{' '}
                <Button onClick={() => void history.refetch()}>
                  {t('trainer.retry')}
                </Button>
              </div>
            )}
            <ul className={styles.list}>
              {learning.data?.active_session_id && (
                <li className={styles.item}>
                  <Activity className={styles.icon} aria-hidden="true" />
                  <div>
                    <strong>{t('profile.diagnostic')}</strong>
                    <span>{t('profile.inProgress')}</span>
                  </div>
                  <div>
                    <strong>
                      {t('profile.subject', { name: selected.name })}
                    </strong>
                  </div>
                  <Link
                    className={styles.action}
                    to="/learn"
                    search={{ subject_id: selected.id, resume: 'diagnostic' }}
                  >
                    {t('profile.resumeDiagnostic')}
                    <ArrowRight size={20} aria-hidden="true" />
                  </Link>
                </li>
              )}
              {learning.data?.diagnostic_completed && (
                <li className={styles.item}>
                  <Activity className={styles.icon} aria-hidden="true" />
                  <div>
                    <strong>{t('profile.diagnostic')}</strong>
                    <span>{t('profile.completed')}</span>
                  </div>
                  <div>
                    <strong>
                      {t('profile.subject', { name: selected.name })}
                    </strong>
                  </div>
                  {learning.data.training_available && (
                    <Link
                      className={styles.action}
                      to="/learn"
                      search={{ subject_id: selected.id, resume: 'training' }}
                    >
                      {t('profile.resumeTraining')}
                      <ArrowRight size={20} aria-hidden="true" />
                    </Link>
                  )}
                </li>
              )}
              {history.data?.map((item) => (
                <li className={styles.item} key={item.id}>
                  <MessagesSquare className={styles.icon} aria-hidden="true" />
                  <div>
                    <strong>{t('profile.exam')}</strong>
                    <span>
                      {new Date(item.startedAt * 1000).toLocaleDateString(
                        i18n.language,
                      )}
                    </span>
                  </div>
                  <div>
                    <strong>
                      {t('profile.subject', { name: selected.name })}
                    </strong>
                    <span>
                      {item.grade && item.scorePercent !== null
                        ? t('profile.grade', {
                            grade: item.grade,
                            score: item.scorePercent,
                          })
                        : t(`home.historyStatus.${item.status}`, {
                            progress: item.progressPercent,
                          })}
                    </span>
                  </div>
                  {item.status !== 'cancelled' && (
                    <Link
                      className={styles.action}
                      to="/exam"
                      search={{ subject_id: selected.id, session_id: item.id }}
                    >
                      {t(
                        item.status === 'active'
                          ? 'profile.resumeExam'
                          : 'profile.openResult',
                      )}
                      <ArrowRight size={20} aria-hidden="true" />
                    </Link>
                  )}
                </li>
              ))}
            </ul>
            {learning.isSuccess &&
              history.isSuccess &&
              !learning.data.active_session_id &&
              !learning.data.diagnostic_completed &&
              history.data.length === 0 && <Text>{t('profile.empty')}</Text>}
          </>
        )}
      </main>
    </div>
  )
}
