-- name: LearningLockSubjectForImport :one
SELECT id FROM subjects WHERE id = $1 FOR UPDATE;

-- name: LearningGetSubjectActiveRevision :one
SELECT COALESCE(state.active_revision, 0)::bigint AS revision
FROM subjects subject LEFT JOIN learning_subject_state state ON state.subject_id = subject.id
WHERE subject.id = $1;

-- name: LearningLockCompetencyMapRevision :one
SELECT revision FROM learning_map_state WHERE singleton = true FOR UPDATE;

-- name: LearningLockSubjectForTaskgen :one
SELECT state.active_revision FROM subjects subject JOIN learning_subject_state state ON state.subject_id = subject.id WHERE subject.id = $1 FOR SHARE OF subject;

-- name: LearningDeleteCompetencyMapImportsForSubject :exec
DELETE FROM learning_map_imports WHERE subject_id = $1;

-- name: LearningInsertCompetencyMapImport :exec
INSERT INTO learning_map_imports(
    revision, subject_id, imported_at, imported_by, competency_count, constituent_count,
    outcome_count, task_count, source_format, source_headers, unparsed_task_cell_count
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: LearningUpdateSubjectActiveRevision :exec
INSERT INTO learning_subject_state(subject_id, active_revision) VALUES ($1, $2) ON CONFLICT (subject_id) DO UPDATE SET active_revision = EXCLUDED.active_revision;

-- name: LearningInsertCompetency :exec
INSERT INTO learning_competencies(id, name, revision) VALUES ($1, $2, $3);

-- name: LearningInsertConstituent :exec
INSERT INTO learning_constituents(id, competency_id, name, topic_level_id)
VALUES ($1, $2, $3, NULLIF(sqlc.arg(topic_level_id)::text, ''));

-- name: LearningInsertOutcome :exec
INSERT INTO learning_outcomes(
    id, constituent_id, name, include_in_test, taxonomy_id,
    ald_level_id, importance, educational_content, theory_minutes, practice_minutes
) VALUES (
    $1, $2, $3, $4,
    (SELECT id FROM learning_taxonomies WHERE code = NULLIF(sqlc.arg(taxonomy_code)::text, '')),
    (SELECT id FROM learning_ald_levels WHERE code = NULLIF(sqlc.arg(ald_level_code)::text, '')),
    NULLIF(sqlc.arg(importance)::smallint, 0), NULLIF(sqlc.arg(educational_content)::text, ''),
    sqlc.narg(theory_minutes)::smallint, sqlc.narg(practice_minutes)::smallint
);

-- name: LearningInsertCurriculumSection :exec
INSERT INTO learning_curriculum_sections(id, revision, code, title) VALUES ($1, $2, $3, $4);

-- name: LearningInsertCurriculumCompetency :exec
INSERT INTO learning_curriculum_competencies(id, revision, code) VALUES ($1, $2, $3);

-- name: LearningInsertConstituentSection :exec
INSERT INTO learning_constituent_sections(id, revision, constituent_id, section_id)
VALUES ($1, $2, $3, $4);

-- name: LearningInsertConstituentSectionCompetency :exec
INSERT INTO learning_constituent_section_competencies(revision, constituent_section_id, curriculum_competency_id)
VALUES ($1, $2, $3);

-- name: LearningInsertTask :exec
INSERT INTO learning_tasks(
    id, outcome_id, question, criteria, source_row, source_column, options,
    voice_instruction, reference_answer, origin, source_revision,
    source_row_index, source_column_index, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, 'authored',
    NULLIF(sqlc.arg(source_revision)::bigint, 0),
    NULLIF(sqlc.arg(source_row_index)::integer, 0),
    NULLIF(sqlc.arg(source_column_index)::integer, 0),
    sqlc.arg(created_at)::bigint
);

-- name: LearningInsertCompetencyMapSourceRow :exec
INSERT INTO learning_map_source_rows(revision, row_index, source_line, cells)
VALUES ($1, $2, $3, $4);

-- name: LearningInsertOutcomeSourceRow :exec
INSERT INTO learning_outcome_source_rows(outcome_id, source_revision, source_row_index)
VALUES ($1, $2, $3);

-- name: LearningUpdateCompetencyMapRevision :exec
UPDATE learning_map_state SET revision = $1 WHERE singleton = true;

-- name: LearningReadSubjectCompetencyMap :many
SELECT COALESCE(state.active_revision, 0)::bigint AS revision, import.imported_at,
       competency.id AS competency_id, competency.name AS competency_name,
       constituent.id AS constituent_id, constituent.name AS constituent_name,
       topic.code AS topic_level_code,
       COALESCE(curriculum_profile.curriculum_sections, '[]')::text AS curriculum_sections,
       outcome.id AS outcome_id, outcome.name AS outcome_name,
       outcome.include_in_test, taxonomy.code AS taxonomy_code,
       ald.code AS ald_level_code, outcome.importance, outcome.educational_content,
       task.id AS task_id, task.question, task.options, task.voice_instruction, task.origin
FROM subjects AS subject
LEFT JOIN learning_subject_state state ON state.subject_id = subject.id
LEFT JOIN learning_map_imports AS import ON import.revision = state.active_revision
LEFT JOIN learning_competencies AS competency ON competency.revision = state.active_revision
LEFT JOIN learning_constituents AS constituent ON constituent.competency_id = competency.id
LEFT JOIN learning_topic_levels AS topic ON topic.id = constituent.topic_level_id
LEFT JOIN learning_constituent_curriculum_profiles AS curriculum_profile ON curriculum_profile.constituent_id = constituent.id
LEFT JOIN learning_outcomes AS outcome ON outcome.constituent_id = constituent.id
LEFT JOIN learning_taxonomies AS taxonomy ON taxonomy.id = outcome.taxonomy_id
LEFT JOIN learning_ald_levels AS ald ON ald.id = outcome.ald_level_id
LEFT JOIN learning_tasks AS task ON task.outcome_id = outcome.id
WHERE subject.id = $1
ORDER BY competency.name, competency.id, constituent.name, constituent.id,
         outcome.name, outcome.id, task.created_at, task.id;

-- name: LearningGetTaskProfile :one
SELECT
    task.id AS task_id, task.question, task.options, task.voice_instruction, task.audio_asset_id,
    task.reference_answer, task.criteria, task.origin, task.created_at,
    outcome.id AS outcome_id, outcome.name AS outcome_name,
    outcome.include_in_test, taxonomy.code AS taxonomy_code,
    ald.code AS ald_level_code, outcome.importance, outcome.educational_content,
    constituent.id AS constituent_id, constituent.name AS constituent_name,
    topic.code AS topic_level_code,
    competency.id AS competency_id, competency.name AS competency_name,
    COALESCE(curriculum_profile.curriculum_sections, '[]')::text AS curriculum_sections
FROM learning_tasks AS task
JOIN learning_outcomes AS outcome ON outcome.id = task.outcome_id
JOIN learning_constituents AS constituent ON constituent.id = outcome.constituent_id
JOIN learning_constituent_curriculum_profiles AS curriculum_profile ON curriculum_profile.constituent_id = constituent.id
JOIN learning_competencies AS competency ON competency.id = constituent.competency_id
JOIN subjects AS subject ON subject.id = sqlc.arg(subject_id)::text
JOIN learning_subject_state state ON state.subject_id = subject.id AND state.active_revision = competency.revision
LEFT JOIN learning_taxonomies AS taxonomy ON taxonomy.id = outcome.taxonomy_id
LEFT JOIN learning_ald_levels AS ald ON ald.id = outcome.ald_level_id
LEFT JOIN learning_topic_levels AS topic ON topic.id = constituent.topic_level_id
WHERE task.id = sqlc.arg(task_id)::text;

-- name: LearningSearchTaskProfiles :many
SELECT task.id, task.question, task.options, task.voice_instruction,
       task.origin, outcome.id AS outcome_id, outcome.name AS outcome_name,
       competency.name AS competency_name, constituent.name AS constituent_name,
       taxonomy.code AS taxonomy_code, ald.code AS ald_level_code,
       topic.code AS topic_level_code, outcome.importance, outcome.include_in_test
FROM learning_tasks AS task
JOIN learning_outcomes AS outcome ON outcome.id = task.outcome_id
JOIN learning_constituents AS constituent ON constituent.id = outcome.constituent_id
JOIN learning_competencies AS competency ON competency.id = constituent.competency_id
JOIN subjects AS subject ON subject.id = sqlc.arg(subject_id)::text
JOIN learning_subject_state state ON state.subject_id = subject.id AND state.active_revision = competency.revision
LEFT JOIN learning_taxonomies AS taxonomy ON taxonomy.id = outcome.taxonomy_id
LEFT JOIN learning_ald_levels AS ald ON ald.id = outcome.ald_level_id
LEFT JOIN learning_topic_levels AS topic ON topic.id = constituent.topic_level_id
WHERE (sqlc.arg(outcome_id)::text = '' OR outcome.id = sqlc.arg(outcome_id)::text)
  AND (sqlc.arg(competency_id)::text = '' OR competency.id = sqlc.arg(competency_id)::text)
  AND (sqlc.arg(constituent_id)::text = '' OR constituent.id = sqlc.arg(constituent_id)::text)
  AND (sqlc.arg(taxonomy_code)::text = '' OR taxonomy.code = sqlc.arg(taxonomy_code)::text)
  AND (sqlc.arg(ald_level_code)::text = '' OR ald.code = sqlc.arg(ald_level_code)::text)
  AND (sqlc.arg(topic_level_code)::text = '' OR topic.code = sqlc.arg(topic_level_code)::text)
  AND (sqlc.arg(importance)::smallint = 0 OR outcome.importance = sqlc.arg(importance)::smallint)
  AND (sqlc.arg(importance_min)::smallint = 0 OR outcome.importance >= sqlc.arg(importance_min)::smallint)
  AND (sqlc.arg(importance_max)::smallint = 0 OR outcome.importance <= sqlc.arg(importance_max)::smallint)
  AND (sqlc.arg(include_in_test)::integer < 0 OR outcome.include_in_test = (sqlc.arg(include_in_test)::integer = 1))
  AND (sqlc.arg(origin)::text = '' OR task.origin = sqlc.arg(origin)::text)
  AND ((sqlc.arg(section_code)::text = '' AND sqlc.arg(curriculum_competency_code)::text = '') OR EXISTS (
      SELECT 1 FROM learning_constituent_sections AS link
      JOIN learning_curriculum_sections AS section ON section.id = link.section_id
      LEFT JOIN learning_constituent_section_competencies AS mapped ON mapped.constituent_section_id = link.id
      LEFT JOIN learning_curriculum_competencies AS curriculum ON curriculum.id = mapped.curriculum_competency_id
      WHERE link.constituent_id = constituent.id
        AND (sqlc.arg(section_code)::text = '' OR section.code = sqlc.arg(section_code)::text)
        AND (sqlc.arg(curriculum_competency_code)::text = '' OR curriculum.code = sqlc.arg(curriculum_competency_code)::text)
  ))
ORDER BY task.created_at, task.id
LIMIT sqlc.arg(task_limit)::integer;

-- name: LearningInsertGenerationRun :execrows
INSERT INTO learning_generation_runs(id, outcome_id, map_revision, model, prompt_version, request_key, requested_profile, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (outcome_id, request_key) DO NOTHING;

-- name: LearningGetGeneratedTaskByKey :one
SELECT task.id, task.outcome_id, task.question, task.options, task.voice_instruction,
       task.reference_answer, task.criteria, task.origin
FROM learning_generation_runs AS run
JOIN learning_tasks AS task ON task.generation_run_id = run.id
JOIN learning_outcomes AS outcome ON outcome.id = run.outcome_id
JOIN learning_constituents AS constituent ON constituent.id = outcome.constituent_id
JOIN learning_competencies AS competency ON competency.id = constituent.competency_id
JOIN subjects AS subject ON subject.id = sqlc.arg(subject_id)::text
JOIN learning_subject_state state ON state.subject_id = subject.id AND state.active_revision = competency.revision
WHERE run.outcome_id = sqlc.arg(outcome_id)::text AND run.request_key = sqlc.arg(request_key)::text;

-- name: LearningInsertGenerationRunExample :exec
INSERT INTO learning_generation_run_examples(generation_run_id, task_id) VALUES ($1, $2);

-- name: LearningInsertGenerationRunChunk :exec
INSERT INTO learning_generation_run_chunks(generation_run_id, chunk_id) VALUES ($1, $2);

-- name: LearningInsertGeneratedTask :exec
INSERT INTO learning_tasks(id, outcome_id, question, options, voice_instruction, reference_answer, criteria,
                  origin, generation_run_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'ai_generated', $8, $9);

-- name: LearningGetOutcomeForGeneration :one
SELECT state.active_revision::bigint AS revision, outcome.id, outcome.name, outcome.include_in_test, taxonomy.code AS taxonomy_code,
       ald.code AS ald_level_code, outcome.importance, outcome.educational_content,
       constituent.name AS constituent_name, topic.code AS topic_level_code,
       competency.name AS competency_name,
       COALESCE(curriculum_profile.curriculum_sections, '[]')::text AS curriculum_sections
FROM learning_outcomes AS outcome
JOIN learning_constituents AS constituent ON constituent.id = outcome.constituent_id
JOIN learning_constituent_curriculum_profiles AS curriculum_profile ON curriculum_profile.constituent_id = constituent.id
JOIN learning_competencies AS competency ON competency.id = constituent.competency_id
JOIN subjects AS subject ON subject.id = sqlc.arg(subject_id)::text
JOIN learning_subject_state state ON state.subject_id = subject.id AND state.active_revision = competency.revision
LEFT JOIN learning_taxonomies AS taxonomy ON taxonomy.id = outcome.taxonomy_id
LEFT JOIN learning_ald_levels AS ald ON ald.id = outcome.ald_level_id
LEFT JOIN learning_topic_levels AS topic ON topic.id = constituent.topic_level_id
WHERE outcome.id = sqlc.arg(outcome_id)::text;

-- name: LearningListGenerationExamples :many
SELECT task.id, task.question, task.options, task.voice_instruction, task.reference_answer, task.criteria
FROM learning_tasks AS task
WHERE task.outcome_id = $1
ORDER BY CASE WHEN task.origin = 'authored' THEN 0 ELSE 1 END, task.created_at DESC, task.id
LIMIT $2;

-- name: LearningInsertMaterialChunk :exec
INSERT INTO learning_material_chunks(id, subject_id, material_name, ordinal, content, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: LearningDeleteMaterialLinksBySubjectAndName :exec
DELETE FROM learning_material_chunk_outcomes
WHERE chunk_id IN (SELECT id FROM learning_material_chunks WHERE subject_id = $1 AND material_name = $2);

-- name: LearningLinkMaterialChunkToOutcome :exec
INSERT INTO learning_material_chunk_outcomes(chunk_id, outcome_id) VALUES ($1, $2);

-- name: LearningCountExistingOutcomesForSubject :one
SELECT count(*)::integer FROM learning_outcomes outcome
JOIN learning_constituents constituent ON constituent.id = outcome.constituent_id
JOIN learning_competencies competency ON competency.id = constituent.competency_id
JOIN subjects subject ON subject.id = sqlc.arg(subject_id)::text
JOIN learning_subject_state state ON state.subject_id = subject.id AND state.active_revision = competency.revision
WHERE outcome.id = ANY(sqlc.arg(outcome_ids)::text[]);

-- name: LearningSearchMaterialChunks :many
SELECT chunk.id, chunk.material_name, chunk.ordinal, chunk.content,
       ts_rank_cd(chunk.search_vector, to_tsquery('russian', replace(plainto_tsquery('russian', sqlc.arg(search_query)::text)::text, ' & ', ' | '))) AS relevance
FROM learning_material_chunks AS chunk
JOIN learning_material_chunk_outcomes AS link ON link.chunk_id = chunk.id
WHERE link.outcome_id = sqlc.arg(outcome_id)::text AND chunk.subject_id = sqlc.arg(subject_id)::text
  AND chunk.search_vector @@ to_tsquery('russian', replace(plainto_tsquery('russian', sqlc.arg(search_query)::text)::text, ' & ', ' | '))
ORDER BY relevance DESC, chunk.id
LIMIT sqlc.arg(chunk_limit)::integer;
