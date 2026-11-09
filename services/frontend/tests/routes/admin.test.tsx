import {
  act,
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
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import { createQueryClient } from '@/bootstrap/providers'
import { routeTree } from '@/routeTree.gen'
import { zipSync } from 'fflate'

// Load the lazy page outside timed user interactions, as in diagnostic tests.
beforeAll(async () => {
  if (!window.URL.createObjectURL) {
    window.URL.createObjectURL = vi.fn(() => 'blob:mock-url')
  }
  if (!window.URL.revokeObjectURL) {
    window.URL.revokeObjectURL = vi.fn()
  }
  await import('@/pages/competency-map')
}, 30000)

const admin = {
  id: 'admin-1',
  email: 'admin@example.com',
  display_name: 'Администратор',
  role: 'admin',
  created_at: 1791158400,
}
const subjects = [
  {
    id: 'subject-a',
    name: 'Машинное обучение',
    ready: true,
    learning_ready: true,
    exam_ready: true,
    preparation: {
      exam: { state: 'ready', source: 'materials', stale: false },
      learn: { state: 'ready' },
    },
  },
  {
    id: 'subject-b',
    name: 'Статистика',
    ready: false,
    learning_ready: false,
    exam_ready: false,
    preparation: {
      exam: { state: 'empty', source: null, stale: false },
      learn: { state: 'empty' },
    },
  },
]

const sampleMaterials = [
  {
    id: 'mat-1',
    subject_id: 'subject-a',
    filename: 'lecture1.pdf',
    content_type: 'application/pdf',
    file_size: 1048576,
    s3_raw_key: 'subjects/subject-a/raw/mat-1_lecture1.pdf',
    s3_parsed_key: 'subjects/subject-a/parsed/mat-1_lecture1.pdf.md',
    parsed_text: '# Лекция 1\n\nФормула: $y = Wx + b$',
    char_count: 35,
    status: 'parsed',
    week: 1 as number | null,
    kind: 'lecture' as 'lecture' | 'lab' | 'tutorial' | null,
    error_message: null,
    created_at: 1791356400,
    updated_at: 1791356400,
  },
]

// The admin panel is the /admin site; the browser address decides the portal.
// A book of two sheets: Geek's and the learning diagnostic's.
function twoSheetBook(name = 'map.xlsx') {
  const sheets = ['Geek', 'Обучение']
    .map(
      (sheet, index) =>
        `<sheet name="${sheet}" sheetId="${index + 1}" r:id="rId${index + 1}"/>`,
    )
    .join('')
  const workbook = zipSync({
    'xl/workbook.xml': new TextEncoder().encode(
      `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets>${sheets}</sheets></workbook>`,
    ),
  })
  return new File([workbook], name)
}

// The submit button once the book has been checked.
async function submitButton() {
  const button = await screen.findByRole('button', {
    name: 'Подготовить для обучения и Geek',
  })
  await waitFor(() => expect(button).toBeEnabled())
  return button
}

function renderApp(path = '/admin/courses/subject-a?direction=exam') {
  window.history.replaceState({}, '', path)
  const router = createRouter({
    context: { queryClient: createQueryClient() },
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
  })
  render(<RouterProvider router={router} />)
  return router
}

function backend(
  options: {
    user?: typeof admin | null
    subjects?: typeof subjects
    subjectsGate?: Promise<void>
    subjectsFail?: boolean
    materials404?: boolean
    removeCourseOnMaterials404?: boolean
    learningMap404?: boolean
    learningMapExists?: boolean
    teacherFails?: boolean
    materials?: typeof sampleMaterials
    partialUpload?: boolean
    uploadGate?: Promise<void>
    downloadFails?: boolean
    metadataFailCount?: number
    deleteFails?: boolean
    graph?: {
      version: string
      published_at?: number | null
      nodes: Array<{
        id: string
        title: string
        content: string
        prerequisites: string[]
        outcomes: string[]
        questions: string[]
      }>
    }
    build?: {
      id: string
      subject_id: string
      status: string
      stage: string | null
      started_at: number | null
      completed_at: number | null
      error: { code: string; message: string } | null
      progress?: {
        stage: string
        stage_index: number
        stage_count: number
        done: number
        total: number
      }
    }
  } = {},
) {
  const activeSubjects = [...(options.subjects ?? subjects)]
  const activeMaterials = (options.materials ?? sampleMaterials).map(
    (item) => ({ ...item }),
  )
  let graphRequested = false
  let unifiedMapRequested = false
  let signedIn = options.user !== null
  let metadataFailuresRemaining = options.metadataFailCount ?? 0
  const fetch = vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/auth/refresh'))
      return new Response(null, { status: 401 })
    if (url.endsWith('/auth/me'))
      return signedIn
        ? new Response(JSON.stringify(options.user ?? admin))
        : new Response(null, { status: 401 })
    // The admin panel enters the shared teacher account by itself.
    if (url.endsWith('/auth/teacher')) {
      if (options.teacherFails)
        return Response.json({ message: 'Сервер недоступен.' }, { status: 503 })
      signedIn = true
      return Response.json({ user: admin })
    }
    if (url.endsWith('/auth/logout')) {
      signedIn = false
      return new Response(null, { status: 204 })
    }
    if (url.endsWith('/subjects') && !init?.method) {
      await options.subjectsGate
      if (options.subjectsFail)
        return Response.json({ message: 'Сервер недоступен.' }, { status: 503 })
      return new Response(JSON.stringify(activeSubjects))
    }
    if (url.includes('/learn/subjects/') && url.endsWith('/competency-map')) {
      if (options.learningMap404)
        return Response.json({ message: 'Карта не найдена.' }, { status: 404 })
      return Response.json({
        revision: options.learningMapExists || unifiedMapRequested ? 1 : 0,
        imported_at:
          options.learningMapExists || unifiedMapRequested ? 2 : null,
        competencies: [],
      })
    }
    if (url.endsWith('/admin/material-kinds'))
      return Response.json([
        { code: 'lecture', label: 'Лекция' },
        { code: 'lab', label: 'Лабораторная' },
        { code: 'tutorial', label: 'Туториал' },
      ])
    if (url.endsWith('/admin/subjects') && init?.method === 'POST') {
      const subject = {
        id: 'subject-c',
        name: 'Новый',
        ready: false,
        learning_ready: false,
        exam_ready: false,
        preparation: {
          exam: { state: 'empty', source: null, stale: false },
          learn: { state: 'empty' },
        },
      }
      activeSubjects.push(subject)
      return new Response(JSON.stringify(subject))
    }
    if (url.endsWith('/subjects/subject-a/learning-state'))
      return new Response(
        JSON.stringify({
          subject_id: 'subject-a',
          subject_name: 'Машинное обучение',
          diagnostic_status: 'not_started',
          diagnostic_completed: false,
          training_available: false,
        }),
      )
    if (
      url.includes('/admin/subjects/') &&
      url.endsWith('/materials') &&
      !init?.method
    ) {
      if (options.materials404) {
        if (options.removeCourseOnMaterials404)
          activeSubjects.splice(0, activeSubjects.length)
        return Response.json({ message: 'Курс не найден.' }, { status: 404 })
      }
      return new Response(JSON.stringify(activeMaterials))
    }
    if (
      url.includes('/admin/subjects/') &&
      url.endsWith('/materials') &&
      init?.method === 'POST'
    ) {
      await options.uploadGate
      if (options.partialUpload) {
        return new Response(
          JSON.stringify({
            uploaded: [],
            failed: [
              {
                filename: 'corrupted.pdf',
                code: 'OCR_FAILED',
                message: 'Ошибка распознавания',
              },
            ],
            results: [
              {
                client_id: JSON.parse(
                  (init.body as FormData).get('items') as string,
                )[0].client_id,
                error: {
                  filename: 'corrupted.pdf',
                  code: 'OCR_FAILED',
                  message: 'Ошибка распознавания',
                },
              },
            ],
          }),
        )
      }
      const newMat = {
        id: 'mat-uploaded',
        subject_id: 'subject-a',
        filename: 'new_lecture.pdf',
        content_type: 'application/pdf',
        file_size: 2048,
        s3_raw_key: 'subjects/subject-a/raw/mat-uploaded_new_lecture.pdf',
        s3_parsed_key:
          'subjects/subject-a/parsed/mat-uploaded_new_lecture.pdf.md',
        parsed_text: '# Новая лекция',
        char_count: 14,
        status: 'parsed',
        week: 1,
        kind: 'lecture' as const,
        error_message: null,
        created_at: 1791356400,
        updated_at: 1791356400,
      }
      activeMaterials.push(newMat)
      const clientId = JSON.parse(
        (init.body as FormData).get('items') as string,
      )[0].client_id
      return new Response(
        JSON.stringify({
          uploaded: [newMat],
          failed: [],
          results: [{ client_id: clientId, material: newMat }],
        }),
      )
    }
    if (
      url.endsWith('/graph') ||
      (url.endsWith('/graph/build') && init?.method === 'POST')
    ) {
      if (init?.method === 'POST') graphRequested = true
      const states: Record<string, string> = {
        queued: 'pending',
        running: 'processing',
        succeeded: 'ready',
        failed: 'failed',
      }
      return Response.json({
        status: graphRequested
          ? 'pending'
          : unifiedMapRequested
            ? 'ready'
            : options.build
              ? states[options.build.status]
              : options.graph
                ? 'ready'
                : 'not_requested',
        attempts: 1,
        error_message: options.build?.error?.message ?? '',
        graph: options.graph
          ? {
              topics: options.graph.nodes.map((node) => ({
                id: node.id,
                title: node.title,
                provides: node.outcomes,
                question: node.questions[0],
              })),
              hierarchy: [],
              prerequisites: options.graph.nodes.flatMap((node) =>
                node.prerequisites.map((source) => ({
                  source,
                  target: node.id,
                })),
              ),
            }
          : null,
        stale: false,
        graph_source: unifiedMapRequested ? 'competency_map' : 'none',
        graph_fingerprint: unifiedMapRequested
          ? 'published-paired-fingerprint'
          : '',
        graph_map_revision: unifiedMapRequested && options.graph ? 1 : null,
        build_source: unifiedMapRequested ? 'competency_map' : null,
        map: unifiedMapRequested
          ? {
              filename: 'map.xlsx',
              sheet: 'Geek',
              fingerprint: 'paired-fingerprint',
              learning_revision: 1,
            }
          : null,
        inputs_ready:
          activeMaterials.length > 0 &&
          activeMaterials.every((item) => item.status === 'parsed'),
        updated_at: options.graph?.published_at ?? 0,
        progress: graphRequested ? null : (options.build?.progress ?? null),
      })
    }
    if (url.endsWith('/graph/competency-map') && init?.method === 'POST') {
      unifiedMapRequested = true
      return Response.json({
        status: 'pending',
        attempts: 1,
        error_message: '',
        graph: null,
        stale: false,
        graph_source: 'none',
        graph_fingerprint: '',
        graph_map_revision: null,
        build_source: 'competency_map',
        map: {
          filename: 'map.xlsx',
          sheet: 'Geek',
          fingerprint: 'paired-fingerprint',
          learning_revision: 1,
        },
        inputs_ready: false,
        updated_at: 1,
      })
    }
    if (
      url.includes('/admin/subjects/') &&
      url.includes('/materials/') &&
      init?.method === 'PATCH'
    ) {
      if (metadataFailuresRemaining > 0) {
        metadataFailuresRemaining -= 1
        return Response.json(
          {
            code: 'DATABASE_ERROR',
            message: 'Не удалось сохранить метаданные.',
          },
          { status: 503 },
        )
      }
      const material =
        activeMaterials.find((item) => url.includes(item.id)) ??
        activeMaterials[0]
      Object.assign(material, JSON.parse(init.body as string))
      return Response.json(material)
    }
    if (
      url.includes('/admin/subjects/') &&
      url.includes('/materials/') &&
      url.endsWith('/raw')
    ) {
      if (options.downloadFails) {
        return new Response('File not found', { status: 404 })
      }
      return new Response('fake raw bytes', {
        headers: { 'Content-Type': 'application/pdf' },
      })
    }
    if (
      url.includes('/admin/subjects/') &&
      url.includes('/materials/') &&
      init?.method === 'DELETE'
    ) {
      if (options.deleteFails)
        return Response.json(
          { code: 'DATABASE_ERROR', message: 'Не удалось удалить материал.' },
          { status: 503 },
        )
      return new Response(null, { status: 204 })
    }
    throw new Error(`Unexpected request: ${url}`)
  })
  vi.stubGlobal('fetch', fetch)
  return fetch
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  window.history.replaceState({}, '', '/')
})

