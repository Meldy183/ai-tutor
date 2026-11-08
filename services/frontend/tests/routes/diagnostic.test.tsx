import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import {
  createMemoryHistory,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { beforeAll, beforeEach, afterEach, expect, it, vi } from 'vitest'
import { createQueryClient } from '@/bootstrap/providers'
import * as audio from '@/shared/lib'
import { validWavBlob } from '../support/audio'
import { routeTree } from '@/routeTree.gen'

// Compile the lazy page before timing user interactions. Cold transforms on
// a Windows bind mount can otherwise consume the route's entire wait budget.
beforeAll(async () => {
  await import('@/pages/trainer')
}, 30000)

vi.mock('@/shared/lib', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/shared/lib')>()),
  createVoiceWaveform: () => ({ setStream: () => {}, dispose: () => {} }),
}))
const main = {
  variant_task_id: 'v-main',
  source_task_id: 'bank-main',
  role: 'main',
  competency_id: 'c1',
  competency_name: 'Модели',
  constituent_id: 's1',
  constituent_name: 'Выбор модели',
  outcome_id: 'o1',
  outcome_name: 'Различать модели',
  question: 'Настоящий вопрос из банка',
  options: ['Первый вариант', 'Второй вариант'],
  voice_instruction: 'Назовите модель и объясните решение.',
}
const basic = {
  ...main,
  variant_task_id: 'v-basic',
  source_task_id: 'bank-basic',
  role: 'basic',
  question: 'Базовый вопрос из банка',
  options: [],
}
const basic2 = {
  ...basic,
  variant_task_id: 'v-basic2',
  source_task_id: 'bank-basic2',
  question: 'Второй базовый вопрос',
}
const progress = {
  session_id: 'session-real',
  status: 'active',
  completed_tasks: 0,
  skipped_tasks: 0,
  total_tasks: 3,
  current: main,
}
const criteria = [
  { key: 'choice', satisfied: true, explanation: 'Модель названа.' },
]
const feedback = 'Серверная оценка. Серверное объяснение. Серверный совет.'
const resultBody = {
  session_id: 'session-real',
  status: 'completed',
  variant_id: 'variant-real',
  map_revision: 2,
  included_competency_count: 1,
  skipped_competencies: [],
  completed_tasks: 1,
  total_tasks: 3,
  diagnostic_score: 3,
  maximum_score: 3,
  report_score: 5,
  report_max_score: 5,
  training_needed: false,
  grade: 'A',
  score_percent: 100,
  answers: [
    {
      variant_task_id: 'v-main',
      source_task_id: 'bank-main',
      competency_id: 'c1',
      outcome_id: 'o1',
      role: 'main',
      task: main,
      transcription_id: 'tr1',
      text: 'Настоящая расшифровка',
      grader_score: 3,
      grader_max_score: 3,
      score: 3,
      verdict: 'correct',
      criterion_results: criteria,
      feedback,
      created_at: 1791200000,
    },
  ],
  untested_basics: [basic, basic2],
}
async function home({ start = true }: { start?: boolean } = {}) {
  const router = createRouter({
    context: { queryClient: createQueryClient() },
    history: createMemoryHistory({ initialEntries: ['/learn'] }),
    routeTree,
  })
  const view = render(<RouterProvider router={router} />)
  if (start) {
    await waitFor(
      () =>
        expect(
          screen.getByRole('button', { name: 'Начать обучение' }),
        ).toBeEnabled(),
      { timeout: 5000 },
    )
    fireEvent.click(screen.getByRole('button', { name: 'Начать обучение' }))
  }
  return view
}

function reportActiveLearningSession(requests: ReturnType<typeof server>) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/subjects/subject%3Atest/learning-state'))
        return Response.json({
          subject_id: 'subject:test',
          subject_name: 'Предмет теста',
          diagnostic_status: 'active',
          diagnostic_completed: false,
          training_available: false,
          active_session_id: 'session-real',
        })
      return requests(url, init)
    }),
  )
}

it('keeps the active learning intent while the diagnostic is open', async () => {
  server()
  await home()
  await waitFor(() =>
    expect(
      JSON.parse(sessionStorage.getItem('ai-tutor:learning-home') ?? 'null')
        ?.sessionId,
    ).toBe('session-real'),
  )
  const storedIntent = sessionStorage.getItem('ai-tutor:learning-home')
  expect(storedIntent).not.toBeNull()
  fireEvent.keyDown(
    screen.getByRole('button', { name: 'Меню профиля: Студент' }),
    { key: 'Enter' },
  )
  expect(
    await screen.findByRole('menuitem', { name: 'К выбору режима' }),
  ).toBeVisible()
  expect(screen.getAllByText('Предмет теста', { exact: true })).toHaveLength(1)
  expect(sessionStorage.getItem('ai-tutor:learning-home')).toBe(storedIntent)
})

