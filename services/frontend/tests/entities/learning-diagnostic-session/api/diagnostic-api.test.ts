import { afterEach, expect, it, vi } from 'vitest'
import {
  createSubmission,
  DiagnosticApiError,
  readDiagnosticAudio,
  regenerateDiagnosticAudio,
  readDiagnostic,
  readDiagnosticFeedback,
  submitDiagnostic,
  createVariant,
} from '@/entities/learning-diagnostic-session'

const accepted = {
  session_id: 's1',
  status: 'completed',
  completed_tasks: 1,
  skipped_tasks: 2,
  total_tasks: 3,
  score: 2,
  grader_score: 2,
  grader_max_score: 2,
  verdict: 'correct',
  criterion_results: [
    { key: 'choice', satisfied: true, explanation: 'Верно.' },
  ],
  feedback: 'Верно. Причина. Совет.',
}
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('creates a variant for the explicitly selected subject', async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(Response.json({ id: 'variant-1' }, { status: 201 }))
  vi.stubGlobal('fetch', fetchMock)
  await expect(
    createVariant('subject:ml', 'key-1', new AbortController().signal),
  ).resolves.toEqual({ id: 'variant-1' })
  expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/learn/variants')
  expect(fetchMock.mock.calls[0][1].method).toBe('POST')
  expect(fetchMock.mock.calls[0][1].headers).toMatchObject({
    'Idempotency-Key': 'key-1',
    'Content-Type': 'application/json',
  })
  expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
    subject_id: 'subject:ml',
  })
})

it('accepts an empty strengths list serialized as null by the backend', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      Response.json({
        session_id: 'session-1',
        diagnostic_score: 0,
        maximum_score: 4,
        score_percentage: 0,
        summary: 'Повторите базовые темы.',
        strengths: null,
        confirmed_gaps: [],
        partial_competencies: [],
        unverified_competencies: [],
        training_recommendations: [],
        generated_at: 1791538582,
      }),
    ),
  )
  await expect(
    readDiagnosticFeedback('session-1', new AbortController().signal),
  ).resolves.toMatchObject({
    strengths: [],
    summary: 'Повторите базовые темы.',
  })
})

it('regenerates only the expected task and returns ready metadata', async () => {
  const response = {
    variant_task_id: 't1',
    status: 'ready',
    audio_url: '/learn/task-audio/audio-1/file',
  }
  const fetchMock = vi.fn().mockResolvedValue(Response.json(response))
  vi.stubGlobal('fetch', fetchMock)
  await expect(
    regenerateDiagnosticAudio('s1', 't1', new AbortController().signal),
  ).resolves.toEqual(response)
  expect(fetchMock.mock.calls[0][0]).toBe(
    '/api/v1/learn/diagnostic-sessions/s1/current/audio/regenerate',
  )
  expect(fetchMock.mock.calls[0][1].method).toBe('POST')
  expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
    variant_task_id: 't1',
  })
  expect(fetchMock.mock.calls[0][1].credentials).toBe('include')
  expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(false)
})

it('reads task audio metadata with the expected task ID and accepts only ready URLs', async () => {
  const response = {
    variant_task_id: 't1',
    status: 'ready',
    audio_url: '/learn/task-audio/audio-1/file',
  }
  const fetchMock = vi.fn().mockResolvedValue(Response.json(response))
  vi.stubGlobal('fetch', fetchMock)
  await expect(
    readDiagnosticAudio('s1', 't1', new AbortController().signal),
  ).resolves.toEqual(response)
  expect(fetchMock.mock.calls[0][0]).toBe(
    '/api/v1/learn/diagnostic-sessions/s1/current/audio?variant_task_id=t1',
  )
  expect(fetchMock.mock.calls[0][1].method).toBeUndefined()

  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValue(
        Response.json({ ...response, variant_task_id: 'stale-task' }),
      ),
  )
  await expect(
    readDiagnosticAudio('s1', 't1', new AbortController().signal),
  ).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      Response.json({
        ...response,
        audio_url: 'https://evil.example/audio.wav',
      }),
    ),
  )
  await expect(
    readDiagnosticAudio('s1', 't1', new AbortController().signal),
  ).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('accepts cancelled task-audio metadata without a file URL', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      Response.json({
        variant_task_id: 't1',
        status: 'cancelled',
        audio_url: null,
      }),
    ),
  )
  await expect(
    readDiagnosticAudio('s1', 't1', new AbortController().signal),
  ).resolves.toMatchObject({ status: 'cancelled', audio_url: null })
})

it('allows an answer to finish after sequential transcription and grading exceed two minutes', async () => {
  vi.useFakeTimers()
  vi.spyOn(AbortSignal, 'timeout').mockImplementation((ms) => {
    const request = new AbortController()
    setTimeout(
      () => request.abort(new DOMException('Timeout', 'TimeoutError')),
      ms,
    )
    return request.signal
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(
      (_url, init: RequestInit) =>
        new Promise((resolve, reject) => {
          const timer = setTimeout(
            () => resolve(Response.json(accepted)),
            150_000,
          )
          init.signal?.addEventListener('abort', () => {
            clearTimeout(timer)
            reject(init.signal?.reason)
          })
        }),
    ),
  )
  const result = submitDiagnostic(
    createSubmission(
      's1',
      't1',
      new Blob(['audio'], { type: 'audio/webm' }),
      'main',
    ),
    new AbortController().signal,
  ).then(
    (value) => ({ value }),
    (error) => ({ error }),
  )
  await vi.advanceTimersByTimeAsync(150_000)
  expect(await result).toMatchObject({
    value: { session_id: 's1', score: 2, status: 'completed' },
  })
})

it.each([
  { ...accepted, completed_tasks: 5 },
  { ...accepted, feedback: ['one'] },
  { ...accepted, session_id: 'other-user-session' },
  { ...accepted, score: 9 },
  { ...accepted, grader_max_score: 1 },
])(
  'rejects malformed accepted responses without recording a score',
  async (response) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json(response)))
    await expect(
      submitDiagnostic(
        createSubmission('s1', 't1', new Blob(['audio']), 'main'),
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  },
)

it('rejects an active session missing the current task', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      Response.json({
        session_id: 's1',
        status: 'active',
        completed_tasks: 0,
        skipped_tasks: 0,
        total_tasks: 3,
      }),
    ),
  )
  await expect(
    readDiagnostic('s1', new AbortController().signal),
  ).rejects.toBeInstanceOf(DiagnosticApiError)
})

it('sends the confirmed transcript with the same idempotency key instead of repeating STT', async () => {
  const fetcher = vi.fn().mockResolvedValue(Response.json(accepted))
  vi.stubGlobal('fetch', fetcher)
  const submission = createSubmission(
    's1',
    't1',
    new Blob(['voice'], { type: 'audio/webm' }),
    'main',
  )
  submission.transcriptionId = 'preview-id'
  await submitDiagnostic(submission, new AbortController().signal)
  expect(fetcher.mock.calls[0][0]).toBe(
    '/api/v1/learn/diagnostic-sessions/s1/answers/transcription',
  )
  expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({
    variant_task_id: 't1',
    transcription_id: 'preview-id',
  })
  expect(fetcher.mock.calls[0][1].headers).toMatchObject({
    'Idempotency-Key': submission.key,
    'Content-Type': 'application/json',
  })
})
