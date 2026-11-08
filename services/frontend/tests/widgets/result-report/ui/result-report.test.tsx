import { Theme } from '@radix-ui/themes'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import '@/shared/i18n'
import {
  BannerPrimaryButton,
  BannerSecondaryButton,
  ResultBanner,
  ResultReport,
  type ReportTopic,
  type ResultReportData,
} from '@/widgets/result-report'

const topic = (overrides: Partial<ReportTopic>): ReportTopic => ({
  id: 'a',
  title: 'Тема',
  status: 'full',
  score: 4,
  maxScore: 4,
  assisted: false,
  skipped: false,
  requires: [],
  main: true,
  marks: [],
  note: '',
  ...overrides,
})

const report = (topics: ReportTopic[]): ResultReportData => ({
  grade: 'B',
  score: 7,
  maxScore: 9,
  percent: 77,
  topics,
})

const show = (
  data: ResultReportData,
  summary = { state: 'ready', text: 'Вывод.' } as const,
) =>
  render(
    <Theme>
      <ResultReport report={data} summary={summary} />
    </Theme>,
  )

it('shows the overall grade as a letter with its points, without a percent', () => {
  show(report([topic({})]))
  const hero = screen.getByRole('region', { name: 'Предварительная оценка' })
  expect(within(hero).getByText('B')).toBeVisible()
  expect(within(hero).getByText('Хорошо')).toBeVisible()
  expect(within(hero).getByText('7 из 9 баллов')).toBeVisible()
  expect(screen.queryByText(/77/)).not.toBeInTheDocument()
})

it('shows the normalized total with one decimal', () => {
  show({ ...report([topic({})]), score: 3.9, maxScore: 5, percent: 77 })
  expect(screen.getByText('3,9 из 5 баллов')).toBeVisible()
})

it('shows topics only in points, with no letters of their own', () => {
  show(report([topic({ score: 2, maxScore: 3, status: 'partial' })]))
  const topics = screen.getByRole('region', { name: 'Граф тем' })
  expect(within(topics).getByText('2 из 3')).toBeVisible()
  expect(within(topics).queryByText(/^[A-D]$/)).not.toBeInTheDocument()
})

it('keeps what to work on even when the grade is A', () => {
  show({
    ...report([
      topic({ id: 'good', title: 'Освоено', note: 'Верно.' }),
      topic({
        id: 'part',
        title: 'Частично',
        status: 'partial',
        score: 1,
        note: 'Неполно.',
      }),
      topic({ id: 'gap', title: 'Пробел', status: 'gap', score: 0 }),
    ]),
    grade: 'A',
  })
  const good = screen.getByRole('region', { name: 'Что получилось' })
  expect(within(good).getByText('Освоено')).toBeVisible()
  const work = screen.getByRole('region', { name: 'Что доработать' })
  expect(
    within(work)
      .getAllByRole('listitem')
      .map((i) => i.querySelector('strong')?.textContent),
  ).toEqual(['Пробел', 'Частично'])
})

it('hides empty cards and unchecked topics', () => {
  show(
    report([
      topic({ id: 'gap', title: 'Пробел', status: 'gap', score: 0 }),
      topic({ id: 'no', title: 'Не дошли', status: 'unchecked', score: null }),
    ]),
  )
  expect(
    screen.queryByRole('region', { name: 'Что получилось' }),
  ).not.toBeInTheDocument()
  expect(screen.queryByText('Не дошли')).not.toBeInTheDocument()
})

it('says so when no topic was checked', () => {
  show(report([topic({ status: 'unchecked', score: null })]))
  expect(screen.getByText('Ни одна тема не была проверена.')).toBeVisible()
  expect(
    screen.queryByRole('region', { name: 'Граф тем' }),
  ).not.toBeInTheDocument()
})

it('removes the separate list of checked topics', () => {
  show(report([topic({})]))
  expect(
    screen.queryByRole('region', { name: 'Проверенные темы' }),
  ).not.toBeInTheDocument()
})

