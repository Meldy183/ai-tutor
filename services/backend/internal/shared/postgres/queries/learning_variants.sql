-- name: LearningLockSubjectForVariant :one
SELECT subject.id, subject.name, COALESCE(state.active_revision, 0)::bigint AS active_revision
FROM subjects subject LEFT JOIN learning_subject_state state ON state.subject_id = subject.id
WHERE subject.id = $1 FOR SHARE OF subject;

-- name: LearningFindVariantByRequestKey :one
SELECT id, subject_id FROM learning_variants WHERE user_id = $1 AND create_request_key = $2;

-- name: LearningFindSubjectForVariantList :one
SELECT id FROM subjects WHERE id = $1;

-- name: LearningReadVariantCandidates :many
SELECT sqlc.arg(revision)::bigint AS revision,
       COALESCE((SELECT min(osr.source_row_index) FROM course_map_outcomes co
                 LEFT JOIN learning_outcome_source_rows osr ON osr.outcome_id = co.id
                 WHERE co.constituent_id IN (
                     SELECT c2.id FROM course_map_constituents c2 WHERE c2.competency_id = competency.id
                 )), 2147483647)::bigint AS competency_source_order,
       COALESCE((SELECT min(source_row_index) FROM learning_outcome_source_rows WHERE outcome_id = outcome.id), 2147483647)::bigint AS outcome_source_order,
       task.id AS task_id,
       jsonb_build_object(
           'id', task.id, 'question', task.question, 'options', task.options,
           'voice_instruction', task.voice_instruction, 'audio_asset_id', task.audio_asset_id,
           'reference_answer', task.reference_answer,
           'criteria', task.criteria, 'origin', task.origin, 'created_at', task.created_at,
           'source_row_index', task.source_row_index, 'source_column_index', task.source_column_index,
           'competency', jsonb_build_object('id', competency.id, 'name', competency.name),
           'constituent', jsonb_build_object(
               'id', constituent.id, 'name', constituent.name, 'topic_level_code', topic.code,
               'sections', COALESCE((SELECT jsonb_agg(jsonb_build_object(
                   'code', section.code, 'title', section.title,
                   'curriculum_competencies', COALESCE((SELECT jsonb_agg(curriculum.code ORDER BY curriculum.code)
                       FROM learning_constituent_section_competencies mapped
                       JOIN learning_curriculum_competencies curriculum ON curriculum.id = mapped.curriculum_competency_id
                       WHERE mapped.constituent_section_id = link.id), '[]'::jsonb)
               ) ORDER BY section.code)
               FROM learning_constituent_sections link JOIN learning_curriculum_sections section ON section.id = link.section_id
               WHERE link.constituent_id = constituent.id), '[]'::jsonb)
           ),
           'outcome', jsonb_build_object(
               'id', outcome.id, 'name', outcome.name, 'include_in_test', outcome.include_in_test,
               'taxonomy_code', taxonomy.code, 'ald_level_code', ald.code,
               'importance', outcome.importance, 'educational_content', outcome.educational_content
           )
       ) AS profile_json
FROM course_map_competencies competency
LEFT JOIN course_map_constituents constituent ON constituent.competency_id = competency.id
LEFT JOIN learning_topic_levels topic ON topic.id = constituent.topic_level_id
LEFT JOIN course_map_outcomes outcome ON outcome.constituent_id = constituent.id
LEFT JOIN learning_taxonomies taxonomy ON taxonomy.id = outcome.taxonomy_id
LEFT JOIN learning_ald_levels ald ON ald.id = outcome.ald_level_id
LEFT JOIN course_map_tasks task ON task.outcome_id = outcome.id AND task.mode = 'diagnostic'
WHERE competency.revision = sqlc.arg(revision)::bigint AND competency.source_mode = 'diagnostic'
ORDER BY competency_source_order, competency.name, competency.id,
         outcome_source_order, outcome.name, outcome.id,
         CASE WHEN task.source_row_index IS NULL THEN 1 ELSE 0 END,
         task.source_row_index, task.source_column_index, task.created_at, task.id;

-- name: LearningInsertVariant :one
INSERT INTO learning_variants(id, user_id, create_request_key, map_revision, algorithm_version,
                     included_competency_count, skipped_competencies, created_at, subject_id, subject_name_snapshot)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (user_id, create_request_key) DO NOTHING
RETURNING id;

-- name: LearningInsertVariantTask :exec
INSERT INTO learning_variant_tasks(id, variant_id, live_task_id, source_task_id_snapshot, audio_asset_id,
                          competency_position, slot, role, task_snapshot, profile_snapshot)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: LearningReadVariantHeaderByOwner :one
SELECT id, user_id, map_revision, algorithm_version, included_competency_count,
       skipped_competencies, created_at, subject_id, subject_name_snapshot
FROM learning_variants WHERE id = $1 AND user_id = $2;

-- name: LearningReadVariantTasks :many
SELECT id, competency_position, slot, role, source_task_id_snapshot, audio_asset_id, task_snapshot, profile_snapshot
FROM learning_variant_tasks WHERE variant_id = $1 ORDER BY competency_position, slot;

-- name: LearningReadVariantTaskByOwner :one
SELECT vt.id, vt.role, vt.audio_asset_id, vt.task_snapshot, vt.profile_snapshot
FROM learning_variant_tasks vt JOIN learning_variants v ON v.id = vt.variant_id
WHERE v.user_id = $1 AND v.id = $2 AND vt.id = $3;

-- name: LearningListVariantsByOwner :many
SELECT id, map_revision, algorithm_version, included_competency_count,
       skipped_competencies, created_at, subject_id, subject_name_snapshot,
       (SELECT count(*) FROM learning_variant_tasks vt WHERE vt.variant_id = learning_variants.id) AS task_count
FROM learning_variants
WHERE user_id = $1 AND subject_id = $2
  AND (sqlc.narg(cursor_created_at)::bigint IS NULL
       OR (created_at, id) < (sqlc.narg(cursor_created_at)::bigint, sqlc.narg(cursor_id)::text))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size)::integer;
