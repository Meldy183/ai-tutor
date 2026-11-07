import {
  AlertDialog,
  Badge,
  Button,
  Flex,
  Heading,
  SegmentedControl,
  Text,
} from '@radix-ui/themes'
import { useBlocker, useNavigate } from '@tanstack/react-router'
import {
  CheckCircle2,
  Clock,
  Download,
  Eye,
  FileText,
  ArrowRight,
  Replace,
  Save,
  Sparkles,
  Trash2,
  X,
  BookOpen,
  Target,
  Layers,
  EyeOff,
} from 'lucide-react'
import ReactModal from 'react-modal'
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'
import {
  CompetencyMapApiError,
  downloadCourseMaterialRaw,
  type CourseMaterial,
  type CourseMaterialKindOption,
} from '@/entities/competency-map'
import {
  buildProgressPercent,
  type CourseGraphBuild,
  type CourseGraphView,
  CourseGraphApiError,
} from '@/entities/course-graph'
import { useAuth } from '@/features/auth'
import {
  useDeleteCourseMaterialMutation,
  useSubjectMaterialsQuery,
  useCourseMaterialKindsQuery,
  useCourseMaterialsChanging,
  useReplaceCourseMaterialMutation,
  useUpdateCourseMaterialMetadataMutation,
  MaterialReorderControls,
  MaterialUploadQueueToolbar,
  MaterialUploadSubmit,
  MaterialQueueCard,
  CourseMaterialCard,
  MaterialMetadataFields,
  useMaterialUploadQueue,
  moveMaterialQueueItem,
  moveMaterialQueueItemToIndex,
  sortMaterialsByWeek,
  materialInProgress,
  materialPreparationStatus,
} from '@/features/import-competency-map'
import type { MaterialQueueItem } from '@/features/import-competency-map'
import {
  useCourseGraphQuery,
  useLatestCourseGraphBuildQuery,
  useStartCourseGraphBuildMutation,
} from '@/features/course-graph-build'
import { useSubjectsQuery } from '@/features/subject-selection'
import { CosmicProgress, MarkdownContent } from '@/shared/ui'
import styles from '@/pages/competency-map/ui/competency-map.module.scss'
import { AdminCourseDetailShell } from '@/widgets/admin-course-detail-shell'
import { CompetencyMapImport } from './competency-map-import'

// React Flow and the layout load only when the graph is shown.
const CourseGraphCanvas = lazy(() => import('@/widgets/course-graph'))

type MaterialListRow =
  | { id: string; week: string; queued: MaterialQueueItem }
  | { id: string; week: string; saved: CourseMaterial }

export function CompetencyMapAdminScreen({
  subjectId,
  direction,
}: {
  subjectId: string
  direction?: 'learn' | 'exam'
}) {
  const { t } = useTranslation()
  const { user } = useAuth()
  const navigate = useNavigate()
  if (user?.role !== 'admin')
    return (
      <main className={styles.denied} data-admin-screen>
        <Heading as="h1">{t('courseGraph.title')}</Heading>
        <Text as="p" role="alert">
          {t('competencyMap.forbidden')}
        </Text>
        <Button onClick={() => void navigate({ to: '/' })}>
          {t('competencyMap.toLesson')}
        </Button>
      </main>
    )
  return (
    <CompetencyMapAdmin
      key={subjectId}
      userId={user.id}
      subjectId={subjectId}
      legacyDirection={direction}
    />
  )
}

