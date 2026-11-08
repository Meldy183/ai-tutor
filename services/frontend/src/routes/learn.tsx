import { createFileRoute } from '@tanstack/react-router'
import { TrainerScreen } from '@/pages/learning-trainer'
import { z } from 'zod'

export const Route = createFileRoute('/learn')({
  validateSearch: z.object({
    subject_id: z.string().optional(),
    resume: z.enum(['diagnostic', 'training']).optional(),
  }),
  component: () => (
    <TrainerScreen
      subjectId={Route.useSearch().subject_id}
      resume={Route.useSearch().resume}
    />
  ),
})
