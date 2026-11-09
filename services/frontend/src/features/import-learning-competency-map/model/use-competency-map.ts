import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CompetencyMapApiError } from '@/entities/learning-competency-map'
import { useCompetencyMapDependencies } from './dependencies-context'
import {
  captureSession,
  isCurrentSession,
  registerSessionRequest,
  replaceSession,
} from '@/entities/user'
import { competencyMapQueryKeys } from './query-keys'

export function useCompetencyMapQuery(userId: string, subjectId: string) {
  const { competencyMap } = useCompetencyMapDependencies()
  const cache = useQueryClient()
  // Auth epoch is QueryClient-local and intentionally not part of the public resource key.
  // eslint-disable-next-line @tanstack/query/exhaustive-deps
  return useQuery({
    queryKey: competencyMapQueryKeys.competencyMap(userId, subjectId),
    enabled: !!userId && !!subjectId,
    queryFn: async ({ signal }) => {
      const token = captureSession(cache)
      const controller = new AbortController()
      const unregister = registerSessionRequest(cache, token, controller)
      try {
        const data = await competencyMap.read(
          subjectId,
          AbortSignal.any([signal, controller.signal]),
        )
        if (!isCurrentSession(cache, token) || token.userId !== userId)
          throw new DOMException('Stale session', 'AbortError')
        return data
      } catch (error) {
        if (
          error instanceof CompetencyMapApiError &&
          error.status === 401 &&
          isCurrentSession(cache, token)
        )
          await replaceSession(cache, null)
        throw error
      } finally {
        unregister()
      }
    },
  })
}

export function useCompetencyMapOutlineQuery(
  userId: string,
  subjectId: string,
) {
  const { competencyMap } = useCompetencyMapDependencies()
  const cache = useQueryClient()
  // Auth epoch is QueryClient-local and intentionally not part of the public resource key.
  // eslint-disable-next-line @tanstack/query/exhaustive-deps
  return useQuery({
    queryKey: competencyMapQueryKeys.outline(userId, subjectId),
    enabled: !!userId && !!subjectId,
    queryFn: async ({ signal }) => {
      const token = captureSession(cache)
      const controller = new AbortController()
      const unregister = registerSessionRequest(cache, token, controller)
      try {
        const data = await competencyMap.readOutline(
          subjectId,
          AbortSignal.any([signal, controller.signal]),
        )
        if (!isCurrentSession(cache, token) || token.userId !== userId)
          throw new DOMException('Stale session', 'AbortError')
        return data
      } catch (error) {
        if (
          error instanceof CompetencyMapApiError &&
          error.status === 401 &&
          isCurrentSession(cache, token)
        )
          await replaceSession(cache, null)
        throw error
      } finally {
        unregister()
      }
    },
  })
}
