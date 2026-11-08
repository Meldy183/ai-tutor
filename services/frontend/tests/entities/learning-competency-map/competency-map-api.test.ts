import { afterEach, expect, it, vi } from 'vitest'
import {
  readCompetencyMap,
  readCompetencyMapOutline,
} from '@/entities/learning-competency-map'

afterEach(() => vi.unstubAllGlobals())

it('reads a map from the encoded subject resource URL', async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      Response.json({ revision: 0, imported_at: null, competencies: [] }),
    )
  vi.stubGlobal('fetch', fetchMock)
  await expect(
    readCompetencyMap('subject:a/b', new AbortController().signal),
  ).resolves.toMatchObject({ importedAt: null })
  expect(fetchMock.mock.calls[0][0]).toBe(
    '/api/v1/learn/subjects/subject%3Aa%2Fb/competency-map',
  )
})

it('reads the server-selected diagnostic blocks without exposing task content', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      Response.json({
        competencies: [
          {
            id: 'c1',
            name: 'Первая',
            display_name: 'Машинное обучение',
            constituents: [
              {
                id: 't1',
                name: 'Выбранная тема',
                display_name: 'Нейронные сети',
                outcomes: [
                  {
                    id: 'o1',
                    name: 'Основной результат',
                    role: 'main',
                    question: 'Не показывать вопрос',
                  },
                  { id: 'o2', name: 'Базовый результат', role: 'basic' },
                ],
              },
            ],
          },
        ],
      }),
    ),
  )
  await expect(
    readCompetencyMapOutline('course/a', new AbortController().signal),
  ).resolves.toEqual({
    competencies: [
      {
        id: 'c1',
        name: 'Первая',
        display_name: 'Машинное обучение',
        constituents: [
          {
            id: 't1',
            name: 'Выбранная тема',
            display_name: 'Нейронные сети',
            outcomes: [
              { id: 'o1', name: 'Основной результат', role: 'main' },
              { id: 'o2', name: 'Базовый результат', role: 'basic' },
            ],
          },
        ],
      },
    ],
  })
  expect(vi.mocked(fetch).mock.calls[0][0]).toBe(
    '/api/v1/learn/subjects/course%2Fa/diagnostic-outline',
  )
})

it('reports malformed map data instead of showing an empty plan', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValue(Response.json({ competencies: [{ id: 'broken' }] })),
  )
  await expect(
    readCompetencyMapOutline('course', new AbortController().signal),
  ).rejects.toThrow()
})

it('allows several slow label batches while retaining caller cancellation', async () => {
  vi.useFakeTimers()
  const timeout = vi.spyOn(AbortSignal, 'timeout').mockImplementation((ms) => {
    const controller = new AbortController()
    setTimeout(
      () => controller.abort(new DOMException('Timed out', 'TimeoutError')),
      ms,
    )
    return controller.signal
  })
  try {
    let finish!: (response: Response) => void
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (_url, options?: RequestInit) =>
          new Promise<Response>((resolve, reject) => {
            finish = resolve
            options?.signal?.addEventListener(
              'abort',
              () => reject(options.signal?.reason),
              { once: true },
            )
          }),
      ),
    )
    const controller = new AbortController()
    const pending = readCompetencyMapOutline('course', controller.signal)
    let settled = false
    void pending.then(
      () => {
        settled = true
      },
      () => {
        settled = true
      },
    )
    await vi.advanceTimersByTimeAsync(5 * 60_000)
    expect(settled).toBe(false)
    finish(Response.json({ competencies: [] }))
    await expect(pending).resolves.toEqual({ competencies: [] })
    const cancelled = readCompetencyMapOutline('course', controller.signal)
    controller.abort(new DOMException('Cancelled', 'AbortError'))
    await expect(cancelled).rejects.toMatchObject({ name: 'AbortError' })
  } finally {
    timeout.mockRestore()
    vi.useRealTimers()
  }
})
