import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'
import { CourseGraphApiError, type CourseGraph } from '@/entities/course-graph'
import {
  captureSession,
  isCurrentSession,
  registerSessionRequest,
  replaceSession,
} from '@/entities/user'
import { useCourseGraphBuildDependencies } from './dependencies-context'
import { courseGraphQueryKeys } from './query-keys'
import { subjectQueryKeys } from '@/entities/subject'

export function useLatestCourseGraphBuildQuery(
  userId: string,
  subjectId: string,
  materialsBusy = false,
  metadataIncomplete = false,
) {
  const { courseGraph } = useCourseGraphBuildDependencies()
  const cache = useQueryClient()
  const lastSuccessRef = useRef<string | null>(null)
  const lastObservedRef = useRef<string | null>(null)
  const wasMaterialsBusy = useRef(materialsBusy)
  useEffect(() => {
    if (wasMaterialsBusy.current && !materialsBusy) {
      void cache.invalidateQueries({
        queryKey: courseGraphQueryKeys.build(userId, subjectId),
      })
    }
    wasMaterialsBusy.current = materialsBusy
  }, [cache, materialsBusy, userId, subjectId])
  // eslint-disable-next-line @tanstack/query/exhaustive-deps -- auth epoch and dependency port are scoped outside the public resource key.
  const query = useQuery({
    queryKey: courseGraphQueryKeys.build(userId, subjectId),
    enabled: !!userId && !!subjectId,
    queryFn: async ({ signal }) => {
      const token = captureSession(cache)
      const controller = new AbortController()
      const unregister = registerSessionRequest(cache, token, controller)
      try {
        const build = await courseGraph.readLatestCourseGraphBuild(
          subjectId,
          AbortSignal.any([signal, controller.signal]),
        )
        if (!isCurrentSession(cache, token) || token.userId !== userId)
          throw new DOMException('Stale session', 'AbortError')
        return build
      } catch (error) {
        if (
          error instanceof CourseGraphApiError &&
          error.status === 401 &&
          isCurrentSession(cache, token)
        )
          await replaceSession(cache, null)
        throw error
      } finally {
        unregister()
      }
    },
    refetchInterval: (current) =>
      materialsBusy ||
      (!metadataIncomplete &&
        current.state.data?.inputsReady === false &&
        current.state.data.source !== 'competency_map') ||
      current.state.data?.status === 'running' ||
      (current.state.data?.status === 'queued' &&
        // A map does not wait for the materials.
        (!metadataIncomplete || current.state.data.source === 'competency_map'))
        ? 2000
        : false,
  })
  useEffect(() => {
    const build = query.data
    if (!build) return
    const key = `${build.status}:${build.source}:${build.inputsReady}`
    if (lastObservedRef.current === key) return
    lastObservedRef.current = key
    void cache.invalidateQueries({
      queryKey: subjectQueryKeys.subjects(userId),
      exact: false,
    })
  }, [
    cache,
    query.data?.inputsReady,
    query.data?.source,
    query.data?.status,
    userId,
  ])
  useEffect(() => {
    if (query.data?.status !== 'succeeded') return
    if (lastSuccessRef.current === query.data.id) return
    lastSuccessRef.current = query.data.id
    void cache.invalidateQueries({
      queryKey: courseGraphQueryKeys.graph(userId, subjectId),
    })
    void cache.invalidateQueries({
      queryKey: subjectQueryKeys.subjects(userId),
    })
  }, [cache, query.data, subjectId, userId])
  return query
}