function server(
  answerStatus = 200,
  totalTasks = 3,
  feedbackStatus = 200,
  strengths: string[] | null = ['Вы уверенно различаете модели.'],
  summary = 'Итоговое педагогическое резюме.',
  mainScore = 3,
) {
  const sessionProgress = { ...progress, total_tasks: totalTasks }
  let answered = false
  const requests = vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/diagnostic-outline'))
      return Response.json({
        competencies: [
          {
            id: main.competency_id,
            name: main.competency_name,
            constituents: [
              {
                id: main.constituent_id,
                name: main.constituent_name,
                outcomes: [main, basic, basic2].map((task) => ({
                  id: task.outcome_id,
                  name: task.outcome_name,
                  role: task.role,
                })),
              },
            ],
          },
        ],
      })
    if (url.endsWith('/subjects'))
      return Response.json([
        {
          id: 'subject:test',
          name: 'Предмет теста',
          ready: true,
          learning_ready: true,
          exam_ready: true,
        },
      ])
    if (url.endsWith('/subjects/subject%3Atest/learning-state'))
      return Response.json({
        subject_id: 'subject:test',
        subject_name: 'Предмет теста',
        diagnostic_status: 'not_started',
        diagnostic_completed: false,
        training_available: false,
      })
    if (url.endsWith('/auth/me'))
      return Response.json({
        id: 'student-real',
        email: 'student@example.com',
        display_name: 'Студент',
        role: 'student',
      })
    if (url.endsWith('/variants') && init?.method === 'POST')
      return Response.json({ id: 'variant-real' }, { status: 201 })
    if (url.endsWith('/diagnostic-sessions') && init?.method === 'POST')
      return Response.json(sessionProgress, { status: 201 })
    if (url.includes('/current/audio?'))
      return Response.json({
        variant_task_id: new URL(url, 'http://test').searchParams.get(
          'variant_task_id',
        ),
        status: 'ready',
        audio_url: '/learn/task-audio/audio-main/file',
      })
    if (url.includes('/learn/task-audio/') && url.endsWith('/file'))
      return new Response(await validWavBlob().arrayBuffer(), {
        headers: { 'Content-Type': 'audio/wav' },
      })
    if (url.endsWith('/skip')) {
      answered = true
      return Response.json({
        ...sessionProgress,
        status: 'active',
        current: basic,
        completed_tasks: 1,
        answer_skipped: true,
        text: 'Я не знаю. Пропустить',
        score: 0,
        grader_score: 0,
        grader_max_score: 3,
        verdict: 'incorrect',
        criterion_results: [
          { key: 'skipped', satisfied: false, explanation: 'Вопрос пропущен.' },
        ],
        feedback:
          'Вопрос пропущен. Ответ оценён в 0 баллов. Продолжите со следующим вопросом.',
      })
    }
    if (url.endsWith('/voice/transcriptions'))
      return Response.json({ id: 'preview-id', text: 'Настоящая расшифровка' })
    if (url.endsWith('/answers/transcription')) {
      if (answerStatus !== 200) {
        answerStatus = 200
        return Response.json(
          { code: 'GRADING_FAILED', message: 'Сбой модели' },
          { status: 502 },
        )
      }
      answered = true
      return Response.json({
        ...sessionProgress,
        status: 'completed',
        current: undefined,
        completed_tasks: 1,
        skipped_tasks: totalTasks - 1,
        text: 'Настоящая расшифровка',
        score: 3,
        grader_score: 3,
        grader_max_score: 3,
        verdict: 'correct',
        criterion_results: criteria,
        feedback,
      })
    }
    if (url.endsWith('/result'))
      return Response.json({
        ...resultBody,
        total_tasks: totalTasks,
        diagnostic_score: mainScore,
        training_needed: mainScore < 3,
        answers: resultBody.answers.map((answer) => ({
          ...answer,
          score: mainScore,
          grader_score: mainScore,
        })),
        untested_basics: resultBody.untested_basics.slice(0, totalTasks - 1),
      })
    if (url.endsWith('/feedback')) {
      if (feedbackStatus !== 200) {
        feedbackStatus = 200
        return Response.json({ code: 'FEEDBACK_FAILED' }, { status: 503 })
      }
      return Response.json({
        session_id: 'session-real',
        diagnostic_score: 3,
        maximum_score: 3,
        score_percentage: 100,
        summary,
        strengths,
        confirmed_gaps: [
          {
            competency_id: 'c1',
            competency_name: 'Модели',
            outcome_id: 'o1',
            outcome_name: 'Различать модели',
            taxonomy_code: 'apply',
            importance: 3,
            failed_criteria: [],
            advice: 'Повторный совет к рекомендации.',
          },
          {
            competency_id: 'c1',
            competency_name: 'Модели',
            outcome_id: 'o2',
            outcome_name: 'Выбирать метрику',
            taxonomy_code: 'apply',
            importance: 3,
            failed_criteria: [],
            advice: 'Сравните метрики на двух задачах.',
          },
        ],
        partial_competencies: [],
        unverified_competencies: [
          {
            competency_id: 'c9',
            competency_name: 'Непроверенная компетенция',
            code: 'NOT_ASKED',
          },
        ],
        training_recommendations: [
          {
            competency_id: 'c1',
            competency_name: 'Модели',
            outcome_id: 'o1',
            outcome_name: 'Различать модели',
            priority: 1,
            rationale: 'Закрепите выбор модели на новых примерах.',
          },
        ],
        generated_at: 1791200000,
      })
    }
    if (url.endsWith('/diagnostic-sessions/session-real'))
      return Response.json(
        answered
          ? {
              ...sessionProgress,
              status: 'completed',
              current: undefined,
              completed_tasks: 1,
              skipped_tasks: totalTasks - 1,
            }
          : sessionProgress,
      )
    throw new Error(`Unexpected request: ${url}`)
  })
  vi.stubGlobal('fetch', requests)
  return requests
}
beforeEach(() => {
  sessionStorage.clear()
  vi.spyOn(audio, 'startRecording').mockResolvedValue({
    stream: {} as MediaStream,
    stop: vi
      .fn()
      .mockResolvedValue(
        new Blob(['recorded-answer'], { type: 'audio/webm;codecs=opus' }),
      ),
    dispose: vi.fn(),
  })
  vi.spyOn(audio, 'createAudioUrl').mockReturnValue({
    url: 'blob:recording',
    dispose: vi.fn(),
  })
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})
it('runs real diagnostic audio, obeys skipped basics and displays the server total and transcript', async () => {
  const requests = server()
  await home()
  expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent(
    'Настоящий вопрос из банка',
  )
  expect(screen.getByText('Первый вариант')).toBeVisible()
  // The instruction button sits in the task card, under the text and above
  // the answer options.
  const listen = screen.getByRole('button', {
    name: /Прослушать инструкцию|Остановить|Отменить/,
  })
  const card = screen.getByRole('heading', { level: 1 }).closest('section')!
  expect(card).toContainElement(listen)
  // There is no way to skip a question: every task is answered by voice.
  expect(
    screen.queryByRole('button', { name: 'Пропустить вопрос' }),
  ).not.toBeInTheDocument()
  expect(
    listen.compareDocumentPosition(
      screen.getByRole('heading', { name: 'Варианты ответа' }),
    ) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy()
  expect(screen.getByRole('heading', { name: 'Ваш ответ' })).toBeVisible()
  expect(
    screen.getByText('Здесь появится расшифровка вашего ответа.'),
  ).toBeVisible()
  expect(screen.queryByText('Голосовая инструкция')).not.toBeInTheDocument()
  // Progress is a percent bar under the header instead of a question list.
  expect(
    screen.getByRole('progressbar', { name: 'Прогресс проверки' }),
  ).toHaveAttribute('aria-valuenow', '0')
  expect(screen.getByText('0%')).toBeVisible()
  expect(
    screen.queryByRole('navigation', { name: 'Вопросы диагностики' }),
  ).not.toBeInTheDocument()
  // The header names only the course, without the question or topic.
  expect(document.querySelector('[aria-current="page"]')).toBeNull()
  // The microphone circle is the record button; there is no separate one.
  expect(
    screen.getByText('Нажмите на микрофон, чтобы начать запись'),
  ).toBeVisible()
  expect(screen.getAllByRole('button', { name: 'Начать запись' })).toHaveLength(
    1,
  )
  fireEvent.click(screen.getByRole('button', { name: 'Начать запись' }))
  const stop = await screen.findByRole('button', { name: 'Завершить запись' })
  expect(screen.getByRole('timer')).toHaveTextContent('00:00')
  expect(
    screen.queryByText('Нажмите на микрофон, чтобы начать запись'),
  ).not.toBeInTheDocument()
  fireEvent.click(stop)
  expect(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  ).toBeVisible()
  expect(
    requests.mock.calls.filter(([url]) =>
      url.endsWith('/answers/transcription'),
    ),
  ).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: 'Отправить ответ' }))
  // The answer is only confirmed: no score, verdict or feedback per task.
  expect(await screen.findByText('Ответ принят')).toBeVisible()
  expect(screen.queryByText(feedback)).not.toBeInTheDocument()
  expect(screen.queryByText('3 / 3')).not.toBeInTheDocument()
  expect(screen.queryByText('Верно')).not.toBeInTheDocument()
  expect(screen.queryByText('Модель названа.')).not.toBeInTheDocument()
  expect(screen.queryByText(/Критерий (не )?выполнен/)).not.toBeInTheDocument()
  // The main answer is done and both basics were skipped by the server.
  expect(screen.getByRole('progressbar')).toHaveAttribute(
    'aria-valuenow',
    '100',
  )
  expect(screen.getByText('Настоящая расшифровка')).toBeVisible()
  expect(
    screen.queryByText('Здесь появится расшифровка вашего ответа.'),
  ).not.toBeInTheDocument()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  expect(
    await screen.findByRole('heading', { name: 'Итог диагностики' }),
  ).toBeVisible()
  expect(screen.getByText('5 из 5 баллов')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Подтянуть слабые темы' }),
  ).not.toBeInTheDocument()
  // The task review is folded until the student opens it.
  expect(screen.queryByText('Настоящая расшифровка')).not.toBeInTheDocument()
  fireEvent.click(
    await screen.findByRole('button', { name: /Посмотреть мои ответы/ }),
  )
  expect(screen.getByText('Настоящая расшифровка')).toBeVisible()
  // The review repeats the task's own review from the "went well" card.
  expect(screen.getAllByText(feedback)).toHaveLength(2)
  expect(screen.queryByText('Модель названа.')).not.toBeInTheDocument()
  expect(
    await screen.findByText('Итоговое педагогическое резюме.'),
  ).toBeVisible()
  // The cards come from the task reviews, not from the overall report.
  const good = screen
    .getByRole('heading', { name: 'Что получилось' })
    .closest('section')!
  expect(good).toHaveTextContent(feedback)
  expect(screen.queryByText('Вы уверенно различаете модели.')).toBeNull()
  // A full main answer leaves nothing to work on and credits its basics.
  expect(
    screen.queryByRole('heading', { name: 'Что доработать' }),
  ).not.toBeInTheDocument()
  const topics = screen.getByRole('list', { name: 'Темы на графе' })
  expect(within(topics).getByText('3 из 3')).toBeVisible()
  expect(within(topics).getAllByText('Засчитано')).toHaveLength(2)
  expect(screen.queryByText(/Критических пробелов нет/)).not.toBeInTheDocument()
  expect(
    screen.queryByRole('heading', { name: 'Рекомендации' }),
  ).not.toBeInTheDocument()
  // The overall feedback names only what was checked.
  expect(
    screen.queryByText('Непроверенная компетенция'),
  ).not.toBeInTheDocument()
  // Skipped basics are not listed: the review shows only the asked tasks.
  expect(screen.queryByText('Базовый вопрос из банка')).not.toBeInTheDocument()
  expect(screen.queryByText(/Не проверено/)).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Подтянуть слабые темы' }),
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Новая диагностика' }),
  ).not.toBeInTheDocument()
  const [url, init] = requests.mock.calls.find(([url]) =>
    url.endsWith('/answers/transcription'),
  )!
  expect(url).toBe(
    '/api/v1/learn/diagnostic-sessions/session-real/answers/transcription',
  )
  expect(init?.credentials).toBe('include')
  expect(init?.headers).toMatchObject({ 'Idempotency-Key': expect.any(String) })
  expect(JSON.parse(init?.body as string)).toEqual({
    variant_task_id: 'v-main',
    transcription_id: 'preview-id',
  })
  expect(
    requests.mock.calls.filter(([url]) =>
      url.endsWith('/voice/transcriptions'),
    ),
  ).toHaveLength(1)
  expect(
    requests.mock.calls.some(([url]) => /assessments\/evaluate/.test(url)),
  ).toBe(false)
})
it('keeps the diagnostic result visible and retries failed overall feedback', async () => {
  const requests = server(200, 3, 503)
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  expect(
    await screen.findByText('Не удалось подготовить общий вывод.'),
  ).toBeVisible()
  expect(screen.getByText('5 из 5 баллов')).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Повторить' }))
  expect(
    await screen.findByText('Итоговое педагогическое резюме.'),
  ).toBeVisible()
  expect(
    requests.mock.calls.filter(([url]) => url.endsWith('/feedback')),
  ).toHaveLength(2)
})
it('shows the task review only together with the overall feedback', async () => {
  const requests = server()
  let finish!: () => void
  const ready = new Promise<void>((resolve) => {
    finish = resolve
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/feedback')) await ready
      return requests(url, init)
    }),
  )
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  expect(await screen.findByText('Готовим общий вывод…')).toBeVisible()
  expect(screen.queryByText('Настоящая расшифровка')).not.toBeInTheDocument()
  finish()
  expect(
    await screen.findByText('Итоговое педагогическое резюме.'),
  ).toBeVisible()
  fireEvent.click(
    await screen.findByRole('button', { name: /Посмотреть мои ответы/ }),
  )
  expect(screen.getByText('Настоящая расшифровка')).toBeVisible()
  expect(screen.queryByText('Готовим общий вывод…')).not.toBeInTheDocument()
})