it.each(['/admin', '/admin/'])('opens the course list at %s', async (path) => {
  backend()
  const router = renderApp(path)
  expect(
    await screen.findByRole('heading', {
      name: 'Курсы',
      level: 1,
    }),
  ).toBeVisible()
  expect(router.state.location.pathname).toBe('/admin/courses')
  expect(
    await screen.findByRole('heading', { name: 'Машинное обучение', level: 2 }),
  ).toBeVisible()
  expect(
    screen.getByRole('heading', { name: 'Статистика', level: 2 }),
  ).toBeVisible()
  fireEvent.click(screen.getByRole('link', { name: /Машинное обучение/ }))
  expect(
    await screen.findByRole('heading', { name: 'Подготовка курса' }),
  ).toBeVisible()
  expect(router.state.location.pathname).toBe('/admin/courses/subject-a')
})

it('opens one course preparation flow from the course list and old direction links', async () => {
  backend()
  const router = renderApp('/admin/courses')
  expect(
    await screen.findByRole('heading', { name: 'Курсы', level: 1 }),
  ).toBeVisible()
  expect(
    screen.queryByRole('navigation', { name: 'Разделы администрирования' }),
  ).not.toBeInTheDocument()
  const emptyCourse = await screen.findByRole('link', { name: /Статистика/ })
  expect(
    within(emptyCourse).getByText('Экзамен: материалы не загружены'),
  ).toBeVisible()
  expect(
    within(emptyCourse).getByText('Обучение: материалы не загружены'),
  ).toBeVisible()
  const readyCourse = await screen.findByRole('link', {
    name: /Машинное обучение/,
  })
  expect(within(readyCourse).getByText('Экзамен: готов')).toBeVisible()
  expect(within(readyCourse).getByText('Обучение: готово')).toBeVisible()
  fireEvent.click(readyCourse)
  expect(
    await screen.findByRole('heading', {
      name: 'Подготовка курса',
      level: 2,
    }),
  ).toBeVisible()
  expect(router.state.location.pathname).toBe('/admin/courses/subject-a')
  expect(
    await screen.findByRole('heading', { name: 'Машинное обучение', level: 1 }),
  ).toBeVisible()
  expect(
    await screen.findByRole('heading', {
      name: 'Загруженные лекции и лабораторные',
      level: 2,
    }),
  ).toBeVisible()
  expect(
    screen.getByRole('heading', { name: 'Машинное обучение', level: 1 }),
  ).toBeVisible()
  expect(router.state.location.pathname).toBe('/admin/courses/subject-a')
  expect(
    screen.queryByRole('navigation', { name: 'Разделы администрирования' }),
  ).not.toBeInTheDocument()
  await router.navigate({
    to: '/admin/courses/$subjectId',
    params: { subjectId: 'subject-a' },
    search: { direction: 'exam' },
  })
  expect(
    await screen.findByRole('heading', { name: 'Подготовка курса', level: 2 }),
  ).toBeVisible()
  expect(router.state.location.search).toEqual({ direction: 'exam' })
  fireEvent.click(screen.getByRole('link', { name: 'Ко всем курсам' }))
  expect(
    await screen.findByRole('heading', { name: 'Курсы', level: 1 }),
  ).toBeVisible()
  expect(router.state.location.pathname).toBe('/admin/courses')
})

