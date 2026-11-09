import { z } from 'zod'
import { apiFetch } from '@/shared/api'
import { fetchStoredAudio } from '@/shared/api'
import type {
  DiagnosticAudioMetadata,
  DiagnosticProgress,
  DiagnosticResult,
  DiagnosticOverallFeedback,
  DiagnosticTask,
} from '@/entities/learning-diagnostic-session'

export const diagnosticApiBase =
  (import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(/\/$/, '') + '/learn'
const id = z.string().min(1)
const count = z.number().int().nonnegative()
// A main task scores 0–2, a basic one 0–1 (decision of 08.11.2026).
const score = count.max(2)
const verdict = z.enum(['correct', 'partial', 'incorrect'])
const criteria = z.array(
  z.object({ key: id, satisfied: z.boolean(), explanation: id }),
)
const feedback = id
const task = z.object({
  variant_task_id: id,
  source_task_id: id,
  role: z.enum(['main', 'basic']),
  competency_id: id,
  competency_name: z.string(),
  constituent_id: id,
  constituent_name: z.string(),
  outcome_id: id,
  outcome_name: z.string(),
  question: id,
  options: z.array(z.string()),
  voice_instruction: id,
})
const progressSchema: z.ZodType<DiagnosticProgress> = z
  .object({
    answer_skipped: z.boolean().optional(),
    session_id: id,
    status: z.enum(['active', 'completed']),
    completed_tasks: count,
    skipped_tasks: count,
    total_tasks: count.min(1),
    competency_count: count.min(1).optional(),
    current_competency: count.min(1).optional(),
    current_step: count.max(2).optional(),
    current: task.optional(),
    text: id.optional(),
    score: score.optional(),
    grader_score: score.optional(),
    grader_max_score: count.min(1).max(2).optional(),
    verdict: verdict.optional(),
    criterion_results: criteria.optional(),
    feedback: feedback.optional(),
  })
  .refine(
    (p) =>
      p.completed_tasks + p.skipped_tasks <= p.total_tasks &&
      (p.status === 'active'
        ? !!p.current && p.completed_tasks + p.skipped_tasks < p.total_tasks
        : !p.current && p.completed_tasks + p.skipped_tasks === p.total_tasks),
  )
const diagnosticAudioSchema: z.ZodType<DiagnosticAudioMetadata> = z
  .object({
    variant_task_id: id,
    status: z.enum([
      'missing',
      'pending',
      'processing',
      'ready',
      'failed',
      'cancelled',
    ]),
    audio_url: z.string().nullable(),
  })
  .refine((value) =>
    value.status === 'ready'
      ? !!value.audio_url &&
        /^\/learn\/task-audio\/[A-Za-z0-9_-]{1,256}\/file$/.test(
          value.audio_url,
        )
      : value.audio_url === null,
  )
const answerSchema = z
  .object({
    skipped: z.boolean().optional(),
    variant_task_id: id,
    source_task_id: id,
    competency_id: id,
    outcome_id: id,
    role: z.enum(['main', 'basic']),
    task,
    transcription_id: id,
    text: id,
    grader_score: score,
    grader_max_score: count.min(1).max(2),
    score,
    verdict,
    criterion_results: criteria,
    feedback,
    created_at: count,
  })
  .refine(
    (a) =>
      a.score <= a.grader_max_score &&
      a.grader_max_score === (a.role === 'main' ? 2 : 1) &&
      a.task.variant_task_id === a.variant_task_id &&
      a.task.role === a.role,
  )
const resultSchema: z.ZodType<DiagnosticResult> = z
  .object({
    session_id: id,
    status: z.literal('completed'),
    variant_id: id,
    map_revision: count.min(1),
    included_competency_count: count.min(1),
    skipped_competencies: z.array(
      z.object({ competency_id: id, competency_name: z.string(), code: id }),
    ),
    completed_tasks: count,
    total_tasks: count.min(1),
    diagnostic_score: count,
    maximum_score: count.min(1),
    report_score: z.number().nonnegative(),
    report_max_score: count,
    training_needed: z.boolean(),
    grade: z.enum(['A', 'B', 'C', 'D']).nullable(),
    score_percent: z.number().int().min(0).max(100).nullable(),
    answers: z.array(answerSchema),
    untested_basics: z.array(task),
  })
  .refine(
    (r) =>
      r.diagnostic_score <= r.maximum_score &&
      r.completed_tasks === r.answers.length &&
      r.completed_tasks + r.untested_basics.length === r.total_tasks,
  )
const overallFeedbackSchema: z.ZodType<DiagnosticOverallFeedback> = z.object({
  session_id: id,
  diagnostic_score: count,
  maximum_score: count,
  score_percentage: count.max(100),
  summary: id,
  strengths: z
    .array(id)
    .nullable()
    .transform((value) => value ?? []),
  confirmed_gaps: z.array(
    z.object({
      competency_id: id,
      competency_name: id,
      outcome_id: id,
      outcome_name: id,
      taxonomy_code: id,
      importance: z.number().int(),
      failed_criteria: z.array(id),
      advice: id,
    }),
  ),
  partial_competencies: z.array(
    z.object({
      competency_id: id,
      competency_name: id,
      details: id,
    }),
  ),
  unverified_competencies: z.array(
    z.object({
      competency_id: id,
      competency_name: id,
      code: id,
    }),
  ),
  training_recommendations: z.array(
    z.object({
      competency_id: id,
      competency_name: id,
      outcome_id: id,
      outcome_name: id,
      priority: count.min(1),
      rationale: id,
    }),
  ),
  generated_at: count,
})

import { DiagnosticApiError } from '../model/diagnostic-error'
export { DiagnosticApiError } from '../model/diagnostic-error'

async function request(
  path: string,
  signal: AbortSignal,
  init: RequestInit = {},
  timeoutMs = 120_000,
) {
  const response = await apiFetch(`${diagnosticApiBase}${path}`, {
    ...init,
    credentials: 'include',
    signal: AbortSignal.any([signal, AbortSignal.timeout(timeoutMs)]),
  })
  signal.throwIfAborted()
  if (!response.ok) {
    const error: unknown = await response.json().catch(() => null)
    const parsed = z.object({ code: z.string().optional() }).safeParse(error)
    throw new DiagnosticApiError(
      response.status,
      parsed.success ? parsed.data.code : '',
    )
  }
  return response
}
async function parse<T>(response: Response, schema: z.ZodType<T>): Promise<T> {
  const value = schema.safeParse(await response.json())
  if (!value.success) throw new DiagnosticApiError(0, 'INVALID_RESPONSE')
  return value.data
}
export async function createVariant(
  subjectId: string,
  key: string,
  signal: AbortSignal,
) {
  return parse(
    await request('/variants', signal, {
      method: 'POST',
      headers: { 'Idempotency-Key': key, 'Content-Type': 'application/json' },
      body: JSON.stringify({ subject_id: subjectId }),
    }),
    z.object({ id }),
  )
}
export async function startDiagnostic(
  variantId: string,
  key: string,
  signal: AbortSignal,
) {
  return parse(
    await request('/diagnostic-sessions', signal, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
      body: JSON.stringify({ variant_id: variantId }),
    }),
    progressSchema,
  )
}
export async function readDiagnostic(sessionId: string, signal: AbortSignal) {
  return parse(
    await request(
      `/diagnostic-sessions/${encodeURIComponent(sessionId)}`,
      signal,
    ),
    progressSchema,
  )
}
import type { DiagnosticSubmission } from '../model/submission'
export type { DiagnosticSubmission } from '../model/submission'
export function createSubmission(
  sessionId: string,
  taskId: string,
  blob: Blob,
  role: DiagnosticTask['role'],
): DiagnosticSubmission {
  const body = new FormData()
  const extension = blob.type.includes('ogg')
    ? 'ogg'
    : blob.type.includes('wav')
      ? 'wav'
      : 'webm'
  body.append('audio', blob, `answer.${extension}`)
  body.append('variant_task_id', taskId)
  return { sessionId, taskId, role, key: crypto.randomUUID(), body }
}
export function createSkipSubmission(
  sessionId: string,
  taskId: string,
  role: DiagnosticTask['role'],
): DiagnosticSubmission {
  return {
    sessionId,
    taskId,
    role,
    key: crypto.randomUUID(),
    body: new FormData(),
    skip: true,
  }
}
export async function submitDiagnostic(
  input: DiagnosticSubmission,
  signal: AbortSignal,
) {
  const progress = await parse(
    await request(
      `/diagnostic-sessions/${encodeURIComponent(input.sessionId)}/${input.skip ? 'skip' : input.transcriptionId ? 'answers/transcription' : 'answers'}`,
      signal,
      {
        method: 'POST',
        headers:
          input.skip || input.transcriptionId
            ? {
                'Idempotency-Key': input.key,
                'Content-Type': 'application/json',
              }
            : { 'Idempotency-Key': input.key },
        body: input.skip
          ? JSON.stringify({ variant_task_id: input.taskId })
          : input.transcriptionId
            ? JSON.stringify({
                variant_task_id: input.taskId,
                transcription_id: input.transcriptionId,
              })
            : input.body,
      },
      240_000,
    ),
    progressSchema,
  )
  if (
    progress.session_id !== input.sessionId ||
    progress.score === undefined ||
    progress.grader_max_score !== (input.role === 'main' ? 2 : 1) ||
    progress.score > progress.grader_max_score ||
    !progress.feedback ||
    !progress.verdict
  )
    throw new DiagnosticApiError(0, 'INVALID_RESPONSE')
  return progress
}
export async function readDiagnosticAudio(
  sessionId: string,
  taskId: string,
  signal: AbortSignal,
): Promise<DiagnosticAudioMetadata> {
  const response = await request(
    `/diagnostic-sessions/${encodeURIComponent(sessionId)}/current/audio?variant_task_id=${encodeURIComponent(taskId)}`,
    signal,
  )
  const metadata = await parse(response, diagnosticAudioSchema)
  if (metadata.variant_task_id !== taskId)
    throw new DiagnosticApiError(0, 'INVALID_RESPONSE')
  return metadata
}
export async function regenerateDiagnosticAudio(
  sessionId: string,
  taskId: string,
  signal: AbortSignal,
): Promise<DiagnosticAudioMetadata> {
  const response = await request(
    `/diagnostic-sessions/${encodeURIComponent(sessionId)}/current/audio/regenerate`,
    signal,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ variant_task_id: taskId }),
    },
    300_000,
  )
  const metadata = await parse(response, diagnosticAudioSchema)
  if (metadata.variant_task_id !== taskId || metadata.status !== 'ready')
    throw new DiagnosticApiError(0, 'INVALID_RESPONSE')
  return metadata
}
export async function fetchDiagnosticAudioFile(
  audioUrl: string,
  signal: AbortSignal,
) {
  return fetchStoredAudio(audioUrl, signal)
}
export async function readDiagnosticResult(
  sessionId: string,
  signal: AbortSignal,
) {
  const result = await parse(
    await request(
      `/diagnostic-sessions/${encodeURIComponent(sessionId)}/result`,
      signal,
    ),
    resultSchema,
  )
  if (result.session_id !== sessionId)
    throw new DiagnosticApiError(0, 'INVALID_RESPONSE')
  return result
}

export async function readDiagnosticFeedback(
  sessionId: string,
  signal: AbortSignal,
) {
  const feedback = await parse(
    await request(
      `/diagnostic-sessions/${encodeURIComponent(sessionId)}/feedback`,
      signal,
    ),
    overallFeedbackSchema,
  )
  if (feedback.session_id !== sessionId)
    throw new DiagnosticApiError(0, 'INVALID_RESPONSE')
  return feedback
}