it('allows listening to task instruction from the review list', async () => {
  const play = vi.spyOn(audio, 'playQuestion').mockResolvedValue(vi.fn())
  server()
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  expect(
    await screen.findByText('Итоговое педагогическое резюме.'),
  ).toBeVisible()
  fireEvent.click(
    await screen.findByRole('button', { name: /Посмотреть мои ответы/ }),
  )
  expect(screen.getByText('Настоящая расшифровка')).toBeVisible()
  expect(screen.getByText('Ваш ответ')).toBeVisible()
  const listenBtn = screen.getByRole('button', {
    name: 'Прослушать инструкцию',
  })
  expect(listenBtn).toBeVisible()
  fireEvent.click(listenBtn)
  await waitFor(() => expect(play).toHaveBeenCalled())
}, 15000)

it('offers training under the grade when the diagnostic found gaps', async () => {
  server(200, 3, 200, [], 'Итоговое педагогическое резюме.', 1)
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  expect(
    await screen.findByText('Следующий шаг — персональная подготовка'),
  ).toBeVisible()
  const button = screen.getByRole('button', { name: 'Подтянуть слабые темы' })
  // It stands right under the grade, before the conclusion and the cards.
  const grade = screen.getByRole('region', { name: 'Предварительная оценка' })
  expect(
    grade.compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy()
  expect(
    button.compareDocumentPosition(
      screen.getByRole('region', { name: 'Граф тем' }),
    ) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy()
  fireEvent.click(button)
  await waitFor(() =>
    expect(
      screen.queryByText('Следующий шаг — персональная подготовка'),
    ).not.toBeInTheDocument(),
  )
})

it('shows overall feedback when the backend sends null strengths', async () => {
  server(200, 3, 200, null)
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  expect(
    await screen.findByText('Итоговое педагогическое резюме.'),
  ).toBeVisible()
  expect(
    screen.queryByText('Не удалось подготовить общий вывод.'),
  ).not.toBeInTheDocument()
  // The "went well" card follows the answers, not the report's strengths.
  expect(screen.getByRole('heading', { name: 'Что получилось' })).toBeVisible()
})
it('renders Markdown in the overall feedback without rewriting code escapes', async () => {
  server(
    200,
    3,
    200,
    null,
    '**Регрессия** требует практики.\n\n```python\nprint("a\\nb")\n```',
  )
  const { container } = await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  await screen.findByRole('button', { name: /Посмотреть мои ответы/ })
  expect(screen.getByText('Регрессия').tagName).toBe('STRONG')
  expect(container.querySelector('pre')?.textContent).toBe('print("a\\nb")\n')
})

it('fills the cards from the reviews, not from the model text', async () => {
  server(
    200,
    3,
    200,
    ['Вы уверенно различаете модели.'],
    'Общая оценка по регрессии.\n\nЧто хорошо:\nРазличает классификацию и регрессию.\n\nЧто доработать:\nПутает MAE и R².',
  )
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  // Only the general paragraph comes from the model; parts it adds under
  // headings are not shown, the cards keep the reviewed facts.
  expect(await screen.findByText('Общая оценка по регрессии.')).toBeVisible()
  expect(
    screen.getByRole('heading', { name: 'Что получилось' }).closest('section'),
  ).toHaveTextContent(feedback)
  expect(screen.queryByText(/Различает классификацию/)).not.toBeInTheDocument()
  expect(screen.queryByText(/Путает MAE/)).not.toBeInTheDocument()
})
it('keeps the audio and idempotency key on a failed submission and retries without recording again', async () => {
  const requests = server(502)
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent('ответ')
  expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(
    'Настоящий вопрос из банка',
  )
  fireEvent.click(screen.getByRole('button', { name: 'Отправить снова' }))
  expect(await screen.findByText('Ответ принят')).toBeVisible()
  const calls = requests.mock.calls.filter(([url]) =>
    url.endsWith('/answers/transcription'),
  )
  expect(calls).toHaveLength(2)
  expect(calls[0][1]?.headers).toEqual(calls[1][1]?.headers)
  expect(calls[0][1]?.body).toBe(calls[1][1]?.body)
  expect(audio.startRecording).toHaveBeenCalledTimes(1)
})
it('shows course progress after remount and resumes the existing session on click', async () => {
  const requests = server()
  const first = await home()
  await screen.findByRole('heading', { name: 'Настоящий вопрос из банка' })
  first.unmount()
  reportActiveLearningSession(requests)
  await home({ start: false })
  expect(
    await screen.findByRole('button', { name: 'Продолжить обучение' }),
  ).toBeVisible()
  expect(
    screen.queryByRole('heading', { name: 'Настоящий вопрос из банка' }),
  ).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Продолжить обучение' }))
  await screen.findByRole('heading', { name: 'Настоящий вопрос из банка' })
  expect(
    requests.mock.calls.filter(
      ([url, init]) => url.endsWith('/variants') && init?.method === 'POST',
    ),
  ).toHaveLength(1)
  expect(
    requests.mock.calls.some(([url]) =>
      url.endsWith('/diagnostic-sessions/session-real'),
    ),
  ).toBe(true)
})
it('plays the stored server instruction automatically when a task opens', async () => {
  const requests = server()
  vi.spyOn(audio, 'playQuestion').mockResolvedValue(vi.fn())
  await home()
  await waitFor(() => expect(audio.playQuestion).toHaveBeenCalledTimes(1))
  expect(
    requests.mock.calls.some(([url]) =>
      url.endsWith(
        '/diagnostic-sessions/session-real/current/audio?variant_task_id=v-main',
      ),
    ),
  ).toBe(true)
  expect(
    requests.mock.calls.some(([url]) =>
      url.endsWith('/learn/task-audio/audio-main/file'),
    ),
  ).toBe(true)
})

it('shows loading while queued audio is prepared and plays it without browser speech', async () => {
  const requests = server()
  const speak = vi.fn()
  let ready = false
  vi.stubGlobal(
    'SpeechSynthesisUtterance',
    class {
      lang = ''
      voice = null
      onend: (() => void) | null = null
      onerror: (() => void) | null = null
      constructor(readonly text: string) {}
    },
  )
  vi.stubGlobal('speechSynthesis', {
    speak,
    cancel: vi.fn(),
    getVoices: () => [],
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) =>
      url.includes('/current/audio?')
        ? Response.json({
            variant_task_id: 'v-main',
            status: ready ? 'ready' : 'pending',
            audio_url: ready ? '/learn/task-audio/audio-main/file' : null,
          })
        : requests(url, init),
    ),
  )
  const play = vi.spyOn(audio, 'playQuestion').mockResolvedValue(vi.fn())
  await home()
  expect(
    await screen.findByRole('button', { name: 'Отменить озвучивание' }),
  ).toBeInTheDocument()
  expect(play).not.toHaveBeenCalled()
  expect(speak).not.toHaveBeenCalled()
  ready = true
  await waitFor(() => expect(play).toHaveBeenCalledOnce(), { timeout: 2500 })
  expect(screen.getByRole('button', { name: 'Остановить' })).toBeInTheDocument()
  expect(screen.queryByText(/Аудио готовится/)).not.toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('shows progress by topic and adds clarifying questions only when asked', async () => {
  const requests = server()
  const start = {
    ...progress,
    total_tasks: 6,
    competency_count: 2,
    current_competency: 1,
    current_step: 0,
  }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/diagnostic-sessions') && init?.method === 'POST')
        return Response.json(start, { status: 201 })
      if (url.endsWith('/answers/transcription'))
        return Response.json({
          ...start,
          current: basic,
          completed_tasks: 1,
          current_step: 1,
          text: 'Неуверенный ответ',
          score: 0,
          grader_score: 0,
          grader_max_score: 3,
          verdict: 'incorrect',
          criterion_results: criteria,
          feedback,
        })
      return requests(url, init)
    }),
  )
  await home()
  expect(await screen.findByRole('progressbar')).toHaveAttribute(
    'aria-valuenow',
    '0',
  )
  expect(screen.queryByText(/Тема 1 из 2/)).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  // A weak main opens two basics: it adds a third of its topic, 1/3 of 50%.
  expect(await screen.findByText('Ответ принят')).toBeVisible()
  expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '17')
  expect(screen.getByText('17%')).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Следующее задание' }))
  expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent(
    basic.question,
  )
  expect(screen.queryByText(/уточняющий вопрос/)).not.toBeInTheDocument()
})

