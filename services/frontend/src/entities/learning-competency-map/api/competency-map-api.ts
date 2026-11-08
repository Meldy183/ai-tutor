import { z } from 'zod'
import { i18n } from '@/shared/i18n'
import type {
  CompetencyMapOutline,
  CompetencyMapSummary,
} from '@/entities/learning-competency-map'
import { apiFetch } from '@/shared/api'

const apiBase =
  (import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(/\/$/, '') + '/learn'
const count = z.number().int().nonnegative()
const mapSchema = z.object({
  revision: count,
  imported_at: count.nullable(),
  competencies: z.array(
    z.object({
      constituents: z.array(
        z.object({
          outcomes: z.array(z.object({ tasks: z.array(z.unknown()) })),
        }),
      ),
    }),
  ),
})
const errorSchema = z.object({
  message: z.string(),
  details: z
    .array(z.object({ path: z.string().optional(), message: z.string() }))
    .optional(),
})

import { CompetencyMapApiError } from '../model/competency-map-error'
export { CompetencyMapApiError } from '../model/competency-map-error'

async function readResponse(response: Response): Promise<unknown> {
  const data: unknown = await response.json().catch(() => null)
  if (!response.ok) {
    const parsed = errorSchema.safeParse(data)
    const key =
      response.status === 401
        ? 'unauthorized'
        : response.status === 403
          ? 'forbidden'
          : response.status === 413
            ? 'tooLarge'
            : response.status === 415
              ? 'invalidType'
              : 'requestError'
    throw new CompetencyMapApiError(
      response.status,
      parsed.success
        ? parsed.data.message
        : i18n.t(`learningCompetencyMap.${key}`),
      parsed.success ? parsed.data.details : undefined,
    )
  }
  return data
}

export async function readCompetencyMap(
  subjectId: string,
  signal: AbortSignal,
): Promise<CompetencyMapSummary> {
  const response = await apiFetch(
    `${apiBase}/subjects/${encodeURIComponent(subjectId)}/competency-map`,
    {
      credentials: 'include',
      signal: AbortSignal.any([signal, AbortSignal.timeout(30_000)]),
    },
  )
  const parsed = mapSchema.safeParse(await readResponse(response))
  if (!parsed.success)
    throw new Error(i18n.t('learningCompetencyMap.invalidResponse'))
  const map = parsed.data
  const constituents = map.competencies.flatMap((item) => item.constituents)
  const outcomes = constituents.flatMap((item) => item.outcomes)
  return {
    revision: map.revision,
    importedAt: map.imported_at,
    competencyCount: map.competencies.length,
    constituentCount: constituents.length,
    outcomeCount: outcomes.length,
    taskCount: outcomes.reduce((sum, item) => sum + item.tasks.length, 0),
  }
}

const outlineSchema = z.object({
  competencies: z.array(
    z.object({
      id: z.string(),
      name: z.string(),
      display_name: z.string().optional(),
      constituents: z.array(
        z.object({
          id: z.string(),
          name: z.string(),
          display_name: z.string().optional(),
          outcomes: z.array(
            z.object({
              id: z.string(),
              name: z.string(),
              role: z.enum(['main', 'basic']),
            }),
          ),
        }),
      ),
    }),
  ),
})

export async function readCompetencyMapOutline(
  subjectId: string,
  signal: AbortSignal,
): Promise<CompetencyMapOutline> {
  const response = await apiFetch(
    `${apiBase}/subjects/${encodeURIComponent(subjectId)}/diagnostic-outline`,
    {
      credentials: 'include',
      // The server budgets queue time and each LLM batch separately.
      // Keep navigation/logout cancellation, without a shorter browser deadline.
      signal,
    },
  )
  const parsed = outlineSchema.safeParse(await readResponse(response))
  if (!parsed.success)
    throw new Error(i18n.t('learningCompetencyMap.invalidResponse'))
  return parsed.data
}