function CompetencyMapAdmin({
  userId,
  subjectId,
  legacyDirection,
}: {
  userId: string
  subjectId: string
  legacyDirection?: 'learn' | 'exam'
}) {
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const subjects = useSubjectsQuery(userId, true)
  const refetchSubjects = subjects.refetch
  const subject = subjects.data?.find((candidate) => candidate.id === subjectId)

  // Course materials state and mutations
  const materialsQuery = useSubjectMaterialsQuery(userId, subjectId)
  const missingMaterialsCourse =
    subject !== undefined &&
    materialsQuery.error instanceof CompetencyMapApiError &&
    materialsQuery.error.status === 404

  useEffect(() => {
    if (!missingMaterialsCourse) return
    void refetchSubjects().then((result) => {
      if (
        result.isSuccess &&
        !result.isError &&
        !result.data.some((candidate) => candidate.id === subjectId)
      )
        void navigate({ to: '/', replace: true })
    })
  }, [missingMaterialsCourse, navigate, refetchSubjects, subjectId])
  const materialKindsQuery = useCourseMaterialKindsQuery(userId)
  const materialKinds = materialKindsQuery.data ?? []
  const queue = useMaterialUploadQueue(userId, subjectId, materialKinds)
  const replaceMaterial = useReplaceCourseMaterialMutation(userId, subjectId)
  const updateMetadata = useUpdateCourseMaterialMetadataMutation(
    userId,
    subjectId,
  )
  const deleteMaterial = useDeleteCourseMaterialMutation(userId, subjectId)
  const materialsBusy = materialsQuery.data?.some(materialInProgress) ?? false
  const metadataIncomplete =
    materialsQuery.data?.some((material) => !material.week || !material.kind) ??
    false
  const latestBuild = useLatestCourseGraphBuildQuery(
    userId,
    subjectId,
    materialsBusy,
    metadataIncomplete,
  )
  const courseGraph = useCourseGraphQuery(
    userId,
    subjectId,
    materialsBusy,
    metadataIncomplete,
  )
  const refetchBuild = latestBuild.refetch
  const refetchGraph = courseGraph.refetch
  useEffect(() => {
    if (!materialsBusy) {
      void refetchBuild()
      void refetchGraph()
    }
  }, [materialsBusy, materialsQuery.dataUpdatedAt, refetchBuild, refetchGraph])
  const startBuild = useStartCourseGraphBuildMutation(userId, subjectId)
  const retryMap = useStartCourseGraphBuildMutation(
    userId,
    subjectId,
    'retryCompetencyMap',
  )
  const [criteriaConfirmation, setCriteriaConfirmation] = useState<{
    fingerprint: string
    warnings: { row: number; column: string; message: string }[]
  } | null>(null)
  const skipCriteria = useStartCourseGraphBuildMutation(
    userId,
    subjectId,
    'skipMissingCriteria',
  )
  const [previewId, setPreviewId] = useState<string | null>(null)
  const [materialOrder, setMaterialOrder] = useState<string[]>([])
  const [draggingMaterialId, setDraggingMaterialId] = useState<string | null>(
    null,
  )
  const [dropTargetMaterialId, setDropTargetMaterialId] = useState<
    string | null
  >(null)

  // Covers queue uploads and saved-material operations.
  const materialsChanging =
    useCourseMaterialsChanging(userId, subjectId) || queue.isPending
  const busy = materialsChanging
  const graphBuilding =
    retryMap.isPending ||
    skipCriteria.isPending ||
    startBuild.isPending ||
    latestBuild.data?.status === 'queued' ||
    latestBuild.data?.status === 'running'
  const uploadsBlocked = busy || graphBuilding || !latestBuild.isSuccess
  const canSkipCriteria =
    !graphBuilding &&
    !courseGraph.data?.stale &&
    latestBuild.data?.status === 'succeeded' &&
    courseGraph.data?.source === 'competency_map' &&
    !!courseGraph.data.map?.fingerprint
  const shouldWarnBeforeExit =
    queue.items.length > 0 ||
    queue.preparing ||
    materialsChanging ||
    graphBuilding
  const shouldBlockNavigation = useCallback(
    () => shouldWarnBeforeExit,
    [shouldWarnBeforeExit],
  )
  const navigationBlocker = useBlocker({
    shouldBlockFn: shouldBlockNavigation,
    withResolver: true,
    enableBeforeUnload: false,
  })

  useEffect(() => {
    if (!shouldWarnBeforeExit) return
    const warnBeforeExit = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', warnBeforeExit)
    return () => window.removeEventListener('beforeunload', warnBeforeExit)
  }, [shouldWarnBeforeExit])

  const materials = materialsQuery.data ?? []
  const materialRows: MaterialListRow[] = [
    ...materials.map((saved) => ({
      id: saved.id,
      week: saved.week?.toString() ?? '',
      saved,
    })),
    ...queue.items.map((queued) => ({
      id: queued.id,
      week: queued.week,
      queued,
    })),
  ]
  const orderedMaterialRows = sortMaterialsByWeek(
    materialRows
      .map((row, index) => ({ ...row, index }))
      .sort((a, b) => {
        const aOrder = materialOrder.indexOf(a.id)
        const bOrder = materialOrder.indexOf(b.id)
        if (aOrder < 0 && bOrder < 0) return a.index - b.index
        if (aOrder < 0) return 1
        if (bOrder < 0) return -1
        return aOrder - bOrder
      }),
  )

  function applyMaterialOrder(
    next: Array<{ id: string; week: string }>,
    movedId: string,
  ) {
    setMaterialOrder(next.map((item) => item.id))
    queue.updateItems(next)
    const moved = next.find((item) => item.id === movedId)
    const previous = materials.find((item) => item.id === movedId)
    const week = Number(moved?.week)
    if (moved && previous && week > 0 && week !== previous.week)
      updateMetadata.mutate({
        materialId: movedId,
        patch: { week },
        subjectId,
      })
  }

  function moveMaterial(movedId: string, targetIndex: number) {
    const current = orderedMaterialRows.map(({ id, week }) => ({ id, week }))
    applyMaterialOrder(
      moveMaterialQueueItemToIndex(current, movedId, targetIndex),
      movedId,
    )
  }
  const graphStateResolved =
    materialsQuery.isSuccess &&
    latestBuild.isSuccess &&
    (courseGraph.isSuccess ||
      (courseGraph.error instanceof CourseGraphApiError &&
        courseGraph.error.status === 404))
  const parsedCount = materials.filter(
    (material) => material.status === 'parsed',
  ).length
  const processingCount = materials.filter(materialInProgress).length
  const readyCount = materials.filter(
    (material) => materialPreparationStatus(material) === 'ready',
  ).length
  const failedCount = materials.filter(
    (material) => material.status === 'failed',
  ).length
  const missingMetadataCount = materials.filter(
    (material) => !material.week || !material.kind,
  ).length
  const graphBlockers = [
    ...(materials.length === 0 ? [t('courseGraph.blockNoMaterials')] : []),
    ...(missingMetadataCount > 0
      ? [t('courseGraph.blockMetadata', { count: missingMetadataCount })]
      : []),
    ...(failedCount > 0
      ? [t('courseGraph.blockFailed', { count: failedCount })]
      : []),
    ...(materialsBusy ? [t('courseGraph.blockPending')] : []),
    ...(materialsChanging || queue.items.length > 0 || queue.preparing
      ? [t('courseGraph.blockChanging')]
      : []),
    ...(latestBuild.data?.inputsReady === false &&
    !materialsBusy &&
    !metadataIncomplete &&
    failedCount === 0
      ? [t('courseGraph.blockPending')]
      : []),
  ]
  const hasGraph = courseGraph.data !== undefined
  const graphIsOutdated = courseGraph.data?.stale ?? false

  return (
    <AdminCourseDetailShell
      active="exam"
      subjectId={subjectId}
      subjectName={subject?.name}
      courseMissing={subjects.isSuccess && !subjects.isError && !subject}
      course={subject}
      showBothStatuses
      statusRefreshError={subjects.isRefetchError}
      onRetryStatus={() => void subjects.refetch()}
      title={t('adminCourses.preparationTitle')}
      description={t('adminCourses.preparationSubtitle')}
    >
      {subjects.isPending ? (
        <Text as="p" role="status">
          {t('competencyMap.subjectsLoading')}
        </Text>
      ) : subjects.isError && !subjects.data ? (
        <div className={styles.card}>
          <Text as="p" role="alert">
            {t('competencyMap.subjectsError')}
          </Text>
          <Button variant="soft" onClick={() => void subjects.refetch()}>
            {t('trainer.retry')}
          </Button>
        </div>
      ) : !subject ? null : (
        <>
          <nav
            className={styles.pipeline}
            aria-label={t('courseGraph.pipeline')}
          >
            <div data-active="true">
              <span className={styles.stepNumber}>1</span>
              <Text weight="bold">{t('courseGraph.materialsStep')}</Text>
            </div>
            <ArrowRight className={styles.pipelineArrow} aria-hidden="true" />
            <div
              data-ready={
                (parsedCount > 0 && graphBlockers.length === 0) ||
                hasGraph ||
                undefined
              }
            >
              <span className={styles.stepNumber}>2</span>
              <Text weight="bold">{t('courseGraph.graphStep')}</Text>
            </div>
            <ArrowRight className={styles.pipelineArrow} aria-hidden="true" />
            <div data-ready={hasGraph || undefined}>
              <span className={styles.stepNumber}>3</span>
              <Text weight="bold">{t('courseGraph.readyStep')}</Text>
            </div>
          </nav>

          <CompetencyMapImport
            userId={userId}
            subjectId={subjectId}
            hasGraph={hasGraph}
            disabled={uploadsBlocked}
            map={courseGraph.data?.map}
            legacyDirection={legacyDirection ?? 'learn'}
          />

          <section
            className={styles.card}
            aria-labelledby="materials-list-title"
          >
            <div className={styles.sectionHeading}>
              <FileText aria-hidden="true" />
              <Heading as="h2" size="4" id="materials-list-title">
                {t('competencyMap.materialsListTitle')}
              </Heading>
            </div>

            {materialKindsQuery.isError && (
              <Flex gap="3" align="center" wrap="wrap">
                <Text role="alert" color="red">
                  {t('courseGraph.kindsUnavailable')}
                </Text>
                <Button
                  variant="ghost"
                  size="1"
                  onClick={() => void materialKindsQuery.refetch()}
                >
                  {t('trainer.retry')}
                </Button>
              </Flex>
            )}
            {materialKindsQuery.isSuccess &&
              materialKindsQuery.data.length === 0 && (
                <Text role="alert" color="amber">
                  {t('courseGraph.kindsEmpty')}
                </Text>
              )}

            <MaterialUploadQueueToolbar
              queue={queue}
              disabled={uploadsBlocked}
            />
            {graphBuilding && (
              <Text as="p" role="status" color="gray" size="2">
                {t('competencyMap.materialUploadBlockedByGraph')}
              </Text>
            )}

            {replaceMaterial.isError && (
              <Text as="p" role="alert" color="red">
                {replaceMaterial.error instanceof Error
                  ? replaceMaterial.error.message
                  : t('competencyMap.requestError')}
              </Text>
            )}
            {materialsQuery.isPending && (
              <Text as="p" role="status" color="gray" size="2">
                {t('competencyMap.loading')}
              </Text>
            )}
            {materialsQuery.isError && (
              <Flex gap="3" align="center" wrap="wrap">
                <Text as="p" role="alert" color="red" size="2">
                  {t('competencyMap.loadError')}
                </Text>
                <Button
                  variant="ghost"
                  size="1"
                  onClick={() => void materialsQuery.refetch()}
                >
                  {t('trainer.retry')}
                </Button>
              </Flex>
            )}
            {materialsQuery.isSuccess && orderedMaterialRows.length === 0 && (
              <Text as="p" color="gray" size="2">
                {t('competencyMap.materialsEmpty')}
              </Text>
            )}
            {orderedMaterialRows.length > 0 && (
              <div className={styles.materialsList} role="list">
                {orderedMaterialRows.map((row, index) => {
                  const name =
                    'queued' in row ? row.queued.file.name : row.saved.filename
                  return (
                    <div
                      className={styles.materialListItem}
                      key={row.id}
                      role="listitem"
                      draggable={!busy && !queue.preparing}
                      data-dragging={draggingMaterialId === row.id || undefined}
                      data-drop-target={
                        dropTargetMaterialId === row.id || undefined
                      }
                      onDragStart={(event) => {
                        const target = event.target
                        if (
                          target instanceof HTMLElement &&
                          target.closest('button, input, [role="combobox"]') &&
                          !target.closest('[data-queue-drag-handle]')
                        ) {
                          event.preventDefault()
                          return
                        }
                        setDraggingMaterialId(row.id)
                        event.dataTransfer.effectAllowed = 'move'
                        event.dataTransfer.setData('text/plain', row.id)
                      }}
                      onDragEnd={() => {
                        setDraggingMaterialId(null)
                        setDropTargetMaterialId(null)
                      }}
                      onDragOver={(event) => {
                        event.preventDefault()
                        setDropTargetMaterialId(row.id)
                        event.dataTransfer.dropEffect = 'move'
                      }}
                      onDragLeave={() => setDropTargetMaterialId(null)}
                      onDrop={(event) => {
                        event.preventDefault()
                        const sourceId =
                          event.dataTransfer.getData('text/plain') ||
                          draggingMaterialId
                        if (sourceId) {
                          const current = orderedMaterialRows.map(
                            ({ id, week }) => ({ id, week }),
                          )
                          applyMaterialOrder(
                            moveMaterialQueueItem(current, sourceId, row.id),
                            sourceId,
                          )
                        }
                        setDraggingMaterialId(null)
                        setDropTargetMaterialId(null)
                      }}
                    >
                      <MaterialReorderControls
                        name={name}
                        disabled={busy || queue.preparing}
                        isFirst={index === 0}
                        isLast={index === orderedMaterialRows.length - 1}
                        onMoveUp={() => moveMaterial(row.id, index - 1)}
                        onMoveDown={() => moveMaterial(row.id, index + 1)}
                      />
                      {'queued' in row ? (
                        <MaterialQueueCard
                          item={row.queued}
                          kinds={materialKinds}
                          disabled={busy || queue.preparing}
                          dropTarget={dropTargetMaterialId === row.id}
                          onWeekChange={(week) =>
                            queue.updateWeek(row.id, week)
                          }
                          onKindChange={(kind) =>
                            queue.updateKind(row.id, kind)
                          }
                          onRemove={() => queue.removeItem(row.id)}
                          onSend={() => queue.sendItem(row.id)}
                          sending={queue.sendingItemId === row.id}
                        />
                      ) : (
                        <MaterialItemCard
                          material={row.saved}
                          kinds={materialKinds}
                          subjectId={subjectId}
                          onUpdateMetadata={(patch) =>
                            updateMetadata.mutateAsync({
                              materialId: row.saved.id,
                              patch,
                              subjectId,
                            })
                          }
                          dropTarget={dropTargetMaterialId === row.id}
                          previewOpen={previewId === row.id}
                          onTogglePreview={() =>
                            setPreviewId((current) =>
                              current === row.id ? null : row.id,
                            )
                          }
                          onDelete={() =>
                            deleteMaterial.mutateAsync({
                              materialId: row.id,
                              subjectId,
                            })
                          }
                          onReplace={(file) =>
                            replaceMaterial.mutate({
                              materialId: row.id,
                              file,
                            })
                          }
                          busy={busy || !latestBuild.isSuccess}
                        />
                      )}
                    </div>
                  )
                })}
              </div>
            )}
            <MaterialUploadSubmit queue={queue} disabled={uploadsBlocked} />
          </section>

          <section className={styles.card} aria-labelledby="graph-title">
            <div className={styles.sectionHeading}>
              <Sparkles aria-hidden="true" />
              <Heading as="h2" size="4" id="graph-title">
                {t('courseGraph.graphTitle')}
              </Heading>
              <Badge
                size="1"
                color={
                  hasGraph ? 'green' : graphBlockers.length ? 'amber' : 'blue'
                }
                className={styles.materialTag}
              >
                {hasGraph
                  ? t('courseGraph.graphStored')
                  : t('courseGraph.graphNotReady')}
              </Badge>
              {graphIsOutdated && (
                <Badge size="1" color="amber" className={styles.materialTag}>
                  {t('courseGraph.graphOutdated')}
                </Badge>
              )}
            </div>
            <Text as="p" color="gray" size="2">
              {t('courseGraph.materialSummary', {
                total: materials.length,
                ready: readyCount,
                processing: processingCount,
                failed: failedCount,
              })}
            </Text>
            {graphBlockers.length > 0 &&
              latestBuild.data?.source !== 'competency_map' && (
                <div className={styles.warning} role="status">
                  <Text weight="bold">
                    {t(
                      hasGraph
                        ? 'courseGraph.rebuildBlockedTitle'
                        : 'courseGraph.blockedTitle',
                    )}
                  </Text>
                  <ul>
                    {graphBlockers.map((reason) => (
                      <li key={reason}>{reason}</li>
                    ))}
                  </ul>
                </div>
              )}
            {latestBuild.isError && (
              <div>
                <Text as="p" role="alert" color="red">
                  {t('courseGraph.statusError')}
                </Text>
                <Button
                  variant="ghost"
                  onClick={() => void latestBuild.refetch()}
                >
                  {t('trainer.retry')}
                </Button>
              </div>
            )}
            {(latestBuild.data?.status === 'queued' ||
              latestBuild.data?.status === 'running') && (
              <BuildProgressCard build={latestBuild.data} />
            )}
            {latestBuild.data?.status === 'failed' && (
              <div className={styles.error} role="alert">
                <Text weight="bold">{t('courseGraph.buildFailed')}</Text>
                <Text as="p">
                  {latestBuild.data.error?.message ??
                    t('courseGraph.buildFailedHint')}
                </Text>
                {latestBuild.data.source === 'competency_map' &&
                  latestBuild.data.map?.fingerprint && (
                    <>
                      <Text as="p">{t('courseGraph.retryMapHint')}</Text>
                      <Button
                        disabled={graphBuilding || latestBuild.isFetching}
                        onClick={() =>
                          retryMap.mutate({
                            subjectId,
                            expectedFingerprint:
                              latestBuild.data?.map?.fingerprint ?? '',
                          })
                        }
                      >
                        {t('courseGraph.retryMap')}
                      </Button>
                    </>
                  )}
              </div>
            )}
            {retryMap.isError && (
              <Text as="p" role="alert" color="red">
                {retryMap.error.message}
              </Text>
            )}
            {courseGraph.isError &&
              courseGraph.error instanceof CourseGraphApiError &&
              courseGraph.error.status !== 404 && (
                <div>
                  <Text as="p" role="alert" color="red">
                    {t('courseGraph.graphReadError')}
                  </Text>
                  <Button
                    variant="ghost"
                    onClick={() => void courseGraph.refetch()}
                  >
                    {t('trainer.retry')}
                  </Button>
                </div>
              )}
            {hasGraph && (
              <div className={styles.success} role="status">
                <CheckCircle2 aria-hidden="true" />
                <span>
                  {t('courseGraph.readyMessage', {
                    count: courseGraph.data.nodes.length,
                  })}
                  {courseGraph.data.publishedAt !== null && (
                    <span className={styles.graphPublishedAt}>
                      {t('courseGraph.graphPublishedAt', {
                        date: new Date(
                          courseGraph.data.publishedAt * 1000,
                        ).toLocaleString(i18n.language),
                      })}
                    </span>
                  )}
                </span>
              </div>
            )}
            {startBuild.isError && (
              <Text as="p" role="alert" color="red">
                {startBuild.error instanceof Error
                  ? startBuild.error.message
                  : t('courseGraph.buildFailedHint')}
              </Text>
            )}
            <AlertDialog.Root>
              <AlertDialog.Trigger>
                <Button
                  size="3"
                  color="green"
                  disabled={
                    !graphStateResolved ||
                    graphBlockers.length > 0 ||
                    latestBuild.data?.status === 'queued' ||
                    latestBuild.data?.status === 'running' ||
                    graphBuilding
                  }
                >
                  <Sparkles size={17} aria-hidden="true" />
                  {hasGraph ? t('courseGraph.rebuild') : t('courseGraph.build')}
                </Button>
              </AlertDialog.Trigger>
              <AlertDialog.Content maxWidth="440px">
                <AlertDialog.Title>
                  {hasGraph
                    ? t('courseGraph.rebuildTitle')
                    : t('courseGraph.buildTitle')}
                </AlertDialog.Title>
                <AlertDialog.Description>
                  {hasGraph
                    ? t('courseGraph.rebuildWarning')
                    : t('courseGraph.buildConfirm')}
                </AlertDialog.Description>
                <Flex gap="3" mt="4" justify="end">
                  <AlertDialog.Cancel>
                    <Button variant="soft" color="gray">
                      {t('competencyMap.cancel')}
                    </Button>
                  </AlertDialog.Cancel>
                  <AlertDialog.Action>
                    <Button
                      color="green"
                      disabled={
                        !graphStateResolved ||
                        graphBlockers.length > 0 ||
                        graphBuilding
                      }
                      onClick={() => startBuild.mutate(subjectId)}
                    >
                      {t('courseGraph.confirmBuild')}
                    </Button>
                  </AlertDialog.Action>
                </Flex>
              </AlertDialog.Content>
            </AlertDialog.Root>
            <CourseGraphViewer
              graph={courseGraph.data?.view}
              fromMap={courseGraph.data?.source === 'competency_map'}
              materials={materials}
            />
            {!!courseGraph.data?.view?.assessment_skipped && (
              <Text as="p" role="status">
                {t('courseGraph.criteriaSkipped', {
                  count: courseGraph.data.view.assessment_skipped,
                })}
              </Text>
            )}
            {!!courseGraph.data?.view?.assessment_warnings?.length && (
              <div role="status">
                <Text as="p" color="amber">
                  {t('courseGraph.defaultAssessmentCriteria')}
                </Text>
                <ul>
                  {courseGraph.data.view.assessment_warnings.map(
                    (warning, index) => (
                      <li key={`${warning.row}:${warning.column}:${index}`}>
                        {t('courseGraph.defaultAssessmentCriteriaRow', warning)}
                      </li>
                    ),
                  )}
                </ul>
                <Text as="p" weight="bold">
                  {t('courseGraph.defaultAssessmentScale')}
                </Text>
                <ul>
                  <li>{t('courseGraph.defaultAssessmentCorrectness')}</li>
                  <li>{t('courseGraph.defaultAssessmentCoverage')}</li>
                </ul>
                <Text as="p">
                  {t('courseGraph.defaultAssessmentRequirements')}
                </Text>
                {!canSkipCriteria && (
                  <Text as="p">
                    {t('courseGraph.skipCriteriaNeedsPublishedMap')}
                  </Text>
                )}
                <AlertDialog.Root
                  onOpenChange={(open) => {
                    setCriteriaConfirmation(
                      open && courseGraph.data?.map?.fingerprint
                        ? {
                            fingerprint: courseGraph.data.map.fingerprint,
                            warnings:
                              courseGraph.data.view?.assessment_warnings ?? [],
                          }
                        : null,
                    )
                  }}
                >
                  <AlertDialog.Trigger>
                    <Button color="amber" disabled={!canSkipCriteria}>
                      {t('courseGraph.skipCriteria')}
                    </Button>
                  </AlertDialog.Trigger>
                  <AlertDialog.Content maxWidth="520px">
                    <AlertDialog.Title>
                      {t('courseGraph.skipCriteria')}
                    </AlertDialog.Title>
                    <AlertDialog.Description>
                      {t('courseGraph.skipCriteriaConfirm')}
                    </AlertDialog.Description>
                    <ul>
                      {(criteriaConfirmation?.warnings ?? []).map(
                        (warning, index) => (
                          <li key={index}>
                            {t(
                              'courseGraph.defaultAssessmentCriteriaRow',
                              warning,
                            )}
                          </li>
                        ),
                      )}
                    </ul>
                    <Flex justify="end" gap="3" mt="4">
                      <AlertDialog.Cancel>
                        <Button variant="soft">
                          {t('competencyMap.cancel')}
                        </Button>
                      </AlertDialog.Cancel>
                      <AlertDialog.Action>
                        <Button
                          color="amber"
                          onClick={() =>
                            skipCriteria.mutate({
                              subjectId,
                              expectedFingerprint:
                                criteriaConfirmation?.fingerprint ?? '',
                            })
                          }
                        >
                          {t('courseGraph.skipCriteriaConfirmButton')}
                        </Button>
                      </AlertDialog.Action>
                    </Flex>
                  </AlertDialog.Content>
                </AlertDialog.Root>
                {skipCriteria.isError && (
                  <Text as="p" role="alert" color="red">
                    {skipCriteria.error.message}
                  </Text>
                )}
              </div>
            )}
          </section>
        </>
      )}
      <AlertDialog.Root
        open={navigationBlocker.status === 'blocked'}
        onOpenChange={(open) => {
          if (!open) navigationBlocker.reset?.()
        }}
      >
        <AlertDialog.Content maxWidth="440px">
          <AlertDialog.Title>
            {t('competencyMap.leaveWithChangesTitle')}
          </AlertDialog.Title>
          <AlertDialog.Description>
            {t('competencyMap.leaveWithChangesDescription')}
          </AlertDialog.Description>
          <Flex gap="3" mt="4" justify="end">
            <AlertDialog.Cancel>
              <Button
                variant="soft"
                color="gray"
                onClick={() => navigationBlocker.reset?.()}
              >
                {t('competencyMap.stayOnCourse')}
              </Button>
            </AlertDialog.Cancel>
            <AlertDialog.Action>
              <Button onClick={() => navigationBlocker.proceed?.()}>
                {t('competencyMap.leaveCourse')}
              </Button>
            </AlertDialog.Action>
          </Flex>
        </AlertDialog.Content>
      </AlertDialog.Root>
    </AdminCourseDetailShell>
  )
}

