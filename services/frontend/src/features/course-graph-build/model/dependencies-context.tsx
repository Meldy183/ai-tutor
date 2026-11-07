import { createContext, useContext } from 'react'
import type { PropsWithChildren } from 'react'
import type { CourseGraph, CourseGraphBuild } from '@/entities/course-graph'

export type CourseGraphBuildDependencies = {
  courseGraph: {
    retryCompetencyMap(
      subjectId: string,
      signal: AbortSignal,
      expectedFingerprint: string,
    ): Promise<CourseGraphBuild>
    skipMissingCriteria(
      subjectId: string,
      signal: AbortSignal,
      expectedFingerprint: string,
    ): Promise<CourseGraphBuild>
    readLatestCourseGraphBuild(
      subjectId: string,
      signal: AbortSignal,
    ): Promise<CourseGraphBuild | null>
    startCourseGraphBuild(
      subjectId: string,
      signal: AbortSignal,
    ): Promise<CourseGraphBuild>
    readCourseGraph(
      subjectId: string,
      signal: AbortSignal,
    ): Promise<CourseGraph>
    importCompetencyMap(
      subjectId: string,
      file: File,
      signal: AbortSignal,
      expectedLearningRevision?: number,
    ): Promise<CourseGraphBuild>
  }
}

const Context = createContext<CourseGraphBuildDependencies | null>(null)

export function CourseGraphBuildDependenciesProvider({
  value,
  children,
}: PropsWithChildren<{ value: CourseGraphBuildDependencies }>) {
  return <Context.Provider value={value}>{children}</Context.Provider>
}

export function useCourseGraphBuildDependencies() {
  const value = useContext(Context)
  if (!value)
    throw new Error('Course graph build dependencies provider is missing')
  return value
}