it.each([
  ['/admin/courses/missing-course', 'learn'],
  ['/admin/courses/missing-course?direction=exam', 'exam'],
])('replaces an unavailable %s course with the home route', async (path) => {
  backend({ subjects: [] })
  const router = renderApp(path)
  await waitFor(() => expect(router.state.location.pathname).toBe('/'))
  expect(
    screen.queryByRole('heading', { name: 'missing-course', level: 1 }),
  ).not.toBeInTheDocument()
  expect(
    screen.queryByText(/курс из ссылки недоступен/i),
  ).not.toBeInTheDocument()
})

it('does not redirect while checking a course or when catalog request fails', async () => {
  let releaseCatalog!: () => void
  const pendingCatalog = new Promise<void>((resolve) => {
    releaseCatalog = resolve
  })
  backend({ subjectsGate: pendingCatalog })
  const router = renderApp('/admin/courses/missing-course')
  await screen.findByRole('heading', { name: 'Загружаем курс…', level: 1 })
  expect(router.state.location.pathname).toBe('/admin/courses/missing-course')
  releaseCatalog()
  await screen.findByRole('heading', { name: 'Выберите режим', level: 1 })
  expect(router.state.location.pathname).toBe('/')

  backend({ subjectsFail: true })
  const failedRouter = renderApp('/admin/courses/missing-course?direction=exam')
  await screen.findByText('Не удалось загрузить предмет.')
  expect(failedRouter.state.location.pathname).toBe(
    '/admin/courses/missing-course',
  )
  expect(
    screen.getByRole('button', { name: 'Попробовать снова' }),
  ).toBeVisible()
})

it('replaces a missing detail with home so Back does not return to that detail', async () => {
  backend({ subjects: [] })
  const router = renderApp('/admin/courses')
  await screen.findByRole('heading', { name: 'Курсы', level: 1 })
  await act(async () => {
    await router.navigate({
      to: '/admin/courses/$subjectId',
      params: { subjectId: 'missing-course' },
    })
  })
  await waitFor(() => expect(router.state.location.pathname).toBe('/'))
  await act(async () => router.history.back())
  await waitFor(() =>
    expect(router.state.location.pathname).toBe('/admin/courses'),
  )
  expect(router.state.location.pathname).not.toBe(
    '/admin/courses/missing-course',
  )
})

