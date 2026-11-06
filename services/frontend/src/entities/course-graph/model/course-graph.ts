export type CourseGraphBuildStatus =
  'queued' | 'running' | 'succeeded' | 'failed'

export type CourseGraphBuildStage =
  | 'topics'
  | 'synonyms'
  | 'prerequisites'
  | 'attach'
  | 'questions'
  | 'prepare_questions'
  | 'retry_questions'
  | 'publish'

// Where a running build is: stage stageIndex of stageCount and the model
// batches of that stage finished so far; a stage without work has total 0.
export type CourseGraphBuildProgress = {
  stage: CourseGraphBuildStage
  stageIndex: number
  stageCount: number
  done: number
  total: number
  remainingSeconds?: number
}

// The share of a build done: finished stages plus the finished part of the
// current one. A stage without counted work adds nothing until it ends.
export function buildProgressPercent(progress: CourseGraphBuildProgress) {
  const stage = progress.total > 0 ? progress.done / progress.total : 0
  return ((progress.stageIndex - 1 + stage) / progress.stageCount) * 100
}

export type CourseGraphBuildError = {
  code: string
  message: string
}

export type CourseGraphBuild = {
  id: string
  subjectId: string
  status: CourseGraphBuildStatus
  stage: CourseGraphBuildStage | null
  progress: CourseGraphBuildProgress | null
  startedAt: number | null
  completedAt: number | null
  error: CourseGraphBuildError | null
  stale?: boolean
  inputsReady?: boolean
  // What the build reads: the course materials or an imported competency map.
  source?: CourseGraphSource | null
  map?: CourseGraphMapFile | null
  graphFingerprint?: string
  graphMapRevision?: number | null
}

export type CourseGraphSource = 'materials' | 'competency_map'

// The competency map imported last and the sheet it was read from.
export type CourseGraphMapFile = {
  fingerprint?: string
  filename: string
  sheet: string
  learningRevision?: number | null
}

export type CourseGraphNode = {
  id: string
  title: string
  content: string
  prerequisites: string[]
  outcomes: string[]
  questions: string[]
}

export type CourseGraph = {
  version: string
  publishedAt: number | null
  sourceMaterialIds?: string[]
  nodes: CourseGraphNode[]
  stale?: boolean
  inputsReady?: boolean
  view?: CourseGraphView
  // built: from the course materials; competency_map: from an imported
  // competency map.
  source?: 'built' | 'competency_map'
  map?: CourseGraphMapFile | null
}

// Native graph payload supplied by the course-graph service.
export type CourseGraphView = {
  assessment_skipped?: number
  assessment_warnings?: { row: number; column: string; message: string }[]
  topics: {
    id: string
    title: string
    question?: string
    // The topic's theory assembled from the materials (Markdown).
    text?: string
    // The tasks of a topic of a competency map; the exam asks one of them.
    // question and text above are those of the first.
    variants?: TopicVariant[]
    provides?: string[]
    requires?: string[]
    parent_title?: string | null
    sources?: TopicSource[]
  }[]
  prerequisites: {
    source: string
    target: string
    requirement?: string
    reason?: string
  }[]
  hierarchy: { source: string; target: string }[]
  quality?: {
    acyclic: boolean
    topics?: number
    requirements?: number
    unresolved_requirements?: number
    [key: string]: unknown
  }
}

// A task of a topic with its reference answer and rubric from the map.
export type TopicVariant = {
  question: string
  content: string
  assessment_guidance?: string
}

export type TopicSource = {
  material_id: string
  block_id: number
  start: number
  end: number
  text: string
  page?: number | null
  week?: number | null
  kind?: string | null
  filename?: string | null
  kind_label?: string | null
  material_revision?: string | null
}
