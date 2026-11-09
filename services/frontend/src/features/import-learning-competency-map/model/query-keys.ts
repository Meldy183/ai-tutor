export const competencyMapQueryKeys = {
  competencyMap: (userId: string, subjectId: string) =>
    ['competency-map', userId, subjectId] as const,
  outline: (userId: string, subjectId: string) =>
    ['competency-map', userId, subjectId, 'outline', 'learn'] as const,
}