it('keeps answered positions after a page reload', async () => {
  const requests = server()
  const first = await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  expect(await screen.findByText('Ответ принят')).toBeVisible()
  const percent = screen.getByRole('progressbar').getAttribute('aria-valuenow')
  expect(percent).not.toBe('0')
  first.unmount()
  reportActiveLearningSession(requests)
  await home({ start: false })
  fireEvent.click(
    await screen.findByRole('button', { name: 'Продолжить обучение' }),
  )
  await waitFor(() =>
    expect(screen.getByRole('progressbar')).toHaveAttribute(
      'aria-valuenow',
      percent,
    ),
  )
})

it('shows the crossed-out microphone when recording cannot start', async () => {
  server()
  vi.mocked(audio.startRecording).mockRejectedValueOnce(
    new DOMException('Denied', 'NotAllowedError'),
  )
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  expect(
    await screen.findByRole('heading', { name: 'Не удалось начать запись' }),
  ).toBeVisible()
  expect(
    screen.getByText('Проверьте доступ к микрофону и попробуйте ещё раз.'),
  ).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Попробовать снова' }))
  expect(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  ).toBeVisible()
})

it('keeps the manual button without an error when the browser blocks autoplay', async () => {
  server()
  vi.spyOn(audio, 'playQuestion')
    .mockRejectedValueOnce(new DOMException('Blocked', 'NotAllowedError'))
    .mockResolvedValue(vi.fn())
  await home()
  await waitFor(() => expect(audio.playQuestion).toHaveBeenCalledTimes(1))
  const replay = await screen.findByRole('button', {
    name: 'Прослушать инструкцию',
  })
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  fireEvent.click(replay)
  await waitFor(() => expect(audio.playQuestion).toHaveBeenCalledTimes(2))
})

