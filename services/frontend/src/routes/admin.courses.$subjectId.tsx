import { createFileRoute } from '@tanstack/react-router'
import { z } from 'zod'
import { CompetencyMapAdminScreen } from '@/pages/competency-map'

export const Route = createFileRoute('/admin/courses/$subjectId')({
  validateSearch: z.object({ direction: z.enum(['learn', 'exam']).optional() }),
  component: CourseMaterialsRoute,
})

function CourseMaterialsRoute() {
  const { subjectId } = Route.useParams()
  const { direction } = Route.useSearch()
  return (
    <CompetencyMapAdminScreen subjectId={subjectId} direction={direction} />
  )
}