it('rechecks the common catalog when an exam materials endpoint reports a missing course', async () => {
  backend({ materials404: true, removeCourseOnMaterials404: true })
  const router = renderApp('/admin/courses/subject-a?direction=exam')
  await waitFor(() => expect(router.state.location.pathname).toBe('/'))
})

it('does not redirect when a learning resource is missing but its course still exists', async () => {
  backend({ learningMap404: true })
  const router = renderApp('/admin/courses/subject-a')
  await screen.findByText('Не удалось загрузить текущую карту.')
  expect(router.state.location.pathname).toBe('/admin/courses/subject-a')
  expect(
    screen.getByRole('heading', { name: 'Машинное обучение', level: 1 }),
  ).toBeVisible()
})

it('imports a map only into the course selected in its route', async () => {
  const fetch = backend()
  renderApp('/admin/courses/subject-b')
  expect(
    await screen.findByRole('heading', {
      name: 'Текущее состояние карты',
      level: 3,
    }),
  ).toBeVisible()
  const file = twoSheetBook()
  fireEvent.change(await screen.findByLabelText('Файл карты компетенций'), {
    target: { files: [file] },
  })
  const submit = await screen.findByRole('button', {
    name: 'Подготовить для обучения и Geek',
  })
  await waitFor(() => expect(submit).toBeEnabled())
  fireEvent.click(submit)
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(
        ([url, request]) =>
          url.endsWith('/exam/admin/subjects/subject-b/graph/competency-map') &&
          request?.method === 'POST',
      ),
    ).toBe(true),
  )
  expect(
    fetch.mock.calls.some(([url]) =>
      url.endsWith('/exam/admin/subjects/subject-a/graph/competency-map'),
    ),
  ).toBe(false)
})

it('sends a two-sheet workbook through the single map import', async () => {
  const fetch = backend()
  renderApp('/admin/courses/subject-b')
  await screen.findByRole('heading', {
    name: 'Текущее состояние карты',
    level: 3,
  })
  const file = twoSheetBook()
  fireEvent.change(screen.getByLabelText('Файл карты компетенций'), {
    target: { files: [file] },
  })
  fireEvent.click(
    await screen.findByRole('button', {
      name: 'Подготовить для обучения и Geek',
    }),
  )
  await screen.findByText('Карта компетенций успешно загружена')
  expect(
    fetch.mock.calls.some(
      ([url, request]) =>
        url.endsWith('/exam/admin/subjects/subject-b/graph/competency-map') &&
        request?.method === 'POST',
    ),
  ).toBe(true)
})

it('asks for confirmation before replacing an existing map', async () => {
  const fetch = backend({ learningMapExists: true })
  renderApp('/admin/courses/subject-b')
  await screen.findByText('Карта компетенций успешно загружена')
  const file = twoSheetBook()
  fireEvent.change(screen.getByLabelText('Файл карты компетенций'), {
    target: { files: [file] },
  })
  fireEvent.click(await submitButton())
  expect(await screen.findByRole('alertdialog')).toBeVisible()
  expect(
    fetch.mock.calls.some(([url]) => url.endsWith('/graph/competency-map')),
  ).toBe(false)
  fireEvent.click(screen.getByRole('button', { name: 'Перезаписать' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(([url]) => url.endsWith('/graph/competency-map')),
    ).toBe(true),
  )
})

it('waits for a fresh map state before submitting a cached empty map', async () => {
  const fetch = backend()
  const router = renderApp('/admin/courses/subject-b')
  await screen.findByText('Карта ещё не загружена.')
  let finish!: (response: Response) => void
  const originalFetch = fetch.getMockImplementation()!
  fetch.mockImplementation((url, init) => {
    if (url.includes('/learn/subjects/') && url.endsWith('/competency-map'))
      return new Promise<Response>((resolve) => {
        finish = resolve
      })
    return originalFetch(url, init)
  })
  fireEvent.change(screen.getByLabelText('Файл карты компетенций'), {
    target: { files: [twoSheetBook()] },
  })
  act(() => {
    void router.options.context.queryClient.invalidateQueries()
  })
  await waitFor(() => expect(finish).toBeDefined())
  const submit = screen.getByRole('button', {
    name: 'Подготовить для обучения и Geek',
  })
  expect(submit).toBeDisabled()
  await act(async () =>
    finish(Response.json({ revision: 1, imported_at: 2, competencies: [] })),
  )
  await waitFor(() => expect(submit).toBeEnabled())
  fireEvent.click(submit)
  expect(await screen.findByRole('alertdialog')).toBeVisible()
})

it('refreshes a stale empty map after an import conflict and asks before retrying', async () => {
  const fetch = backend()
  renderApp('/admin/courses/subject-b')
  await screen.findByText('Карта ещё не загружена.')
  const originalFetch = fetch.getMockImplementation()!
  let conflict = false
  fetch.mockImplementation((url, init) => {
    if (url.endsWith('/graph/competency-map')) {
      conflict = true
      return Promise.resolve(
        Response.json({ message: 'Карта изменилась.' }, { status: 409 }),
      )
    }
    if (
      conflict &&
      url.includes('/learn/subjects/') &&
      url.endsWith('/competency-map')
    )
      return Promise.resolve(
        Response.json({ revision: 1, imported_at: 2, competencies: [] }),
      )
    return originalFetch(url, init)
  })
  fireEvent.change(screen.getByLabelText('Файл карты компетенций'), {
    target: { files: [twoSheetBook()] },
  })
  fireEvent.click(await submitButton())
  await screen.findByText('Карта изменилась.')
  await screen.findByText('Карта компетенций успешно загружена')
  const submit = screen.getByRole('button', {
    name: 'Подготовить для обучения и Geek',
  })
  await waitFor(() => expect(submit).toBeEnabled())
  fireEvent.click(submit)
  expect(await screen.findByRole('alertdialog')).toBeVisible()
})

it('blocks confirmation while the map state is refreshing', async () => {
  const fetch = backend({ learningMapExists: true })
  const router = renderApp('/admin/courses/subject-b')
  await screen.findByText('Карта компетенций успешно загружена')
  fireEvent.change(screen.getByLabelText('Файл карты компетенций'), {
    target: { files: [twoSheetBook()] },
  })
  fireEvent.click(await submitButton())
  const originalFetch = fetch.getMockImplementation()!
  fetch.mockImplementation((url, init) => {
    if (url.includes('/learn/subjects/') && url.endsWith('/competency-map'))
      return new Promise<Response>(() => {})
    return originalFetch(url, init)
  })
  act(() => {
    void router.options.context.queryClient.invalidateQueries()
  })
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Перезаписать' })).toBeDisabled(),
  )
  expect(
    fetch.mock.calls.some(([url]) => url.endsWith('/graph/competency-map')),
  ).toBe(false)
})

