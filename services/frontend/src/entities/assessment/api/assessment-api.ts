import { apiFetch } from '@/shared/api'
import type {
  Assessment,
  AssessmentTask,
  AssessmentVerdict,
  CriterionResult,
} from '../model/assessment'

const apiBase =
  (import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(/\/$/, '') + '/learn'

import { AssessmentApiError } from '../model/service-errors'
export { AssessmentApiError } from '../model/service-errors'

function isVerdict(value: unknown): value is AssessmentVerdict {
  return value === 'correct' || value === 'partial' || value === 'incorrect'
}

function isCriterionResult(value: unknown): value is CriterionResult {
  return (
    !!value &&
    typeof value === 'object' &&
    'key' in value &&
    typeof value.key === 'string' &&
    !!value.key.trim() &&
    'satisfied' in value &&
    typeof value.satisfied === 'boolean' &&
    'explanation' in value &&
    typeof value.explanation === 'string' &&
    !!value.explanation.trim()
  )
}

export async function evaluateAnswer(
  transcriptionId: string,
  task: AssessmentTask,
  signal: AbortSignal,
): Promise<Assessment> {
  if (!task.variantId.trim() || !task.variantTaskId.trim())
    throw new Error('Saved variant and task IDs are required')
  const response = await apiFetch(`${apiBase}/assessments/evaluate`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      transcription_id: transcriptionId,
      variant_id: task.variantId,
      variant_task_id: task.variantTaskId,
    }),
    signal: AbortSignal.any([signal, AbortSignal.timeout(120_000)]),
  })
  if (!response.ok) throw new AssessmentApiError(response.status)

  const data: unknown = await response.json()
  if (
    !data ||
    typeof data !== 'object' ||
    !('score' in data) ||
    typeof data.score !== 'number' ||
    !Number.isInteger(data.score) ||
    data.score < 0 ||
    data.score > 2 ||
    !('max_score' in data) ||
    (data.max_score !== 1 && data.max_score !== 2) ||
    data.score > data.max_score ||
    !('verdict' in data) ||
    !isVerdict(data.verdict) ||
    data.verdict !==
      (data.score === 0
        ? 'incorrect'
        : data.score === data.max_score
          ? 'correct'
          : 'partial') ||
    !('criterion_results' in data) ||
    !Array.isArray(data.criterion_results) ||
    data.criterion_results.length === 0 ||
    !data.criterion_results.every(isCriterionResult) ||
    !('feedback' in data) ||
    typeof data.feedback !== 'string' ||
    !data.feedback.trim()
  ) {
    throw new Error('Invalid assessment response')
  }

  return {
    score: data.score as 0 | 1 | 2 | 3,
    maxScore: data.max_score,
    verdict: data.verdict,
    criterionResults: data.criterion_results,
    feedback: data.feedback,
  }
}
