-- name: RequestCourseGraphBuild :one
-- The admin's "build graph" button: queue a build of the current materials
-- unless one is already queued or running. No row: inputs are not ready
-- (a material is still processing or failed, or the course has none) or a
-- build is already in progress.
INSERT INTO course_graph_jobs(subject_id, source, fingerprint, updated_at)
SELECT i.subject_id, 'materials', i.fingerprint, sqlc.arg(now)::bigint FROM course_graph_inputs i
WHERE i.subject_id = sqlc.arg(subject_id) AND EXISTS (SELECT 1 FROM subjects s WHERE s.id=i.subject_id)
ON CONFLICT(subject_id) DO UPDATE SET source=excluded.source, fingerprint=excluded.fingerprint,
    status='pending', attempts=0, available_at=0, lease_until=0, lease_token='', error_message='', updated_at=excluded.updated_at
WHERE course_graph_jobs.status NOT IN ('pending', 'processing')
RETURNING subject_id;

-- name: RequestCourseGraphMapBuild :one
-- The admin imported a competency map: queue a build of it unless a build of
-- either source is already queued or running.
INSERT INTO course_graph_jobs(subject_id, source, fingerprint, updated_at)
SELECT m.subject_id, 'competency_map', m.fingerprint, sqlc.arg(now)::bigint FROM course_graph_maps m
WHERE m.subject_id = sqlc.arg(subject_id) AND EXISTS (SELECT 1 FROM subjects s WHERE s.id=m.subject_id)
ON CONFLICT(subject_id) DO UPDATE SET source=excluded.source, fingerprint=excluded.fingerprint,
    status='pending', attempts=0, available_at=0, lease_until=0, lease_token='', error_message='', updated_at=excluded.updated_at
WHERE course_graph_jobs.status NOT IN ('pending', 'processing')
RETURNING subject_id;

-- name: SaveCourseGraphMap :exec
INSERT INTO course_graph_maps(subject_id, filename, sheet, fingerprint, graph, uploaded_at, map_revision)
SELECT sqlc.arg(subject_id), sqlc.arg(filename), sqlc.arg(sheet), sqlc.arg(fingerprint), sqlc.arg(graph)::jsonb, sqlc.arg(now)::bigint,
       sqlc.narg(map_revision)::bigint
WHERE EXISTS (SELECT 1 FROM subjects s WHERE s.id=sqlc.arg(subject_id))
ON CONFLICT(subject_id) DO UPDATE SET filename=excluded.filename, sheet=excluded.sheet,
    fingerprint=excluded.fingerprint, graph=excluded.graph, uploaded_at=excluded.uploaded_at,
    map_revision=excluded.map_revision;

-- name: ReadCourseGraphMap :one
SELECT graph FROM course_graph_maps WHERE subject_id = $1;

-- name: CourseMapOutcomesOfRevision :many
-- The outcomes of a map revision from one source with their place in the
-- structure, so the Geek sheet's topics resolve to them: its own exam
-- outcomes, or the diagnostic ones when a unified map shares one set.
SELECT outcome.id, competency.id AS competency_id, constituent.id AS constituent_id,
       competency.name AS competency_name, constituent.name AS constituent_name, outcome.name AS outcome_name
FROM course_map_outcomes outcome
JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
JOIN course_map_competencies competency ON competency.id = constituent.competency_id
WHERE competency.revision = sqlc.arg(revision) AND competency.source_mode = sqlc.arg(source_mode)::text;

-- name: InsertCourseMapExamTask :exec
INSERT INTO course_map_tasks(id, outcome_id, mode, position, question, voice_instruction, reference_answer, criteria, created_at)
VALUES (sqlc.arg(id), sqlc.arg(outcome_id), 'exam', sqlc.arg(position), sqlc.arg(question), sqlc.arg(question),
        sqlc.arg(reference_answer), sqlc.arg(criteria), sqlc.arg(created_at));

-- name: UpdateCourseMapExamTaskRephrase :execrows
UPDATE course_map_tasks SET rephrase = sqlc.arg(rephrase)
WHERE id = sqlc.arg(id) AND mode = 'exam';