it('requires a new confirmation when the map changes while the dialog is open', async () => {
  const fetch = backend({ learningMapExists: true })
  const router = renderApp('/admin/courses/subject-b')
  await screen.findByText('Карта компетенций успешно загружена')
  fireEvent.change(screen.getByLabelText('Файл карты компетенций'), {
    target: { files: [twoSheetBook()] },
  })
  fireEvent.click(await submitButton())
  const originalFetch = fetch.getMockImplementation()!
  fetch.mockImplementation((url, init) => {
    if (url.includes('/learn/subjects/') && url.endsWith('/competency-map'))
      return Promise.resolve(
        Response.json({ revision: 2, imported_at: 3, competencies: [] }),
      )
    return originalFetch(url, init)
  })
  await act(async () => {
    await router.options.context.queryClient.invalidateQueries()
  })
  expect(
    await screen.findByText('Карта изменилась. Подтвердите замену ещё раз.'),
  ).toBeVisible()
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(
    fetch.mock.calls.some(([url]) => url.endsWith('/graph/competency-map')),
  ).toBe(false)
  fireEvent.click(await submitButton())
  fireEvent.click(await screen.findByRole('button', { name: 'Перезаписать' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(
        ([url, init]) =>
          url.endsWith('/graph/competency-map') &&
          new Headers(init?.headers).get('X-Expected-Learning-Revision') ===
            '2',
      ),
    ).toBe(true),
  )
})

it('returns focus to the submit button after cancelling replacement', async () => {
  backend({ learningMapExists: true })
  renderApp('/admin/courses/subject-b')
  await screen.findByText('Карта компетенций успешно загружена')
  fireEvent.change(screen.getByLabelText('Файл карты компетенций'), {
    target: { files: [twoSheetBook()] },
  })
  const submit = await submitButton()
  submit.focus()
  fireEvent.click(submit)
  fireEvent.click(await screen.findByRole('button', { name: 'Отмена' }))
  await waitFor(() => expect(submit).toHaveFocus())
})

it('does not mount the lesson while the current session is being checked', async () => {
  let finish!: (response: Response) => void
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValue(new Response('{}', { status: 401 }))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve
          }),
      ),
  )
  renderApp()
  expect(await screen.findByRole('status')).toHaveAccessibleName(
    'Проверяем сессию…',
  )
  expect(screen.queryByText('Машинное обучение')).not.toBeInTheDocument()
  await act(async () => finish(new Response('{}', { status: 401 })))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Не удалось открыть админ-панель.',
  )
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
})

it('creates a trimmed course from the course list', async () => {
  const fetch = backend({ subjects: [], materials: [] })
  renderApp('/admin/courses')
  fireEvent.click(await screen.findByRole('button', { name: 'Новый курс' }))
  const field = screen.getByLabelText('Название курса')
  const submit = screen.getByRole('button', { name: 'Создать курс' })
  expect(submit).toBeDisabled()
  fireEvent.change(field, { target: { value: '  Новый  ' } })
  fireEvent.click(submit)
  expect(
    await screen.findByRole('heading', { name: 'Новый', level: 2 }),
  ).toBeVisible()
  const [, init] = fetch.mock.calls.find(
    ([url, request]) =>
      url.endsWith('/admin/subjects') && request?.method === 'POST',
  )!
  expect(JSON.parse(init?.body as string)).toEqual({ name: 'Новый' })
})

it('creates an additional course without replacing the existing course', async () => {
  const fetch = backend()
  renderApp('/admin/courses')
  fireEvent.click(await screen.findByRole('button', { name: 'Новый курс' }))
  fireEvent.change(screen.getByLabelText('Название курса'), {
    target: { value: '  Новый курс  ' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Создать курс' }))
  expect(
    await screen.findByRole('heading', { name: 'Новый', level: 2 }),
  ).toBeVisible()
  expect(
    screen.getByRole('heading', { name: 'Машинное обучение', level: 2 }),
  ).toBeVisible()
  const [, init] = fetch.mock.calls.find(
    ([url, request]) =>
      url.endsWith('/admin/subjects') && request?.method === 'POST',
  )!
  expect(JSON.parse(init?.body as string)).toEqual({ name: 'Новый курс' })
})

it('shows the shared mode chooser to an admin account without entering admin', async () => {
  backend()
  const router = renderApp('/')
  expect(
    await screen.findByRole('heading', { name: 'Выберите режим' }),
  ).toBeVisible()
  expect(router.state.location.pathname).toBe('/')
  expect(
    screen.queryByRole('heading', { name: 'Материалы для экзамена' }),
  ).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: /Обучение/ })).toHaveAttribute(
    'href',
    '/learn',
  )
  expect(
    screen.getByRole('link', { name: /Симуляция экзамена/ }),
  ).toHaveAttribute('href', '/exam')
})

it('denies students direct access to the admin screen without fetching materials', async () => {
  const fetch = backend({ user: { ...admin, role: 'student' } })
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Доступно только администраторам',
  )
  expect(
    screen.queryByLabelText('Загрузить файлы материалов'),
  ).not.toBeInTheDocument()
  expect(fetch.mock.calls.every(([url]) => url.endsWith('/auth/me'))).toBe(true)
})

