import { createContext, useContext } from 'react'
import type { PropsWithChildren } from 'react'
import type {
  CompetencyMapOutline,
  CompetencyMapSummary,
} from '@/entities/learning-competency-map'
// A map is imported with its Geek sheet through the course graph; the
// learning side only reads it.
export type CompetencyMapDependencies = {
  competencyMap: {
    readOutline(
      subjectId: string,
      signal: AbortSignal,
    ): Promise<CompetencyMapOutline>
    read(subjectId: string, signal: AbortSignal): Promise<CompetencyMapSummary>
  }
}
const Context = createContext<CompetencyMapDependencies | null>(null)
export function CompetencyMapDependenciesProvider({
  value,
  children,
}: PropsWithChildren<{ value: CompetencyMapDependencies }>) {
  return <Context.Provider value={value}>{children}</Context.Provider>
}
export function useCompetencyMapDependencies() {
  const value = useContext(Context)
  if (!value) throw new Error('Competency map dependencies provider is missing')
  return value
}
