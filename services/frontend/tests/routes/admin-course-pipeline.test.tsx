import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { zipSync } from 'fflate'
import {
  createMemoryHistory,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { afterEach, expect, it, vi } from 'vitest'
import { createQueryClient } from '@/bootstrap/providers'
import { routeTree } from '@/routeTree.gen'

const admin = {
  id: 'admin-1',
  email: 'admin@example.com',
  display_name: 'Администратор',
  role: 'admin',
  created_at: 1,
}
const material = {
  id: 'mat-1',
  subject_id: 'subject-b',
  filename: 'lecture.pdf',
  content_type: 'application/pdf',
  file_size: 100,
  s3_raw_key: 'raw',
  s3_parsed_key: 'parsed',
  parsed_text: '# Тема лекции\n\nФормула: $y = Wx + b$',
  char_count: 12,
  status: 'parsed',
  week: 1,
  kind: 'lecture',
  created_at: 1,
  updated_at: 1,
}
function setup({
  processing = false,
  pending = false,
  cleaning = false,
  cleaningFailed = false,
  graphReady = false,
  competencyMap,
  materials,
  uploadGate,
  buildGate,
  learningMapImported: initiallyLearningMapImported = false,
  learningWarnings = [],
  assessmentWarnings = [],
  buildProgress,
  buildSource,
  buildStatus,
  mapState = { fingerprint: 'map-a', stale: false, failed: false },
  learningMapRevision,
}: {
  mapState?: {
    fingerprint: string
    stale: boolean
    failed: boolean
    pending?: boolean
    learningRevision?: number
    publishedRevision?: number | null
  }
  processing?: boolean
  pending?: boolean
  cleaning?: boolean
  cleaningFailed?: boolean
  graphReady?: boolean
  materials?: (typeof material)[]
  buildGate?: Promise<void>
  learningMapImported?: boolean
  learningMapRevision?: number
  learningWarnings?: {
    row: number
    column_index: number
    column: string
    code: string
  }[]
  // The import of a competency map: a bad map names its cells; a good one is
  // built from its first sheet and the build publishes the graph of the map.
  competencyMap?: 'valid' | 'invalid'
  uploadGate?: Promise<void>
  assessmentWarnings?: { row: number; column: string; message: string }[]
  buildProgress?: {
    stage: string
    stage_index: number
    stage_count: number
    done: number
    total: number
    remaining_seconds?: number
  }
  buildSource?: 'materials' | 'competency_map'
  buildStatus?: 'pending' | 'processing' | 'ready' | 'failed'
} = {}) {
  let graphRequested = false
  let mapImported = false
  let pairedMapImported = false
  let activeLearningRevision =
    learningMapRevision ?? (initiallyLearningMapImported ? 1 : 0)
  const hasSavedMap =
    assessmentWarnings.length > 0 ||
    mapState.failed ||
    mapState.publishedRevision !== undefined
  const courseMaterials = materials ?? [material]
  const calls = vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/auth/me')) return Response.json(admin)
    if (url.endsWith('/admin/material-kinds'))
      return Response.json([
        { code: 'lecture', label: 'Лекция' },
        { code: 'lab', label: 'Лабораторная' },
      ])
    if (url.endsWith('/subjects'))
      return Response.json([
        {
          id: 'subject-a',
          name: 'Первый курс',
          ready: false,
          learning_ready: activeLearningRevision > 0,
          exam_ready: false,
          preparation: {
            exam: { state: 'empty', source: null, stale: false },
            learn: { state: activeLearningRevision > 0 ? 'ready' : 'empty' },
          },
        },
        {
          id: 'subject-b',
          name: 'Второй курс',
          ready: graphReady || hasSavedMap || (mapImported && !pairedMapImported),
          learning_ready: activeLearningRevision > 0,
          exam_ready: graphReady || hasSavedMap || (mapImported && !pairedMapImported),
          preparation: {
            exam:
              graphReady ||
              (mapImported && !pairedMapImported) ||
              (hasSavedMap && !mapImported)
                ? {
                    state: 'ready',
                    source: mapImported ? 'competency_map' : 'materials',
                    stale: false,
                  }
                : mapState.failed
                  ? {
                      state: 'failed',
                      source: 'competency_map',
                      stale: mapState.stale,
                    }
                  : mapImported || mapState.pending
                  ? { state: 'queued', source: 'competency_map', stale: false }
                  : { state: 'empty', source: null, stale: false },
            learn: {
              state: activeLearningRevision > 0 ? 'ready' : 'empty',
            },
          },
        },
      ])
    if (url.includes('/learn/subjects/') && url.endsWith('/competency-map'))
      return Response.json({
        revision: activeLearningRevision,
        imported_at: activeLearningRevision > 0 ? 2 : null,
        competencies:
          activeLearningRevision > 0
            ? [
                {
                  constituents: [
                    {
                      outcomes: [{ tasks: [{}] }, { tasks: [{}, {}] }],
                    },
                    { outcomes: [{ tasks: [{}] }] },
                  ],
                },
              ]
            : [],
      })
    if (
      url.includes('/learn/admin/subjects/') &&
      url.endsWith('/competency-map/import')
    ) {
      activeLearningRevision += 1
      return Response.json({
        revision: activeLearningRevision,
        imported_at: 2,
        competency_count: 1,
        constituent_count: 1,
        outcome_count: 3,
        task_count: 3,
        unparsed_task_cell_count: learningWarnings.length,
        warnings: learningWarnings,
      })
    }
    if (url.endsWith('/materials') && init?.method === 'POST') {
      await uploadGate
      const files = (init.body as FormData).getAll('file') as File[]
      return Response.json({
        uploaded: files.map((file) => ({ ...material, filename: file.name })),
        failed: [],
        results: JSON.parse((init.body as FormData).get('items') as string).map(
          (item: { client_id: string }, index: number) => ({
            client_id: item.client_id,
            material: { ...material, filename: files[index].name },
          }),
        ),
      })
    }
    if (url.endsWith('/materials'))
      return Response.json(
        courseMaterials.length === 0
          ? []
          : [
              cleaningFailed
                ? {
                    ...material,
                    cleaning: {
                      status: 'failed',
                      updated_at: 2,
                      attempts: 3,
                      error_message: 'Очистка материала не удалась.',
                    },
                  }
                : pending
                  ? { ...material, status: 'pending' }
                  : cleaning
                    ? {
                        ...material,
                        cleaning: {
                          status: 'processing',
                          updated_at: 1,
                          attempts: 1,
                          error_message: '',
                        },
                      }
                    : processing
                      ? {
                          ...material,
                          processing: {
                            stage: 'translation',
                            status: 'processing',
                            updated_at: 1,
                            attempts: 1,
                          },
                        }
                      : material,
            ],
      )
    if (url.endsWith('/graph/competency-map/retry')) {
      if (
        new Headers(init?.headers).get('X-Expected-Map-Fingerprint') !==
        mapState.fingerprint
      ) {
        return Response.json(
          {
            code: 'COMPETENCY_MAP_CHANGED',
            message:
              'Карта изменилась. Обновите страницу и повторите подготовку.',
          },
          { status: 409 },
        )
      }
      mapState.failed = false
      graphRequested = true
      return Response.json(
        {
          status: 'pending',
          attempts: 0,
          error_message: '',
          graph: null,
          graph_source: hasSavedMap ? 'competency_map' : 'none',
          graph_fingerprint: hasSavedMap ? 'published-map' : '',
          graph_map_revision: mapState.publishedRevision ?? null,
          stale: mapState.stale,
          inputs_ready: false,
          updated_at: 1,
          build_source: 'competency_map',
          map: mapState.learningRevision
            ? {
                filename: 'map.xlsx',
                sheet: 'Карта компетенций v2',
                fingerprint: mapState.fingerprint,
                learning_revision: mapState.learningRevision,
              }
            : null,
        },
        { status: 202 },
      )
    }
    if (url.endsWith('/graph/competency-map/skip-missing-criteria')) {
      if (
        new Headers(init?.headers).get('X-Expected-Map-Fingerprint') !==
        mapState.fingerprint
      ) {
        return Response.json(
          {
            code: 'COMPETENCY_MAP_CHANGED',
            message: 'Карта изменилась. Подтвердите исключение заново.',
          },
          { status: 409 },
        )
      }
      graphRequested = true
      return Response.json(
        {
          status: 'pending',
          attempts: 1,
          error_message: '',
          graph: null,
          graph_source: hasSavedMap ? 'competency_map' : 'none',
          graph_fingerprint: hasSavedMap ? 'published-map' : '',
          graph_map_revision: mapState.publishedRevision ?? null,
          stale: mapState.stale,
          inputs_ready: true,
          updated_at: 3,
          build_source: 'competency_map',
          map: mapState.learningRevision
            ? {
                filename: 'map.xlsx',
                sheet: 'Карта компетенций v2',
                fingerprint: mapState.fingerprint,
                learning_revision: mapState.learningRevision,
              }
            : null,
        },
        { status: 202 },
      )
    }
    if (url.endsWith('/graph/competency-map')) {
      if (competencyMap === 'invalid')
        return Response.json(
          {
            code: 'COMPETENCY_MAP_INVALID',
            message: 'Карта на листе «Карта» содержит ошибки.',
            details: [
              {
                path: 'file',
                code: 'COMPETENCY_MAP_CELL',
                message:
                  'Строка 3, «Уровень по Блуму»: Неизвестный уровень по Блуму.',
              },
            ],
          },
          { status: 422 },
        )
      mapImported = true
      const uploadedFile = (init?.body as FormData).get('file') as File
      pairedMapImported = uploadedFile.name === 'paired-map.xlsx'
      if (pairedMapImported) {
        activeLearningRevision =
          mapState.learningRevision ?? activeLearningRevision + 1
      }
      return Response.json(
        {
          status: 'pending',
          attempts: 0,
          error_message: '',
          graph: null,
          graph_source: hasSavedMap ? 'competency_map' : 'none',
          build_source: 'competency_map',
          map: {
            filename: 'map.xlsx',
            sheet: 'Карта компетенций v2',
            fingerprint: mapState.fingerprint,
            learning_revision: pairedMapImported ? activeLearningRevision : null,
          },
          graph_map_revision: mapState.publishedRevision ?? null,
          stale: mapState.stale,
          inputs_ready: true,
          updated_at: 0,
          graph_fingerprint: hasSavedMap ? 'published-map' : '',
        },
        { status: 202 },
      )
    }
    if (mapImported && url.endsWith('/graph') && pairedMapImported) {
      await buildGate
      return Response.json({
        status: 'ready',
        attempts: 1,
        error_message: '',
        graph: {
          assessment_warnings: assessmentWarnings,
          topics: [
            { id: 'basic', title: 'Называет части агента' },
            { id: 'main', title: 'Выбирает тип агента' },
          ],
          prerequisites: [{ source: 'basic', target: 'main' }],
          hierarchy: [{ source: 'main', target: 'basic' }],
        },
        graph_source: 'competency_map',
        graph_fingerprint: mapState.fingerprint,
        graph_map_revision: activeLearningRevision,
        build_source: 'competency_map',
        map: {
          filename: 'paired-map.xlsx',
          sheet: 'Карта компетенций v2',
          fingerprint: mapState.fingerprint,
          learning_revision: activeLearningRevision,
        },
        stale: mapState.stale,
        inputs_ready: true,
        updated_at: 2,
      })
    }
    if (mapImported && url.endsWith('/graph')) {
      await buildGate
      return Response.json({
        status: 'ready',
        attempts: 0,
        error_message: '',
        graph: {
          assessment_warnings: assessmentWarnings,
          topics: [
            { id: 'basic', title: 'Называет части агента' },
            { id: 'main', title: 'Выбирает тип агента' },
          ],
          prerequisites: [{ source: 'basic', target: 'main' }],
          hierarchy: [{ source: 'main', target: 'basic' }],
        },
        graph_source: 'competency_map',
        graph_fingerprint: mapState.fingerprint,
        graph_map_revision: mapState.learningRevision ?? activeLearningRevision,
        build_source: 'competency_map',
        map: {
          filename: 'map.xlsx',
          sheet: 'Карта компетенций v2',
          fingerprint: mapState.fingerprint,
          learning_revision:
            mapState.learningRevision ?? activeLearningRevision,
        },
        stale: mapState.stale,
        inputs_ready: true,
        updated_at: 2,
      })
    }
    if (url.endsWith('/graph') || url.endsWith('/graph/build')) {
      if (init?.method === 'POST') graphRequested = true
      return Response.json({
        graph_source: hasSavedMap
          ? 'competency_map'
          : mapState.pending
            ? 'none'
            : 'built',
        progress: buildProgress,
        build_source:
          buildSource ??
          (buildProgress || hasSavedMap || mapState.pending
            ? 'competency_map'
            : 'materials'),
        status:
          buildStatus ??
          (mapState.failed
            ? 'failed'
            : mapState.pending
              ? 'pending'
              : buildProgress
                ? 'processing'
                : graphRequested
                  ? 'pending'
                  : graphReady
                    ? 'ready'
                    : 'not_requested'),
        attempts: 0,
        error_message: mapState.failed ? 'Не удалось подготовить граф.' : '',
        graph: graphReady
          ? {
              assessment_warnings: assessmentWarnings,
              topics: [
                {
                  id: 'topic-1',
                  title: 'Линейная регрессия',
                  provides: ['regression'],
                  requires: [],
                },
              ],
              prerequisites: [],
              hierarchy: [],
            }
          : null,
        stale: mapState.stale,
        inputs_ready: !processing && !cleaningFailed,
        updated_at: 1,
        map:
          hasSavedMap || mapState.pending
            ? {
                filename: 'map.xlsx',
                sheet: 'Карта компетенций v2',
                fingerprint: mapState.fingerprint,
                learning_revision: mapState.learningRevision ?? null,
              }
            : null,
        graph_fingerprint: hasSavedMap ? 'published-map' : '',
        graph_map_revision: mapState.publishedRevision ?? null,
      })
    }
    throw new Error(`Unexpected API call: ${url}`)
  })
  vi.stubGlobal('fetch', calls)
  return calls
}
function open(path = '/admin/courses/subject-b?direction=exam') {
  window.history.replaceState({}, '', path)
  const router = createRouter({
    context: { queryClient: createQueryClient() },
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree,
  })
  const container = document.createElement('div')
  container.id = 'root'
  document.body.appendChild(container)
  render(<RouterProvider router={router} />, { container })
  return router
}
afterEach(() => vi.unstubAllGlobals())

