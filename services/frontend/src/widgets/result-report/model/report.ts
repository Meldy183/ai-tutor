// The result screen shared by the learning diagnostic and the exam. Each mode
// translates its own result into this shape; the screen knows neither of them.

export type ReportGrade = 'A' | 'B' | 'C' | 'D'

export type ReportStatus =
  'full' | 'partial' | 'gap' | 'credited' | 'insufficient' | 'unchecked'

// One criterion of a topic: in points, met or not, or only a ground of the
// topic's score without points of its own.
export type ReportMark = {
  id: string
  title: string
  score?: number
  maxScore?: number
  met?: boolean
  reason: string
  quote: string
}

export type ReportTopic = {
  id: string
  title: string
  status: ReportStatus
  // Points of the topic; null when it has none, such as a credited topic.
  score: number | null
  maxScore: number | null
  assisted: boolean
  skipped: boolean
  // Topics this one relies on, by id; they stand left of it on the graph.
  requires: string[]
  // The topics the rest lead to: top-level topics or main tasks.
  main: boolean
  marks: ReportMark[]
  // What is known about the answer: Geek's closing reply or the task review.
  note: string
}

export type ResultReportData = {
  grade: ReportGrade | null
  // The normalized points of the whole result, 5 per competency.
  score: number
  maxScore: number
  percent: number | null
  topics: ReportTopic[]
}

export type ReportSummary =
  | { state: 'loading' }
  | { state: 'error'; retrying: boolean; onRetry: () => void }
  | { state: 'ready'; text: string }

// The order of the statuses in the summary tiles and the graph legend.
export const statusOrder: ReportStatus[] = [
  'full',
  'partial',
  'gap',
  'credited',
  'insufficient',
]

// The state of the conclusion from the query that loads it.
export function reportSummary<T>(
  query: {
    data: T | undefined
    isError: boolean
    isFetching: boolean
    refetch: () => unknown
  },
  text: (data: T) => string,
): ReportSummary {
  if (query.isError)
    return {
      state: 'error',
      retrying: query.isFetching,
      onRetry: () => void query.refetch(),
    }
  if (query.data === undefined) return { state: 'loading' }
  return { state: 'ready', text: text(query.data) }
}

// The status of an asked topic follows only from its points.
export function statusByPoints(score: number, maxScore: number): ReportStatus {
  if (score <= 0) return 'gap'
  return score >= maxScore ? 'full' : 'partial'
}
