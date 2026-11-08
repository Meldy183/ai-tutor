import { expect, test } from '@playwright/test'

test('shows an available old graph while a new course build is queued', async ({
  page,
}) => {
  let created = false
  let subjectReads = 0
  const emptyCourse = {
    id: 'new-course',
    name: 'Новый курс',
    ready: false,
    learning_ready: false,
    exam_ready: false,
    preparation: {
      exam: { state: 'empty', source: null, stale: false },
      learn: { state: 'empty' },
    },
  }
  const updatingCourse = {
    ...emptyCourse,
    ready: true,
    exam_ready: true,
    preparation: {
      exam: { state: 'queued', source: 'materials', stale: true },
      learn: { state: 'empty' },
    },
  }

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace('/api/v1', '')
    if (path === '/auth/me') {
      await route.fulfill({
        json: {
          id: 'admin-1',
          email: 'admin@example.invalid',
          display_name: 'Администратор',
          role: 'admin',
        },
      })
      return
    }
    if (path === '/auth/refresh') {
      await route.fulfill({ status: 401, body: '' })
      return
    }
    if (path === '/exam/subjects') {
      subjectReads += 1
      await route.fulfill({
        json: !created
          ? []
          : subjectReads >= 4
            ? [updatingCourse]
            : [emptyCourse],
      })
      return
    }
    if (path === '/learn/subjects') {
      await route.fulfill({
        json: created
          ? [
              {
                ...emptyCourse,
                ready: false,
                exam_ready: true,
                preparation: updatingCourse.preparation,
              },
            ]
          : [],
      })
      return
    }
    if (path === '/exam/admin/subjects' && request.method() === 'POST') {
      created = true
      await route.fulfill({ status: 201, json: emptyCourse })
      return
    }
    await route.fulfill({ status: 404, body: '' })
  })

  await page.goto('/admin/courses')
  await page.getByRole('button', { name: 'Новый курс' }).click()
  await page.getByRole('textbox', { name: 'Название курса' }).fill('Новый курс')
  await page.getByRole('button', { name: 'Создать курс' }).click()
  const courseCard = page.getByRole('link', { name: /Новый курс/ })
  await expect(
    courseCard.getByText('Экзамен: материалы не загружены'),
  ).toBeVisible()
  await expect(
    courseCard.getByText('Экзамен: готов · обновление в очереди'),
  ).toBeVisible({ timeout: 15_000 })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    for (const width of [360, 600, 1000]) {
      await page.setViewportSize({ width, height: 900 })
      await expect(
        courseCard.getByText('Экзамен: готов · обновление в очереди'),
      ).toBeVisible()
      const overflow = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        elements: [...document.querySelectorAll('body *')]
          .map((element) => ({
            tag: element.tagName,
            className:
              typeof element.className === 'string' ? element.className : '',
            text: element.textContent?.trim().slice(0, 80),
            right: element.getBoundingClientRect().right,
          }))
          .filter((element) => element.right > window.innerWidth + 1)
          .slice(0, 8),
      }))
      expect(
        overflow.scrollWidth,
        JSON.stringify(overflow.elements),
      ).toBeLessThanOrEqual(width)
    }
  }
  await courseCard.click()
  await expect(
    page.getByRole('heading', { name: 'Подготовка курса', level: 2 }),
  ).toBeVisible()
  await expect(
    page.getByText('Экзамен: готов · обновление в очереди'),
  ).toBeVisible()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    for (const width of [360, 600, 1000]) {
      await page.setViewportSize({ width, height: 900 })
      const overflow = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        elements: [...document.querySelectorAll('body *')]
          .map((element) => ({
            tag: element.tagName,
            className:
              typeof element.className === 'string' ? element.className : '',
            text: element.textContent?.trim().slice(0, 80),
            right: element.getBoundingClientRect().right,
          }))
          .filter((element) => element.right > window.innerWidth + 1)
          .slice(0, 8),
      }))
      expect(
        overflow.scrollWidth,
        JSON.stringify(overflow.elements),
      ).toBeLessThanOrEqual(width)
    }
  }
})
