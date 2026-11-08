import { z } from 'zod'
import { apiFetch } from '@/shared/api'
import { DiagnosticApiError } from '../model/diagnostic-error'
import type {
  DiagnosticAnswer,
  DiagnosticGap,
  DiagnosticResult,
  DiagnosticSession,
  DiagnosticSummary,
  TurnAudioPart,
} from '../model/diagnostic-session'

const apiBase = `${(import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(
  /\/$/,
  '',
)}/exam`

// An answer waits for the model: evaluation and the next question.
const ANSWER_TIMEOUT = 240_000
const READ_TIMEOUT = 30_000
const AUDIO_TIMEOUT = 120_000

const id = z.string().min(1)
const unix = z.number().int().nonnegative()
const grade = z.enum(['A', 'B', 'C', 'D']).nullable()
const status = z.enum(['active', 'completed', 'cancelled'])
const materialReferenceSchema = z.object({
  material_id: id,
  filename: z.string().nullable().optional().default(null),
  kind: z.string().nullable().optional().default(null),
  kind_label: z.string().nullable().optional().default(null),
  week: z.number().int().positive().nullable().optional().default(null),
  material_revision: z.string().nullable().optional().default(null),
})
const sourceCitationSchema = materialReferenceSchema.extend({
  block_id: z.number().int(),
  start: z.number().int(),
  end: z.number().int(),
  text: z.string(),
  page: z.number().int().nullable().optional().default(null),
})

const sessionSchema = z
  .object({
    id,
    subject_id: id,
    status,
    progress_percent: z.number().int().min(0).max(100),
    current_turn_id: id.nullable(),
    started_at: unix,
    completed_at: unix.nullable(),
    turns: z.array(
      z.object({
        id,
        line: z.number().int().positive(),
        speaker: z.enum(['geek', 'student']),
        kind: z.enum(['question', 'followup', 'rephrase', 'closing', 'answer']),
        node_id: z.string().nullable(),
        source: z.enum(['voice', 'text', 'dont_know']).nullable(),
        text: z.string(),
        // Older backends send no reply; the whole text is then the question.
        reply: z.string().default(''),
        meta: z.boolean().default(false),
        audio_intro: z.string().default(''),
        audio_transition: z.string().default(''),
        reply_audio_parts: z.number().int().min(0).max(100).default(0),
        created_at: unix,
      }),
    ),
  })
  .transform((s): DiagnosticSession => ({
    id: s.id,
    subjectId: s.subject_id,
    status: s.status,
    progressPercent: s.progress_percent,
    currentTurnId: s.current_turn_id,
    startedAt: s.started_at,
    completedAt: s.completed_at,
    turns: s.turns.map((t) => ({
      id: t.id,
      line: t.line,
      speaker: t.speaker,
      kind: t.kind,
      nodeId: t.node_id,
      source: t.source,
      text: t.text,
      reply: t.reply,
      meta: t.meta,
      audioIntro: t.audio_intro,
      audioTransition: t.audio_transition,
      replyAudioParts: t.reply_audio_parts,
      createdAt: t.created_at,
    })),
  }))

const gapSchema = z.object({
  node_id: id,
  title: z.string(),
  weeks: z.array(z.number().int().positive()).optional().default([]),
  sources: z.array(sourceCitationSchema).optional().default([]),
  learning_materials: z
    .object({
      theory: z.array(materialReferenceSchema).optional().default([]),
      practice: z.array(materialReferenceSchema).optional().default([]),
      additional: z.array(materialReferenceSchema).optional().default([]),
    })
    .optional()
    .default({ theory: [], practice: [], additional: [] }),
})

const points = z.number().int().min(0)

const resultSummarySchema = z
  .object({ session_id: id, summary: z.string() })
  .transform((r) => r.summary)

