import type { PropsWithChildren } from 'react'
import { Badge, Button, Heading, Text } from '@radix-ui/themes'
import { Link } from '@tanstack/react-router'
import { useNavigate } from '@tanstack/react-router'
import { LoaderCircle, ShieldCheck } from 'lucide-react'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { AccountMenu, useAuth } from '@/features/auth'
import type { Subject } from '@/entities/subject'
import { preparationBadge } from '@/entities/subject'
import styles from './admin-course-detail-shell.module.scss'
import { AudioPreparationPanel } from './audio-preparation-panel'

type Direction = 'learn' | 'exam'

type Props = PropsWithChildren<{
  active: Direction
  subjectId: string
  subjectName?: string
  courseMissing?: boolean
  course?: Subject
  showBothStatuses?: boolean
  localImporting?: boolean
  statusRefreshError?: boolean
  onRetryStatus?: () => void
  title: string
  description: string
}>

export function AdminCourseDetailShell({
  active,
  subjectId,
  subjectName,
  courseMissing = false,
  course,
  showBothStatuses = false,
  localImporting = false,
  statusRefreshError = false,
  onRetryStatus,
  title,
  description,
  children,
}: Props) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const navigate = useNavigate()
  const directions: Direction[] = showBothStatuses
    ? ['learn', 'exam']
    : [active]
  const hasStatuses = course !== undefined

  useEffect(() => {
    if (courseMissing) void navigate({ to: '/', replace: true })
  }, [courseMissing, navigate])

  if (courseMissing) return null

  return (
    <div className={styles.page} data-admin-screen>
      <header className={styles.header}>
        <div className={styles.brand}>
          <ShieldCheck size={24} aria-hidden="true" />
          <Text weight="bold">{t('adminCourses.admin')}</Text>
        </div>
        <AccountMenu canLogout={false} />
      </header>
      <main className={styles.content}>
        <section className={styles.courseHeader} aria-labelledby="course-name">
          <div className={styles.courseIdentity}>
            <Text as="p" size="2" weight="medium" color="gray">
              {t('adminCourses.courseLabel')}
            </Text>
            <Heading as="h1" size="7" id="course-name">
              {subjectName ?? t('adminCourses.courseLoading')}
            </Heading>
          </div>
          <div className={styles.courseNavigation}>
            <Button asChild variant="soft">
              <Link to="/admin/courses" activeOptions={{ exact: true }}>
                {t('adminCourses.backToCourses')}
              </Link>
            </Button>
          </div>
        </section>
        {statusRefreshError && (
          <section className={styles.statusError}>
            <Text as="p" role="status">
              {t('adminCourses.statusRefreshError')}
            </Text>
            <Button variant="soft" onClick={onRetryStatus}>
              {t('trainer.retry')}
            </Button>
          </section>
        )}
        <section className={styles.directionHeader}>
          <div className={styles.directionIntro}>
            <Heading as="h2" size="6">
              {title}
            </Heading>
            <Text as="p" color="gray">
              {description}
            </Text>
          </div>
          {hasStatuses && (
            <div className={styles.statuses}>
              {directions.map((direction) => {
                const status = course
                  ? preparationBadge(course, direction)
                  : undefined
                if (!status) return null
                return (
                  <Badge
                    key={direction}
                    color={status.color}
                    className={styles.status}
                  >
                    {(status.spinning ||
                      (direction === 'learn' && localImporting)) && (
                      <LoaderCircle
                        size={12}
                        className={styles.spinner}
                        aria-hidden="true"
                      />
                    )}
                    {t(
                      direction === 'learn' && localImporting
                        ? 'sharedCourse.learn.importing'
                        : status.key,
                    )}
                  </Badge>
                )
              })}
            </div>
          )}
        </section>
        {user?.role === 'admin' && (
          <AudioPreparationPanel
            userId={user.id}
            subjectId={subjectId}
            direction={active}
            stale={course?.preparation?.exam.stale}
          />
        )}
        {children}
      </main>
    </div>
  )
}
