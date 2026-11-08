export type CompetencyMapSummary = {
  revision: number
  importedAt: number | null
  competencyCount: number
  constituentCount: number
  outcomeCount: number
  taskCount: number
}

export type CompetencyMapOutline = {
  competencies: {
    id: string
    name: string
    display_name?: string
    constituents: {
      id: string
      name: string
      display_name?: string
      outcomes: { id: string; name: string; role?: 'main' | 'basic' }[]
    }[]
  }[]
}