it('shows which imported tasks will use default assessment criteria', async () => {
  setup({
    graphReady: true,
    assessmentWarnings: [
      { row: 3, column: 'Задание 2', message: 'Используются общие критерии.' },
    ],
  })
  open()
  expect(
    await screen.findByText(/Для части заданий критерии в карте не заполнены/),
  ).toBeVisible()
  expect(
    screen.getByText('Строка 3, Задание 2: Используются общие критерии.'),
  ).toBeVisible()
  expect(screen.getByText('Общие критерии — максимум 4 балла:')).toBeVisible()
  expect(screen.getByText(/Предметная корректность \(0–2\): 2 —/)).toBeVisible()
  expect(screen.getByText(/Покрытие вопроса \(0–2\): 2 —/)).toBeVisible()
  expect(
    screen.getByText(/Объяснение, пример, расчёт или сравнение обязательны/),
  ).toBeVisible()
})

it('opens the selected course with original metadata controls and master graph API', async () => {
  const calls = setup()
  open()
  expect(
    await screen.findByRole('heading', { name: 'Второй курс' }),
  ).toBeInTheDocument()
  expect(await screen.findByText('lecture.pdf')).toBeInTheDocument()
  expect(
    calls.mock.calls.some(([url]) =>
      url.endsWith('/admin/subjects/subject-b/materials'),
    ),
  ).toBe(true)
  expect(calls.mock.calls.some(([url]) => /graph-builds/.test(url))).toBe(false)
  expect(screen.getByRole('combobox', { name: 'Тип материала' })).toBeVisible()
  expect(screen.getByRole('spinbutton', { name: 'Неделя' })).toHaveValue(1)
})