function MaterialItemCard({
  material,
  kinds,
  subjectId,
  onUpdateMetadata,
  dropTarget,
  previewOpen,
  onTogglePreview,
  onDelete,
  onReplace,
  busy,
}: {
  material: CourseMaterial
  kinds: CourseMaterialKindOption[]
  subjectId: string
  onUpdateMetadata: (patch: { week: number; kind: string }) => Promise<unknown>
  dropTarget: boolean
  previewOpen: boolean
  onTogglePreview: () => void
  onDelete: () => Promise<void>
  onReplace: (file: File) => void
  busy: boolean
}) {
  const { t, i18n } = useTranslation()
  const replaceInputRef = useRef<HTMLInputElement>(null)
  const [downloading, setDownloading] = useState(false)
  const [downloadError, setDownloadError] = useState<string | null>(null)
  const [week, setWeek] = useState(material.week?.toString() ?? '')
  const [kind, setKind] = useState(material.kind ?? '')
  const [metadataError, setMetadataError] = useState<string | null>(null)
  const [savingMetadata, setSavingMetadata] = useState(false)
  const [actionError, setActionError] = useState<{
    message: string
    retry: () => Promise<unknown>
  } | null>(null)
  async function runDelete() {
    try {
      await onDelete()
      setActionError(null)
    } catch (error) {
      setActionError({
        message:
          error instanceof Error
            ? error.message
            : t('competencyMap.requestError'),
        retry: onDelete,
      })
    }
  }
  async function retryAction() {
    if (!actionError) return
    try {
      await actionError.retry()
      setActionError(null)
    } catch (error) {
      setActionError({
        ...actionError,
        message:
          error instanceof Error
            ? error.message
            : t('competencyMap.requestError'),
      })
    }
  }
  const formattedSize =
    material.fileSize > 1024 * 1024
      ? `${(material.fileSize / (1024 * 1024)).toFixed(1)} МБ`
      : `${Math.round(material.fileSize / 1024)} КБ`

  const dateStr = new Date(material.createdAt * 1000).toLocaleString(
    i18n.language,
    { dateStyle: 'medium', timeStyle: 'short' },
  )

  const processing = materialInProgress(material)
  const preparationStatus = materialPreparationStatus(material)
  const statusTone =
    preparationStatus === 'failed' ? 'red' : processing ? 'amber' : 'green'
  const metadataIsValid =
    /^\d+$/.test(week) &&
    Number(week) > 0 &&
    kinds.some((option) => option.code === kind)
  const metadataChanged =
    Number(week) !== material.week || kind !== material.kind

  useEffect(() => {
    setWeek(material.week?.toString() ?? '')
    setKind(material.kind ?? '')
    setMetadataError(null)
  }, [material.week, material.kind])

  async function saveMetadata() {
    if (!metadataIsValid || !metadataChanged || savingMetadata) return
    setSavingMetadata(true)
    setMetadataError(null)
    try {
      await onUpdateMetadata({ week: Number(week), kind })
    } catch (error) {
      setMetadataError(
        error instanceof Error
          ? error.message
          : t('competencyMap.requestError'),
      )
    } finally {
      setSavingMetadata(false)
    }
  }
  async function handleDownload() {
    setDownloading(true)
    setDownloadError(null)
    try {
      await downloadCourseMaterialRaw(subjectId, material.id, material.filename)
    } catch (err) {
      setDownloadError(
        err instanceof Error
          ? err.message
          : t('competencyMap.materialsDownloadError'),
      )
    } finally {
      setDownloading(false)
    }
  }

  return (
    <CourseMaterialCard
      name={material.filename}
      status={t(`competencyMap.materialPreparation.${preparationStatus}`)}
      statusColor={statusTone}
      nameAction={
        <Button
          size="1"
          variant="ghost"
          color="gray"
          disabled={downloading}
          onClick={() => void handleDownload()}
          aria-label={`Скачать ${material.filename}`}
          title={`Скачать ${material.filename}`}
        >
          <Download size={14} />
        </Button>
      }
      statusAction={
        <>
          {material.status === 'parsed' && (
            <Button
              size="1"
              variant="soft"
              color="gray"
              className={styles.previewButton}
              onClick={onTogglePreview}
            >
              <Eye size={14} />
              {t('competencyMap.materialsPreview')}
            </Button>
          )}
          {!processing && (
            <>
              <Button
                size="1"
                variant="soft"
                color="gray"
                className={styles.previewButton}
                disabled={busy}
                onClick={() => replaceInputRef.current?.click()}
                aria-label={t('competencyMap.materialReplace')}
                title={t('competencyMap.materialReplace')}
              >
                <Replace size={14} />
                {t('competencyMap.materialReplaceAction')}
              </Button>
              <input
                ref={replaceInputRef}
                type="file"
                accept=".pdf,application/pdf"
                disabled={busy}
                className={styles.hiddenReplaceInput}
                aria-label={t('competencyMap.materialReplace')}
                onChange={(event) => {
                  const file = event.currentTarget.files?.[0]
                  if (file) onReplace(file)
                  event.currentTarget.value = ''
                }}
              />
            </>
          )}
        </>
      }
      dropTarget={dropTarget}
      meta={
        <>
          <span>
            {t('competencyMap.materialsSize', { size: formattedSize })}
          </span>
          <span>•</span>
          <span>
            {t('competencyMap.materialsCharCount', {
              count: material.charCount.toLocaleString(i18n.language),
            })}
          </span>
          <span>•</span>
          <Flex gap="1" align="center">
            <Clock size={12} />
            <span>{dateStr}</span>
          </Flex>
        </>
      }
      actions={
        !processing ? (
          <Flex gap="2" align="center">
            <AlertDialog.Root>
              <AlertDialog.Trigger>
                <Button
                  size="1"
                  color="red"
                  variant="soft"
                  disabled={busy}
                  aria-label={t('competencyMap.materialsDelete')}
                  title={t('competencyMap.materialsDelete')}
                >
                  <Trash2 size={14} />
                </Button>
              </AlertDialog.Trigger>
              <AlertDialog.Content maxWidth="400px">
                <AlertDialog.Title>
                  {t('competencyMap.materialsDelete')}
                </AlertDialog.Title>
                <AlertDialog.Description>
                  {t('competencyMap.materialsDeleteConfirm')}
                </AlertDialog.Description>
                <Flex gap="3" mt="4" justify="end">
                  <AlertDialog.Cancel>
                    <Button variant="soft" color="gray">
                      {t('competencyMap.cancel')}
                    </Button>
                  </AlertDialog.Cancel>
                  <AlertDialog.Action>
                    <Button color="red" onClick={() => void runDelete()}>
                      {t('competencyMap.confirm')}
                    </Button>
                  </AlertDialog.Action>
                </Flex>
              </AlertDialog.Content>
            </AlertDialog.Root>
          </Flex>
        ) : undefined
      }
    >
      <MaterialMetadataFields
        week={week}
        kind={kind}
        kinds={kinds}
        disabled={busy || savingMetadata}
        onWeekChange={setWeek}
        onKindChange={setKind}
        action={
          <Flex gap="2" align="center" wrap="wrap">
            <Button
              type="button"
              size="2"
              variant="soft"
              color="green"
              disabled={
                busy || savingMetadata || !metadataIsValid || !metadataChanged
              }
              onClick={() => void saveMetadata()}
            >
              <Save size={14} aria-hidden="true" />
              {savingMetadata
                ? t('competencyMap.metadataSaving')
                : t('competencyMap.metadataSave')}
            </Button>
            {(!material.week || !material.kind) && (
              <Badge color="amber" size="1" className={styles.materialTag}>
                {t('courseGraph.metadataMissing')}
              </Badge>
            )}
          </Flex>
        }
      />
      {metadataError && (
        <Text role="alert" color="red" size="1">
          {metadataError}
        </Text>
      )}
      {material.status === 'failed' && material.errorMessage && (
        <Text color="red" size="2">
          {material.errorMessage}
        </Text>
      )}

      {downloadError && (
        <Text as="p" role="alert" color="red" size="2">
          {downloadError}
        </Text>
      )}
      {actionError && (
        <Flex gap="2" align="center" wrap="wrap">
          <Text as="p" role="alert" color="red" size="2">
            {actionError.message}
          </Text>
          <Button size="1" variant="soft" onClick={() => void retryAction()}>
            {t('trainer.retry')}
          </Button>
        </Flex>
      )}

      <ReactModal
        isOpen={previewOpen}
        appElement={
          document.getElementById('root') ??
          document.querySelector<HTMLElement>('[data-admin-screen]') ??
          undefined
        }
        onRequestClose={onTogglePreview}
        contentLabel={`${t('competencyMap.materialsPreview')}: ${material.filename}`}
        className={styles.previewModal}
        overlayClassName={styles.previewModalOverlay}
      >
        <div className={styles.previewModalHeader}>
          <Heading as="h2" size="4">
            {material.filename}
          </Heading>
          <Button
            type="button"
            variant="ghost"
            color="gray"
            aria-label={t('competencyMap.closePreview')}
            onClick={onTogglePreview}
          >
            <X size={18} />
          </Button>
        </div>
        <MarkdownContent
          text={material.parsedText || '(текст отсутствует)'}
          className={styles.previewMarkdown}
        />
      </ReactModal>
    </CourseMaterialCard>
  )
}