it('shows the shared account without a logout in the admin panel', async () => {
  backend()
  renderApp()
  await screen.findByRole('heading', { name: 'Подготовка курса' })
  expect(screen.getByLabelText('Профиль: Администратор')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: /Меню профиля/ }),
  ).not.toBeInTheDocument()
  expect(screen.queryByText('Выйти')).not.toBeInTheDocument()
})

it('does not expose the upload form when the admin panel cannot sign in', async () => {
  const fetch = backend({ user: null, teacherFails: true })
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Не удалось открыть админ-панель.',
  )
  expect(
    screen.queryByLabelText('Загрузить файлы материалов'),
  ).not.toBeInTheDocument()
  expect(
    fetch.mock.calls.every(([url]) =>
      /\/auth\/(me|refresh|teacher)$/.test(url),
    ),
  ).toBe(true)
})

it('restores the admin screen after reload with an expired access cookie', async () => {
  let valid = false
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      if (url.endsWith('/auth/refresh')) {
        valid = true
        return Response.json({})
      }
      if (url.endsWith('/auth/me'))
        return valid
          ? Response.json(admin)
          : new Response(null, { status: 401 })
      if (url.endsWith('/subjects')) return Response.json(subjects)
      if (url.endsWith('/materials')) return Response.json([])
      throw new Error(`Unexpected request: ${url}`)
    }),
  )
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(
    await screen.findByRole('heading', { name: 'Подготовка курса' }),
  ).toBeVisible()
  expect(screen.queryByText('Открываем админ-панель…')).not.toBeInTheDocument()
})

it('retries a failed session check without exposing the lesson', async () => {
  const fetch = backend()
  fetch.mockRejectedValueOnce(new Error('offline'))
  renderApp()
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Не удалось связаться с сервером',
  )
  expect(screen.queryByText('Машинное обучение')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Попробовать снова' }))
  expect(
    await screen.findByRole('heading', { name: 'Подготовка курса' }),
  ).toBeVisible()
})

it('uploads lecture files and shows success message', async () => {
  const fetch = backend()
  renderApp('/admin/courses/subject-a?direction=exam')
  const input = await screen.findByLabelText('Добавить PDF или ZIP')
  fireEvent.change(input, {
    target: {
      files: [
        new File(['%PDF-1.7 pdfcontent'], 'new_lecture.pdf', {
          type: 'application/pdf',
        }),
      ],
    },
  })
  expect(await screen.findByText(/new_lecture\.pdf/)).toBeVisible()
  const submitBtn = screen.getByRole('button', { name: 'Отправить всё' })
  fireEvent.click(submitBtn)
  expect(
    await screen.findByText(
      'Материалы загружены и сохранены. Распознавание и обработка продолжаются в фоне.',
    ),
  ).toBeVisible()
  expect(
    fetch.mock.calls.some(
      ([url, req]) => url.endsWith('/materials') && req?.method === 'POST',
    ),
  ).toBe(true)
  const [, request] = fetch.mock.calls.find(
    ([url, init]) => url.endsWith('/materials') && init?.method === 'POST',
  )!
  expect(request?.body).toBeInstanceOf(FormData)
  expect((request?.body as FormData).get('items')).toBeTruthy()
})

it('blocks graph generation for legacy materials until their metadata is fixed', async () => {
  const fetch = backend({
    materials: [{ ...sampleMaterials[0], week: null, kind: null }],
  })
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(
    await screen.findByText('У 1 материалов не указаны неделя или тип.'),
  ).toBeVisible()
  const legacyWeek = screen.getByRole('spinbutton', { name: 'Неделя' })
  expect(legacyWeek).toBeEnabled()
  expect(screen.getByRole('combobox', { name: 'Тип материала' })).toBeVisible()
  expect(
    screen.getByRole('button', { name: /Собрать граф тем/ }),
  ).toBeDisabled()
  fireEvent.change(legacyWeek, { target: { value: '3' } })
  fireEvent.click(screen.getByRole('combobox', { name: 'Тип материала' }))
  fireEvent.click(await screen.findByRole('option', { name: 'Лекция' }))
  fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(
        ([url, request]) =>
          url.endsWith('/materials/mat-1') && request?.method === 'PATCH',
      ),
    ).toBe(true),
  )
})

it('clears staged PDFs when changing the selected course', async () => {
  backend()
  const router = renderApp('/admin/courses/subject-a?direction=exam')
  fireEvent.change(await screen.findByLabelText('Добавить PDF или ZIP'), {
    target: { files: [new File(['%PDF-1.7 content'], 'subject-a.pdf')] },
  })
  expect(await screen.findByText('subject-a.pdf')).toBeVisible()
  act(() => {
    void router.navigate({
      to: '/admin/courses/$subjectId',
      params: { subjectId: 'subject-b' },
      search: { direction: 'exam' },
    })
  })
  const leaveDialog = await screen.findByRole('alertdialog', {
    name: 'Материалы ещё не отправлены',
  })
  fireEvent.click(
    within(leaveDialog).getByRole('button', { name: 'Уйти со страницы' }),
  )
  expect(
    await screen.findByRole('heading', { name: 'Статистика' }),
  ).toBeVisible()
  expect(screen.queryByText('subject-a.pdf')).not.toBeInTheDocument()
})

it('allows changing uploaded material metadata without re-uploading the PDF', async () => {
  const fetch = backend()
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(await screen.findByRole('spinbutton', { name: 'Неделя' })).toHaveValue(
    1,
  )
  fireEvent.change(screen.getByRole('spinbutton', { name: 'Неделя' }), {
    target: { value: '2' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(
        ([url, request]) =>
          url.endsWith('/materials/mat-1') && request?.method === 'PATCH',
      ),
    ).toBe(true),
  )
  const patchRequest = fetch.mock.calls.find(
    ([url, request]) =>
      url.endsWith('/materials/mat-1') && request?.method === 'PATCH',
  )?.[1]
  expect(JSON.parse(patchRequest?.body as string)).toEqual({
    week: 2,
    kind: 'lecture',
  })

  fireEvent.click(screen.getByRole('combobox', { name: 'Тип материала' }))
  fireEvent.click(await screen.findByRole('option', { name: 'Туториал' }))
  fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(([url, request]) => {
        if (!url.endsWith('/materials/mat-1') || request?.method !== 'PATCH')
          return false
        return JSON.parse(request.body as string).kind === 'tutorial'
      }),
    ).toBe(true),
  )
  expect(
    fetch.mock.calls.some(
      ([url, request]) =>
        url.endsWith('/materials') && request?.method === 'POST',
    ),
  ).toBe(false)
})