it('requires confirmation before excluding tasks without criteria', async () => {
  const calls = setup({
    graphReady: true,
    assessmentWarnings: [
      { row: 3, column: 'Задание 2', message: 'Критерии не заданы.' },
    ],
  })
  open()
  fireEvent.click(
    await screen.findByRole('button', {
      name: 'Пропустить задания без критериев',
    }),
  )
  expect(
    calls.mock.calls.some(([url]) => url.endsWith('/skip-missing-criteria')),
  ).toBe(false)
  const dialog = await screen.findByRole('alertdialog')
  expect(
    within(dialog).getByText(/активные диагностики отменятся/),
  ).toBeVisible()
  fireEvent.click(
    within(dialog).getByRole('button', { name: 'Исключить и пересобрать' }),
  )
  await waitFor(() =>
    expect(
      calls.mock.calls.some(([url]) => url.endsWith('/skip-missing-criteria')),
    ).toBe(true),
  )
})

it('shows actual question preparation and estimate without PDF blockers for a map', async () => {
  setup({
    materials: [],
    buildProgress: {
      stage: 'prepare_questions',
      stage_index: 1,
      stage_count: 2,
      done: 5,
      total: 15,
      remaining_seconds: 25,
    },
  })
  open()
  expect(
    await screen.findByText(
      /Подготовка вопросов и переформулировок · этап 1 из 2 · шаг 5 из 15/,
    ),
  ).toBeVisible()
  expect(screen.getByText(/осталось примерно 25 с/)).toBeVisible()
  expect(
    screen.queryByText('Загрузите хотя бы один материал курса.'),
  ).not.toBeInTheDocument()
})