const resultSchema = z
  .object({
    session_id: id,
    grade,
    score_percent: z.number().int().min(0).max(100).nullable(),
    score: z.number().min(0),
    max_score: points,
    nodes: z.array(
      z.object({
        node_id: id,
        title: z.string(),
        status: z.enum([
          'independent',
          'assisted',
          'gap',
          'insufficient',
          'credited_by_parent',
          'unchecked',
        ]),
        top_level: z.boolean(),
        // Older backends do not send prerequisites; the graph then has no edges.
        requires: z.array(id).optional().default([]),
        assisted: z.boolean(),
        // 0–2 for a top-level topic, 0–1 for a prerequisite.
        score: points.nullable(),
        max_score: z.number().int().min(1).max(2),
        reasons: z.array(z.string()),
        reply: z.string(),
      }),
    ),
    gaps: z.array(gapSchema),
    // Older backends send no weak spots.
    weak_spots: z.array(gapSchema).optional().default([]),
  })
  .transform((r): DiagnosticResult => ({
    sessionId: r.session_id,
    grade: r.grade,
    scorePercent: r.score_percent,
    score: r.score,
    maxScore: r.max_score,
    topics: r.nodes.map((n) => ({
      nodeId: n.node_id,
      title: n.title,
      status: n.status,
      topLevel: n.top_level,
      requires: n.requires,
      assisted: n.assisted,
      score: n.score,
      maxScore: n.max_score,
      reasons: n.reasons,
      reply: n.reply,
    })),
    gaps: r.gaps.map(toGap),
    weakSpots: r.weak_spots.map(toGap),
  }))

function toGap(g: z.infer<typeof gapSchema>): DiagnosticGap {
  return {
    nodeId: g.node_id,
    title: g.title,
    weeks: g.weeks,
    sources: g.sources.map((s) => ({
      materialId: s.material_id,
      filename: s.filename,
      kind: s.kind,
      kindLabel: s.kind_label,
      week: s.week,
      revision: s.material_revision,
      blockId: s.block_id,
      start: s.start,
      end: s.end,
      text: s.text,
      page: s.page,
    })),
    learningMaterials: {
      theory: g.learning_materials.theory.map(toMaterialReference),
      practice: g.learning_materials.practice.map(toMaterialReference),
      additional: g.learning_materials.additional.map(toMaterialReference),
    },
  }
}

function toMaterialReference(s: z.infer<typeof materialReferenceSchema>) {
  return {
    materialId: s.material_id,
    filename: s.filename,
    kind: s.kind,
    kindLabel: s.kind_label,
    week: s.week,
    revision: s.material_revision,
  }
}

const historySchema = z
  .object({
    items: z.array(
      z.object({
        id,
        status,
        progress_percent: z.number().int().min(0).max(100),
        started_at: unix,
        completed_at: unix.nullable(),
        grade,
        score_percent: z.number().int().min(0).max(100).nullable(),
      }),
    ),
  })
  .transform((h): DiagnosticSummary[] =>
    h.items.map((item) => ({
      id: item.id,
      status: item.status,
      progressPercent: item.progress_percent,
      startedAt: item.started_at,
      completedAt: item.completed_at,
      grade: item.grade,
      scorePercent: item.score_percent,
    })),
  )

async function request(
  path: string,
  signal: AbortSignal,
  timeout: number,
  init: RequestInit = {},
) {
  let response: Response
  const requestSignal = AbortSignal.any([signal, AbortSignal.timeout(timeout)])
  try {
    response = await apiFetch(`${apiBase}${path}`, {
      ...init,
      credentials: 'include',
      signal: requestSignal,
    })
  } catch (error) {
    signal.throwIfAborted()
    if (error instanceof DOMException && error.name === 'TimeoutError')
      throw new DiagnosticApiError(0, 'TIMEOUT')
    if (error instanceof TypeError) throw new DiagnosticApiError(0, 'NETWORK')
    throw error
  }
  signal.throwIfAborted()
  if (!response.ok) {
    const body: unknown = await response.json().catch(() => null)
    const parsed = z.object({ code: z.string() }).safeParse(body)
    throw new DiagnosticApiError(
      response.status,
      parsed.success ? parsed.data.code : '',
    )
  }
  return response
}

async function parse<S extends z.ZodType>(
  response: Response,
  schema: S,
  signal: AbortSignal,
): Promise<z.output<S>> {
  let body: unknown
  try {
    body = await response.json()
  } catch {
    signal.throwIfAborted()
    throw new DiagnosticApiError(0, 'INVALID_RESPONSE')
  }
  const parsed = schema.safeParse(body)
  if (!parsed.success) throw new DiagnosticApiError(0, 'INVALID_RESPONSE')
  return parsed.data
}