it('allows starting a fresh diagnostic after the backend has lost the stored session', async () => {
  const requests = server()
  const first = await home()
  await screen.findByRole('heading', { name: 'Настоящий вопрос из банка' })
  first.unmount()
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) =>
      url.endsWith('/subjects/subject%3Atest/learning-state')
        ? Response.json({
            subject_id: 'subject:test',
            subject_name: 'Предмет теста',
            diagnostic_status: 'active',
            diagnostic_completed: false,
            training_available: false,
            active_session_id: 'session-real',
          })
        : url.endsWith('/diagnostic-sessions/session-real')
          ? Response.json(
              { code: 'DIAGNOSTIC_SESSION_NOT_FOUND' },
              { status: 404 },
            )
          : requests(url, init),
    ),
  )
  await home({ start: false })
  fireEvent.click(
    await screen.findByRole('button', { name: 'Продолжить обучение' }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Сессия больше недоступна',
  )
  fireEvent.click(screen.getByRole('button', { name: 'Начать заново' }))
  expect(
    await screen.findByRole('heading', { name: 'Настоящий вопрос из банка' }),
  ).toBeVisible()
  expect(
    requests.mock.calls.filter(
      ([url, init]) => url.endsWith('/variants') && init?.method === 'POST',
    ),
  ).toHaveLength(2)
})