// The running build: its stage, stage i of n and step k of m on it, and a bar
// of the share done. Before course-graph reports a stage the bar sweeps.
function BuildProgressCard({ build }: { build: CourseGraphBuild }) {
  const { t } = useTranslation()
  const progress = build.progress
  const percent = progress ? Math.round(buildProgressPercent(progress)) : null
  return (
    <div role="status" className={styles.buildProgress}>
      <Flex justify="between" align="baseline" gap="3">
        <Text weight="bold">{t(`courseGraph.status.${build.status}`)}</Text>
        {percent !== null && (
          <Text className={styles.buildPercent}>
            {t('session.percent', { value: percent })}
          </Text>
        )}
      </Flex>
      <Text size="2" color="gray">
        {progress
          ? [
              t(`courseGraph.stages.${progress.stage}`),
              t('courseGraph.stageOf', {
                index: progress.stageIndex,
                count: progress.stageCount,
              }),
              progress.total > 0
                ? t('courseGraph.stepOf', {
                    done: progress.done,
                    total: progress.total,
                  })
                : null,
            ]
              .filter(Boolean)
              .join(' · ')
          : t('courseGraph.stagePreparing')}
      </Text>
      {progress?.stage === 'prepare_questions' && (
        <Text as="p" size="2">
          {t('courseGraph.questionPreparationHint')}
        </Text>
      )}
      {progress?.remainingSeconds !== undefined &&
        progress.remainingSeconds > 0 && (
          <Text as="p" size="2" color="gray">
            {t('courseGraph.estimatedRemaining', {
              seconds: progress.remainingSeconds,
            })}
          </Text>
        )}
      <CosmicProgress
        value={percent}
        label={t('courseGraph.status.running')}
        valueText={
          percent === null
            ? t('courseGraph.stagePreparing')
            : t('session.percent', { value: percent })
        }
      />
    </div>
  )
}

