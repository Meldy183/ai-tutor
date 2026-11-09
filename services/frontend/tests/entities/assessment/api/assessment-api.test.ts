import { afterEach, expect, it, vi } from 'vitest'

import { evaluateAnswer } from '@/entities/assessment'

afterEach(() => vi.unstubAllGlobals())

const savedTask = {
  variantId: 'saved-variant-1',
  variantTaskId: 'saved-position-1',
  question: 'Вопрос',
  options: [],
  voiceInstruction: 'Ответьте',
}
const validResult = {
  score: 1,
  max_score: 1,
  verdict: 'correct',
  criterion_results: [
    { key: 'instruction_following', satisfied: true, explanation: 'Верно.' },
  ],
  feedback: 'Верно. Причина. Совет.',
}

it('preserves the basic scale without clamping', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({ ok: true, json: async () => validResult }),
  )
  const grade = await evaluateAnswer(
    'tr-1',
    savedTask,
    new AbortController().signal,
  )
  expect(grade).toMatchObject({ score: 1, maxScore: 1, verdict: 'correct' })
})

it.each([
  { ...validResult, score: 2 },
  { ...validResult, max_score: undefined },
  { ...validResult, verdict: 'partial' },
  {
    ...validResult,
    criterion_results: [
      { key: 'instruction_following', explanation: 'Верно.' },
    ],
  },
  {
    ...validResult,
    criterion_results: [
      { key: 'instruction_following', satisfied: null, explanation: 'Верно.' },
    ],
  },
  { ...validResult, feedback: ['Одна строка.'] },
])('rejects incomplete or inconsistent grading result %#', async (result) => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({ ok: true, json: async () => result }),
  )
  await expect(
    evaluateAnswer('tr-1', savedTask, new AbortController().signal),
  ).rejects.toThrow('Invalid assessment response')
})

it('does not send an assessment without saved IDs', async () => {
  const fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
  await expect(
    evaluateAnswer(
      'tr-1',
      { ...savedTask, variantId: '' },
      new AbortController().signal,
    ),
  ).rejects.toThrow('Saved variant')
  expect(fetchMock).not.toHaveBeenCalled()
})

it('sends saved variant IDs and transcription id and preserves structured grading result', async () => {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({
      score: 2,
      max_score: 2,
      verdict: 'correct',
      criterion_results: [
        {
          key: 'task_type',
          satisfied: true,
          explanation: 'Тип задачи назван правильно.',
        },
        {
          key: 'justification',
          satisfied: true,
          explanation: 'Выбор объяснён.',
        },
      ],
      feedback: 'Верно. Оба критерия выполнены. Закрепите тему.',
    }),
  })
  vi.stubGlobal('fetch', fetchMock)

  const task = {
    variantId: 'saved-variant-1',
    variantTaskId: 'saved-position-1',
    question: 'Вопрос',
    options: ['Классификация', 'Регрессия'],
    voiceInstruction: 'Назовите тип',
  }
  const grade = await evaluateAnswer('tr-1', task, new AbortController().signal)

  expect(grade.score).toBe(2)
  expect(grade.maxScore).toBe(2)
  expect(grade.verdict).toBe('correct')
  expect(grade.criterionResults).toHaveLength(2)

  const [url, init] = fetchMock.mock.calls[0]
  expect(url).toBe('/api/v1/learn/assessments/evaluate')
  expect(init.credentials).toBe('include')
  expect(JSON.parse(init.body)).toEqual({
    transcription_id: 'tr-1',
    variant_id: 'saved-variant-1',
    variant_task_id: 'saved-position-1',
  })
})

it('rejects malformed structured grading response', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        score: 2,
        max_score: 2,
        verdict: 'correct',
        criterion_results: [],
        feedback: 'Верно. Причина. Совет.',
      }),
    }),
  )

  await expect(
    evaluateAnswer(
      'tr-1',
      {
        variantId: 'saved-variant-1',
        variantTaskId: 'saved-position-1',
        question: 'Вопрос',
        options: [],
        voiceInstruction: 'Ответьте',
      },
      new AbortController().signal,
    ),
  ).rejects.toThrow('Invalid assessment response')
})

it('accepts two points for a fully correct main answer', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ ...validResult, score: 2, max_score: 2 }),
    }),
  )
  await expect(
    evaluateAnswer('tr-1', savedTask, new AbortController().signal),
  ).resolves.toMatchObject({ score: 2, maxScore: 2, verdict: 'correct' })
})
it('rejects a three-point main scale', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        ...validResult,
        score: 3,
        max_score: 3,
        verdict: 'correct',
      }),
    }),
  )
  await expect(
    evaluateAnswer('tr-1', savedTask, new AbortController().signal),
  ).rejects.toThrow('Invalid assessment response')
})