-- name: ClaimCourseGraph :one
-- A requested build takes its source as it is when it starts: the materials
-- (it waits while any of them is still being processed) or the imported map.
UPDATE course_graph_jobs j SET status='processing', fingerprint=i.fingerprint,
    attempts=j.attempts+CASE WHEN j.status='pending' THEN 1 ELSE 0 END,
    lease_token=sqlc.arg(token), lease_until=sqlc.arg(now)::bigint+60, updated_at=sqlc.arg(now)
FROM course_graph_job_inputs i
WHERE i.subject_id=j.subject_id AND i.source=j.source AND j.subject_id=(SELECT c.subject_id FROM course_graph_jobs c
    WHERE ((c.status='pending' AND c.available_at<=sqlc.arg(now))
       OR (c.status='processing' AND c.lease_until<=sqlc.arg(now)))
      AND EXISTS(SELECT 1 FROM course_graph_job_inputs r WHERE r.subject_id=c.subject_id AND r.source=c.source)
      AND EXISTS(SELECT 1 FROM subjects s WHERE s.id=c.subject_id)
    ORDER BY c.updated_at FOR UPDATE OF c SKIP LOCKED LIMIT 1)
RETURNING j.*;

-- name: RenewCourseGraph :execrows
UPDATE course_graph_jobs SET lease_until=sqlc.arg(now)::bigint+60, updated_at=sqlc.arg(now)
WHERE subject_id=sqlc.arg(subject_id) AND lease_token=sqlc.arg(token) AND status='processing';

-- name: ReleaseCourseGraph :execrows
UPDATE course_graph_jobs SET status='pending', attempts=GREATEST(attempts-1,0), lease_until=0,
    lease_token='', updated_at=sqlc.arg(now)
WHERE subject_id=sqlc.arg(subject_id) AND lease_token=sqlc.arg(token) AND status='processing';

-- name: FinishCourseGraph :one
WITH accepted AS (
    UPDATE course_graph_jobs j SET status='ready', lease_until=0, error_message='', updated_at=sqlc.arg(now)
    WHERE j.subject_id=sqlc.arg(subject_id) AND j.lease_token=sqlc.arg(token) AND j.status='processing'
      AND j.fingerprint=sqlc.arg(fingerprint)
      AND EXISTS(SELECT 1 FROM course_graph_job_inputs i WHERE i.subject_id=sqlc.arg(subject_id)
                 AND i.source=j.source AND i.fingerprint=sqlc.arg(fingerprint))
    RETURNING j.subject_id, j.source, j.fingerprint
), published AS (
-- A graph of a competency map keeps the revision of the map it was built from.
INSERT INTO course_graphs(subject_id,source,fingerprint,graph,updated_at,map_revision)
SELECT accepted.subject_id,accepted.source,accepted.fingerprint,sqlc.arg(graph)::jsonb,sqlc.arg(now)::bigint,
       CASE WHEN accepted.source='competency_map' THEN (SELECT m.map_revision FROM course_graph_maps m WHERE m.subject_id=accepted.subject_id) END
FROM accepted
ON CONFLICT(subject_id) DO UPDATE SET source=excluded.source,fingerprint=excluded.fingerprint,graph=excluded.graph,
    updated_at=excluded.updated_at,map_revision=excluded.map_revision
RETURNING subject_id
)
SELECT subject_id FROM published;

-- name: FailCourseGraph :exec
UPDATE course_graph_jobs SET status=CASE WHEN attempts>=3 THEN 'failed' ELSE 'pending' END,
    available_at=sqlc.arg(now)::bigint+30*attempts, lease_until=0, error_message=sqlc.arg(error_message),updated_at=sqlc.arg(now)
WHERE subject_id=sqlc.arg(subject_id) AND lease_token=sqlc.arg(token) AND status='processing';

