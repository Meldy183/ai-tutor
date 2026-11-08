import type { CourseGraphBuild } from '@/entities/course-graph'

export function selectVisibleMapBuild({
  latest,
  uploaded,
  pairedFingerprint,
  previousBuildId,
}: {
  latest: CourseGraphBuild | null
  uploaded: CourseGraphBuild | null
  pairedFingerprint: string | null
  previousBuildId: string | null
}): CourseGraphBuild | null {
  const latestForImport =
    pairedFingerprint !== null && latest?.map?.fingerprint === pairedFingerprint
      ? latest
      : null
  const latestIsNewImport = latest !== null && latest.id !== previousBuildId

  return (
    latestForImport ??
    uploaded ??
    (latestIsNewImport ? latest : null) ??
    (pairedFingerprint === null ? latest : null)
  )
}