it('keeps a card without a breakdown from opening', () => {
  show(report([topic({ title: 'Без разбора' })]))
  const graph = screen.getByRole('region', { name: 'Граф тем' })
  expect(within(graph).getByText('Без разбора')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: /Без разбора/ }),
  ).not.toBeInTheDocument()
})

it('renders what the mode puts under the grade', () => {
  render(
    <Theme>
      <ResultReport
        report={report([topic({})])}
        summary={{ state: 'ready', text: 'Вывод.' }}
        highlight={<button type="button">Дальше</button>}
      />
    </Theme>,
  )
  expect(screen.getByRole('button', { name: 'Дальше' })).toBeVisible()
})

it('opens the points and reasons of a topic from its graph card', () => {
  show(
    report([
      topic({
        status: 'partial',
        score: 3,
        maxScore: 4,
        marks: [
          {
            id: 'r1',
            title: 'Суть',
            score: 1,
            maxScore: 2,
            reason: 'Неполно',
            quote: 'мой ответ',
          },
          {
            id: 'reason-0',
            title: '',
            reason: 'Связь не объяснена',
            quote: '',
          },
        ],
      }),
    ]),
  )
  fireEvent.click(screen.getByRole('button', { name: /3 из 4/ }))
  const details = within(screen.getByRole('dialog'))
  expect(details.getByText('Суть')).toBeVisible()
  expect(details.getByText('Неполно')).toBeVisible()
  expect(details.getByText('«мой ответ»')).toBeVisible()
  // A ground of the score is shown without a title or points.
  expect(details.getByText('Связь не объяснена')).toBeVisible()
  expect(details.queryByText('Пункт 2')).not.toBeInTheDocument()
})

it('shows a criterion without points as met or not', () => {
  show(
    report([
      topic({
        score: 1,
        maxScore: 3,
        status: 'partial',
        marks: [
          { id: 'a', title: '', met: false, reason: 'Нет причины', quote: '' },
          { id: 'b', title: '', met: true, reason: 'Верно', quote: '' },
        ],
      }),
    ]),
  )
  fireEvent.click(screen.getByRole('button', { name: /1 из 3/ }))
  const details = within(screen.getByRole('dialog'))
  expect(details.getByText('Не выполнено')).toBeVisible()
  expect(details.getByText('Нет причины')).toBeVisible()
  expect(details.getByText('Выполнено')).toBeVisible()
})

it('shows the next step as a banner with its actions', () => {
  const onClick = vi.fn()
  render(
    <Theme>
      <ResultBanner title="Что дальше?" text="Подсказка">
        <BannerPrimaryButton icon={<span />} onClick={onClick}>
          Заново
        </BannerPrimaryButton>
        <BannerSecondaryButton disabled>Назад</BannerSecondaryButton>
      </ResultBanner>
    </Theme>,
  )
  expect(screen.getByText('Что дальше?')).toBeVisible()
  expect(screen.getByText('Подсказка')).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Заново' }))
  expect(onClick).toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Назад' })).toBeDisabled()
})

it('lets the student retry a failed conclusion and keeps the result visible', () => {
  const onRetry = vi.fn()
  show(report([topic({})]), {
    state: 'error',
    retrying: false,
    onRetry,
  } as never)
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Не удалось подготовить общий вывод.',
  )
  fireEvent.click(screen.getByRole('button', { name: 'Повторить' }))
  expect(onRetry).toHaveBeenCalled()
  expect(screen.getByText('Хорошо')).toBeVisible()
})

it('shows the graph of the topics in points', () => {
  show(
    report([
      topic({
        id: 'm',
        title: 'Основное',
        score: 1,
        maxScore: 3,
        status: 'partial',
        requires: ['b'],
      }),
      topic({ id: 'b', title: 'Базовое', main: false, score: 1, maxScore: 1 }),
    ]),
  )
  const graph = screen.getByRole('region', { name: 'Граф тем' })
  expect(within(graph).getByText('Опирается на: Базовое')).toBeInTheDocument()
  expect(within(graph).getByText('1 из 3')).toBeVisible()
})