it('uses backend basic tasks with a 0/1 scale and includes them in the diagnostic total', async () => {
  const requests = server()
  let position = 0
  const tasks = [main, basic, basic2]
  const scores = [1, 1, 0]
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/answers/transcription')) {
        const index = position++
        return Response.json({
          ...progress,
          completed_tasks: position,
          skipped_tasks: 0,
          current: tasks[position],
          status: position === 3 ? 'completed' : 'active',
          score: scores[index],
          grader_score: scores[index],
          grader_max_score: index === 0 ? 3 : 1,
          verdict:
            index === 2 ? 'incorrect' : index === 1 ? 'correct' : 'partial',
          criterion_results: criteria,
          feedback,
        })
      }
      if (url.endsWith('/result'))
        return Response.json({
          ...resultBody,
          diagnostic_score: 2,
          report_score: 2.2,
          report_max_score: 5,
          training_needed: true,
          grade: 'D',
          score_percent: 44,
          completed_tasks: 3,
          untested_basics: [],
          answers: tasks.map((task, index) => ({
            ...resultBody.answers[0],
            task,
            variant_task_id: task.variant_task_id,
            source_task_id: task.source_task_id,
            role: task.role,
            score: scores[index],
            grader_score: scores[index],
            grader_max_score: index === 0 ? 3 : 1,
            verdict:
              index === 2 ? 'incorrect' : index === 1 ? 'correct' : 'partial',
          })),
        })
      return requests(url, init)
    }),
  )
  await home()
  for (let index = 0; index < 3; index++) {
    fireEvent.click(
      await screen.findByRole('button', { name: 'Начать запись' }),
    )
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(
      tasks[index].question,
    )
    if (index > 0)
      expect(
        screen.queryByRole('heading', { name: 'Варианты ответа' }),
      ).not.toBeInTheDocument()
    fireEvent.click(
      await screen.findByRole('button', { name: 'Завершить запись' }),
    )
    fireEvent.click(
      await screen.findByRole('button', { name: 'Отправить ответ' }),
    )
    expect(await screen.findByText('Ответ принят')).toBeVisible()
    expect(
      screen.queryByText(`${scores[index]} / ${index === 0 ? 3 : 1}`),
    ).not.toBeInTheDocument()
    fireEvent.click(
      screen.getByRole('button', {
        name: index === 2 ? 'Посмотреть итог' : 'Следующее задание',
      }),
    )
  }
  expect(await screen.findByText('2,2 из 5 баллов')).toBeVisible()
  expect(screen.queryByText(/Не проверено/)).not.toBeInTheDocument()
})

