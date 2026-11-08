import { createFileRoute } from '@tanstack/react-router'
import { z } from 'zod'
import { CompetencyMapAdminScreen } from '@/pages/competency-map'

export const Route = createFileRoute('/admin/courses/$subjectId')({
  validateSearch: z.object({ direction: z.enum(['learn', 'exam']).optional() }),
  component: CourseMaterialsRoute,
})

// The direction of the catalog the admin came from; the preparation of a course
// is the same for both.
function CourseMaterialsRoute() {
  const { subjectId } = Route.useParams()
  return <CompetencyMapAdminScreen subjectId={subjectId} />
}