it('restores the confirmed week and retries a rejected metadata update', async () => {
  const fetch = backend({ metadataFailCount: 1 })
  renderApp('/admin/courses/subject-a?direction=exam')
  const week = await screen.findByRole('spinbutton', { name: 'Неделя' })
  fireEvent.change(week, { target: { value: '2' } })
  fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.filter(
        ([url, init]) =>
          url.endsWith('/materials/mat-1') && init?.method === 'PATCH',
      ),
    ).toHaveLength(1),
  )
  const error = await screen.findByRole('alert')
  expect(error).toHaveTextContent('Не удалось сохранить метаданные.')
  expect(week).toHaveValue(2)
  fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }))
  await waitFor(() => expect(week).toHaveValue(2))
  expect(
    fetch.mock.calls.filter(
      ([url, init]) =>
        url.endsWith('/materials/mat-1') && init?.method === 'PATCH',
    ),
  ).toHaveLength(2)
})

it('shows a rejected kind update and retries it', async () => {
  const fetch = backend({ metadataFailCount: 1 })
  renderApp('/admin/courses/subject-a?direction=exam')
  fireEvent.click(
    await screen.findByRole('combobox', {
      name: 'Тип материала',
    }),
  )
  fireEvent.click(await screen.findByRole('option', { name: 'Туториал' }))
  fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }))
  const error = await screen.findByRole('alert')
  expect(error).toHaveTextContent('Не удалось сохранить метаданные.')
  fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.filter(
        ([url, init]) =>
          url.endsWith('/materials/mat-1') && init?.method === 'PATCH',
      ),
    ).toHaveLength(2),
  )
})

it('shows material blockers when an existing graph cannot be rebuilt', async () => {
  backend({
    materials: [{ ...sampleMaterials[0], week: null, kind: null }],
    graph: { version: 'v1', nodes: [] },
  })
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(
    await screen.findByText('Пересборка недоступна из-за материалов'),
  ).toBeVisible()
  expect(
    screen.getByText('У 1 материалов не указаны неделя или тип.'),
  ).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Пересобрать граф' }),
  ).toBeDisabled()
})

it('keeps the last successful publication date visible after a failed rebuild', async () => {
  backend({
    graph: {
      version: 'v1',
      published_at: 1791356400,
      nodes: [
        {
          id: 'topic-1',
          title: 'Матрицы',
          content: 'Основы',
          prerequisites: [],
          outcomes: [],
          questions: [],
        },
      ],
    },
    build: {
      id: 'build-2',
      subject_id: 'subject-a',
      status: 'failed',
      stage: null,
      started_at: 1791360000,
      completed_at: 1791363600,
      error: { code: 'GRAPH_BUILD_FAILED', message: 'Сборка не удалась.' },
    },
  })
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(await screen.findByText('Сборка не удалась.')).toBeVisible()
  expect(screen.getByText(/Последняя успешная публикация: /)).toHaveTextContent(
    '2026',
  )
  expect(screen.queryByText(/Завершение сборки: /)).not.toBeInTheDocument()
})

it('blocks graph generation while a material upload is pending', async () => {
  let finishUpload!: () => void
  backend({
    uploadGate: new Promise<void>((resolve) => (finishUpload = resolve)),
  })
  renderApp('/admin/courses/subject-a?direction=exam')
  const buildButton = await screen.findByRole('button', {
    name: /Собрать граф тем/,
  })
  await waitFor(() => expect(buildButton).toBeEnabled())
  fireEvent.change(await screen.findByLabelText('Добавить PDF или ZIP'), {
    target: {
      files: [
        new File(['%PDF-1.7 pdfcontent'], 'new_lecture.pdf', {
          type: 'application/pdf',
        }),
      ],
    },
  })
  fireEvent.click(await screen.findByRole('button', { name: 'Отправить всё' }))
  await waitFor(() => expect(buildButton).toBeDisabled())
  for (const kind of screen.getAllByRole('combobox', { name: 'Тип материала' }))
    expect(kind).toBeDisabled()
  expect(
    screen.getByText('Дождитесь сохранения изменений материалов.'),
  ).toBeVisible()
  await act(async () => finishUpload())
  await waitFor(() => expect(buildButton).toBeEnabled())
})

it('shows the durable active build stage recovered from the server', async () => {
  backend({
    build: {
      id: 'build-1',
      subject_id: 'subject-a',
      status: 'running',
      stage: 'dedup',
      started_at: 1791356400,
      completed_at: null,
      error: null,
    },
  })
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(await screen.findByText('Подготовка задачи…')).toBeVisible()
  expect(screen.getByText('Собираем граф тем')).toBeVisible()
  expect(
    screen.getByRole('button', { name: /Собрать граф тем/ }),
  ).toBeDisabled()
})

it('shows the stage, its steps and the share done of a running build', async () => {
  backend({
    build: {
      id: 'build-1',
      subject_id: 'subject-a',
      status: 'running',
      stage: null,
      started_at: null,
      completed_at: null,
      error: null,
      progress: {
        stage: 'prerequisites',
        stage_index: 3,
        stage_count: 5,
        done: 2,
        total: 8,
      },
    },
  })
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(
    await screen.findByText(
      'Проверка пререквизитов · этап 3 из 5 · шаг 2 из 8',
    ),
  ).toBeVisible()
  const bar = screen.getByRole('progressbar', { name: 'Собираем граф тем' })
  expect(bar).toHaveAttribute('aria-valuenow', '45')
  expect(screen.getByText('45%')).toBeVisible()
})

