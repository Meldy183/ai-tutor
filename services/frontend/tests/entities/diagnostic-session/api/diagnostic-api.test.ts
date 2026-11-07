import { afterEach, expect, it, vi } from 'vitest'
import {
  answerDiagnostic,
  DiagnosticApiError,
  fetchTurnAudio,
  fetchDiagnosticMaterialRaw,
  listDiagnostics,
  readDiagnostic,
  readDiagnosticResult,
  startDiagnostic,
} from '@/entities/diagnostic-session'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

const signal = () => new AbortController().signal

export const apiSession = {
  id: 'd1',
  subject_id: 'subject:ml',
  status: 'active',
  progress_percent: 25,
  current_turn_id: 't3',
  started_at: 1791158400,
  completed_at: null,
  turns: [
    {
      id: 't1',
      line: 1,
      speaker: 'geek',
      kind: 'question',
      node_id: 'a-star',
      source: null,
      text: 'Что такое A*?',
      created_at: 1,
    },
    {
      id: 't2',
      line: 2,
      speaker: 'student',
      kind: 'answer',
      node_id: 'a-star',
      source: 'text',
      text: 'Поиск',
      created_at: 2,
    },
    {
      id: 't3',
      line: 3,
      speaker: 'geek',
      kind: 'rephrase',
      node_id: 'a-star',
      source: null,
      text: 'Иначе: зачем эвристика?',
      created_at: 3,
    },
  ],
}

function stub(response: Response) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

it('starts a diagnostic with the idempotency key and maps the session', async () => {
  const fetchMock = stub(Response.json(apiSession, { status: 201 }))
  const session = await startDiagnostic('subject:ml', 'start:k', signal())
  const [url, init] = fetchMock.mock.calls[0]
  expect(url).toBe('/api/v1/exam/diagnostic-sessions')
  expect(init.method).toBe('POST')
  expect(new Headers(init.headers).get('Idempotency-Key')).toBe('start:k')
  expect(JSON.parse(init.body)).toEqual({ subject_id: 'subject:ml' })
  expect(session).toMatchObject({
    id: 'd1',
    subjectId: 'subject:ml',
    progressPercent: 25,
    currentTurnId: 't3',
    completedAt: null,
  })
  expect(session.turns[1]).toEqual({
    id: 't2',
    line: 2,
    speaker: 'student',
    kind: 'answer',
    nodeId: 'a-star',
    source: 'text',
    text: 'Поиск',
    reply: '',
    meta: false,
    audioIntro: '',
    audioTransition: '',
    replyAudioParts: 0,
    createdAt: 2,
  })
})

it.each([
  [
    { kind: 'text', text: 'Ответ' },
    { turn_id: 't3', text: 'Ответ' },
  ],
  [
    { kind: 'voice', transcriptionId: 'tr1' },
    { turn_id: 't3', transcription_id: 'tr1' },
  ],
  [{ kind: 'dont_know' }, { turn_id: 't3', dont_know: true }],
] as const)(
  'sends the %o answer as the contract body',
  async (answer, body) => {
    const fetchMock = stub(Response.json(apiSession))
    await answerDiagnostic('d/1', 't3', answer, 'answer:k', signal())
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/exam/diagnostic-sessions/d%2F1/answers')
    expect(JSON.parse(init.body)).toEqual(body)
    expect(new Headers(init.headers).get('Idempotency-Key')).toBe('answer:k')
  },
)

it('reads the session, history and result by encoded URLs', async () => {
  let fetchMock = stub(Response.json(apiSession))
  await readDiagnostic('d1', signal())
  expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/exam/diagnostic-sessions/d1')

  fetchMock = stub(
    Response.json({
      items: [
        {
          id: 'd1',
          status: 'completed',
          progress_percent: 100,
          started_at: 1,
          completed_at: 2,
          grade: 'B',
          score_percent: 81,
        },
      ],
    }),
  )
  await expect(listDiagnostics('subject:ml/a', signal())).resolves.toEqual([
    {
      id: 'd1',
      status: 'completed',
      progressPercent: 100,
      startedAt: 1,
      completedAt: 2,
      grade: 'B',
      scorePercent: 81,
    },
  ])
  expect(fetchMock.mock.calls[0][0]).toBe(
    '/api/v1/exam/diagnostic-sessions?subject_id=subject%3Aml%2Fa',
  )

  stub(
    Response.json({
      session_id: 'd1',
      grade: null,
      score_percent: null,
      score: 0,
      max_score: 0,
      nodes: [
        {
          node_id: 'a',
          title: 'A*',
          status: 'insufficient',
          grade: null,
          top_level: true,
          assisted: false,
          score: null,
          max_score: null,
          marks: [],
          reply: '',
        },
        {
          node_id: 'b',
          title: 'Эвристика',
          status: 'gap',
          grade: 'D',
          top_level: false,
          requires: ['c'],
          assisted: false,
          score: null,
          max_score: null,
          marks: [],
          reply: '',
        },
      ],
      gaps: [{ node_id: 'b', title: 'Эвристика' }],
    }),
  )
  await expect(readDiagnosticResult('d1', signal())).resolves.toEqual({
    sessionId: 'd1',
    grade: null,
    scorePercent: null,
    score: 0,
    maxScore: 0,
    topics: [
      {
        nodeId: 'a',
        title: 'A*',
        status: 'insufficient',
        grade: null,
        topLevel: true,
        // An older backend sends no prerequisites.
        requires: [],
        assisted: false,
        score: null,
        maxScore: null,
        marks: [],
        reply: '',
      },
      {
        nodeId: 'b',
        title: 'Эвристика',
        status: 'gap',
        grade: 'D',
        topLevel: false,
        requires: ['c'],
        assisted: false,
        score: null,
        maxScore: null,
        marks: [],
        reply: '',
      },
    ],
    gaps: [
      {
        nodeId: 'b',
        title: 'Эвристика',
        weeks: [],
        sources: [],
        learningMaterials: { theory: [], practice: [], additional: [] },
      },
    ],
    // An older backend sends no weak spots.
    weakSpots: [],
  })
})

