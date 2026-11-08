-- name: LearningListLearningOutcomes :many
SELECT competency.id AS competency_id, competency.name AS competency_name,
	   constituent.id AS constituent_id, competency.revision AS map_revision,
       outcome.id AS outcome_id, outcome.name AS outcome_name,
       COALESCE(taxonomy.code, '')::text AS taxonomy_code,
       COALESCE(outcome.importance, 0)::smallint AS importance,
       COALESCE(outcome.educational_content, '')::text AS educational_content,
       outcome.theory_minutes, outcome.practice_minutes
FROM course_map_outcomes AS outcome
JOIN course_map_constituents AS constituent ON constituent.id = outcome.constituent_id
JOIN course_map_competencies AS competency ON competency.id = constituent.competency_id
LEFT JOIN learning_taxonomies AS taxonomy ON taxonomy.id = outcome.taxonomy_id
WHERE competency.id = ANY(sqlc.arg(competency_ids)::text[])
ORDER BY competency.id,
         (SELECT MIN(source_row.source_row_index) FROM learning_outcome_source_rows AS source_row WHERE source_row.outcome_id = outcome.id) NULLS LAST,
         outcome.name, outcome.id;

-- name: LearningFindLearningPlanByDiagnostic :one
SELECT plan_data FROM learning_plans WHERE owner_id = $1 AND diagnostic_id = $2;

-- name: LearningReadLearningPlan :one
SELECT plan_data FROM learning_plans WHERE owner_id = $1 AND id = $2;

-- name: LearningLockLearningPlan :one
SELECT plan_data FROM learning_plans WHERE owner_id = $1 AND id = $2 FOR UPDATE;

-- name: LearningInsertLearningPlan :execrows
INSERT INTO learning_plans(id, owner_id, diagnostic_id, plan_data) VALUES ($1, $2, $3, $4)
ON CONFLICT (owner_id, diagnostic_id) DO NOTHING;

-- name: LearningSaveLearningPlan :exec
UPDATE learning_plans SET plan_data = $3 WHERE owner_id = $1 AND id = $2;

-- name: LearningNextLearningSequence :one
SELECT (COALESCE(MAX(sequence), 0) + 1)::bigint FROM learning_plan_messages WHERE plan_id = $1;

-- name: LearningInsertLearningMessage :exec
INSERT INTO learning_plan_messages(plan_id, sequence, item_index, role, kind, body, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: LearningListLearningMessages :many
SELECT sequence, item_index, role, kind, body, created_at
FROM learning_plan_messages WHERE plan_id = $1 AND item_index = $2 ORDER BY sequence;