it('explains when no competency in the map is eligible instead of displaying mock tasks', async () => {
  const requests = server()
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) =>
      url.endsWith('/variants')
        ? Response.json(
            {
              code: 'NO_ELIGIBLE_COMPETENCIES',
              message: 'Нет подходящих компетенций',
            },
            { status: 409 },
          )
        : requests(url, init),
    ),
  )
  await home()
  expect(await screen.findByRole('alert')).toHaveTextContent('карта')
  expect(
    screen.queryByRole('button', { name: 'Начать запись' }),
  ).not.toBeInTheDocument()
  expect(screen.queryByText('Классификация')).not.toBeInTheDocument()
})

it('returns to login if diagnostic submission loses authorization', async () => {
  const requests = server()
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) =>
      url.endsWith('/answers/transcription')
        ? Response.json({ code: 'UNAUTHORIZED' }, { status: 401 })
        : requests(url, init),
    ),
  )
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  expect(
    await screen.findByRole('heading', { name: 'Вход в AI Tutor' }),
  ).toBeVisible()
  expect(
    screen.queryByText('Настоящий вопрос из банка'),
  ).not.toBeInTheDocument()
})

it('retries variant and session creation with the same keys after lost responses', async () => {
  const requests = server()
  const variantKeys: unknown[] = []
  const sessionKeys: unknown[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/variants')) {
        variantKeys.push(init?.headers)
        if (variantKeys.length === 1) throw new TypeError('offline')
      }
      if (url.endsWith('/diagnostic-sessions') && init?.method === 'POST') {
        sessionKeys.push(init.headers)
        if (sessionKeys.length === 1) throw new TypeError('offline')
      }
      return requests(url, init)
    }),
  )
  await home()
  await screen.findByRole('alert')
  fireEvent.click(screen.getByRole('button', { name: 'Попробовать снова' }))
  await waitFor(() => expect(sessionKeys).toHaveLength(1))
  await screen.findByRole('alert')
  fireEvent.click(screen.getByRole('button', { name: 'Попробовать снова' }))
  expect(
    await screen.findByRole('heading', { name: 'Настоящий вопрос из банка' }),
  ).toBeVisible()
  expect(variantKeys).toHaveLength(2)
  expect(variantKeys[0]).toEqual(variantKeys[1])
  expect(sessionKeys).toHaveLength(2)
  expect(sessionKeys[0]).toEqual(sessionKeys[1])
})

it('retains the original audio/key when the backend reports that an answer is still in progress', async () => {
  const requests = server()
  let attempt = 0
  const submissions: RequestInit[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/answers/transcription')) {
        submissions.push(init!)
        if (++attempt === 1)
          return Response.json(
            { code: 'DIAGNOSTIC_ANSWER_IN_PROGRESS' },
            { status: 409 },
          )
      }
      return requests(url, init)
    }),
  )
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  await screen.findByRole('alert')
  fireEvent.click(screen.getByRole('button', { name: 'Отправить снова' }))
  expect(await screen.findByText('Ответ принят')).toBeVisible()
  expect(submissions[0].headers).toEqual(submissions[1].headers)
  expect(submissions[0].body).toBe(submissions[1].body)
})

