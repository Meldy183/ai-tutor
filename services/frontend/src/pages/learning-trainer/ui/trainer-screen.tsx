import { LearningHome } from './learning-home'

export function TrainerScreen({
  subjectId,
  resume,
}: {
  subjectId?: string
  resume?: 'diagnostic' | 'training'
}) {
  return (
    <LearningHome requestedSubjectId={subjectId} requestedResume={resume} />
  )
}