it.each([
  ['queued build from materials', 'pending', 'materials'],
  ['processing build from competency map', 'processing', 'competency_map'],
] as const)(
  'disables material uploads during a reloaded %s',
  async (_name, status, source) => {
    setup({
      materials: [],
      buildStatus: status,
      buildSource: source,
      mapState: {
        fingerprint: 'building-map',
        stale: false,
        failed: false,
        pending: status === 'pending',
      },
      buildProgress:
        status === 'processing'
          ? {
              stage: 'topics',
              stage_index: 1,
              stage_count: 2,
              done: 1,
              total: 2,
            }
          : undefined,
    })
    open()

    const input = await screen.findByLabelText('Добавить PDF или ZIP')
    expect(input).toBeDisabled()
    expect(
      screen.getByText(
        'Загрузка материалов недоступна, пока собирается граф курса.',
      ),
    ).toBeVisible()
  },
)

it('ignores dropped files on the disabled upload area during graph preparation', async () => {
  const calls = setup({
    materials: [],
    buildStatus: 'processing',
    buildSource: 'materials',
    buildProgress: {
      stage: 'topics',
      stage_index: 1,
      stage_count: 2,
      done: 1,
      total: 2,
    },
  })
  open()

  const upload = await screen.findByLabelText('Добавить PDF или ZIP')
  const dropArea = upload.closest('label')
  expect(dropArea).not.toBeNull()
  const over = fireEvent.dragOver(dropArea!, {
    dataTransfer: { files: [new File(['pdf'], 'drop.pdf')] },
  })
  fireEvent.drop(dropArea!, {
    dataTransfer: { files: [new File(['pdf'], 'drop.pdf')] },
  })

  expect(over).toBe(false)
  expect(screen.queryByText('drop.pdf')).not.toBeInTheDocument()
  expect(
    calls.mock.calls.some(
      ([url, init]) =>
        String(url).endsWith('/materials') && init?.method === 'POST',
    ),
  ).toBe(false)
})

it('keeps actions on already uploaded materials available during graph preparation', async () => {
  setup({
    materials: [material],
    graphReady: true,
    buildStatus: 'processing',
    buildSource: 'materials',
    buildProgress: {
      stage: 'topics',
      stage_index: 1,
      stage_count: 2,
      done: 1,
      total: 2,
    },
  })
  open()

  expect(await screen.findByLabelText('Добавить PDF или ZIP')).toBeDisabled()
  const materials = await screen.findByRole('region', {
    name: 'Загруженные лекции и лабораторные',
  })
  expect(
    within(materials).getByRole('button', { name: 'Заменить PDF' }),
  ).toBeEnabled()
  expect(
    within(materials).getByRole('button', { name: 'Удалить' }),
  ).toBeEnabled()
})

it.each([
  ['ready', true],
  ['failed', false],
] as const)(
  'enables material uploads again after graph build is %s',
  async (status, ready) => {
    setup({
      materials: [],
      graphReady: ready,
      buildStatus: status,
      mapState: {
        fingerprint: 'finished-map',
        stale: false,
        failed: status === 'failed',
      },
    })
    open()

    expect(await screen.findByLabelText('Добавить PDF или ZIP')).toBeEnabled()
    expect(
      screen.queryByText(
        'Загрузка материалов недоступна, пока собирается граф курса.',
      ),
    ).not.toBeInTheDocument()
  },
)

it('shows the master pipeline stage while translation is running and blocks graph builds', async () => {
  setup({ processing: true })
  open()
  expect(
    await screen.findByText(
      'Дождитесь завершения обработки материалов: распознавания, перевода и очистки текста.',
    ),
  ).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /Собрать граф/ })).toBeDisabled()
})

it('keeps the master graph viewer for a published graph', async () => {
  setup({ graphReady: true })
  open()
  expect(
    await screen.findByText('Курс готов для студентов. В графе тем: 1.'),
  ).toBeInTheDocument()
  // The graph opens as a map without an extra click.
  const canvas = await screen.findByRole('region', {
    name: 'Граф знаний курса',
  })
  expect(within(canvas).getByText('Линейная регрессия')).toBeInTheDocument()
  expect(screen.queryByText('По карте компетенций')).not.toBeInTheDocument()
  expect(
    screen.queryByText('Общие критерии — максимум 4 балла:'),
  ).not.toBeInTheDocument()
})

it('keeps the legacy exam route on the unified course preparation screen', async () => {
  setup()
  const router = open('/admin/courses/subject-b?direction=exam')
  expect(
    await screen.findByRole('heading', { name: 'Подготовка курса' }),
  ).toBeVisible()
  expect(
    await screen.findByRole('button', {
      name: 'Импортировать карту компетенций',
    }),
  ).toBeVisible()
  expect(router.state.location.search).toEqual({ direction: 'exam' })
  expect(
    screen.queryByRole('heading', {
      name: 'Карта для экзаменационного графа',
    }),
  ).not.toBeInTheDocument()
})

it('lets admins choose the destination of a one-sheet workbook on the legacy exam URL', async () => {
  const calls = setup({ materials: [] })
  open('/admin/courses/subject-b?direction=exam')
  await chooseMap()

  const modePicker = await screen.findByRole('radiogroup', {
    name: 'Куда загрузить однолистовую карту?',
  })
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  fireEvent.click(
    within(modePicker).getByRole('radio', { name: 'Карта обучения' }),
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Подтвердить изменения' }),
  )

  await waitFor(() =>
    expect(
      calls.mock.calls.some(([url]) =>
        String(url).endsWith(
          '/learn/admin/subjects/subject-b/competency-map/import',
        ),
      ),
    ).toBe(true),
  )
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
})

function chooseMap() {
  return screen.findByLabelText('Файл карты компетенций').then((input) =>
    fireEvent.change(input, {
      target: {
        files: [new File(['PK'], 'map.xlsx', { type: 'application/zip' })],
      },
    }),
  )
}