function CourseGraphViewer({
  graph,
  fromMap,
  materials,
}: {
  graph: CourseGraphView | null | undefined
  fromMap: boolean
  materials: CourseMaterial[]
}) {
  const { t } = useTranslation()
  // The graph is the point of this card, so it starts open as a map.
  const [treeOpen, setTreeOpen] = useState(true)
  const [mode, setMode] = useState<'canvas' | 'outline'>('canvas')
  const materialNames = useMemo(
    () => new Map(materials.map((m) => [m.id, m.filename])),
    [materials],
  )
  if (!graph) return null
  const topicsCount = graph.topics.length
  const prereqsCount = graph.prerequisites.length
  const hierarchyCount = graph.hierarchy.length
  return (
    <div className={styles.graphBody}>
      <>
        {fromMap && (
          <Flex align="center" gap="2" wrap="wrap">
            <Badge color="violet" size="1">
              {t('competencyMap.graphSourceMap')}
            </Badge>
            <Text size="2" color="gray">
              {t('competencyMap.graphMapHint')}
            </Text>
          </Flex>
        )}
        <dl className={styles.stats}>
          <div className={styles.statRow} data-tone="blue">
            <span className={styles.statIcon} aria-hidden="true">
              <BookOpen size={16} />
            </span>
            <dt>{t('competencyMap.graphStats.topics')}</dt>
            <dd>{topicsCount}</dd>
          </div>
          <div className={styles.statRow} data-tone="green">
            <span className={styles.statIcon} aria-hidden="true">
              <Target size={16} />
            </span>
            <dt>{t('competencyMap.graphStats.prerequisites')}</dt>
            <dd>{prereqsCount}</dd>
          </div>
          <div className={styles.statRow} data-tone="violet">
            <span className={styles.statIcon} aria-hidden="true">
              <Layers size={16} />
            </span>
            <dt>{t('competencyMap.graphStats.hierarchy')}</dt>
            <dd>{hierarchyCount}</dd>
          </div>
        </dl>

        <div>
          <Button
            size="2"
            variant="soft"
            onClick={() => setTreeOpen((prev) => !prev)}
          >
            {treeOpen ? (
              <>
                <EyeOff size={16} aria-hidden="true" />
                {t('competencyMap.graphHideView')}
              </>
            ) : (
              <>
                <Eye size={16} aria-hidden="true" />
                {t('competencyMap.graphToggleView')}
              </>
            )}
          </Button>
        </div>

        {treeOpen && (
          <SegmentedControl.Root
            size="2"
            value={mode}
            onValueChange={(value) => setMode(value as 'canvas' | 'outline')}
            className={styles.graphMode}
          >
            <SegmentedControl.Item value="canvas">
              {t('competencyMap.graphView.modeCanvas')}
            </SegmentedControl.Item>
            <SegmentedControl.Item value="outline">
              {t('competencyMap.graphView.modeOutline')}
            </SegmentedControl.Item>
          </SegmentedControl.Root>
        )}

        {treeOpen && mode === 'canvas' && (
          <Suspense
            fallback={
              <Text as="p" role="status" color="gray" size="2">
                {t('competencyMap.graphView.loading')}
              </Text>
            }
          >
            <CourseGraphCanvas graph={graph} materialNames={materialNames} />
          </Suspense>
        )}

        {treeOpen && mode === 'outline' && (
          <div className={styles.graphTree}>
            <Text weight="bold" size="2">
              {t('competencyMap.graphSections')}
            </Text>
            {(() => {
              const topicsById = new Map(graph.topics.map((t) => [t.id, t]))
              const childrenByParent = new Map<string, string[]>()
              const childIds = new Set<string>()

              for (const h of graph.hierarchy) {
                const list = childrenByParent.get(h.source) || []
                list.push(h.target)
                childrenByParent.set(h.source, list)
                childIds.add(h.target)
              }

              const roots = graph.topics.filter((t) => !childIds.has(t.id))

              const renderedIds = new Set<string>()

              const renderSubtree = (
                topicId: string,
                visitedInBranch: Set<string>,
              ): React.ReactNode => {
                renderedIds.add(topicId)
                const children = childrenByParent.get(topicId) || []
                if (children.length === 0) return null

                return (
                  <ul className={styles.graphChildrenList}>
                    {children.map((childId) => {
                      if (visitedInBranch.has(childId)) {
                        return null
                      }
                      const child = topicsById.get(childId)
                      const nextVisited = new Set(visitedInBranch).add(childId)
                      return (
                        <li key={childId}>
                          <Text size="2" color="gray">
                            └─ {child?.title || childId}
                          </Text>
                          {renderSubtree(childId, nextVisited)}
                        </li>
                      )
                    })}
                  </ul>
                )
              }

              const primaryRoots = roots.length > 0 ? roots : graph.topics

              return (
                <div className={styles.graphSectionsList}>
                  {primaryRoots.map((root) => {
                    renderedIds.add(root.id)
                    return (
                      <div key={root.id} className={styles.graphSectionItem}>
                        <Text weight="bold" size="2">
                          {root.title}
                        </Text>
                        {renderSubtree(root.id, new Set([root.id]))}
                      </div>
                    )
                  })}
                  {graph.topics
                    .filter((t) => !renderedIds.has(t.id))
                    .map((orphan) => {
                      renderedIds.add(orphan.id)
                      return (
                        <div
                          key={orphan.id}
                          className={styles.graphSectionItem}
                        >
                          <Text weight="bold" size="2">
                            {orphan.title}
                          </Text>
                          {renderSubtree(orphan.id, new Set([orphan.id]))}
                        </div>
                      )
                    })}
                </div>
              )
            })()}

            {graph.prerequisites.length > 0 && (
              <>
                <Text
                  weight="bold"
                  size="2"
                  className={styles.graphDependenciesTitle}
                >
                  {t('competencyMap.graphDependencies')}
                </Text>
                {(() => {
                  const topicsById = new Map(graph.topics.map((t) => [t.id, t]))
                  return (
                    <div className={styles.graphPrereqList}>
                      {graph.prerequisites.map((p, idx) => {
                        const src = topicsById.get(p.source)?.title || p.source
                        const tgt = topicsById.get(p.target)?.title || p.target
                        return (
                          <div key={idx} className={styles.graphPrereqItem}>
                            <Text size="2" weight="bold">
                              {src}
                            </Text>
                            <Text size="2" color="gray">
                              →
                            </Text>
                            <Text size="2">{tgt}</Text>
                            {p.reason && (
                              <Text size="1" color="gray">
                                ({p.reason})
                              </Text>
                            )}
                          </div>
                        )
                      })}
                    </div>
                  )
                })()}
              </>
            )}
          </div>
        )}
      </>
    </div>
  )
}