it('offers only a new recording when speech was not recognized', async () => {
  const requests = server()
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) =>
      url.endsWith('/voice/transcriptions')
        ? Response.json(
            {
              code: 'NO_SPEECH_DETECTED',
              message: 'Речь не распознана; повторите запись.',
            },
            { status: 422 },
          )
        : requests(url, init),
    ),
  )
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Речь не распознана',
  )
  expect(
    screen.queryByRole('button', { name: 'Отправить снова' }),
  ).not.toBeInTheDocument()
  expect(screen.queryByText('Попробуйте ещё раз')).not.toBeInTheDocument()
  expect(
    screen.getAllByRole('button', { name: 'Записать заново' }),
  ).toHaveLength(1)
  fireEvent.click(screen.getByRole('button', { name: 'Записать заново' }))
  expect(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  ).toBeVisible()
})

it('offers a new diagnostic if the completed result disappears after a backend restart', async () => {
  const requests = server()
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) =>
      url.endsWith('/result')
        ? Response.json(
            { code: 'DIAGNOSTIC_SESSION_NOT_FOUND' },
            { status: 404 },
          )
        : requests(url, init),
    ),
  )
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Сессия больше недоступна',
  )
  fireEvent.click(screen.getByRole('button', { name: 'Начать заново' }))
  expect(
    await screen.findByRole('button', { name: 'Начать запись' }),
  ).toBeVisible()
})

it.each([1, 2])(
  'finishes a competency with %i available tasks without inventing missing basics',
  async (totalTasks) => {
    server(200, totalTasks)
    await home()
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent(
      'Настоящий вопрос из банка',
    )
    expect(screen.getByRole('progressbar')).toHaveAttribute(
      'aria-valuenow',
      '0',
    )
    fireEvent.click(screen.getByRole('button', { name: 'Начать запись' }))
    fireEvent.click(
      await screen.findByRole('button', { name: 'Завершить запись' }),
    )
    fireEvent.click(
      await screen.findByRole('button', { name: 'Отправить ответ' }),
    )
    fireEvent.click(
      await screen.findByRole('button', { name: 'Посмотреть итог' }),
    )
    expect(
      await screen.findByRole('heading', { name: 'Итог диагностики' }),
    ).toBeVisible()
    expect(screen.getByText('5 из 5 баллов')).toBeVisible()
    fireEvent.click(
      await screen.findByRole('button', { name: /Посмотреть мои ответы/ }),
    )
    expect(screen.queryByText('Второй базовый вопрос')).not.toBeInTheDocument()
    // Basics left out after a full main answer are not listed.
    expect(
      screen.queryByText('Базовый вопрос из банка'),
    ).not.toBeInTheDocument()
  },
)

it('lets the learner replace diagnostic audio without sending the discarded answer', async () => {
  const requests = server()
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  expect(await screen.findByText('Настоящая расшифровка')).toBeVisible()
  expect(
    requests.mock.calls.filter(([url]) =>
      url.endsWith('/answers/transcription'),
    ),
  ).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: 'Перезаписать ответ' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  expect(await screen.findByText('Ответ принят')).toBeVisible()
  expect(audio.startRecording).toHaveBeenCalledTimes(2)
  expect(
    requests.mock.calls.filter(([url]) =>
      url.endsWith('/answers/transcription'),
    ),
  ).toHaveLength(1)
})

it('shows preparing audio and permits retry in the completed review', async () => {
  const requests = server()
  const original = requests.getMockImplementation()!
  let preparing = true
  requests.mockImplementation(async (url, init) => {
    if (
      preparing &&
      url.includes('/learn/task-audio/') &&
      url.endsWith('/file')
    )
      return Response.json({ code: 'AUDIO_NOT_READY' }, { status: 409 })
    return original(url, init)
  })
  const play = vi.spyOn(audio, 'playQuestion').mockResolvedValue(vi.fn())
  await home()
  fireEvent.click(await screen.findByRole('button', { name: 'Начать запись' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Завершить запись' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Отправить ответ' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Посмотреть итог' }),
  )
  await screen.findByText('Итоговое педагогическое резюме.')
  fireEvent.click(screen.getByRole('button', { name: /Посмотреть мои ответы/ }))
  fireEvent.click(screen.getByRole('button', { name: 'Прослушать инструкцию' }))
  expect(
    await screen.findByText('Аудио готовится. Попробуйте немного позже.'),
  ).toBeVisible()
  expect(screen.getByText(main.voice_instruction)).toBeVisible()
  expect(play).not.toHaveBeenCalled()
  preparing = false
  fireEvent.click(screen.getByRole('button', { name: 'Прослушать инструкцию' }))
  await waitFor(() => expect(play).toHaveBeenCalledOnce())
})

it('opens the active learning diagnostic from the profile', async () => {
  const requests = server()
  reportActiveLearningSession(requests)
  const router = createRouter({
    context: { queryClient: createQueryClient() },
    history: createMemoryHistory({
      initialEntries: ['/learn?subject_id=subject%3Atest&resume=diagnostic'],
    }),
    routeTree,
  })
  render(<RouterProvider router={router} />)
  expect(
    await screen.findByText('Настоящий вопрос из банка', {}, { timeout: 5000 }),
  ).toBeVisible()
  expect(
    JSON.parse(sessionStorage.getItem('ai-tutor:learning-home') ?? 'null')
      ?.sessionId,
  ).toBe('session-real')
})
