import {
  AlertDialog,
  Button,
  Flex,
  Heading,
  RadioGroup,
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
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  CompetencyMapApiError,
  type CompetencyMapSummary,
} from '@/entities/learning-competency-map'
import {
  CourseGraphApiError,
  type CourseGraphMapFile,
} from '@/entities/course-graph'
import {
  useCompetencyMapQuery,
  hasMultipleXlsxSheets,
  useImportCompetencyMapMutation as useImportLearningMapMutation,
} from '@/features/import-learning-competency-map'
import {
  useImportCompetencyMapMutation as useImportCourseMapMutation,
  useLatestCourseGraphBuildQuery,
} from '@/features/course-graph-build'
import styles from './competency-map.module.scss'

type ImportMode = 'learn' | 'exam' | 'both'

// One card owns competency-map uploads for both modes. Two-sheet books are
// always imported together; old CSV and one-sheet paths stay available.
export function CompetencyMapImport({
  userId,
  subjectId,
  hasGraph,
  disabled,
  map,
  legacyDirection,
}: {
  userId: string
  subjectId: string
  hasGraph: boolean
  disabled: boolean
  map: CourseGraphMapFile | null | undefined
  legacyDirection: 'learn' | 'exam'
}) {
  const { t } = useTranslation()
  const input = useRef<HTMLInputElement>(null)
  const submitRef = useRef<HTMLButtonElement>(null)
  const learningMap = useCompetencyMapQuery(userId, subjectId)
  const importLearningMap = useImportLearningMapMutation(userId, subjectId)
  const importCourseMap = useImportCourseMapMutation(userId, subjectId)
  const graphBuild = useLatestCourseGraphBuildQuery(userId, subjectId)
  const [file, setFile] = useState<File | null>(null)
  const [sheetCount, setSheetCount] = useState(0)
  const [checkingWorkbook, setCheckingWorkbook] = useState(false)
  const [mode, setMode] = useState<ImportMode>(legacyDirection)
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
    if (!file || !/\.xlsx$/i.test(file.name)) {
      setSheetCount(0)
      setCheckingWorkbook(false)
      return
    }
    let cancelled = false
    setCheckingWorkbook(true)
    void hasMultipleXlsxSheets(file)
      .then((paired) => {
        if (cancelled) return
        setSheetCount(paired ? 2 : 1)
        const nextMode = paired ? 'both' : legacyDirection
        setMode(nextMode)
        setCheckingWorkbook(false)
      })
      .catch(() => {
        if (cancelled) return
        setSheetCount(1)
        setMode(legacyDirection)
        setCheckingWorkbook(false)
      })
    return () => {
      cancelled = true
    }
  }, [file, legacyDirection])

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
      !importLearningMap.isSuccess &&
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
    importLearningMap.isSuccess,
    learningMap.isSuccess,
    learningMap.data,
  ])

  function selectFile(selected?: File) {
    importLearningMap.reset()
    importCourseMap.reset()
    setPairedFingerprint(null)
    setBuildObserved(false)
    setSheetCount(0)
    setFile(null)
    setFileError(null)
    setConfirmedRevision(null)
    setRevisionChanged(false)
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
    setMode(
      selected.name.toLowerCase().endsWith('.csv') ? 'learn' : legacyDirection,
    )
    setFile(selected)
  }

  function sendLearningMap(expectedRevision: number) {
    if (!file) return
    importLearningMap.mutate(
      { file, subjectId, expectedRevision },
      {
        onSuccess: () => setFile(null),
        onError: (error) => {
          if (
            error instanceof CompetencyMapApiError &&
            [400, 413, 415, 422].includes(error.status)
          )
            setFile(null)
        },
      },
    )
  }

  function sendCourseMap(expectedLearningRevision: number) {
    if (!file) return
    previousBuildId.current = graphBuild.data?.id ?? null
    importCourseMap.mutate(
      { file, expectedLearningRevision },
      {
        onSuccess: (build) => {
          setPairedFingerprint(build.map?.fingerprint ?? null)
          setBuildObserved(
            build.status === 'queued' || build.status === 'running',
          )
          setFile(null)
          void learningMap.refetch()
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
    importLearningMap.isPending ||
    importCourseMap.isPending ||
    (pairedFingerprint !== null &&
      graphBuild.data?.map?.fingerprint === pairedFingerprint &&
      graphBuild.data.status !== 'failed' &&
      graphBuild.data.status !== 'succeeded')

  function startImport() {
    if (!file || busy || !learningMap.data) return
    setRevisionChanged(false)
    if (mode === 'both') {
      const expectedRevision = learningMap.data.revision
      setConfirmedRevision(expectedRevision)
      if (learningMap.data.importedAt !== null || hasGraph) {
        setConfirming(true)
        return
      }
      sendCourseMap(expectedRevision)
      return
    }
    if (mode === 'exam') {
      setConfirmedRevision(learningMap.data.revision)
      setConfirming(true)
      return
    }
    if (learningMap.data.importedAt !== null) {
      setConfirmedRevision(learningMap.data.revision)
      setConfirming(true)
      return
    }
    sendLearningMap(learningMap.data.revision)
  }

  function confirmImport() {
    if (!file || busy) return
    setConfirming(false)
    if (mode === 'learn') {
      if (
        confirmedRevision !== null &&
        learningMap.data?.revision === confirmedRevision
      )
        sendLearningMap(confirmedRevision)
      return
    }
    if (
      confirmedRevision !== null &&
      learningMap.data?.revision === confirmedRevision
    ) {
      sendCourseMap(confirmedRevision)
      return
    }
    setRevisionChanged(true)
  }

  const error = importLearningMap.error ?? importCourseMap.error
  const details =
    error instanceof CompetencyMapApiError ||
    error instanceof CourseGraphApiError
      ? error.details
      : []
  const warnings = groupWarnings([
    ...(importLearningMap.data?.warnings ?? []),
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
  const latestBuildIsNewImport =
    latestMapBuild !== null && latestMapBuild.id !== previousBuildId.current
  const visibleMapBuild =
    uploadedMapBuild ??
    (latestBuildIsNewImport ? latestMapBuild : null) ??
    (pairedFingerprint === null ? latestMapBuild : null)
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
          accept=".csv,.xlsx,text/csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
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
              {mode === 'both'
                ? t('learningCompetencyMap.submitUnified')
                : mode === 'exam'
                  ? t('courseGraph.mapImportStart')
                  : t('learningCompetencyMap.submitEdit')}
            </Button>
          )}
        </Flex>
        {checkingWorkbook && (
          <Text as="p" role="status" size="2">
            {t('learningCompetencyMap.checkingWorkbook')}
          </Text>
        )}
        {file &&
          sheetCount === 1 &&
          file.name.toLowerCase().endsWith('.xlsx') && (
            <fieldset className={styles.mapMode} disabled={busy}>
              <legend>{t('learningCompetencyMap.singleSheetMode')}</legend>
              <RadioGroup.Root
                value={mode}
                onValueChange={(value) => setMode(value as ImportMode)}
                aria-label={t('learningCompetencyMap.singleSheetMode')}
              >
                <RadioGroup.Item value="learn">
                  {t('learningCompetencyMap.modeLearning')}
                </RadioGroup.Item>
                <RadioGroup.Item value="exam">
                  {t('learningCompetencyMap.modeGeek')}
                </RadioGroup.Item>
              </RadioGroup.Root>
            </fieldset>
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
        {importLearningMap.isPending || importCourseMap.isPending ? (
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
                    {detail.message}
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
              {t(
                mode === 'both'
                  ? 'learningCompetencyMap.confirmHelpUnified'
                  : mode === 'exam'
                    ? hasGraph
                      ? 'courseGraph.mapImportReplace'
                      : 'courseGraph.mapImportConfirm'
                    : 'learningCompetencyMap.confirmHelp',
                { filename: file?.name ?? '' },
              )}
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
                  {mode === 'learn'
                    ? t('learningCompetencyMap.confirm')
                    : t('courseGraph.mapImportStart')}
                </Button>
              </AlertDialog.Action>
            </Flex>
          </AlertDialog.Content>
        </AlertDialog.Root>
      </section>
      {(importLearningMap.data || warnings.length > 0) &&
        ((importLearningMap.data?.unparsedTaskCellCount ?? 0) > 0 ||
          warnings.length > 0) && (
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
            {(importLearningMap.data?.unparsedTaskCellCount ?? 0) > 0 && (
              <Text as="p" color="gray" size="2">
                {t('learningCompetencyMap.unparsed', {
                  count: importLearningMap.data?.unparsedTaskCellCount ?? 0,
                  rows: warnings.length,
                })}
              </Text>
            )}
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