function pairedWorkbook() {
  const sheets = ['Geek', 'Обучение']
    .map(
      (name, index) =>
        `<sheet name="${name}" sheetId="${index + 1}" r:id="rId${index + 1}"/>`,
    )
    .join('')
  return new File(
    [
      zipSync({
        'xl/workbook.xml': new TextEncoder().encode(
          `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets>${sheets}</sheets></workbook>`,
        ),
      }),
    ],
    'paired-map.xlsx',
  )
}

it('shows immediate learning readiness and a pending Geek build after page reload', async () => {
  setup({
    materials: [],
    learningMapImported: true,
    learningMapRevision: 4,
    mapState: {
      fingerprint: 'pending-map',
      stale: false,
      failed: false,
      pending: true,
      learningRevision: 4,
    },
  })
  open('/admin/courses/subject-b')
  const state = await screen.findByRole('region', {
    name: 'Текущее состояние карты',
  })

  expect(
    within(state).getByText('Карта компетенций успешно загружена'),
  ).toBeVisible()
  expect(within(state).getByText('Готовые задания')).toBeVisible()
  expect(within(state).getByText('Обучение: версия 4 доступна')).toBeVisible()
  expect(within(state).getByText('Geek: готовится версия 4')).toBeVisible()
  expect(
    within(state).queryByText('Карта ещё не загружена.'),
  ).not.toBeInTheDocument()
})

it('shows active learning and published Geek versions separately during replacement', async () => {
  setup({
    materials: [],
    learningMapImported: true,
    learningMapRevision: 2,
    mapState: {
      fingerprint: 'replacement-map',
      stale: false,
      failed: false,
      pending: true,
      learningRevision: 2,
      publishedRevision: 1,
    },
  })
  open('/admin/courses/subject-b')
  const state = await screen.findByRole('region', {
    name: 'Текущее состояние карты',
  })

  expect(
    within(state).getByText('Карта компетенций успешно загружена'),
  ).toBeVisible()
  expect(within(state).getByText('Готовые задания')).toBeVisible()
  expect(within(state).getByText('Обучение: версия 2 доступна')).toBeVisible()
  expect(
    within(state).getByText('Geek: версия 1 работает; готовится версия 2'),
  ).toBeVisible()
  expect(
    within(state).queryByText('Карта ещё не загружена.'),
  ).not.toBeInTheDocument()
})

it('keeps the previous active map visible and reports a failed pending replacement after reload', async () => {
  setup({
    materials: [],
    learningMapImported: true,
    learningMapRevision: 2,
    mapState: {
      fingerprint: 'failed-replacement',
      stale: true,
      failed: true,
      learningRevision: 2,
      publishedRevision: 1,
    },
  })
  open('/admin/courses/subject-b')
  const state = await screen.findByRole('region', {
    name: 'Текущее состояние карты',
  })

  expect(
    within(state).getByText('Карта компетенций успешно загружена'),
  ).toBeVisible()
  expect(within(state).getByText('Готовые задания')).toBeVisible()
  expect(within(state).getByRole('alert')).toHaveTextContent(
    'Не удалось подготовить граф.',
  )
  expect(within(state).getByText('Обучение: версия 2 доступна')).toBeVisible()
  expect(
    within(state).getByText(
      'Geek: версия 1 работает; версия 2 не опубликована',
    ),
  ).toBeVisible()
})

it('shows the learning map statistics separately from the one shared upload', async () => {
  setup({ learningMapImported: true })
  open()
  const state = await screen.findByRole('region', {
    name: 'Текущее состояние карты',
  })
  expect(within(state).getByText('Компетенции')).toBeVisible()
  expect(within(state).getByText('Составляющие')).toBeVisible()
  expect(within(state).getByText('Образовательные результаты')).toBeVisible()
  expect(within(state).getByText('Готовые задания')).toBeVisible()
  expect(within(state).getByText('Загружена')).toBeVisible()
  expect(within(state).getByText('Версия карты')).toBeVisible()
  expect(within(state).getAllByText('1')).toHaveLength(2)
  expect(within(state).getByText('2')).toBeVisible()
  expect(within(state).getByText('3')).toBeVisible()
  expect(
    await screen.findByRole('region', { name: 'Обновить карту компетенций' }),
  ).toBeVisible()
})

it('refreshes learning map statistics after the shared background build completes', async () => {
  let finishBuild!: () => void
  const buildGate = new Promise<void>((resolve) => {
    finishBuild = resolve
  })
  const calls = setup({ materials: [], buildGate })
  open()
  await screen.findByText('Карта ещё не загружена.')
  fireEvent.change(await screen.findByLabelText('Файл карты компетенций'), {
    target: { files: [pairedWorkbook()] },
  })
  fireEvent.click(
    await screen.findByRole('button', {
      name: 'Подготовить для обучения и Geek',
    }),
  )
  await waitFor(() =>
    expect(
      calls.mock.calls.some(([url]) =>
        String(url).endsWith('/graph/competency-map'),
      ),
    ).toBe(true),
  )
  expect(await screen.findByText(/Подготавливаем единый граф/)).toBeVisible()
  const stateWhileBuilding = screen.getByRole('region', {
    name: 'Текущее состояние карты',
  })
  expect(
    await within(stateWhileBuilding).findByText('Обучение: версия 1 доступна'),
  ).toBeVisible()
  expect(
    within(stateWhileBuilding).getByText('Geek: готовится версия 1'),
  ).toBeVisible()
  expect(within(stateWhileBuilding).getByText('Готовые задания')).toBeVisible()
  expect(
    within(stateWhileBuilding).queryByText('Карта ещё не загружена.'),
  ).not.toBeInTheDocument()

  finishBuild()
  const state = await screen.findByRole('region', {
    name: 'Текущее состояние карты',
  })
  await waitFor(() => expect(within(state).getByText('4')).toBeVisible())
  expect(
    within(state).getByText('Карта компетенций успешно загружена'),
  ).toBeVisible()
  expect(within(state).getByText('Geek: версия 1 опубликована')).toBeVisible()
})