it('requires confirmation before replacing an existing graph', async () => {
  const fetch = backend({
    graph: {
      version: 'v1',
      nodes: [
        {
          id: 'topic-1',
          title: 'Матрицы',
          content: 'Основы матриц',
          prerequisites: [],
          outcomes: [],
          questions: [],
        },
      ],
    },
  })
  renderApp('/admin/courses/subject-a?direction=exam')
  expect(
    await screen.findByText('Курс готов для студентов. В графе тем: 1.'),
  ).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Пересобрать граф' }))
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent(
    'Активные диагностики по курсу будут отменены.',
  )
  expect(
    fetch.mock.calls.some(
      ([url, request]) =>
        url.endsWith('/graph/build') && request?.method === 'POST',
    ),
  ).toBe(false)
  fireEvent.click(within(dialog).getByRole('button', { name: 'Начать сборку' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(
        ([url, request]) =>
          url.endsWith('/graph/build') && request?.method === 'POST',
      ),
    ).toBe(true),
  )
})

it('toggles markdown text preview for an uploaded material', async () => {
  backend()
  renderApp('/admin/courses/subject-a?direction=exam')
  const previewBtn = await screen.findByRole('button', {
    name: 'Предпросмотр',
  })
  expect(screen.queryByText(/Формула: \$y = Wx \+ b\$/)).not.toBeInTheDocument()
  fireEvent.click(previewBtn)
  const dialog = await screen.findByRole('dialog')
  expect(
    within(dialog).getByRole('heading', { name: 'Лекция 1' }),
  ).toBeVisible()
  expect(dialog.querySelector('.katex')).toBeInTheDocument()
})

it('downloads raw material file using downloadCourseMaterialRaw', async () => {
  const fetch = backend()
  renderApp('/admin/courses/subject-a?direction=exam')
  const downloadBtn = await screen.findByRole('button', {
    name: 'Скачать lecture1.pdf',
  })
  fireEvent.click(downloadBtn)
  await waitFor(() => {
    expect(
      fetch.mock.calls.some(([url]) => url.endsWith('/materials/mat-1/raw')),
    ).toBe(true)
  })
})

it('deletes an uploaded material after confirmation', async () => {
  const fetch = backend()
  renderApp('/admin/courses/subject-a?direction=exam')
  const deleteBtn = await screen.findByRole('button', {
    name: 'Удалить',
  })
  fireEvent.click(deleteBtn)
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent(
    'Удалить этот файл и его распознанный текст?',
  )
  fireEvent.click(
    within(dialog).getByRole('button', {
      name: 'Подтвердить',
    }),
  )
  await waitFor(() => {
    expect(
      fetch.mock.calls.some(
        ([url, req]) =>
          url.endsWith('/materials/mat-1') && req?.method === 'DELETE',
      ),
    ).toBe(true)
  })
})

it('shows a warning dialog and keeps failed files available for retry', async () => {
  backend({ partialUpload: true })
  renderApp('/admin/courses/subject-a?direction=exam')
  const input = await screen.findByLabelText('Добавить PDF или ZIP')
  fireEvent.change(input, {
    target: {
      files: [
        new File(['%PDF-1.7 bad'], 'corrupted.pdf', {
          type: 'application/pdf',
        }),
      ],
    },
  })
  expect(await screen.findByText(/corrupted\.pdf/)).toBeVisible()
  const submitBtn = screen.getByRole('button', { name: 'Отправить всё' })
  fireEvent.click(submitBtn)
  expect(
    await screen.findByRole('dialog', { name: 'Загружены не все материалы' }),
  ).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Понятно' }))
  expect(await screen.findByText(/Ошибка распознавания/)).toBeVisible()
  expect(screen.getByText('corrupted.pdf')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Отправить всё' })).toBeEnabled()
})

it('shows download error alert when downloading raw material fails', async () => {
  backend({ downloadFails: true })
  renderApp('/admin/courses/subject-a?direction=exam')
  const downloadBtn = await screen.findByRole('button', {
    name: 'Скачать lecture1.pdf',
  })
  fireEvent.click(downloadBtn)
  expect(await screen.findByRole('alert')).toBeVisible()
})

it('expands a PDF-only ZIP into independently editable queue rows', async () => {
  const { zipSync, strToU8 } = await import('fflate')
  backend()
  renderApp('/admin/courses/subject-a?direction=exam')
  const input = await screen.findByLabelText('Добавить PDF или ZIP')
  const archive = new File(
    [
      zipSync({
        'week-1.pdf': strToU8('%PDF-1.7 one'),
        'week-2.pdf': strToU8('%PDF-1.7 two'),
      }),
    ],
    'materials.zip',
  )
  fireEvent.change(input, { target: { files: [archive] } })
  expect(await screen.findByText('week-1.pdf')).toBeVisible()
  expect(await screen.findByText('week-2.pdf')).toBeVisible()
  const materialsList = screen
    .getAllByRole('list')
    .find((list) => within(list).queryByText('week-1.pdf'))
  expect(materialsList).toBeDefined()
  expect(
    within(materialsList!)
      .getAllByRole('listitem')
      .filter((row) => /week-[12]\.pdf/.test(row.textContent ?? ''))
      .map((row) => within(row).getByRole('spinbutton', { name: 'Неделя' })),
  ).toHaveLength(2)
})

it('rejects a mixed ZIP without adding any PDFs to the queue', async () => {
  const { zipSync, strToU8 } = await import('fflate')
  backend()
  renderApp('/admin/courses/subject-a?direction=exam')
  const input = await screen.findByLabelText('Добавить PDF или ZIP')
  const archive = new File(
    [
      zipSync({
        'week.pdf': strToU8('%PDF-1.7'),
        'notes.txt': strToU8('not a PDF'),
      }),
    ],
    'materials.zip',
  )
  fireEvent.change(input, { target: { files: [archive] } })
  expect(
    await screen.findByRole('dialog', {
      name: 'Не удалось загрузить материалы',
    }),
  ).toHaveTextContent('notes.txt')
  expect(screen.queryByText('week.pdf')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Отправить всё' }),
  ).not.toBeInTheDocument()
})
