import {
  AlertDialog,
  Button,
  Flex,
  Heading,
  Table,
  Text,
} from '@radix-ui/themes'
import { useNavigate } from '@tanstack/react-router'
import {
  Check,
  CheckCircle2,
  FileSpreadsheet,
  Clock,
  History,
  Layers,
  ListChecks,
  Shapes,
  Target,
  TriangleAlert,
  Upload,
  X,
} from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CompetencyMapApiError } from '@/entities/learning-competency-map'
import { useAuth } from '@/features/auth'
import {
  useCompetencyMapQuery,
  useImportCompetencyMapMutation,
} from '@/features/import-learning-competency-map'
import type { CompetencyMapSummary } from '@/entities/learning-competency-map'
import { useSubjectsQuery } from '@/features/learning-subject-selection'
import styles from '@/pages/learning-competency-map/ui/competency-map.module.scss'
import { groupWarnings } from '../model/import-warnings'
import { AdminCourseDetailShell } from '@/widgets/admin-course-detail-shell'

export function CompetencyMapAdminScreen({ subjectId }: { subjectId: string }) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const navigate = useNavigate()
  if (user?.role !== 'admin')
    return (
      <main className={styles.denied}>
        <Heading as="h1">{t('learningCompetencyMap.title')}</Heading>
        <Text as="p" role="alert">
          {t('learningCompetencyMap.forbidden')}
        </Text>
        <Button onClick={() => void navigate({ to: '/' })}>
          {t('learningCompetencyMap.toLesson')}
        </Button>
      </main>
    )
  return <CompetencyMapAdmin userId={user.id} selectedSubjectId={subjectId} />
}