it('keeps imported learning warnings in a readable row and reason table', async () => {
  setup({
    materials: [],
    learningWarnings: [
      {
        row: 4,
        column_index: 2,
        column: 'Задание 1',
        code: 'TASK_MISSING_SCREEN',
      },
    ],
  })
  open('/admin/courses/subject-b?direction=learn')
  fireEvent.change(await screen.findByLabelText('Файл карты компетенций'), {
    target: { files: [new File(['map'], 'map.csv', { type: 'text/csv' })] },
  })
  fireEvent.click(
    await screen.findByRole('button', { name: 'Подтвердить изменения' }),
  )

  const warnings = await screen.findByRole('region', {
    name: 'Предупреждения импорта',
  })
  expect(
    within(warnings).getByRole('columnheader', { name: 'Строка' }),
  ).toBeVisible()
  expect(
    within(warnings).getByRole('columnheader', { name: 'Что не так' }),
  ).toBeVisible()
  expect(within(warnings).getByText('4')).toBeVisible()
  expect(within(warnings).getByText('Задание 1:')).toBeVisible()
  expect(within(warnings).getByText(/обрывок без «Экран:»/)).toBeVisible()
})

it('builds the graph from the first sheet of a competency map', async () => {
  const calls = setup({ competencyMap: 'valid' })
  open()
  await chooseMap()
  fireEvent.click(
    await screen.findByRole('button', {
      name: /^(Импортировать|Подготовить для обучения и Geek|Подтвердить изменения)$/,
    }),
  )
  const dialog = await screen.findByRole('alertdialog')
  expect(within(dialog).getByText(/по карте «map\.xlsx»/)).toBeVisible()
  fireEvent.click(within(dialog).getByRole('button', { name: 'Импортировать' }))
  expect(
    await screen.findByText('По карте компетенций', undefined, {
      timeout: 8000,
    }),
  ).toBeVisible()
  expect(
    screen.getByText('Последняя карта: map.xlsx, лист «Карта компетенций v2».'),
  ).toBeVisible()
  expect(
    screen.getByText('Курс готов для студентов. В графе тем: 2.'),
  ).toBeInTheDocument()
  const imports = calls.mock.calls.filter(([url]) =>
    String(url).endsWith('/graph/competency-map'),
  )
  // One send, with the file only: the server reads the first sheet.
  expect(imports).toHaveLength(1)
  expect((imports[0][1]?.body as FormData).has('sheet')).toBe(false)
  expect(screen.queryByLabelText('Лист карты')).not.toBeInTheDocument()
}, 15000)

it('imports a map through the shared course ID and activates learning while Geek prepares', async () => {
  let finishBuild!: () => void
  const buildGate = new Promise<void>((resolve) => {
    finishBuild = resolve
  })
  const calls = setup({ competencyMap: 'valid', materials: [], buildGate })
  const router = open('/admin/courses/subject-b?direction=exam')
  expect(
    await screen.findByRole('heading', { name: 'Второй курс', level: 1 }),
  ).toBeVisible()
  expect(await screen.findByText(/Материалов: 0/)).toBeVisible()
  fireEvent.change(await screen.findByLabelText('Файл карты компетенций'), {
    target: { files: [pairedWorkbook()] },
  })
  fireEvent.click(
    await screen.findByRole('button', {
      name: 'Подготовить для обучения и Geek',
    }),
  )
  await waitFor(() =>
    expect(
      calls.mock.calls.some(([url]) =>
        String(url).endsWith('/graph/competency-map'),
      ),
    ).toBe(true),
  )
  expect(await screen.findByText('Обучение: готово')).toBeVisible()
  expect(await screen.findByText('Экзамен: подготовка в очереди')).toBeVisible()
  expect(await screen.findByText(/Обучение: версия 1 доступна/)).toBeVisible()
  expect(await screen.findByText(/Geek: готовится версия 1/)).toBeVisible()
  finishBuild()
  expect(
    await screen.findByText('Курс готов для студентов. В графе тем: 2.', {
      timeout: 8000,
    }),
  ).toBeVisible()
  expect(router.state.location.pathname).toBe('/admin/courses/subject-b')
  expect(router.state.location.search).toEqual({ direction: 'exam' })
  expect(
    calls.mock.calls.some(([url]) =>
      String(url).endsWith('/admin/subjects/subject-b/graph/competency-map'),
    ),
  ).toBe(true)
  expect(
    calls.mock.calls.some(([url]) =>
      String(url).endsWith('/admin/subjects/subject-b/materials'),
    ),
  ).toBe(true)
}, 15000)

it('opens the same course preparation flow from both legacy directions', async () => {
  setup()
  const router = open('/admin/courses/subject-b?direction=exam')
  expect(
    await screen.findByRole('heading', { name: 'Второй курс', level: 1 }),
  ).toBeVisible()
  expect(
    screen.getByRole('region', { name: 'Загруженные лекции и лабораторные' }),
  ).toBeVisible()
  expect(screen.getByRole('region', { name: 'Граф тем курса' })).toBeVisible()
  expect(
    screen.queryByRole('navigation', { name: 'Разделы администрирования' }),
  ).not.toBeInTheDocument()

  await router.navigate({
    to: '/admin/courses/$subjectId',
    params: { subjectId: 'subject-b' },
    search: { direction: 'learn' },
  })
  expect(
    await screen.findByRole('region', {
      name: 'Загруженные лекции и лабораторные',
    }),
  ).toBeVisible()
  expect(screen.getByRole('region', { name: 'Граф тем курса' })).toBeVisible()
  expect(
    screen.queryByRole('navigation', { name: 'Разделы администрирования' }),
  ).not.toBeInTheDocument()
  expect(router.state.location.search).toEqual({ direction: 'learn' })
})

it('names the bad cells of a rejected competency map', async () => {
  setup({ competencyMap: 'invalid' })
  open()
  await chooseMap()
  fireEvent.click(
    await screen.findByRole('button', {
      name: /^(Импортировать|Подготовить для обучения и Geek|Подтвердить изменения)$/,
    }),
  )
  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Импортировать' }))
  const alert = await screen.findByText(
    'Строка 3, «Уровень по Блуму»: Неизвестный уровень по Блуму.',
  )
  expect(alert).toBeVisible()
  expect(
    screen.getByText('Карта на листе «Карта» содержит ошибки.'),
  ).toBeVisible()
})

