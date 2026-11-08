import { expect, it } from 'vitest'
import { selectVisibleMapBuild } from '@/pages/competency-map/model/select-visible-map-build'
import type { CourseGraphBuild } from '@/entities/course-graph'

function build(
  id: string,
  status: CourseGraphBuild['status'],
): CourseGraphBuild {
  return {
    id,
    subjectId: 'subject-1',
    status,
    stage: null,
    progress: null,
    startedAt: null,
    completedAt: null,
    error:
      status === 'failed' ? { code: 'FAILED', message: 'Сборка упала.' } : null,
    source: 'competency_map',
    map: {
      filename: 'map.xlsx',
      sheet: 'Geek',
      fingerprint: 'fingerprint-1',
      learningRevision: 2,
    },
  }
}

it('prefers the latest failed snapshot for this import over its queued POST response', () => {
  const latest = build('same-snapshot', 'failed')
  const uploaded = build('same-snapshot', 'queued')

  expect(
    selectVisibleMapBuild({
      latest,
      uploaded,
      pairedFingerprint: 'fingerprint-1',
      previousBuildId: 'same-snapshot',
    }),
  ).toBe(latest)
})

it('keeps the upload response visible until a matching latest snapshot arrives', () => {
  const uploaded = build('upload', 'queued')
  const older = build('older', 'succeeded')
  older.map = { ...older.map!, fingerprint: 'fingerprint-old' }

  expect(
    selectVisibleMapBuild({
      latest: older,
      uploaded,
      pairedFingerprint: 'fingerprint-1',
      previousBuildId: 'older',
    }),
  ).toBe(uploaded)
})
