import {
  AlertDialog,
  Button,
  Flex,
  Heading,
  Table,
  Text,
} from '@radix-ui/themes'
import {
  Check,
  CheckCircle2,
  Clock,
  FileSpreadsheet,
  History,
  Layers,
  ListChecks,
  Shapes,
  Target,
  TriangleAlert,
  Upload,
} from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { CompetencyMapSummary } from '@/entities/learning-competency-map'
import { subjectQueryKeys as learningSubjectQueryKeys } from '@/entities/learning-subject'
import {
  CourseGraphApiError,
  type CourseGraphMapFile,
} from '@/entities/course-graph'
import {
  useCompetencyMapQuery,
  hasMultipleXlsxSheets,
} from '@/features/import-learning-competency-map'
import {
  useImportCompetencyMapMutation as useImportCourseMapMutation,
  useLatestCourseGraphBuildQuery,
} from '@/features/course-graph-build'
import { selectVisibleMapBuild } from '../model/select-visible-map-build'
import styles from './competency-map.module.scss'

// One card owns competency-map uploads for both modes: a map is one two-sheet
// book, the first sheet Geek's and the second the learning diagnostic's.
export function CompetencyMapImport({
  userId,
  subjectId,
  hasGraph,
  disabled,
  map,
}: {
  userId: string
  subjectId: string
  hasGraph: boolean
  disabled: boolean
  map: CourseGraphMapFile | null | undefined
}) {
  const { t } = useTranslation()
  const input = useRef<HTMLInputElement>(null)
  const submitRef = useRef<HTMLButtonElement>(null)
  const learningMap = useCompetencyMapQuery(userId, subjectId)
  const importCourseMap = useImportCourseMapMutation(userId, subjectId)
  const cache = useQueryClient()
  const graphBuild = useLatestCourseGraphBuildQuery(userId, subjectId)
  const [file, setFile] = useState<File | null>(null)
  const [checkingWorkbook, setCheckingWorkbook] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [confirmedRevision, setConfirmedRevision] = useState<number | null>(
    null,
  )
  const [revisionChanged, setRevisionChanged] = useState(false)
  const [pairedFingerprint, setPairedFingerprint] = useState<string | null>(
    null,
  )
  const [buildObserved, setBuildObserved] = useState(false)
  const previousBuildId = useRef<string | null>(null)
  const refreshedBuildId = useRef<string | null>(null)
  const [fileError, setFileError] = useState<string | null>(null)

  useEffect(() => {
    if (!file) {
      setCheckingWorkbook(false)
      return
    }
    let cancelled = false
    setCheckingWorkbook(true)
    const reject = () => {
      if (cancelled) return
      setFile(null)
      setFileError(t('learningCompetencyMap.pairedRequired'))
      setCheckingWorkbook(false)
    }
    void hasMultipleXlsxSheets(file)
      .then((paired) => {
        if (!paired) return reject()
        if (!cancelled) setCheckingWorkbook(false)
      })
      .catch(reject)
    return () => {
      cancelled = true
    }
  }, [file, t])

  useEffect(() => {
    const build = graphBuild.data
    if (!pairedFingerprint || build?.map?.fingerprint !== pairedFingerprint)
      return
    if (build.id === previousBuildId.current) return
    if (build.status === 'queued' || build.status === 'running') {
      setBuildObserved(true)
      return
    }
    if (build.status === 'succeeded' && buildObserved) {
      setPairedFingerprint(null)
      setBuildObserved(false)
      void learningMap.refetch()
    }
  }, [
    graphBuild.data?.map?.fingerprint,
    graphBuild.data?.status,
    pairedFingerprint,
    buildObserved,
    learningMap.refetch,
  ])

  useEffect(() => {
    const build = graphBuild.data
    if (
      build?.source !== 'competency_map' ||
      build.status !== 'succeeded' ||
      refreshedBuildId.current === build.id
    )
      return
    refreshedBuildId.current = build.id
    void learningMap.refetch()
  }, [graphBuild.data, learningMap.refetch])

  useEffect(() => {
    if (
      !importCourseMap.isSuccess &&
      confirmedRevision !== null &&
      learningMap.isSuccess &&
      learningMap.data.revision !== confirmedRevision
    ) {
      setConfirmedRevision(null)
      setConfirming(false)
      setRevisionChanged(true)
    }
  }, [
    confirmedRevision,
    importCourseMap.isSuccess,
    learningMap.isSuccess,
    learningMap.data,
  ])

  function selectFile(selected?: File) {
    importCourseMap.reset()
    setPairedFingerprint(null)
    setBuildObserved(false)
    setFile(null)
    setFileError(null)
    setConfirmedRevision(null)
    setRevisionChanged(false)
    if (!selected) return
    if (!/\.xlsx$/i.test(selected.name)) {
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

  // The import changes the learning side too: its map, catalog and state.
  // The server may have committed before a timeout or a conflict answer.
  function refreshLearning() {
    void learningMap.refetch()
    void cache.invalidateQueries({
      queryKey: learningSubjectQueryKeys.subjects(userId),
      exact: false,
    })
    void cache.invalidateQueries({
      queryKey: learningSubjectQueryKeys.learningState(userId, subjectId),
    })
  }

  function sendCourseMap(expectedLearningRevision: number) {
    if (!file) return
    previousBuildId.current = graphBuild.data?.id ?? null
    importCourseMap.mutate(
      { file, expectedLearningRevision },
      {
        onSettled: refreshLearning,
        onSuccess: (build) => {
          setPairedFingerprint(build.map?.fingerprint ?? null)
          setBuildObserved(
            build.status === 'queued' || build.status === 'running',
          )
          setFile(null)
          if (build.status === 'succeeded') {
            setPairedFingerprint(null)
          }
          void graphBuild.refetch()
        },
        onError: (error) => {
          if (
            error instanceof CourseGraphApiError &&
            [400, 413, 415, 422].includes(error.status)
          )
            setFile(null)
        },
      },
    )
  }

  const busy =
    disabled ||
    checkingWorkbook ||
    learningMap.isFetching ||
    importCourseMap.isPending ||
    (pairedFingerprint !== null &&
      graphBuild.data?.map?.fingerprint === pairedFingerprint &&
      graphBuild.data.status !== 'failed' &&
      graphBuild.data.status !== 'succeeded')

  function startImport() {
    if (!file || busy || !learningMap.data) return
    setRevisionChanged(false)
    const expectedRevision = learningMap.data.revision
    setConfirmedRevision(expectedRevision)
    if (learningMap.data.importedAt !== null || hasGraph) {
      setConfirming(true)
      return
    }
    sendCourseMap(expectedRevision)
  }

  function confirmImport() {
    if (!file || busy) return
    setConfirming(false)
    if (
      confirmedRevision !== null &&
      learningMap.data?.revision === confirmedRevision
    ) {
      sendCourseMap(confirmedRevision)
      return
    }
    setRevisionChanged(true)
  }

  const error = importCourseMap.error
  const details = error instanceof CourseGraphApiError ? error.details : []
  const warnings = groupWarnings([
    ...(importCourseMap.data?.learningWarnings ?? []).map((warning) => ({
      row: warning.row,
      columnIndex: warning.column_index,
      column: warning.column,
      code: warning.code,
    })),
  ])
  const buildError =
    pairedFingerprint &&
    graphBuild.data?.map?.fingerprint === pairedFingerprint &&
    graphBuild.data.status === 'failed'
      ? (graphBuild.data.error?.message ??
        t('learningCompetencyMap.unifiedBuildFailed'))
      : null

  const latestMapBuild =
    graphBuild.data?.source === 'competency_map' && graphBuild.data.map
      ? graphBuild.data
      : null
  const uploadedMapBuild =
    pairedFingerprint &&
    importCourseMap.data?.map?.fingerprint === pairedFingerprint
      ? importCourseMap.data
      : null
  const visibleMapBuild = selectVisibleMapBuild({
    latest: latestMapBuild,
    uploaded: uploadedMapBuild,
    pairedFingerprint,
    previousBuildId: previousBuildId.current,
  })
  const mapBuildPending =
    visibleMapBuild?.status === 'queued' ||
    visibleMapBuild?.status === 'running'
  const mapBuildFailed = visibleMapBuild?.status === 'failed'
  const mapBuildSucceeded = visibleMapBuild?.status === 'succeeded'
  const mapBuildFilename = visibleMapBuild?.map?.filename ?? ''
  const hasActiveMap =
    learningMap.data?.importedAt !== null && learningMap.data !== undefined
  const pairedLearningRevision =
    uploadedMapBuild?.map?.learningRevision ??
    latestMapBuild?.map?.learningRevision ??
    map?.learningRevision ??
    null
  const activeLearningVersionMatches =
    pairedLearningRevision !== null &&
    learningMap.data?.revision !== undefined &&
    learningMap.data.revision >= pairedLearningRevision
  const publishedGeekRevision =
    visibleMapBuild !== null
      ? (visibleMapBuild.graphMapRevision ?? null)
      : (graphBuild.data?.graphMapRevision ?? null)

  return (
    <>
      <section className={styles.card} aria-labelledby="learning-map-state">
        <Heading as="h3" size="4" id="learning-map-state">
          {t('learningCompetencyMap.currentMap')}
        </Heading>
        {learningMap.isPending ? (
          <Text as="p" role="status" color="gray" size="2">
            {t('learningCompetencyMap.loading')}
          </Text>
        ) : learningMap.isError ? (
          <Flex gap="3" align="center" wrap="wrap">
            <Text role="alert" color="red" size="2">
              {t('learningCompetencyMap.loadError')}
            </Text>
            <Button
              variant="ghost"
              size="1"
              onClick={() => void learningMap.refetch()}
            >
              {t('trainer.retry')}
            </Button>
          </Flex>
        ) : pairedLearningRevision !== null && !activeLearningVersionMatches ? (
          <Text as="p" role="status" color="gray" size="2">
            {t('learningCompetencyMap.learningVersionUpdating', {
              revision: pairedLearningRevision,
            })}
          </Text>
        ) : learningMap.data.importedAt === null ? (
          !mapBuildPending && !mapBuildFailed && !mapBuildSucceeded ? (
            <Text as="p" color="gray" size="2">
              {t('learningCompetencyMap.empty')}
            </Text>
          ) : null
        ) : (
          <>
            <Text as="p" role="status" className={styles.success}>
              <CheckCircle2 size={20} aria-hidden="true" />
              {t('learningCompetencyMap.success')}
            </Text>
            <MapSummary summary={learningMap.data} />
          </>
        )}
        {mapBuildPending && (
          <>
            <Text as="p" role="status" size="2">
              {t('learningCompetencyMap.pendingMap', {
                filename: mapBuildFilename,
              })}
            </Text>
            <Text as="p" color="gray" size="2">
              {t(
                publishedGeekRevision !== null
                  ? 'learningCompetencyMap.pendingMapPreviousActive'
                  : 'learningCompetencyMap.pendingMapHelp',
              )}
            </Text>
          </>
        )}
        {mapBuildSucceeded && !hasActiveMap && (
          <Text as="p" role="status" size="2">
            {t('learningCompetencyMap.publishingMap', {
              revision: pairedLearningRevision,
            })}
          </Text>
        )}
        {mapBuildFailed && (
          <>
            <Text as="p" role="alert" color="red" size="2">
              {t('learningCompetencyMap.failedMap', {
                filename: mapBuildFilename,
                message: visibleMapBuild.error?.message ?? '',
              })}
            </Text>
            {hasActiveMap && (
              <Text as="p" color="gray" size="2">
                {t('learningCompetencyMap.failedMapPreviousActive')}
              </Text>
            )}
          </>
        )}
        {pairedLearningRevision !== null && (
          <>
            {activeLearningVersionMatches ? (
              <Text as="p" role="status" size="2">
                {t('learningCompetencyMap.learningVersionReady', {
                  revision:
                    learningMap.data?.revision ?? pairedLearningRevision,
                })}
              </Text>
            ) : null}
            <Text as="p" role="status" size="2">
              {publishedGeekRevision === pairedLearningRevision
                ? t('learningCompetencyMap.geekVersionReady', {
                    revision: pairedLearningRevision,
                  })
                : mapBuildFailed
                  ? publishedGeekRevision !== null
                    ? t('learningCompetencyMap.geekVersionFailedWithPrevious', {
                        publishedRevision: publishedGeekRevision,
                        revision: pairedLearningRevision,
                      })
                    : t('learningCompetencyMap.geekVersionFailed', {
                        revision: pairedLearningRevision,
                      })
                  : publishedGeekRevision !== null
                    ? t(
                        'learningCompetencyMap.geekVersionPreparingWithPrevious',
                        {
                          publishedRevision: publishedGeekRevision,
                          revision: pairedLearningRevision,
                        },
                      )
                    : t('learningCompetencyMap.geekVersionPreparing', {
                        revision: pairedLearningRevision,
                      })}
            </Text>
          </>
        )}
      </section>

      <section
        className={styles.card}
        aria-labelledby="competency-map-import-title"
      >
        <div className={styles.sectionHeading}>
          <FileSpreadsheet aria-hidden="true" />
          <Heading as="h3" size="4" id="competency-map-import-title">
            {t('learningCompetencyMap.edit')}
          </Heading>
        </div>
        <Text as="p" color="gray" size="2">
          {t('courseGraph.mapImportHint')}
        </Text>
        {map && (
          <Text as="p" size="2">
            {t('courseGraph.mapImported', {
              filename: map.filename,
              sheet: map.sheet,
            })}
          </Text>
        )}
        <input
          ref={input}
          type="file"
          accept=".xlsx,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
          hidden
          aria-label={t('learningCompetencyMap.file')}
          onChange={(event) => {
            selectFile(event.target.files?.[0])
            event.target.value = ''
          }}
        />
        <Flex gap="3" align="center" wrap="wrap">
          <Button
            className={styles.mapImportButton}
            size="3"
            variant="soft"
            disabled={busy || learningMap.isError}
            onClick={() => input.current?.click()}
          >
            <Upload size={17} aria-hidden="true" />
            {file ? file.name : t('courseGraph.mapImport')}
          </Button>
          {file && (
            <Button
              ref={submitRef}
              size="3"
              color="green"
              disabled={busy || !learningMap.data}
              onClick={startImport}
            >
              <Check size={18} aria-hidden="true" />
              {t('learningCompetencyMap.submitUnified')}
            </Button>
          )}
        </Flex>
        {checkingWorkbook && (
          <Text as="p" role="status" size="2">
            {t('learningCompetencyMap.checkingWorkbook')}
          </Text>
        )}
        {fileError && (
          <Text as="p" role="alert" color="red" size="2">
            {fileError}
          </Text>
        )}
        {revisionChanged && (
          <Text as="p" role="alert" color="red" size="2">
            {t('learningCompetencyMap.revisionChanged')}
          </Text>
        )}
        {file && !busy && (
          <Button
            size="2"
            variant="soft"
            color="gray"
            onClick={() => selectFile(undefined)}
          >
            {t('learningCompetencyMap.cancelEdit')}
          </Button>
        )}
        {importCourseMap.isPending ? (
          <Text as="p" role="status" size="2">
            {t('learningCompetencyMap.uploading')}
          </Text>
        ) : null}
        {pairedFingerprint && !buildError && (
          <Text as="p" role="status" size="2">
            {t('learningCompetencyMap.preparingUnified')}
          </Text>
        )}
        {buildError && (
          <Text as="p" role="alert" color="red" size="2">
            {buildError}
          </Text>
        )}
        {error && (
          <div className={styles.error} role="alert">
            <Text as="p">{error.message}</Text>
            {details.length > 0 && (
              <ul>
                {details.map((detail, index) => (
                  <li key={`${detail.path ?? ''}:${index}`}>
                    {(() => {
                      const location = /^(?:sheet:(\d+)\/)?row:(\d+)$/.exec(
                        detail.path ?? '',
                      )
                      return location
                        ? t('learningCompetencyMap.rowError', {
                            row: location[1]
                              ? `лист ${location[1]}, ${location[2]}`
                              : location[2],
                            message: detail.message,
                          })
                        : detail.message
                    })()}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        <AlertDialog.Root
          open={confirming}
          onOpenChange={(open) => {
            if (!open) setConfirming(false)
          }}
        >
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
              {t('learningCompetencyMap.confirmHelpUnified', {
                filename: file?.name ?? '',
              })}
            </AlertDialog.Description>
            <Flex gap="3" mt="4" justify="end">
              <AlertDialog.Cancel>
                <Button variant="soft" color="gray">
                  {t('learningCompetencyMap.cancel')}
                </Button>
              </AlertDialog.Cancel>
              <AlertDialog.Action>
                <Button
                  color="red"
                  disabled={
                    busy ||
                    confirmedRevision === null ||
                    learningMap.data?.revision !== confirmedRevision
                  }
                  onClick={confirmImport}
                >
                  {t('learningCompetencyMap.confirm')}
                </Button>
              </AlertDialog.Action>
            </Flex>
          </AlertDialog.Content>
        </AlertDialog.Root>
      </section>
      {warnings.length > 0 && (
        <section
          className={styles.card}
          aria-labelledby="import-warnings-title"
        >
          <div className={styles.sectionHeading} data-tone="warning">
            <TriangleAlert aria-hidden="true" />
            <Heading as="h3" size="4" id="import-warnings-title">
              {t('learningCompetencyMap.warnings')}
            </Heading>
          </div>
          {warnings.length > 0 && (
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
                  {warnings.map((warning) => (
                    <Table.Row key={warning.row}>
                      <Table.RowHeaderCell>{warning.row}</Table.RowHeaderCell>
                      <Table.Cell>
                        {warning.reasons.map((reason) => (
                          <Text
                            as="p"
                            size="2"
                            key={reason.code}
                            className={styles.warningReason}
                          >
                            <strong>{reason.columns.join(', ')}:</strong>{' '}
                            {t(`learningCompetencyMap.warning.${reason.code}`, {
                              defaultValue: t(
                                'learningCompetencyMap.warning.unknown',
                                { code: reason.code },
                              ),
                            })}
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
    </>
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

function groupWarnings(
  warnings: {
    row: number
    columnIndex: number
    column: string
    code: string
  }[],
) {
  const grouped = new Map<
    number,
    { row: number; reasons: Map<string, { code: string; columns: string[] }> }
  >()
  for (const warning of warnings) {
    const group = grouped.get(warning.row) ?? {
      row: warning.row,
      reasons: new Map(),
    }
    const reason = group.reasons.get(warning.code) ?? {
      code: warning.code,
      columns: [],
    }
    if (!reason.columns.includes(warning.column))
      reason.columns.push(warning.column)
    group.reasons.set(warning.code, reason)
    grouped.set(warning.row, group)
  }
  return [...grouped.values()].map((group) => ({
    row: group.row,
    reasons: [...group.reasons.values()],
  }))
}
