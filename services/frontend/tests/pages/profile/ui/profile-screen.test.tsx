import '@/shared/i18n'
import { fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { ProfileScreen } from '@/pages/profile'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
    search,
  }: {
    children: React.ReactNode
    to: string
    search?: Record<string, string>
  }) => (
    <a href={to + (search ? `?${new URLSearchParams(search)}` : '')}>
      {children}
    </a>
  ),
}))
vi.mock('@/features/auth', () => ({
  useAuth: () => ({
    user: { id: 'u1', display_name: 'Анна', email: 'anna@example.com' },
  }),
  AccountMenu: () => null,
}))
vi.mock('@/features/subject-selection', () => ({
  useSubjectsQuery: () => ({
    data: [
      { id: 'one', name: 'Математика' },
      { id: 'two', name: 'Физика' },
    ],
  }),
}))
const learning = vi.fn()
vi.mock('@/features/learning-subject-selection', () => ({
  useLearningStateQuery: (...args: unknown[]) => learning(...args),
}))
const history = vi.fn()
vi.mock('@/features/diagnostic-session', () => ({
  useDiagnosticHistoryQuery: (...args: unknown[]) => history(...args),
}))

beforeEach(() => {
  history.mockReset()
  learning.mockReset()
  learning.mockReturnValue({
    isSuccess: true,
    data: { diagnostic_completed: false },
  })
})

it('показывает данные пользователя и историю выбранного курса', () => {
  history.mockReturnValue({
    data: [
      {
        id: 's1',
        status: 'completed',
        startedAt: 1700000000,
        grade: 'B',
        scorePercent: 80,
      },
    ],
  })
  render(<ProfileScreen />)
  expect(screen.getByRole('heading', { name: 'Анна' })).toBeInTheDocument()
  expect(screen.getByText('anna@example.com')).toBeInTheDocument()
  expect(
    screen.queryByText(/Показаны текущая и последняя завершённая/),
  ).not.toBeInTheDocument()
  expect(screen.getByText('B: 80/100')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Открыть итог' })).toHaveAttribute(
    'href',
    '/exam?subject_id=one&session_id=s1',
  )
  fireEvent.change(screen.getByRole('combobox', { name: 'Курс' }), {
    target: { value: 'two' },
  })
  expect(history).toHaveBeenLastCalledWith('u1', 'two')
  expect(screen.getByText('Предмет: Физика')).toBeInTheDocument()
})

it('позволяет повторить загрузку истории после ошибки', () => {
  const refetch = vi.fn()
  history.mockReturnValue({ isError: true, refetch })
  render(<ProfileScreen />)
  fireEvent.click(screen.getByRole('button', { name: 'Попробовать снова' }))
  expect(refetch).toHaveBeenCalledOnce()
})

it('показывает действия для учебной диагностики и тренировки', () => {
  history.mockReturnValue({ isSuccess: true, data: [] })
  learning.mockReturnValue({
    isSuccess: true,
    data: {
      active_session_id: 'active',
      diagnostic_completed: true,
      diagnostic_session_id: 'completed',
      training_available: true,
    },
  })
  render(<ProfileScreen />)
  expect(screen.getAllByText('Диагностика')).toHaveLength(2)
  expect(
    screen.getByRole('link', { name: 'Продолжить диагностику' }),
  ).toBeInTheDocument()
  expect(
    screen.getByRole('link', { name: 'Продолжить тренировку' }),
  ).toHaveAttribute('href', '/learn?subject_id=one&resume=training')
})