it('maps provenance snapshots and downloads only through the diagnostic route', async () => {
  const ref = {
    material_id: 'm1',
    filename: 'lecture.pdf',
    kind: 'lecture',
    kind_label: 'Лекция',
    week: 2,
    material_revision: 'r1',
  }
  stub(
    Response.json({
      session_id: 'd1',
      grade: null,
      score_percent: null,
      score: 0,
      max_score: 0,
      nodes: [],
      gaps: [
        {
          node_id: 'n',
          title: 'Матрицы',
          weeks: [2],
          sources: [
            {
              ...ref,
              block_id: 1,
              start: 0,
              end: 5,
              text: 'Матрица',
              page: null,
            },
          ],
          learning_materials: { theory: [ref], practice: [], additional: [] },
        },
      ],
    }),
  )
  const result = await readDiagnosticResult('d1', signal())
  expect(result.gaps[0]).toMatchObject({
    weeks: [2],
    sources: [{ materialId: 'm1', kindLabel: 'Лекция' }],
    learningMaterials: { theory: [{ materialId: 'm1' }] },
  })
  const fetchMock = stub(new Response('%PDF'))
  await fetchDiagnosticMaterialRaw('d1', 'm1', signal())
  expect(fetchMock.mock.calls[0][0]).toBe(
    '/api/v1/exam/diagnostic-sessions/d1/materials/m1/raw',
  )
})

it('keeps the error code and rejects responses outside the contract', async () => {
  stub(
    Response.json(
      { code: 'GRAPH_NOT_READY', message: 'нет графа' },
      { status: 409 },
    ),
  )
  await expect(startDiagnostic('s', 'k', signal())).rejects.toMatchObject({
    status: 409,
    code: 'GRAPH_NOT_READY',
  })
  stub(Response.json({ ...apiSession, status: 'paused' }))
  await expect(readDiagnostic('d1', signal())).rejects.toBeInstanceOf(
    DiagnosticApiError,
  )
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')))
  await expect(readDiagnostic('d1', signal())).rejects.toMatchObject({
    code: 'NETWORK',
  })
})

it('fetches the WAV of a Geek turn', async () => {
  const fetchMock = stub(
    new Response(new Uint8Array([82, 73, 70, 70]), {
      headers: { 'Content-Type': 'audio/wav' },
    }),
  )
  const audio = await fetchTurnAudio('d1', 't 1', signal())
  expect(fetchMock.mock.calls[0][0]).toBe(
    '/api/v1/exam/diagnostic-sessions/d1/turns/t%201/audio',
  )
  expect(audio.type).toBe('audio/wav')
  const partMock = stub(
    new Response(new Uint8Array([82, 73, 70, 70]), {
      headers: { 'Content-Type': 'audio/wav' },
    }),
  )
  await fetchTurnAudio('d1', 't1', signal(), 'reply')
  expect(partMock.mock.calls[0][0]).toBe(
    '/api/v1/exam/diagnostic-sessions/d1/turns/t1/audio?part=reply',
  )
  stub(Response.json({ code: 'X' }))
  await expect(fetchTurnAudio('d1', 't1', signal())).rejects.toMatchObject({
    code: 'INVALID_RESPONSE',
  })
})

function delayedTurnAudio(delay: number) {
  vi.useFakeTimers()
  vi.spyOn(AbortSignal, 'timeout').mockImplementation((milliseconds) => {
    const controller = new AbortController()
    setTimeout(
      () => controller.abort(new DOMException('Timed out', 'TimeoutError')),
      milliseconds,
    )
    return controller.signal
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(
      (_url, options: RequestInit) =>
        new Promise<Response>((resolve, reject) => {
          const signal = options.signal!
          const timer = setTimeout(
            () =>
              resolve(
                new Response(new Uint8Array([82, 73, 70, 70]), {
                  headers: { 'Content-Type': 'audio/wav' },
                }),
              ),
            delay,
          )
          const abort = () => {
            clearTimeout(timer)
            reject(signal.reason)
          }
          if (signal.aborted) abort()
          else signal.addEventListener('abort', abort, { once: true })
        }),
    ),
  )
}

it('waits for a queued multi-part turn synthesis within the backend budget', async () => {
  delayedTurnAudio(325_000)
  const checked = expect(
    fetchTurnAudio('d1', 't1', signal()),
  ).resolves.toMatchObject({ type: 'audio/wav', size: 4 })
  await vi.advanceTimersByTimeAsync(325_000)
  await checked
})

it('bounds turn audio waiting by 360 seconds', async () => {
  delayedTurnAudio(400_000)
  const checked = expect(
    fetchTurnAudio('d1', 't1', signal()),
  ).rejects.toMatchObject({ code: 'TIMEOUT' })
  await vi.advanceTimersByTimeAsync(360_000)
  await checked
})

it('cancels queued turn audio immediately when the listener leaves', async () => {
  delayedTurnAudio(325_000)
  const controller = new AbortController()
  const checked = expect(
    fetchTurnAudio('d1', 't1', controller.signal),
  ).rejects.toMatchObject({ name: 'AbortError' })
  await vi.advanceTimersByTimeAsync(150_000)
  controller.abort()
  await checked
})