it('uploads a queued PDF using the master multipart field', async () => {
  const calls = setup()
  open()
  const input = await screen.findByLabelText('Добавить PDF или ZIP')
  fireEvent.change(input, {
    target: {
      files: [
        new File(['%PDF-1.4 lecture'], 'new.pdf', { type: 'application/pdf' }),
      ],
    },
  })
  const send = await screen.findByRole('button', { name: /Отправить всё/ })
  fireEvent.click(send)
  await waitFor(() =>
    expect(calls.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(
      true,
    ),
  )
  const request = calls.mock.calls.find(
    ([, init]) => init?.method === 'POST',
  )?.[1]
  const body = request?.body as FormData
  expect(body.getAll('file')).toHaveLength(1)
  expect(body.has('items')).toBe(true)
  await waitFor(() =>
    expect(screen.queryByText('new.pdf')).not.toBeInTheDocument(),
  )
})

it('asks before leaving a course with an unsent material in the queue', async () => {
  setup()
  const router = open()
  fireEvent.change(await screen.findByLabelText('Добавить PDF или ZIP'), {
    target: { files: [new File(['%PDF-1.4 content'], 'unsent.pdf')] },
  })
  expect(await screen.findByText('unsent.pdf')).toBeVisible()

  act(() => {
    void router.navigate({ to: '/admin/courses' })
  })

  expect(
    await screen.findByRole('alertdialog', {
      name: 'Материалы ещё не отправлены',
    }),
  ).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Остаться' }))
  await waitFor(() => {
    expect(router.state.location.pathname).toBe('/admin/courses/subject-b')
    expect(router.state.location.search).toEqual({ direction: 'exam' })
  })
  expect(await screen.findByText('unsent.pdf')).toBeVisible()
  expect(screen.getByRole('heading', { name: 'Второй курс' })).toBeVisible()
})

it('blocks building the graph while a queued upload is in flight', async () => {
  let finish!: () => void
  const uploadGate = new Promise<void>((resolve) => {
    finish = resolve
  })
  setup({ uploadGate })
  open()
  fireEvent.change(await screen.findByLabelText('Добавить PDF или ZIP'), {
    target: { files: [new File(['%PDF-1.4 content'], 'new.pdf')] },
  })
  fireEvent.click(await screen.findByRole('button', { name: 'Отправить всё' }))
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Собрать граф тем' }),
    ).toBeDisabled(),
  )
  finish()
  fireEvent.click(await screen.findByRole('button', { name: 'Понятно' }))
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Собрать граф тем' }),
    ).toBeEnabled(),
  )
})

it('confirms a graph build before using the master build endpoint', async () => {
  const calls = setup()
  open()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Собрать граф тем' }),
  )
  const dialog = await screen.findByRole('alertdialog')
  expect(calls.mock.calls.some(([url]) => url.endsWith('/graph/build'))).toBe(
    false,
  )
  fireEvent.click(within(dialog).getByRole('button', { name: 'Начать сборку' }))
  await waitFor(() =>
    expect(
      calls.mock.calls.some(
        ([url, init]) =>
          url.endsWith('/graph/build') && init?.method === 'POST',
      ),
    ).toBe(true),
  )
  expect(await screen.findByText('В очереди')).toBeInTheDocument()
})

it('preserves the three numbered preparation blocks and arrows in their original order', async () => {
  setup()
  open()
  const pipeline = await screen.findByRole('navigation', {
    name: 'Этапы подготовки курса',
  })
  expect(
    [...pipeline.children].map((element) => element.tagName.toLowerCase()),
  ).toEqual(['div', 'svg', 'div', 'svg', 'div'])
  const blocks = [...pipeline.children].filter(
    (element) => element.tagName === 'DIV',
  )
  expect(blocks.map((element) => element.textContent)).toEqual([
    '1Материалы',
    '2Граф тем',
    '3Готов студентам',
  ])
})

it('uses one card list for queued and saved materials and keeps reorder controls', async () => {
  const calls = setup()
  open()
  fireEvent.change(await screen.findByLabelText('Добавить PDF или ZIP'), {
    target: { files: [new File(['%PDF-1.4 new'], 'queued.pdf')] },
  })
  await screen.findByText('queued.pdf')
  const list = screen
    .getAllByRole('list')
    .find((candidate) => within(candidate).queryByText('lecture.pdf'))
  expect(list).toBeDefined()
  let rows = within(list!).getAllByRole('listitem')
  expect(rows).toHaveLength(2)
  expect(within(rows[0]).getByRole('article')).toHaveTextContent('lecture.pdf')
  expect(within(rows[1]).getByRole('article')).toHaveTextContent('queued.pdf')
  fireEvent.click(
    screen.getByRole('button', { name: 'Переместить lecture.pdf ниже' }),
  )
  rows = within(list!).getAllByRole('listitem')
  expect(rows[0]).toHaveTextContent('queued.pdf')
  expect(rows[1]).toHaveTextContent('lecture.pdf')
  expect(calls.mock.calls.some(([, init]) => init?.method === 'PATCH')).toBe(
    false,
  )
})

it('opens rendered Markdown and formulas in a closable preview dialog', async () => {
  setup()
  open()
  fireEvent.click(
    await screen.findByRole('button', {
      name: 'Предпросмотр',
    }),
  )
  const dialog = await screen.findByRole('dialog')
  expect(
    within(dialog).getByRole('heading', { name: 'Тема лекции' }),
  ).toBeVisible()
  expect(dialog.querySelector('.katex')).toBeInTheDocument()
  fireEvent.click(
    within(dialog).getByRole('button', { name: 'Закрыть предпросмотр' }),
  )
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
  )
})