const json = (key: string, body: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
  body: JSON.stringify(body),
})

const session = (sessionId: string) =>
  `/diagnostic-sessions/${encodeURIComponent(sessionId)}`

// Starts a diagnostic on the course or resumes the active one.
export async function startDiagnostic(
  subjectId: string,
  key: string,
  signal: AbortSignal,
): Promise<DiagnosticSession> {
  return parse(
    await request(
      '/diagnostic-sessions',
      signal,
      ANSWER_TIMEOUT,
      json(key, { subject_id: subjectId }),
    ),
    sessionSchema,
    signal,
  )
}

export async function readDiagnostic(
  sessionId: string,
  signal: AbortSignal,
): Promise<DiagnosticSession> {
  return parse(
    await request(session(sessionId), signal, READ_TIMEOUT),
    sessionSchema,
    signal,
  )
}

export async function listDiagnostics(
  subjectId: string,
  signal: AbortSignal,
): Promise<DiagnosticSummary[]> {
  return parse(
    await request(
      `/diagnostic-sessions?subject_id=${encodeURIComponent(subjectId)}`,
      signal,
      READ_TIMEOUT,
    ),
    historySchema,
    signal,
  )
}

export async function answerDiagnostic(
  sessionId: string,
  turnId: string,
  answer: DiagnosticAnswer,
  key: string,
  signal: AbortSignal,
): Promise<DiagnosticSession> {
  const body =
    answer.kind === 'text'
      ? { turn_id: turnId, text: answer.text }
      : answer.kind === 'voice'
        ? { turn_id: turnId, transcription_id: answer.transcriptionId }
        : { turn_id: turnId, dont_know: true }
  return parse(
    await request(
      `${session(sessionId)}/answers`,
      signal,
      ANSWER_TIMEOUT,
      json(key, body),
    ),
    sessionSchema,
    signal,
  )
}

export async function readDiagnosticResult(
  sessionId: string,
  signal: AbortSignal,
): Promise<DiagnosticResult> {
  return parse(
    await request(`${session(sessionId)}/result`, signal, READ_TIMEOUT),
    resultSchema,
    signal,
  )
}

// The model writes the conclusion once, so the first read may take a while.
export async function readDiagnosticResultSummary(
  sessionId: string,
  signal: AbortSignal,
): Promise<string> {
  return parse(
    await request(`${session(sessionId)}/feedback`, signal, ANSWER_TIMEOUT),
    resultSummarySchema,
    signal,
  )
}

export async function fetchDiagnosticMaterialRaw(
  sessionId: string,
  materialId: string,
  signal: AbortSignal,
): Promise<Blob> {
  const response = await request(
    `${session(sessionId)}/materials/${encodeURIComponent(materialId)}/raw`,
    signal,
    READ_TIMEOUT,
  )
  return response.blob()
}

// The voice of a Geek turn as WAV: the whole turn or one of its parts, the
// reply to the previous answer or the question after it.
export async function fetchTurnAudio(
  sessionId: string,
  turnId: string,
  signal: AbortSignal,
  part?: TurnAudioPart,
  chunk?: number,
): Promise<Blob> {
  const query = part
    ? `?part=${part}${chunk === undefined ? '' : `&chunk=${chunk}`}`
    : ''
  const response = await request(
    `${session(sessionId)}/turns/${encodeURIComponent(turnId)}/audio${query}`,
    signal,
    AUDIO_TIMEOUT,
  )
  const type = response.headers.get('Content-Type')?.split(';', 1)[0].trim()
  const audio = await response.blob()
  if (type !== 'audio/wav' || !audio.size)
    throw new DiagnosticApiError(0, 'INVALID_RESPONSE')
  return new Blob([audio], { type: 'audio/wav' })
}

export type DiagnosticApi = {
  startDiagnostic: typeof startDiagnostic
  readDiagnostic: typeof readDiagnostic
  listDiagnostics: typeof listDiagnostics
  answerDiagnostic: typeof answerDiagnostic
  readDiagnosticResult: typeof readDiagnosticResult
  readDiagnosticResultSummary: typeof readDiagnosticResultSummary
  fetchTurnAudio: typeof fetchTurnAudio
}
