import { expect, test } from '@playwright/test'

test('one browser context switches modes while student and admin sessions stay separate', async ({
  page,
}, testInfo) => {
  test.setTimeout(60_000)
  let studentSession = false
  let adminSession = false
  let examSession: {
    id: string
    subject_id: string
    status: string
    progress_percent: number
    current_turn_id: string
    started_at: number
    completed_at: number | null
    turns: {
      id: string
      line: number
      speaker: string
      kind: string
      node_id: string
      source: string | null
      text: string
      created_at: number
    }[]
  } | null = null
  const calls: Array<{
    path: string
    method: string
    portal: string | undefined
  }> = []
  const subject = {
    id: 'subject-1',
    name: 'Введение в искусственный интеллект',
    ready: true,
    learning_ready: true,
    exam_ready: true,
    preparation: {
      exam: { state: 'ready', source: 'competency_map', stale: false },
      learn: { state: 'ready' },
    },
  }
  const otherSubject = {
    id: 'subject-2',
    name: 'Другой курс',
    ready: false,
    learning_ready: false,
    exam_ready: false,
    preparation: {
      exam: { state: 'empty', source: null, stale: false },
      learn: { state: 'empty' },
    },
  }

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace('/api/v1', '')
    const portal = request.headers()['x-ai-tutor-portal']
    calls.push({ path, method: request.method(), portal })

    if (path === '/auth/me') {
      const user = portal === 'admin' ? adminSession : studentSession
      if (!user) {
        await route.fulfill({ status: 401, body: '' })
        return
      }
      await route.fulfill({
        json: {
          id: portal === 'admin' ? 'teacher-1' : 'student-1',
          email:
            portal === 'admin'
              ? 'teacher@example.invalid'
              : 'student@example.invalid',
          display_name: portal === 'admin' ? 'Учитель' : 'Студент',
          role: portal === 'admin' ? 'admin' : 'student',
        },
      })
      return
    }
    if (path === '/auth/refresh') {
      await route.fulfill({ status: 401, body: '' })
      return
    }
    if (path === '/auth/register' && request.method() === 'POST') {
      studentSession = true
      await route.fulfill({
        json: {
          user: {
            id: 'student-1',
            email: 'student@example.invalid',
            display_name: 'Студент',
            role: 'student',
          },
        },
      })
      return
    }
    if (path === '/auth/teacher' && request.method() === 'POST') {
      adminSession = true
      await route.fulfill({
        json: {
          user: {
            id: 'teacher-1',
            email: 'teacher@example.invalid',
            display_name: 'Учитель',
            role: 'admin',
          },
        },
      })
      return
    }
    if (path === '/auth/logout' && request.method() === 'POST') {
      if (portal === 'admin') adminSession = false
      else studentSession = false
      await route.fulfill({ status: 204, body: '' })
      return
    }
    if (path.endsWith('/subjects')) {
      await route.fulfill({ json: [subject, otherSubject] })
      return
    }
    if (path.endsWith('/audio-preparation')) {
      const direction = {
        version: '1',
        state: 'ready',
        total: 1,
        unchecked: 0,
        queued: 0,
        processing: 0,
        retry_wait: 0,
        ready: 1,
        failed: 0,
        unavailable: 0,
        observed_at: '2026-10-05T00:00:00Z',
      }
      await route.fulfill({
        json: {
          subject_id: path.split('/')[4],
          exam: { ...direction, published: true },
          learn: { ...direction, revision: 1 },
        },
      })
      return
    }
    if (path.endsWith('/learning-state')) {
      await route.fulfill({
        json: {
          subject_id: subject.id,
          subject_name: subject.name,
          diagnostic_status: 'not_started',
          diagnostic_completed: false,
          training_available: false,
        },
      })
      return
    }
    if (path.endsWith('/competency-map') && request.method() === 'GET') {
      if (!path.includes('/subject-1/')) {
        await route.fulfill({
          json: { revision: 0, imported_at: null, competencies: [] },
        })
        return
      }
      await route.fulfill({
        json: {
          revision: 1,
          imported_at: 1791158400,
          competencies: [
            {
              constituents: [
                { outcomes: [{ tasks: [{ question: 'Что такое ИИ?' }] }] },
              ],
            },
          ],
        },
      })
      return
    }
    if (path.endsWith('/material-kinds')) {
      await route.fulfill({ json: [] })
      return
    }
    if (path.endsWith('/materials') && request.method() === 'GET') {
      await route.fulfill({ json: [] })
      return
    }
    if (path.endsWith('/graph') && request.method() === 'GET') {
      if (path.includes('/subject-1/')) {
        await route.fulfill({
          json: {
            status: 'ready',
            attempts: 1,
            error_message: '',
            stale: false,
            inputs_ready: true,
            updated_at: 1791158400,
            graph_source: 'competency_map',
            graph_fingerprint: 'map-v1',
            graph_map_revision: 1,
            build_source: 'competency_map',
            map: {
              filename: 'ai-map.xlsx',
              sheet: 'Карта',
              fingerprint: 'map-v1',
              learning_revision: 1,
            },
            progress: null,
            graph: {
              topics: [
                {
                  id: 'topic-1',
                  title: 'Искусственный интеллект',
                  question: 'Что такое искусственный интеллект?',
                  variants: [
                    {
                      question: 'Что такое ИИ?',
                      content: 'Эталонный ответ',
                    },
                  ],
                  provides: [],
                  requires: [],
                },
              ],
              prerequisites: [],
              hierarchy: [],
              quality: { acyclic: true, topics: 1, requirements: 0 },
            },
          },
        })
        return
      }
      await route.fulfill({
        json: {
          status: 'not_requested',
          attempts: 0,
          error_message: '',
          stale: false,
          inputs_ready: false,
          updated_at: 0,
          graph_source: 'none',
          graph_fingerprint: '',
          graph_map_revision: null,
          build_source: null,
          map: null,
          progress: null,
          graph: null,
        },
      })
      return
    }
    if (path === '/exam/diagnostic-sessions' && request.method() === 'GET') {
      const subjectId = url.searchParams.get('subject_id')
      const items =
        examSession?.subject_id === subjectId
          ? [
              {
                id: examSession.id,
                status: examSession.status,
                progress_percent: examSession.progress_percent,
                started_at: examSession.started_at,
                completed_at: examSession.completed_at,
                grade: null,
                score_percent: null,
              },
            ]
          : []
      await route.fulfill({ json: { items } })
      return
    }
    if (path === '/exam/diagnostic-sessions' && request.method() === 'POST') {
      examSession = {
        id: 'session-1',
        subject_id: subject.id,
        status: 'active',
        progress_percent: 0,
        current_turn_id: 'turn-1',
        started_at: 1791158400,
        completed_at: null,
        turns: [
          {
            id: 'turn-1',
            line: 1,
            speaker: 'geek',
            kind: 'question',
            node_id: 'topic-1',
            source: null,
            text: 'Расскажите, как работает этот курс?',
            created_at: 1791158400,
          },
        ],
      }
      await route.fulfill({ status: 201, json: examSession })
      return
    }
    if (path === '/exam/diagnostic-sessions/session-1' && examSession) {
      await route.fulfill({ json: examSession })
      return
    }
    if (
      path.startsWith('/exam/diagnostic-sessions/session-1/turns/') &&
      path.endsWith('/audio')
    ) {
      await route.fulfill({ status: 503, body: '' })
      return
    }
    await route.fulfill({ status: 404, json: { code: 'NOT_FOUND' } })
  })

  await page.goto('/')
  await expect(page.getByLabel('Пароль')).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('start-login.png') })
  await page.getByRole('button', { name: 'Регистрация' }).click()
  await page.getByLabel('Имя').fill('Студент')
  await page.getByLabel('Email').fill('student@example.invalid')
  await page.getByLabel('Пароль').fill('test-password')
  await page.getByRole('button', { name: 'Создать аккаунт' }).click()
  await expect(
    page.getByRole('heading', { name: 'Выберите режим' }),
  ).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('start-choice.png') })

  await page.getByRole('link', { name: /Обучение/ }).click()
  await expect(page).toHaveURL(/\/learn$/)
  await expect(page.getByRole('heading', { name: 'Обучение' })).toBeVisible()
  await expect(
    page.getByRole('navigation', { name: 'Направления' }),
  ).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Курс' })).toHaveCount(0)
  await page.reload()
  await page.screenshot({ path: testInfo.outputPath('learn-home.png') })
  await page.getByRole('button', { name: /Меню профиля/ }).click()
  await page.getByRole('menuitem', { name: 'К выбору режима' }).click()
  await page.getByRole('link', { name: /Симуляция экзамена/ }).click()
  await expect(
    page.getByRole('heading', { name: 'Симуляция экзамена' }),
  ).toBeVisible()
  await expect(page.getByRole('region', { name: 'Курс' })).toHaveCount(0)
  await page.screenshot({ path: testInfo.outputPath('exam-home.png') })

  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/learn?subject_id=subject-1')
  await expect(page.getByRole('heading', { name: 'Обучение' })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('learn-home-narrow.png') })
  await page.goto('/exam?subject_id=subject-1')
  await expect(
    page.getByRole('heading', { name: 'Симуляция экзамена' }),
  ).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('exam-home-narrow.png') })
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    ),
  ).toBe(false)

  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.getByRole('button', { name: 'Начать проверку знаний' }).click()
  // This test deliberately makes TTS unavailable. The question stays hidden
  // until the student explicitly chooses the text-only fallback.
  await expect(page.getByText('Не удалось озвучить реплику')).toBeVisible()
  await page.getByRole('button', { name: 'Показать текст без озвучки' }).click()
  await expect(
    page.getByText('Расскажите, как работает этот курс?'),
  ).toBeVisible()
  await page.getByRole('button', { name: /Меню профиля/ }).click()
  await page.getByRole('menuitem', { name: 'К выбору режима' }).click()
  await expect(
    page.getByRole('heading', { name: 'Выберите режим' }),
  ).toBeVisible()
  await page.getByRole('link', { name: /Симуляция экзамена/ }).click()
  await expect(
    page.getByRole('heading', { name: 'Симуляция экзамена' }),
  ).toBeVisible()
  await page.getByRole('button', { name: 'Продолжить проверку знаний' }).click()
  await page.getByRole('button', { name: 'Показать текст без озвучки' }).click()
  await expect(
    page.getByText('Расскажите, как работает этот курс?'),
  ).toBeVisible()
  await expect(page.getByRole('button', { name: /Меню профиля/ })).toBeVisible()
  await page.goto('/exam?subject_id=subject-2')
  await expect(
    page.getByText('Расскажите, как работает этот курс?'),
  ).toHaveCount(0)
  await page.goto('/exam')
  await expect(
    page.getByText('Расскажите, как работает этот курс?'),
  ).toHaveCount(0)
  await page.goto('/exam?subject_id=subject-1')
  await expect(
    page.getByText('Расскажите, как работает этот курс?'),
  ).toHaveCount(0)
  await page.getByRole('button', { name: 'Продолжить проверку знаний' }).click()
  await page.getByRole('button', { name: 'Показать текст без озвучки' }).click()
  await expect(
    page.getByText('Расскажите, как работает этот курс?'),
  ).toBeVisible()
  await expect(page.getByText(subject.name, { exact: true })).toHaveCount(1)
  await expect
    .poll(() =>
      calls.some(
        (call) =>
          call.path === '/exam/diagnostic-sessions/session-1' &&
          call.method === 'GET',
      ),
    )
    .toBe(true)
  expect(
    calls.filter(
      (call) =>
        call.path === '/exam/diagnostic-sessions' && call.method === 'POST',
    ),
  ).toHaveLength(1)

  await page.goto('/admin/courses')
  await expect(page).toHaveURL(/\/admin\/courses$/)
  await expect(
    page.getByRole('heading', { name: 'Курсы', level: 1 }),
  ).toBeVisible()
  await expect(
    page.getByRole('navigation', { name: 'Разделы администрирования' }),
  ).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Новый курс' })).toBeVisible()
  await expect(page.getByRole('link', { name: subject.name })).toBeVisible()
  await expect(
    page.getByRole('link', { name: otherSubject.name }),
  ).toBeVisible()
  await page.goto(`/admin/courses/missing-course`)
  await expect(page).toHaveURL(/\/$/)
  await expect(
    page.getByRole('heading', { name: 'Выберите режим' }),
  ).toBeVisible()
  await expect(page.getByLabel('Файл карты компетенций')).toHaveCount(0)
  await page.goBack()
  await expect(page).toHaveURL(/\/admin\/courses$/)
  await page.goto('/admin/courses/missing-course?direction=exam')
  await expect(page).toHaveURL(/\/$/)
  await page.goBack()
  await expect(page).toHaveURL(/\/admin\/courses$/)
  await page.getByRole('link', { name: otherSubject.name }).click()
  await expect(page).toHaveURL(new RegExp(`/admin/courses/${otherSubject.id}$`))
  await expect(
    page.getByRole('heading', { name: otherSubject.name }),
  ).toBeVisible()
  await expect(
    page.getByRole('heading', { name: otherSubject.name, level: 1 }),
  ).toBeVisible()
  // Both directions share one preparation screen. Exercise the visible
  // import button rather than expecting its hidden native input to be visible.
  const importButton = page.getByRole('button', {
    name: 'Импортировать карту компетенций',
    exact: true,
  })
  await expect(importButton).toBeEnabled()
  const chooserPromise = page.waitForEvent('filechooser')
  await importButton.click()
  const chooser = await chooserPromise
  expect(await chooser.element().getAttribute('aria-label')).toBe(
    'Файл карты компетенций',
  )
  const expectCoursePreparation = async () => {
    await expect(
      page.getByRole('heading', { name: subject.name, level: 1 }),
    ).toBeVisible()
    await expect(importButton).toBeEnabled()
    await expect(
      page.getByText('Карта компетенций успешно загружена'),
    ).toBeVisible()
    await expect(page.getByLabel('Добавить PDF или ZIP')).toBeVisible()
    await expect(
      page.getByRole('navigation', { name: 'Разделы администрирования' }),
    ).toHaveCount(0)
  }
  await page.goto(`/admin/courses/${subject.id}`)
  await expectCoursePreparation()
  await page.reload()
  await expectCoursePreparation()
  await page.screenshot({ path: testInfo.outputPath('course-admin.png') })
  await expect
    .poll(() =>
      calls.some(
        (call) => call.path === '/auth/teacher' && call.portal === 'admin',
      ),
    )
    .toBe(true)
  await page.goto('/learn')
  await expect(page).toHaveURL(/\/learn$/)
  await expect
    .poll(() =>
      calls.some(
        (call) => call.path === '/auth/me' && call.portal === undefined,
      ),
    )
    .toBe(true)
  await page.getByRole('button', { name: /Меню профиля/ }).click()
  await page.getByRole('menuitem', { name: 'Выйти' }).click()
  await expect(page.getByLabel('Пароль')).toBeVisible()

  // Student logout leaves the independent admin session usable.
  await page.goto('/admin/courses')
  await expect(
    page.getByRole('heading', { name: 'Курсы', level: 1 }),
  ).toBeVisible()
  await expect(page.getByText('Экзамен: готов')).toBeVisible()
  await expect(page.getByText('Обучение: готово')).toBeVisible()
  await page.getByRole('link', { name: subject.name }).click()
  await expect(page).toHaveURL(new RegExp(`/admin/courses/${subject.id}$`))
  await expectCoursePreparation()
  const courseHeader = page.locator('section[aria-labelledby="course-name"]')
  const desktopHeader = await courseHeader.boundingBox()
  expect(desktopHeader).not.toBeNull()

  // Bookmarked direction URLs still open the unified preparation UI.
  await page.goto(`/admin/courses/${subject.id}?direction=exam`)
  await expectCoursePreparation()
  const graphCard = page.locator('section[aria-labelledby="graph-title"]')
  await expect(graphCard).toContainText('Курс готов для студентов.')
  await expect(graphCard).toContainText('Искусственный интеллект')
  await page.reload()
  await expectCoursePreparation()
  expect(await courseHeader.boundingBox()).toEqual(desktopHeader)
  await page.goBack()
  await expect(page).toHaveURL(new RegExp(`/admin/courses/${subject.id}$`))
  await expectCoursePreparation()
  await page.goForward()
  await expect(page).toHaveURL(
    new RegExp(`/admin/courses/${subject.id}\\?direction=exam$`),
  )
  await expectCoursePreparation()

  await page.setViewportSize({ width: 390, height: 844 })
  const narrowHeader = await courseHeader.boundingBox()
  await page.screenshot({ path: testInfo.outputPath('exam-admin-narrow.png') })
  await page.goto(`/admin/courses/${subject.id}?direction=learn`)
  await expectCoursePreparation()
  expect(await courseHeader.boundingBox()).toEqual(narrowHeader)
  await page.screenshot({ path: testInfo.outputPath('learn-admin-narrow.png') })
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    ),
  ).toBe(false)
  await page.getByRole('link', { name: 'Ко всем курсам' }).click()
  await expect(page).toHaveURL(/\/admin\/courses$/)
  await page.getByRole('link', { name: subject.name }).click()
  await expectCoursePreparation()
  await expect
    .poll(() =>
      calls.some((call) => call.path === '/auth/me' && call.portal === 'admin'),
    )
    .toBe(true)
})
