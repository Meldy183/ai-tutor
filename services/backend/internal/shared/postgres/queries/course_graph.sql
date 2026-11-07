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
INSERT INTO course_graph_maps(subject_id, filename, sheet, fingerprint, graph, uploaded_at, learning_revision)
SELECT sqlc.arg(subject_id), sqlc.arg(filename), sqlc.arg(sheet), sqlc.arg(fingerprint), sqlc.arg(graph)::jsonb, sqlc.arg(now)::bigint,
       NULLIF(sqlc.arg(graph)::jsonb->>'learning_revision','')::bigint
WHERE EXISTS (SELECT 1 FROM subjects s WHERE s.id=sqlc.arg(subject_id))
ON CONFLICT(subject_id) DO UPDATE SET filename=excluded.filename, sheet=excluded.sheet,
    fingerprint=excluded.fingerprint, graph=excluded.graph, uploaded_at=excluded.uploaded_at,
    learning_revision=excluded.learning_revision;

-- name: ReadCourseGraphMap :one
SELECT graph FROM course_graph_maps WHERE subject_id = $1;

-- name: ReadCourseGraphMapProjection :one
SELECT CASE WHEN m.graph->>'canonical'='true'
            THEN course_graph_map_projection(m.subject_id,COALESCE(m.learning_revision,(m.graph->>'learning_revision')::bigint),m.graph,false)
            ELSE m.graph END::jsonb AS graph
FROM course_graph_maps m WHERE m.subject_id=$1;

-- name: CourseGraphInsertExamTopic :exec
INSERT INTO course_graph_exam_topics(map_revision,exam_revision,outcome_id,topic_order,role)
VALUES($1,$2,$3,$4,$5);

-- name: CourseGraphCreateExamVersion :one
INSERT INTO course_graph_exam_versions(map_revision,created_at) VALUES($1,$2) RETURNING id;

-- name: CourseGraphInsertExamTask :exec
INSERT INTO course_graph_exam_tasks(map_revision,exam_revision,outcome_id,position,question,content,assessment_guidance)
VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: CourseGraphInsertPrerequisite :exec
INSERT INTO learning_graph_prerequisites(map_revision,source_outcome_id,target_outcome_id,reason)
VALUES($1,$2,$3,$4);

-- name: CourseGraphDeleteExamTopics :exec
DELETE FROM course_graph_exam_topics WHERE exam_revision=$1;

-- name: CourseGraphDeletePrerequisites :exec
DELETE FROM learning_graph_prerequisites WHERE map_revision=$1;

-- name: CourseGraphUpdateExamTaskRephrase :execrows
UPDATE course_graph_exam_tasks SET rephrase=$4
WHERE exam_revision=$1 AND outcome_id=$2 AND position=$3;

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
INSERT INTO course_graphs(subject_id,source,fingerprint,graph,updated_at,map_revision)
SELECT accepted.subject_id,accepted.source,accepted.fingerprint,sqlc.arg(graph)::jsonb-'learning_revision',sqlc.arg(now)::bigint,
       NULLIF(sqlc.arg(graph)::jsonb->>'learning_revision','')::bigint FROM accepted
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
       COALESCE(m.learning_revision,(m.graph->>'learning_revision')::bigint) AS map_learning_revision
FROM subjects s LEFT JOIN course_graph_jobs j ON j.subject_id=s.id
LEFT JOIN course_graphs g ON g.subject_id=s.id
LEFT JOIN course_graph_inputs i ON i.subject_id=s.id
LEFT JOIN course_graph_maps m ON m.subject_id=s.id WHERE s.id=$1;

-- name: ReadPublishedCourseGraph :one
-- The graph Geek examines by. It stays published while its source changes
-- until the next build; a graph of materials goes with the last of them, a
-- graph of a competency map needs none.
SELECT CASE WHEN g.source='competency_map' AND g.graph->>'canonical'='true'
            THEN course_graph_map_projection(g.subject_id,g.map_revision,g.graph,true)
            ELSE g.graph END::jsonb AS graph
FROM course_graphs g JOIN subjects s ON s.id=g.subject_id
WHERE g.subject_id=$1 AND (g.source='competency_map' OR EXISTS(SELECT 1 FROM subject_materials m WHERE m.subject_id=g.subject_id));

-- name: ListPublishedCourseGraphs :many
-- Every graph Geek examines by, under the condition of ReadPublishedCourseGraph;
-- the newest first, so their questions are voiced first.
SELECT CASE WHEN g.source='competency_map' AND g.graph->>'canonical'='true'
            THEN course_graph_map_projection(g.subject_id,g.map_revision,g.graph,true)
            ELSE g.graph END::jsonb AS graph
FROM course_graphs g JOIN subjects s ON s.id=g.subject_id
WHERE g.source='competency_map' OR EXISTS(SELECT 1 FROM subject_materials m WHERE m.subject_id=g.subject_id)
ORDER BY g.updated_at DESC, g.subject_id;

-- name: ListPublishedCourseGraphsForAudio :many
SELECT g.subject_id,
       CASE WHEN g.source='competency_map' AND g.graph->>'canonical'='true'
            THEN course_graph_map_projection(g.subject_id,g.map_revision,g.graph,true)
            ELSE g.graph END::jsonb AS graph
FROM course_graphs g JOIN subjects s ON s.id=g.subject_id
WHERE g.source='competency_map' OR EXISTS(SELECT 1 FROM subject_materials m WHERE m.subject_id=g.subject_id)
ORDER BY g.updated_at DESC, g.subject_id;

-- name: ReplaceCurrentCourseGraphMap :execrows
-- Compare-and-swap the confirmed, published map. Re-evaluation after a
-- concurrent map update checks its fingerprint before changing any data.
UPDATE course_graph_maps m
SET filename=sqlc.arg(filename), sheet=sqlc.arg(sheet),
    fingerprint=sqlc.arg(fingerprint), graph=sqlc.arg(graph)::jsonb, uploaded_at=sqlc.arg(now)::bigint,
    learning_revision=CASE WHEN EXISTS (SELECT 1 FROM learning_subject_state state
                                        WHERE state.subject_id=m.subject_id
                                          AND state.active_revision=m.learning_revision)
                           THEN m.learning_revision ELSE NULL END
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