-- name: ReadCourseGraph :one
SELECT s.id, coalesce(j.status,'not_requested')::text AS status,
       coalesce(j.attempts,0)::integer AS attempts, coalesce(j.error_message,'')::text AS error_message,
       coalesce(g.graph,'null'::jsonb)::jsonb AS graph,
       coalesce(g.updated_at,0)::bigint AS updated_at,
       coalesce(g.source,'')::text AS graph_source,
       coalesce(g.fingerprint,'')::text AS graph_fingerprint,
       g.map_revision AS graph_map_revision,
       -- Stale: the source the graph was built from changed since.
       (g.fingerprint IS NOT NULL AND g.fingerprint IS DISTINCT FROM
           CASE g.source WHEN 'competency_map' THEN m.fingerprint ELSE i.fingerprint END)::boolean AS stale,
       (i.subject_id IS NOT NULL)::boolean AS inputs_ready,
       coalesce(j.source,'')::text AS job_source,
       coalesce(m.fingerprint,'')::text AS map_fingerprint,
       coalesce(m.filename,'')::text AS map_filename, coalesce(m.sheet,'')::text AS map_sheet,
       m.map_revision AS map_revision
FROM subjects s LEFT JOIN course_graph_jobs j ON j.subject_id=s.id
LEFT JOIN course_graphs g ON g.subject_id=s.id
LEFT JOIN course_graph_inputs i ON i.subject_id=s.id
LEFT JOIN course_graph_maps m ON m.subject_id=s.id WHERE s.id=$1;

-- name: ReadPublishedCourseGraph :one
-- The graph Geek examines by. It stays published while its source changes
-- until the next build; a graph of materials goes with the last of them, a
-- graph of a competency map needs none.
SELECT g.graph
FROM course_graphs g JOIN subjects s ON s.id=g.subject_id
WHERE g.subject_id=$1 AND (g.source='competency_map' OR EXISTS(SELECT 1 FROM subject_materials m WHERE m.subject_id=g.subject_id));

-- name: ListPublishedCourseGraphs :many
-- Every graph Geek examines by, under the condition of ReadPublishedCourseGraph;
-- the newest first, so their questions are voiced first.
SELECT g.graph
FROM course_graphs g JOIN subjects s ON s.id=g.subject_id
WHERE g.source='competency_map' OR EXISTS(SELECT 1 FROM subject_materials m WHERE m.subject_id=g.subject_id)
ORDER BY g.updated_at DESC, g.subject_id;

-- name: ListPublishedCourseGraphsForAudio :many
SELECT g.subject_id, g.graph
FROM course_graphs g JOIN subjects s ON s.id=g.subject_id
WHERE g.source='competency_map' OR EXISTS(SELECT 1 FROM subject_materials m WHERE m.subject_id=g.subject_id)
ORDER BY g.updated_at DESC, g.subject_id;

-- name: ReplaceCurrentCourseGraphMap :execrows
-- Compare-and-swap the confirmed, published map. Re-evaluation after a
-- concurrent map update checks its fingerprint before changing any data.
UPDATE course_graph_maps m
SET filename=sqlc.arg(filename), sheet=sqlc.arg(sheet),
    fingerprint=sqlc.arg(fingerprint), graph=sqlc.arg(graph)::jsonb, uploaded_at=sqlc.arg(now)::bigint
WHERE m.subject_id=sqlc.arg(subject_id) AND m.fingerprint=sqlc.arg(expected_fingerprint)
  AND EXISTS (SELECT 1 FROM course_graphs g WHERE g.subject_id=m.subject_id
              AND g.source='competency_map' AND g.fingerprint=m.fingerprint)
  AND EXISTS (SELECT 1 FROM course_graph_jobs j WHERE j.subject_id=m.subject_id AND j.status='ready');

-- name: LockCourseGraphMapFingerprint :one
-- Serialize retries with map imports and exclusions (map row before job row).
SELECT fingerprint FROM course_graph_maps WHERE subject_id=$1 FOR UPDATE;

-- name: RetryFailedCourseGraphMapBuild :execrows
UPDATE course_graph_jobs SET status='pending', attempts=0, available_at=0,
    lease_until=0, lease_token='', error_message='', updated_at=sqlc.arg(now)::bigint
WHERE subject_id=sqlc.arg(subject_id) AND source='competency_map' AND status='failed'
  AND fingerprint=sqlc.arg(expected_fingerprint);

-- name: InsertCourseMapExamCompetency :exec
INSERT INTO course_map_competencies(id, name, revision, source_mode)
VALUES ($1, $2, $3, 'exam');
