import { afterEach, expect, it, vi } from 'vitest'
import {
  buildProgressPercent,
  CourseGraphApiError,
  importCompetencyMap,
  readCourseGraph,
  readLatestCourseGraphBuild,
  startCourseGraphBuild,
  skipMissingCriteria,
  retryCompetencyMap,
} from '@/entities/course-graph'

afterEach(() => vi.unstubAllGlobals())
const signal = () => new AbortController().signal
const graph = {
  topics: [
    { id: 'intro', title: 'Основы', provides: ['Определения'] },
    {
      id: 'matrix',
      title: 'Матрицы',
      question: 'Что такое матрица?',
      requires: ['Определения'],
      parent_title: 'Алгебра',
    },
  ],
  prerequisites: [
    {
      source: 'intro',
      target: 'matrix',
      requirement: 'Определения',
      reason: 'Основа темы',
    },
  ],
  hierarchy: [{ source: 'intro', target: 'matrix' }],
  quality: {
    acyclic: true,
    topics: 2,
    unresolved_requirements: 0,
    semantic_quality: 'not_expert_validated',
  },
}
const snapshot = (status = 'ready', overrides = {}) => ({
  status,
  attempts: 1,
  error_message: '',
  graph,
  stale: false,
  inputs_ready: true,
  updated_at: 42,
  graph_fingerprint: '',
  graph_map_revision: null,
  map: null,
  ...overrides,
})
function respond(value: unknown, status = 200) {
  const fetchMock = vi.fn().mockResolvedValue(Response.json(value, { status }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

it('posts the confirmed exclusion and reads preparation progress', async () => {
  const fetch = respond(
    snapshot('processing', {
      build_source: 'competency_map',
      progress: {
        stage: 'prepare_questions',
        stage_index: 1,
        stage_count: 2,
        done: 3,
        total: 15,
        remaining_seconds: 42,
      },
    }),
  )
  const build = await skipMissingCriteria('subject:one', signal(), 'map-a')
  expect(fetch.mock.calls[0][0]).toBe(
    '/api/v1/exam/admin/subjects/subject%3Aone/graph/competency-map/skip-missing-criteria',
  )
  expect(fetch.mock.calls[0][1]).toMatchObject({
    method: 'POST',
    headers: { 'X-Expected-Map-Fingerprint': 'map-a' },
  })
  expect(build.progress).toMatchObject({
    stage: 'prepare_questions',
    done: 3,
    total: 15,
    remainingSeconds: 42,
  })
})

it('preserves per-task warnings about default assessment criteria', async () => {
  const warnings = [
    { row: 3, column: 'Задание 2', message: 'Используются общие критерии.' },
  ]
  respond(
    snapshot('ready', { graph: { ...graph, assessment_warnings: warnings } }),
  )
  const result = await readCourseGraph('subject:one', signal())
  expect(result?.view?.assessment_warnings).toEqual(warnings)
})

it.each([
  ['not_requested', null],
  ['pending', 'queued'],
  ['processing', 'running'],
  ['ready', 'succeeded'],
  ['failed', 'failed'],
])(
  'reads the master %s snapshot from GET /graph as %s',
  async (wireStatus, status) => {
    const fetchMock = respond(
      snapshot(wireStatus, {
        error_message: wireStatus === 'failed' ? 'Ошибка сборки' : '',
      }),
    )
    const build = await readLatestCourseGraphBuild('subject:one', signal())
    expect(fetchMock.mock.calls[0][0]).toBe(
      '/api/v1/exam/admin/subjects/subject%3Aone/graph',
    )
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ credentials: 'include' })
    if (status === null) expect(build).toBeNull()
    else
      expect(build).toMatchObject({
        subjectId: 'subject:one',
        status,
        stage: null,
        startedAt: null,
        completedAt: null,
        inputsReady: true,
        stale: false,
        error:
          status === 'failed'
            ? { code: 'GRAPH_BUILD_FAILED', message: 'Ошибка сборки' }
            : null,
      })
  },
)

it('starts with POST /graph/build and parses the returned snapshot', async () => {
  const fetchMock = respond(
    snapshot('pending', { graph: null, updated_at: 0 }),
    202,
  )
  await expect(
    startCourseGraphBuild('subject:one', signal()),
  ).resolves.toMatchObject({ status: 'queued', stage: null })
  expect(fetchMock.mock.calls[0][0]).toBe(
    '/api/v1/exam/admin/subjects/subject%3Aone/graph/build',
  )
  expect(fetchMock.mock.calls[0][1]).toMatchObject({
    method: 'POST',
    credentials: 'include',
  })
})

it('maps native topics and prerequisite direction while preserving the complete graph view', async () => {
  respond(snapshot('ready', { stale: true, inputs_ready: false }))
  await expect(readCourseGraph('subject-1', signal())).resolves.toMatchObject({
    version: 'subject-1:ready:42',
    publishedAt: 42,
    stale: true,
    inputsReady: false,
    view: graph,
    nodes: [
      {
        id: 'intro',
        title: 'Основы',
        content: '',
        prerequisites: [],
        outcomes: ['Определения'],
        questions: [],
      },
      {
        id: 'matrix',
        title: 'Матрицы',
        content: '',
        prerequisites: ['intro'],
        outcomes: [],
        questions: ['Что такое матрица?'],
      },
    ],
  })
})

it('treats a valid snapshot with null graph as the existing empty graph state', async () => {
  respond(snapshot('not_requested', { graph: null, updated_at: 0 }))
  await expect(readCourseGraph('subject-1', signal())).rejects.toMatchObject({
    status: 404,
    code: 'NOT_FOUND',
  })
})

it('does not invent a publication timestamp when master has no persisted graph date', async () => {
  respond(snapshot('ready', { updated_at: 0 }))
  await expect(readCourseGraph('subject-1', signal())).resolves.toMatchObject({
    publishedAt: null,
  })
})

it('changes snapshot identity when the persisted graph changes', async () => {
  respond(snapshot())
  const first = await readLatestCourseGraphBuild('subject-1', signal())
  respond(snapshot('ready', { updated_at: 43 }))
  const second = await readLatestCourseGraphBuild('subject-1', signal())
  expect(second?.id).not.toBe(first?.id)
})

it.each([
  [401, 'UNAUTHORIZED'],
  [403, 'FORBIDDEN'],
  [409, 'GRAPH_BUILD_ACTIVE'],
  [422, 'VALIDATION_ERROR'],
])('preserves structured %i HTTP errors', async (status, code) => {
  respond({ code, message: 'Backend error' }, Number(status))
  await expect(
    startCourseGraphBuild('subject-1', signal()),
  ).rejects.toMatchObject({ status, code, message: 'Backend error' })
})

it('does not mask missing-subject HTTP errors as no prior build', async () => {
  respond({ code: 'NOT_FOUND', message: 'Subject missing' }, 404)
  await expect(
    readLatestCourseGraphBuild('missing', signal()),
  ).rejects.toMatchObject({ status: 404, message: 'Subject missing' })
})

it.each([
  { status: 'ready' },
  snapshot('queued'),
  snapshot('ready', { graph: { topics: [] } }),
])('rejects a malformed master snapshot', async (body) => {
  respond(body)
  await expect(readCourseGraph('subject-1', signal())).rejects.toMatchObject({
    code: 'INVALID_RESPONSE',
  })
})

it('rejects invalid JSON as a typed parsing error', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{')))
  await expect(
    readLatestCourseGraphBuild('subject-1', signal()),
  ).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('preserves cancellation even when fetch has already resolved', async () => {
  const controller = new AbortController()
  respond(snapshot())
  controller.abort()
  await expect(
    readCourseGraph('subject-1', controller.signal),
  ).rejects.toMatchObject({ name: 'AbortError' })
})

it('parses the progress of a running build and its share done', async () => {
  respond(
    snapshot('processing', {
      progress: {
        stage: 'prerequisites',
        stage_index: 3,
        stage_count: 5,
        done: 2,
        total: 8,
      },
    }),
  )
  const build = await readLatestCourseGraphBuild('subject:one', signal())
  expect(build).toMatchObject({
    status: 'running',
    stage: 'prerequisites',
    progress: {
      stage: 'prerequisites',
      stageIndex: 3,
      stageCount: 5,
      done: 2,
      total: 8,
    },
  })
  // Two stages done and a quarter of the third: (2 + 0.25) / 5.
  expect(buildProgressPercent(build!.progress!)).toBeCloseTo(45)
  // A stage without counted work adds nothing until it ends.
  expect(
    buildProgressPercent({
      stage: 'topics',
      stageIndex: 1,
      stageCount: 5,
      done: 0,
      total: 0,
    }),
  ).toBe(0)
})

it('reads a snapshot without progress as no progress', async () => {
  respond(snapshot('processing', { progress: null }))
  await expect(
    readLatestCourseGraphBuild('subject:one', signal()),
  ).resolves.toMatchObject({ stage: null, progress: null })
})

it('imports a competency map as multipart and reads the graph of the map', async () => {
  const fetchMock = respond(
    snapshot('pending', {
      graph: null,
      graph_source: 'none',
      build_source: 'competency_map',
      map: {
        filename: 'map.xlsx',
        sheet: 'Карта',
        learning_revision: 7,
      },
    }),
    202,
  )
  const file = new File(['PK'], 'map.xlsx')
  const build = await importCompetencyMap('course/1', file, signal())
  expect(build.status).toBe('queued')
  expect(build.source).toBe('competency_map')
  const [url, init] = fetchMock.mock.calls[0]
  expect(url).toBe(
    '/api/v1/exam/admin/subjects/course%2F1/graph/competency-map',
  )
  expect(init.method).toBe('POST')
  expect((init.body as FormData).get('file')).toBe(file)
  expect((init.body as FormData).has('sheet')).toBe(false)

  const variants = [
    { question: 'Устно?', content: 'Эталон 1', assessment_guidance: 'Балл' },
    { question: 'Академически?', content: 'Эталон 2' },
  ]
  respond(
    snapshot('ready', {
      graph_source: 'competency_map',
      graph_fingerprint: 'geek-map-a',
      graph_map_revision: 7,
      map: {
        filename: 'map.xlsx',
        sheet: 'Карта',
        fingerprint: 'map-a',
        learning_revision: 7,
      },
      graph: {
        topics: [{ id: 'm', title: 'ОР', question: 'Устно?', variants }],
        prerequisites: [],
        hierarchy: [],
      },
    }),
  )
  const read = await readCourseGraph('course/1', signal())
  expect(read.view?.topics[0].variants).toEqual(variants)
  expect(read.nodes[0].questions).toEqual(['Устно?', 'Академически?'])
  expect(read.source).toBe('competency_map')
  expect(read.map).toEqual({
    filename: 'map.xlsx',
    sheet: 'Карта',
    fingerprint: 'map-a',
    learningRevision: 7,
  })
})

it('keeps the details of a rejected competency map', async () => {
  respond(
    {
      code: 'COMPETENCY_MAP_INVALID',
      message: 'Карта на листе «Карта» содержит ошибки.',
      details: [
        {
          path: 'file',
          code: 'COMPETENCY_MAP_CELL',
          message: 'Строка 3, «Важность»: Ожидается целое число от 1 до 5.',
        },
      ],
    },
    422,
  )
  const error = await importCompetencyMap(
    's',
    new File(['PK'], 'map.xlsx'),
    signal(),
  ).catch((e: unknown) => e)
  expect(error).toBeInstanceOf(CourseGraphApiError)
  expect((error as CourseGraphApiError).code).toBe('COMPETENCY_MAP_INVALID')
  expect((error as CourseGraphApiError).details).toEqual([
    {
      path: 'file',
      code: 'COMPETENCY_MAP_CELL',
      message: 'Строка 3, «Важность»: Ожидается целое число от 1 до 5.',
    },
  ])
})

it('reads saved map metadata after preparation fails before first publication', async () => {
  const map = {
    filename: 'map.xlsx',
    sheet: 'Карта',
    fingerprint: 'filtered-b',
    learning_revision: null,
  }
  respond(
    snapshot('failed', {
      graph: null,
      map,
      build_source: 'competency_map',
      stale: true,
    }),
  )
  expect(await readLatestCourseGraphBuild('s', signal())).toMatchObject({
    status: 'failed',
    source: 'competency_map',
    map: {
      filename: map.filename,
      sheet: map.sheet,
      fingerprint: map.fingerprint,
      learningRevision: null,
    },
  })
})

it('retries the saved map using its fingerprint without importing a file', async () => {
  const fetch = respond(
    snapshot('pending', { build_source: 'competency_map' }),
    202,
  )
  expect(
    await retryCompetencyMap('subject:one', signal(), 'filtered-b'),
  ).toMatchObject({ status: 'queued', source: 'competency_map' })
  expect(fetch.mock.calls[0][0]).toBe(
    '/api/v1/exam/admin/subjects/subject%3Aone/graph/competency-map/retry',
  )
  expect(fetch.mock.calls[0][1]).toMatchObject({
    method: 'POST',
    headers: { 'X-Expected-Map-Fingerprint': 'filtered-b' },
  })
  expect(fetch.mock.calls[0][1].body).toBeUndefined()
})