export function useCourseGraphQuery(
  userId: string,
  subjectId: string,
  materialsBusy = false,
  metadataIncomplete = false,
) {
  const { courseGraph } = useCourseGraphBuildDependencies()
  const cache = useQueryClient()
  const wasMaterialsBusy = useRef(materialsBusy)
  useEffect(() => {
    if (wasMaterialsBusy.current && !materialsBusy) {
      void cache.invalidateQueries({
        queryKey: courseGraphQueryKeys.graph(userId, subjectId),
      })
    }
    wasMaterialsBusy.current = materialsBusy
  }, [cache, materialsBusy, userId, subjectId])
  // eslint-disable-next-line @tanstack/query/exhaustive-deps -- auth epoch and dependency port are scoped outside the public resource key.
  return useQuery<CourseGraph>({
    queryKey: courseGraphQueryKeys.graph(userId, subjectId),
    refetchInterval: (current) =>
      materialsBusy ||
      (!metadataIncomplete &&
        current.state.data?.inputsReady === false &&
        current.state.data.source !== 'competency_map')
        ? 2000
        : false,
    enabled: !!userId && !!subjectId,
    queryFn: async ({ signal }) => {
      const token = captureSession(cache)
      const controller = new AbortController()
      const unregister = registerSessionRequest(cache, token, controller)
      try {
        const graph = await courseGraph.readCourseGraph(
          subjectId,
          AbortSignal.any([signal, controller.signal]),
        )
        if (!isCurrentSession(cache, token) || token.userId !== userId)
          throw new DOMException('Stale session', 'AbortError')
        return graph
      } catch (error) {
        if (
          error instanceof CourseGraphApiError &&
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

export function useStartCourseGraphBuildMutation(
  userId: string,
  subjectId: string,
  operation:
    'materials' | 'skipMissingCriteria' | 'retryCompetencyMap' = 'materials',
) {
  const { courseGraph } = useCourseGraphBuildDependencies()
  const cache = useQueryClient()
  const tokenRef = useRef(captureSession(cache))
  return useMutation({
    mutationKey: ['course-graph-build', operation, userId, subjectId],
    gcTime: 0,
    onMutate: () => {
      tokenRef.current = captureSession(cache)
    },
    mutationFn: async (
      input: string | { subjectId: string; expectedFingerprint: string },
    ) => {
      const targetSubjectId =
        typeof input === 'string' ? input : input.subjectId
      const expectedFingerprint =
        typeof input === 'string' ? '' : input.expectedFingerprint
      const controller = new AbortController()
      const unregister = registerSessionRequest(
        cache,
        tokenRef.current,
        controller,
      )
      try {
        if (operation === 'materials') {
          return await courseGraph.startCourseGraphBuild(
            targetSubjectId,
            controller.signal,
          )
        }
        return await courseGraph[operation](
          targetSubjectId,
          controller.signal,
          expectedFingerprint,
        )
      } finally {
        unregister()
      }
    },
    onSuccess: async (_data, input) => {
      const targetSubjectId =
        typeof input === 'string' ? input : input.subjectId
      if (!isCurrentSession(cache, tokenRef.current)) return
      await Promise.all([
        cache.invalidateQueries({
          queryKey: courseGraphQueryKeys.build(userId, targetSubjectId),
        }),
        cache.invalidateQueries({
          queryKey: subjectQueryKeys.subjects(userId),
        }),
        cache.invalidateQueries({
          queryKey: subjectQueryKeys.audioPreparation(userId, targetSubjectId),
        }),
      ])
    },
    onError: async (error, input) => {
      const targetSubjectId =
        typeof input === 'string' ? input : input.subjectId
      if (!isCurrentSession(cache, tokenRef.current)) return
      if (error instanceof CourseGraphApiError && error.status === 401)
        await replaceSession(cache, null)
      await Promise.all([
        cache.invalidateQueries({
          queryKey: courseGraphQueryKeys.build(userId, targetSubjectId),
        }),
        cache.invalidateQueries({
          queryKey: subjectQueryKeys.subjects(userId),
          exact: false,
        }),
        cache.invalidateQueries({
          queryKey: subjectQueryKeys.audioPreparation(userId, targetSubjectId),
        }),
      ])
    },
    retry: false,
  })
}

export type ImportCompetencyMapInput = {
  file: File
}

// Builds the course graph from a competency map instead of the materials.
export function useImportCompetencyMapMutation(
  userId: string,
  subjectId: string,
) {
  const { courseGraph } = useCourseGraphBuildDependencies()
  const cache = useQueryClient()
  const tokenRef = useRef(captureSession(cache))
  return useMutation({
    mutationKey: ['course-graph-build', 'import-map', userId, subjectId],
    gcTime: 0,
    onMutate: () => {
      tokenRef.current = captureSession(cache)
    },
    mutationFn: async ({ file }: ImportCompetencyMapInput) => {
      const controller = new AbortController()
      const unregister = registerSessionRequest(
        cache,
        tokenRef.current,
        controller,
      )
      try {
        return await courseGraph.importCompetencyMap(
          subjectId,
          file,
          controller.signal,
        )
      } finally {
        unregister()
      }
    },
    onSuccess: () => {
      if (!isCurrentSession(cache, tokenRef.current)) return
      void Promise.all([
        cache.invalidateQueries({
          queryKey: courseGraphQueryKeys.build(userId, subjectId),
        }),
        cache.invalidateQueries({
          queryKey: subjectQueryKeys.subjects(userId),
        }),
        cache.invalidateQueries({
          queryKey: subjectQueryKeys.audioPreparation(userId, subjectId),
        }),
      ])
    },
    onError: async (error) => {
      if (!isCurrentSession(cache, tokenRef.current)) return
      if (error instanceof CourseGraphApiError && error.status === 401)
        await replaceSession(cache, null)
      await cache.invalidateQueries({
        queryKey: subjectQueryKeys.subjects(userId),
        exact: false,
      })
      await cache.invalidateQueries({
        queryKey: subjectQueryKeys.audioPreparation(userId, subjectId),
      })
    },
    retry: false,
  })
}