it('shows terminal cleaning failure in the original card without a pending blocker', async () => {
  setup({ cleaningFailed: true, graphReady: true })
  open()
  expect(await screen.findByText('Очистка материала не удалась.')).toBeVisible()
  expect(
    screen.getByText(
      'У 1 материалов ошибка обработки. Подробности — в карточках; замените файл или удалите его.',
    ),
  ).toBeVisible()
  expect(
    screen.queryByText(
      'Дождитесь завершения обработки материалов: распознавания, перевода и очистки текста.',
    ),
  ).not.toBeInTheDocument()
})

it.each([{ pending: true }, { processing: true }, { cleaning: true }])(
  'hides replacement and deletion while a document is being processed (%j)',
  async (state) => {
    setup(state)
    open()
    expect(await screen.findByText('lecture.pdf')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Заменить PDF' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Удалить' }),
    ).not.toBeInTheDocument()
  },
)

it.each([{}, { cleaningFailed: true }])(
  'keeps replacement and deletion available after processing (%j)',
  async (state) => {
    setup(state)
    open()
    expect(
      await screen.findByRole('button', { name: 'Заменить PDF' }),
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Удалить' })).toBeVisible()
  },
)

it.each([
  [{ processing: true }, 'Переводится на русский'],
  [{ cleaning: true }, 'Очищается текст'],
  [{ pending: true }, 'В очереди на распознавание'],
] as const)(
  'shows the active processing stage and counts only ready materials (%j)',
  async (state, label) => {
    setup(state)
    open()
    expect(await screen.findByText(label)).toBeVisible()
    expect(
      screen.getByText(
        'Материалов: 1 · готово к графу: 0 · в обработке: 1 · с ошибкой: 0',
      ),
    ).toBeVisible()
    expect(screen.queryByText('Готов к графу')).not.toBeInTheDocument()
  },
)

it('marks completed materials ready for the graph', async () => {
  setup()
  open()
  expect(await screen.findByText('Готов к графу')).toBeVisible()
  expect(
    screen.getByText(
      'Материалов: 1 · готово к графу: 1 · в обработке: 0 · с ошибкой: 0',
    ),
  ).toBeVisible()
})

it('blocks exclusion of a stale map after replacement preparation fails', async () => {
  const calls = setup({
    graphReady: true,
    assessmentWarnings: [
      { row: 3, column: 'Задание 2', message: 'Критерии не заданы.' },
    ],
    mapState: { fingerprint: 'map-b', stale: true, failed: true },
  })
  open()
  expect(
    await screen.findByRole('button', {
      name: 'Пропустить задания без критериев',
    }),
  ).toBeDisabled()
  expect(
    screen.getByText(
      'Исключение доступно после успешной публикации актуальной карты.',
    ),
  ).toBeVisible()
  expect(
    calls.mock.calls.some(([url]) => url.endsWith('/skip-missing-criteria')),
  ).toBe(false)
})

it('keeps the confirmed fingerprint when another tab changes the map', async () => {
  const mapState = { fingerprint: 'map-a', stale: false, failed: false }
  const calls = setup({
    graphReady: true,
    mapState,
    assessmentWarnings: [
      { row: 3, column: 'Задание 2', message: 'Критерии не заданы.' },
    ],
  })
  const router = open()
  fireEvent.click(
    await screen.findByRole('button', {
      name: 'Пропустить задания без критериев',
    }),
  )
  const dialog = await screen.findByRole('alertdialog')
  mapState.fingerprint = 'map-b'
  await act(async () => {
    await router.options.context.queryClient.invalidateQueries()
  })
  expect(
    within(dialog).getByText('Строка 3, Задание 2: Критерии не заданы.'),
  ).toBeVisible()
  fireEvent.click(
    within(dialog).getByRole('button', { name: 'Исключить и пересобрать' }),
  )
  expect(
    await screen.findByText('Карта изменилась. Подтвердите исключение заново.'),
  ).toBeVisible()
  const request = calls.mock.calls.find(([url]) =>
    url.endsWith('/skip-missing-criteria'),
  )
  expect(
    new Headers(request?.[1]?.headers).get('X-Expected-Map-Fingerprint'),
  ).toBe('map-a')
})

it.each([true, false])(
  'retries the saved failed map without PDFs (published graph: %s)',
  async (graphReady) => {
    const mapState = {
      fingerprint: 'filtered-b',
      stale: graphReady,
      failed: true,
    }
    const calls = setup({
      graphReady,
      materials: [],
      mapState,
      assessmentWarnings: graphReady
        ? [{ row: 3, column: 'Задание 2', message: 'Критерии не заданы.' }]
        : [],
    })
    open()
    const retry = await screen.findByRole('button', {
      name: 'Повторить подготовку карты',
    })
    await waitFor(() => expect(retry).toBeEnabled())
    expect(
      screen.getByText(/исключённые задания останутся исключёнными/),
    ).toBeVisible()
    fireEvent.click(retry)
    await waitFor(() =>
      expect(
        calls.mock.calls.some(([url]) => url.endsWith('/competency-map/retry')),
      ).toBe(true),
    )
    const request = calls.mock.calls.find(([url]) =>
      url.endsWith('/competency-map/retry'),
    )
    expect(
      new Headers(request?.[1]?.headers).get('X-Expected-Map-Fingerprint'),
    ).toBe('filtered-b')
    expect(
      calls.mock.calls.some(
        ([url, init]) =>
          init?.method === 'POST' && !url.endsWith('/competency-map/retry'),
      ),
    ).toBe(false)
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Повторить подготовку карты' }),
      ).not.toBeInTheDocument(),
    )
  },
)

it('shows a retry conflict if the saved map changes in another tab', async () => {
  const mapState = { fingerprint: 'filtered-b', stale: true, failed: true }
  setup({ graphReady: true, materials: [], mapState })
  open()
  const retry = await screen.findByRole('button', {
    name: 'Повторить подготовку карты',
  })
  await waitFor(() => expect(retry).toBeEnabled())
  mapState.fingerprint = 'map-c'
  fireEvent.click(retry)
  expect(
    await screen.findByText(
      'Карта изменилась. Обновите страницу и повторите подготовку.',
    ),
  ).toBeVisible()
})
