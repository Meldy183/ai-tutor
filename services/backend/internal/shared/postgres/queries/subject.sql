-- name: CreateSubject :one
INSERT INTO subjects(id, name, created_at)
VALUES ($1, $2, $3)
RETURNING id, name;

-- name: SubjectAudioPreparationState :one
SELECT EXISTS (SELECT 1 FROM subjects WHERE id = sqlc.arg(subject_id)::text) AS subject_exists,
       EXISTS (
           SELECT 1 FROM course_graphs graph
           WHERE graph.subject_id = sqlc.arg(subject_id)::text
             AND (graph.source = 'competency_map'
                  OR EXISTS (SELECT 1 FROM subject_materials material WHERE material.subject_id = graph.subject_id))
       ) AS graph_published;

-- name: ListSubjects :many
SELECT subject.id, subject.name,
       EXISTS (
           SELECT 1 FROM course_graphs graph
           WHERE graph.subject_id = subject.id
             AND (graph.source = 'competency_map'
                  OR EXISTS (SELECT 1 FROM subject_materials material WHERE material.subject_id = subject.id))
       ) AS exam_ready,
       EXISTS (
           SELECT 1
           FROM learning_subject_state state
           JOIN course_map_competencies competency ON competency.revision = state.active_revision
           JOIN course_map_constituents constituent ON constituent.competency_id = competency.id
           JOIN course_map_outcomes outcome ON outcome.constituent_id = constituent.id
           JOIN learning_taxonomies taxonomy ON taxonomy.id = outcome.taxonomy_id
           JOIN course_map_tasks task ON task.outcome_id = outcome.id AND task.mode = 'diagnostic'
           WHERE state.subject_id = subject.id
             AND outcome.include_in_test IS TRUE
             AND taxonomy.code IN ('knowledge', 'understanding', 'application', 'analysis')
             AND outcome.importance BETWEEN 1 AND 5
             AND task.origin IN ('authored', 'ai_generated')
             AND task.question ~ '[^[:space:]]'
             AND char_length(task.question) <= 5000
             AND task.voice_instruction ~ '[^[:space:]]'
             AND char_length(task.voice_instruction) <= 500
             AND task.reference_answer ~ '[^[:space:]]'
             AND char_length(task.reference_answer) <= 2000
             AND CASE
                   WHEN jsonb_typeof(task.options) = 'array' THEN
                     jsonb_array_length(task.options) <= 12
                     AND NOT EXISTS (
                       SELECT 1
                       FROM jsonb_array_elements_text(task.options) AS options(option_text)
                       WHERE options.option_text !~ '[^[:space:]]'
                          OR char_length(options.option_text) > 500
                     )
                   ELSE false
                 END
           GROUP BY constituent.id
           HAVING count(DISTINCT outcome.id) >= 3
       ) AS learning_ready,
       EXISTS (SELECT 1 FROM subject_materials m WHERE m.subject_id=subject.id) AS has_materials,
       EXISTS (SELECT 1 FROM course_graph_maps m WHERE m.subject_id=subject.id) AS has_map,
       coalesce(job.source,'')::text AS job_source,
       coalesce(job.status,'')::text AS job_status,
       coalesce(job.fingerprint IS NOT NULL AND job.fingerprint IS NOT DISTINCT FROM
           (SELECT input.fingerprint FROM course_graph_job_inputs input
            WHERE input.subject_id=subject.id AND input.source=job.source),false)::boolean AS job_fingerprint_current,
       coalesce(graph.subject_id IS NOT NULL,false)::boolean AS graph_exists,
       coalesce(graph.source,'')::text AS graph_source,
       coalesce(graph.fingerprint IS NOT NULL AND graph.fingerprint IS NOT DISTINCT FROM
           CASE graph.source
             WHEN 'competency_map' THEN (SELECT map.fingerprint FROM course_graph_maps map WHERE map.subject_id=subject.id)
             ELSE (SELECT input.fingerprint FROM course_graph_inputs input WHERE input.subject_id=subject.id)
           END,false)::boolean AS graph_fingerprint_current,
       coalesce(graph.fingerprint IS NOT NULL AND graph.fingerprint IS DISTINCT FROM
           CASE graph.source
             WHEN 'competency_map' THEN (SELECT map.fingerprint FROM course_graph_maps map WHERE map.subject_id=subject.id)
             ELSE (SELECT input.fingerprint FROM course_graph_inputs input WHERE input.subject_id=subject.id)
           END,false)::boolean AS graph_stale,
       EXISTS (SELECT 1 FROM subject_materials m
               LEFT JOIN material_ocr_jobs o ON o.material_id=m.id
               LEFT JOIN material_cleaning_jobs c ON c.material_id=m.id
               WHERE m.subject_id=subject.id AND (o.status='processing' OR c.status='processing')) AS materials_processing,
       EXISTS (SELECT 1 FROM subject_materials m
               LEFT JOIN material_ocr_jobs o ON o.material_id=m.id
               LEFT JOIN material_cleaning_jobs c ON c.material_id=m.id
               WHERE m.subject_id=subject.id AND (o.status='pending' OR c.status='pending')) AS materials_queued,
       EXISTS (SELECT 1 FROM subject_materials m
               LEFT JOIN material_ocr_jobs o ON o.material_id=m.id
               LEFT JOIN material_cleaning_jobs c ON c.material_id=m.id
               WHERE m.subject_id=subject.id AND (m.status='failed' OR o.status='failed' OR c.status='failed')) AS materials_failed,
       EXISTS (SELECT 1 FROM subject_materials m
               LEFT JOIN material_kinds kind ON kind.code=m.kind
               WHERE m.subject_id=subject.id AND (m.week IS NULL OR m.kind IS NULL OR kind.code IS NULL)) AS materials_need_metadata,
       EXISTS (SELECT 1 FROM learning_subject_state state
               WHERE state.subject_id=subject.id AND state.active_revision > 0) AS learning_map_active
FROM subjects subject
LEFT JOIN course_graph_jobs job ON job.subject_id=subject.id
LEFT JOIN course_graphs graph ON graph.subject_id=subject.id
ORDER BY lower(subject.name), subject.id;
