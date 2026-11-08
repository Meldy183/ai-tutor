import { LearningHome } from './learning-home'

export function TrainerScreen({
  subjectId,
  sessionId,
}: {
  subjectId?: string
  sessionId?: string
}) {
  return (
    <LearningHome
      requestedSubjectId={subjectId}
      requestedSessionId={sessionId}
    />
  )
}