function CompetencyMapAdmin({
  userId,
  selectedSubjectId,
}: {
  userId: string
  selectedSubjectId: string
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const subjects = useSubjectsQuery(userId, true)
  const subject = subjects.data?.find(
    (candidate) => candidate.id === selectedSubjectId,
  )
  const subjectId = selectedSubjectId
  const query = useCompetencyMapQuery(userId, subjectId)
  const upload = useImportCompetencyMapMutation(userId, subjectId)
  const refetchSubjects = subjects.refetch
  const missingMapCourse =
    subject !== undefined &&
    query.error instanceof CompetencyMapApiError &&
    query.error.status === 404

  useEffect(() => {
    if (!missingMapCourse) return
    void refetchSubjects().then((result) => {
      if (
        result.isSuccess &&
        !result.isError &&
        !result.data.some((candidate) => candidate.id === selectedSubjectId)
      )
        void navigate({ to: '/', replace: true })
    })
  }, [missingMapCourse, navigate, refetchSubjects, selectedSubjectId])
  const [confirmedRevision, setConfirmedRevision] = useState<number | null>(
    null,
  )
  const [revisionChanged, setRevisionChanged] = useState(false)
  useEffect(() => {
    if (
      confirmedRevision !== null &&
      query.isSuccess &&
      query.data.revision !== confirmedRevision
    ) {
      setConfirmedRevision(null)
      setRevisionChanged(true)
    }
  }, [confirmedRevision, query.isSuccess, query.data])
  const submitRef = useRef<HTMLButtonElement>(null)
  const [file, setFile] = useState<File | null>(null)
  const [fileError, setFileError] = useState<string | null>(null)
  // A file the server rejected (format or size) is dropped, so there is
  // nothing left to confirm; technical failures keep it for a retry.
  const settleFile = {
    onSuccess: () => setFile(null),
    onError: (error: Error) => {
      if (
        error instanceof CompetencyMapApiError &&
        [400, 413, 415, 422].includes(error.status)
      )
        setFile(null)
    },
  }

  function selectFile(selected: File | undefined) {
    upload.reset()
    setRevisionChanged(false)
    setFile(null)
    setFileError(null)
    if (!selected) return
    if (!/\.(csv|xlsx)$/i.test(selected.name)) {
      setFileError(t('learningCompetencyMap.invalidType'))
      return
    }
    if (selected.size > 25 * 1024 * 1024) {
      setFileError(t('learningCompetencyMap.tooLarge'))
      return
    }
    if (selected.size === 0) {
      setFileError(t('learningCompetencyMap.emptyFile'))
      return
    }
    setFile(selected)
  }

  function submitMap(expectedRevision: number) {
    if (file) upload.mutate({ file, subjectId, expectedRevision }, settleFile)
  }

  const busy = upload.isPending
  const mapUnavailable = busy || query.isFetching || !query.isSuccess
  const warningGroups = groupWarnings(upload.data?.warnings ?? [])
  const error = upload.error
  return (
    <AdminCourseDetailShell
      active="learn"
      subjectId={subjectId}
      subjectName={subject?.name}
      courseMissing={subjects.isSuccess && !subjects.isError && !subject}
      course={subject}
      localImporting={upload.isPending}
      statusRefreshError={subjects.isRefetchError}
      onRetryStatus={() => void subjects.refetch()}
      title={t('learningCompetencyMap.title')}
      description={`${t('learningCompetencyMap.importTitle')}. ${t('learningCompetencyMap.editSubtitle')}`}
    >
      {subjects.isPending ? (
        <Text as="p" role="status">
          {t('learningCompetencyMap.subjectsLoading')}
        </Text>
      ) : subjects.isError && !subjects.data ? (
        <div className={styles.card}>
          <Text as="p" role="alert">
            {t('learningCompetencyMap.subjectsError')}
          </Text>
          <Button variant="soft" onClick={() => void subjects.refetch()}>
            {t('learningTrainer.retry')}
          </Button>
        </div>
      ) : !subject ? null : (
        <>
          <section className={styles.card} aria-labelledby="learning-map-state">
            <Heading as="h3" size="4" id="learning-map-state">
              {t('learningCompetencyMap.currentMap')}
            </Heading>
            {query.isPending ? (
              <Text as="p" role="status" color="gray" size="2">
                {t('learningCompetencyMap.loading')}
              </Text>
            ) : query.isError ? (
              <Flex gap="3" align="center" wrap="wrap">
                <Text as="p" role="alert" color="red" size="2">
                  {t('learningCompetencyMap.loadError')}
                </Text>
                <Button
                  variant="ghost"
                  size="1"
                  onClick={() => void query.refetch()}
                >
                  {t('learningTrainer.retry')}
                </Button>
              </Flex>
            ) : query.data.importedAt === null ? (
              <Text as="p" color="gray" size="2">
                {t('learningCompetencyMap.empty')}
              </Text>
            ) : (
              <>
                <Text as="p" role="status" className={styles.success}>
                  <CheckCircle2 size={20} aria-hidden="true" />
                  {t('learningCompetencyMap.success')}
                </Text>
                <MapSummary summary={query.data} />
              </>
            )}
          </section>
          <section className={styles.card} aria-labelledby="subject-form-title">
            <Heading as="h2" size="4" id="subject-form-title">
              {t('learningCompetencyMap.edit')}
            </Heading>
            <MapFileZone file={file} disabled={busy} onSelect={selectFile} />
            {revisionChanged && (
              <Text as="p" role="alert" color="red" size="2">
                {t('learningCompetencyMap.revisionChanged')}
              </Text>
            )}
            {fileError && (
              <Text as="p" role="alert" color="red" size="2">
                {fileError}
              </Text>
            )}
            {busy && (
              <Text as="p" role="status" size="2">
                {t('learningCompetencyMap.uploading')}
              </Text>
            )}
            {error && (
              <div role="alert" className={styles.error}>
                <Text as="p">
                  {error instanceof CompetencyMapApiError
                    ? error.message
                    : t('learningCompetencyMap.unknownResult')}
                </Text>
                {error instanceof CompetencyMapApiError &&
                  error.details.length > 0 && (
                    <ul>
                      {error.details.map((detail, index) => (
                        <li key={index}>
                          {detail.path?.startsWith('row:')
                            ? t('learningCompetencyMap.rowError', {
                                row: detail.path.slice(4),
                                message: detail.message,
                              })
                            : detail.message}
                        </li>
                      ))}
                    </ul>
                  )}
              </div>
            )}
            {file && (
              <div className={styles.actions}>
                <AlertDialog.Root
                  open={confirmedRevision !== null}
                  onOpenChange={(open) => {
                    if (!open) setConfirmedRevision(null)
                  }}
                >
                  <Button
                    ref={submitRef}
                    size="3"
                    color="green"
                    className={styles.submit}
                    disabled={mapUnavailable}
                    onClick={() => {
                      if (!query.data) return
                      setRevisionChanged(false)
                      if (query.data.importedAt !== null) {
                        setConfirmedRevision(query.data.revision)
                      } else submitMap(query.data.revision)
                    }}
                  >
                    <Check size={18} aria-hidden="true" />
                    {t('learningCompetencyMap.submitEdit')}
                  </Button>
                  <AlertDialog.Content
                    maxWidth="440px"
                    onCloseAutoFocus={(event) => {
                      event.preventDefault()
                      submitRef.current?.focus()
                    }}
                  >
                    <AlertDialog.Title>
                      {t('learningCompetencyMap.confirmTitle')}
                    </AlertDialog.Title>
                    <AlertDialog.Description>
                      {t('learningCompetencyMap.confirmHelp')}
                    </AlertDialog.Description>
                    <Flex gap="3" mt="5" justify="end" wrap="wrap">
                      <AlertDialog.Cancel>
                        <Button variant="soft" color="gray">
                          {t('learningCompetencyMap.cancel')}
                        </Button>
                      </AlertDialog.Cancel>
                      <AlertDialog.Action>
                        <Button
                          color="red"
                          disabled={
                            mapUnavailable ||
                            query.data?.revision !== confirmedRevision
                          }
                          onClick={() => {
                            if (
                              file &&
                              !mapUnavailable &&
                              confirmedRevision !== null &&
                              query.data?.revision === confirmedRevision
                            )
                              submitMap(confirmedRevision)
                          }}
                        >
                          {t('learningCompetencyMap.confirm')}
                        </Button>
                      </AlertDialog.Action>
                    </Flex>
                  </AlertDialog.Content>
                </AlertDialog.Root>
                {file && !busy && (
                  <Button
                    size="3"
                    color="red"
                    variant="soft"
                    className={styles.submit}
                    onClick={() => selectFile(undefined)}
                  >
                    <X size={18} aria-hidden="true" />
                    {t('learningCompetencyMap.cancelEdit')}
                  </Button>
                )}
              </div>
            )}
          </section>
        </>
      )}
      {upload.data &&
        upload.variables?.subjectId === subjectId &&
        (upload.data.unparsedTaskCellCount > 0 ||
          upload.data.warnings.length > 0) && (
          <section
            className={styles.card}
            aria-labelledby="import-warnings-title"
          >
            <div className={styles.sectionHeading} data-tone="warning">
              <TriangleAlert aria-hidden="true" />
              <Heading as="h2" size="4" id="import-warnings-title">
                {t('learningCompetencyMap.warnings')}
              </Heading>
            </div>
            {upload.data.unparsedTaskCellCount > 0 && (
              <Text as="p" color="gray" size="2">
                {t('learningCompetencyMap.unparsed', {
                  count: upload.data.unparsedTaskCellCount,
                  rows: warningGroups.length,
                })}
              </Text>
            )}
            {warningGroups.length > 0 && (
              <div className={styles.tableScroll}>
                <Table.Root variant="surface">
                  <Table.Header>
                    <Table.Row>
                      <Table.ColumnHeaderCell>
                        {t('learningCompetencyMap.row')}
                      </Table.ColumnHeaderCell>
                      <Table.ColumnHeaderCell>
                        {t('learningCompetencyMap.reason')}
                      </Table.ColumnHeaderCell>
                    </Table.Row>
                  </Table.Header>
                  <Table.Body>
                    {warningGroups.map((group) => (
                      <Table.Row key={group.row}>
                        <Table.RowHeaderCell>{group.row}</Table.RowHeaderCell>
                        <Table.Cell>
                          {group.reasons.map((reason) => (
                            <Text
                              as="p"
                              size="2"
                              key={reason.code}
                              className={styles.warningReason}
                            >
                              <strong>{reason.columns.join(', ')}:</strong>{' '}
                              {t(
                                `learningCompetencyMap.warning.${reason.code}`,
                                {
                                  defaultValue: t(
                                    'learningCompetencyMap.warning.unknown',
                                    { code: reason.code },
                                  ),
                                },
                              )}
                            </Text>
                          ))}
                        </Table.Cell>
                      </Table.Row>
                    ))}
                  </Table.Body>
                </Table.Root>
              </div>
            )}
          </section>
        )}
    </AdminCourseDetailShell>
  )
}

const summaryRows = [
  { key: 'competencyCount', icon: Layers, tone: 'blue' },
  { key: 'constituentCount', icon: Shapes, tone: 'violet' },
  { key: 'outcomeCount', icon: Target, tone: 'green' },
  { key: 'taskCount', icon: ListChecks, tone: 'amber' },
] as const

function MapSummary({ summary }: { summary: CompetencyMapSummary }) {
  const { t, i18n } = useTranslation()
  return (
    <dl className={styles.stats}>
      {summaryRows.map(({ key, icon: Icon, tone }) => (
        <div key={key} className={styles.statRow} data-tone={tone}>
          <span className={styles.statIcon} aria-hidden="true">
            <Icon size={16} />
          </span>
          <dt>{t(`learningCompetencyMap.stats.${key}`)}</dt>
          <dd>{summary[key].toLocaleString(i18n.language)}</dd>
        </div>
      ))}
      {summary.importedAt !== null && (
        <div className={styles.statRow} data-tone="gray">
          <span className={styles.statIcon} aria-hidden="true">
            <Clock size={16} />
          </span>
          <dt>{t('learningCompetencyMap.stats.importedAt')}</dt>
          <dd>
            {/* Date and time without the locale's separating comma. */}
            {new Date(summary.importedAt * 1000).toLocaleDateString(
              i18n.language,
              { dateStyle: 'medium' },
            )}{' '}
            {new Date(summary.importedAt * 1000).toLocaleTimeString(
              i18n.language,
              { timeStyle: 'short' },
            )}
          </dd>
        </div>
      )}
      <div className={styles.statRow} data-tone="gray">
        <span className={styles.statIcon} aria-hidden="true">
          <History size={16} />
        </span>
        <dt>{t('learningCompetencyMap.stats.revision')}</dt>
        <dd>{summary.revision}</dd>
      </div>
    </dl>
  )
}

function MapFileZone({
  file,
  disabled,
  onSelect,
}: {
  file: File | null
  disabled: boolean
  onSelect: (file: File | undefined) => void
}) {
  const { t, i18n } = useTranslation()
  const [dragging, setDragging] = useState(false)
  return (
    <label
      className={styles.dropzone}
      data-dragging={dragging || undefined}
      data-selected={file ? true : undefined}
      data-disabled={disabled || undefined}
      onDragOver={(event) => {
        event.preventDefault()
        if (!disabled) setDragging(true)
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={(event) => {
        event.preventDefault()
        setDragging(false)
        if (!disabled) onSelect(event.dataTransfer.files[0])
      }}
    >
      <input
        type="file"
        className={styles.fileInput}
        aria-label={t('learningCompetencyMap.file')}
        accept=".csv,.xlsx,text/csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
        disabled={disabled}
        onChange={(event) => {
          onSelect(event.target.files?.[0])
          // Allow choosing the same file again after an error.
          event.target.value = ''
        }}
      />
      <span className={styles.dropzoneIcon} aria-hidden="true">
        {file ? <FileSpreadsheet size={22} /> : <Upload size={22} />}
      </span>
      <span className={styles.dropzoneText}>
        <Text weight="bold">
          {file ? file.name : t('learningCompetencyMap.dropTitle')}
        </Text>
        <Text size="2" color="gray">
          {file
            ? `${t('learningCompetencyMap.fileSize', {
                size: (file.size / (1024 * 1024)).toLocaleString(
                  i18n.language,
                  { maximumFractionDigits: 2 },
                ),
              })} · ${t('learningCompetencyMap.dropReplace')}`
            : t('learningCompetencyMap.dropHint')}
        </Text>
      </span>
    </label>
  )
}
