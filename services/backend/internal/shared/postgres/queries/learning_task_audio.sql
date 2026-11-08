-- name: LearningEnqueueTaskAudio :exec
INSERT INTO learning_audio_assets(id, instruction, object_key)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO NOTHING;

-- name: LearningGetTaskAudio :one
SELECT id, instruction, object_key, bucket, storage_uri, audio_url, status, attempts
FROM learning_audio_assets WHERE id = $1;

-- name: LearningAudioPreparationForSubject :many
-- One statement snapshot keeps the active revision, expected tasks, and asset statuses coherent.
SELECT COALESCE(state.active_revision, 0)::bigint AS revision,
       task.id AS task_id, task.voice_instruction, task.audio_asset_id,
       asset.status, asset.attempts, asset.next_attempt_at, asset.lease_until
FROM subjects subject
LEFT JOIN learning_subject_state state ON state.subject_id = subject.id
LEFT JOIN course_map_competencies competency ON competency.revision = state.active_revision
LEFT JOIN course_map_constituents constituent ON constituent.competency_id = competency.id
LEFT JOIN course_map_outcomes outcome ON outcome.constituent_id = constituent.id
LEFT JOIN course_map_tasks task ON task.outcome_id = outcome.id AND task.mode = 'diagnostic'
LEFT JOIN learning_audio_assets asset ON asset.id = task.audio_asset_id
WHERE subject.id = sqlc.arg(subject_id)::text
ORDER BY task.id;

-- name: LearningGetAccessibleTaskAudio :one
SELECT asset.id, asset.instruction, asset.object_key, asset.bucket, asset.storage_uri,
       asset.audio_url, asset.status, asset.attempts
FROM learning_audio_assets asset
WHERE asset.id = $2 AND (
    EXISTS (SELECT 1 FROM course_map_tasks task WHERE task.audio_asset_id = asset.id)
    OR EXISTS (
        SELECT 1 FROM learning_variant_tasks variant_task
        JOIN learning_variants variant ON variant.id = variant_task.variant_id
        WHERE variant.user_id = $1 AND variant_task.audio_asset_id = asset.id
    )
    OR EXISTS (
        SELECT 1 FROM learning_training_exercises exercise
        JOIN learning_training_sessions session ON session.id=exercise.session_id
        WHERE session.owner_id=$1 AND exercise.audio_asset_id=asset.id
          AND session.state_data->'Current'->>'ID'=exercise.id
    )
);

-- name: LearningRecoverExpiredTaskAudio :exec
UPDATE learning_audio_assets
SET status = CASE
        WHEN NOT EXISTS (
            SELECT 1
            FROM course_map_tasks task
            JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
            JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
            JOIN course_map_competencies competency ON competency.id = constituent.competency_id
            JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
            WHERE task.audio_asset_id = learning_audio_assets.id
        ) THEN 'cancelled'
        ELSE 'pending'
    END,
    attempts = greatest(attempts - 1, 0),
    lease_until = NULL,
    claim_token = NULL,
    next_attempt_at = now(),
    last_error_code = CASE
        WHEN NOT EXISTS (
            SELECT 1
            FROM course_map_tasks task
            JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
            JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
            JOIN course_map_competencies competency ON competency.id = constituent.competency_id
            JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
            WHERE task.audio_asset_id = learning_audio_assets.id
        ) THEN 'task_no_longer_current'
        ELSE 'worker_interrupted'
    END,
    updated_at = now()
WHERE status = 'processing' AND lease_until <= now();

-- name: LearningCancelPendingTaskAudioForSubject :execrows
UPDATE learning_audio_assets asset
SET status = 'cancelled', last_error_code = 'task_no_longer_current', updated_at = now()
WHERE asset.status = 'pending'
  AND EXISTS (
      SELECT 1
      FROM course_map_tasks task
      JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
      JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
      JOIN course_map_competencies competency ON competency.id = constituent.competency_id
      JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
      WHERE task.audio_asset_id = asset.id AND subject.id = $1
  );

-- name: LearningClaimTaskAudio :one
WITH candidate AS (
    SELECT id FROM learning_audio_assets
    WHERE status = 'pending' AND next_attempt_at <= now() AND attempts < 5
      AND EXISTS (
          SELECT 1
          FROM course_map_tasks task
          JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
          JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
          JOIN course_map_competencies competency ON competency.id = constituent.competency_id
          JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
          WHERE task.audio_asset_id = learning_audio_assets.id
      )
    -- Students hear audio first where they wait for it: current training
    -- exercises, then started learning_variants in question order, then the rest.
    ORDER BY
        priority_requested_at DESC NULLS LAST,
        EXISTS (
            SELECT 1 FROM learning_training_exercises exercise
            WHERE exercise.audio_asset_id = learning_audio_assets.id
        ) DESC,
        (
            SELECT min(variant_task.competency_position * 3 + variant_task.slot)
            FROM learning_variant_tasks variant_task
            WHERE variant_task.audio_asset_id = learning_audio_assets.id
        ) ASC NULLS LAST,
        created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE learning_audio_assets asset
SET status = 'processing',
    attempts = asset.attempts + 1,
    claim_token = sqlc.arg(claim_token),
    lease_until = now() + sqlc.arg(lease_seconds)::double precision * interval '1 second',
    updated_at = now()
FROM candidate
WHERE asset.id = candidate.id
RETURNING asset.id, asset.instruction, asset.object_key, asset.bucket, asset.storage_uri,
          asset.audio_url, asset.status, asset.attempts;

-- name: LearningClaimTaskAudioForRepair :one
WITH candidate AS MATERIALIZED (
    SELECT asset.id, asset.bucket AS previous_bucket
    FROM learning_audio_assets asset
    WHERE asset.id = sqlc.arg(id)
      AND asset.status IN ('ready', 'failed')
      AND EXISTS (
      SELECT 1
      FROM course_map_tasks task
      JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
      JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
      JOIN course_map_competencies competency ON competency.id = constituent.competency_id
      JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
      WHERE task.audio_asset_id = asset.id
      )
    FOR UPDATE
), claimed AS (
    UPDATE learning_audio_assets asset
    SET status = 'processing',
        attempts = 1,
        bucket = NULL,
        storage_uri = NULL,
        audio_url = NULL,
        claim_token = sqlc.arg(claim_token),
        lease_until = now() + sqlc.arg(lease_seconds)::double precision * interval '1 second',
        updated_at = now()
    FROM candidate
    WHERE asset.id = candidate.id AND asset.status IN ('ready', 'failed')
    RETURNING asset.id, asset.instruction, asset.object_key, asset.bucket, asset.storage_uri,
              asset.audio_url, asset.status, asset.attempts, candidate.previous_bucket
)
SELECT * FROM claimed;

-- name: LearningIsCurrentTaskAudio :one
SELECT EXISTS (
    SELECT 1
    FROM learning_audio_assets asset
    JOIN course_map_tasks task ON task.audio_asset_id = asset.id
    JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
    JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
    JOIN course_map_competencies competency ON competency.id = constituent.competency_id
    JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
    WHERE asset.id = sqlc.arg(id)
      AND asset.status = 'processing'
      AND asset.claim_token = sqlc.arg(claim_token)
      AND asset.lease_until > now()
);

-- name: LearningCancelClaimedTaskAudio :execrows
UPDATE learning_audio_assets
SET status = 'cancelled', lease_until = NULL, claim_token = NULL,
    last_error_code = 'task_no_longer_current', updated_at = now()
WHERE learning_audio_assets.id = sqlc.arg(id) AND status = 'processing' AND claim_token = sqlc.arg(claim_token)
  AND NOT EXISTS (
      SELECT 1
      FROM course_map_tasks task
      JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
      JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
      JOIN course_map_competencies competency ON competency.id = constituent.competency_id
      JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
      WHERE task.audio_asset_id = learning_audio_assets.id
  );

-- name: LearningCompleteTaskAudio :execrows
UPDATE learning_audio_assets
SET status = 'ready', bucket = $3, storage_uri = $4, audio_url = $5,
    lease_until = NULL, claim_token = NULL, last_error_code = NULL, updated_at = now()
WHERE id = $1 AND status = 'processing' AND claim_token = $2;

-- name: LearningFailTaskAudio :execrows
UPDATE learning_audio_assets
SET status = CASE
        WHEN NOT EXISTS (
            SELECT 1
            FROM course_map_tasks task
            JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
            JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
            JOIN course_map_competencies competency ON competency.id = constituent.competency_id
            JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
            WHERE task.audio_asset_id = learning_audio_assets.id
        ) THEN 'cancelled'
        WHEN attempts >= 5 THEN 'failed'
        ELSE 'pending'
    END,
    next_attempt_at = CASE
        WHEN attempts >= 5 OR NOT EXISTS (
            SELECT 1
            FROM course_map_tasks task
            JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
            JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
            JOIN course_map_competencies competency ON competency.id = constituent.competency_id
            JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
            WHERE task.audio_asset_id = learning_audio_assets.id
        ) THEN next_attempt_at
        ELSE now() + sqlc.arg(delay_seconds)::double precision * interval '1 second'
    END,
    lease_until = NULL, claim_token = NULL,
    last_error_code = CASE
        WHEN NOT EXISTS (
            SELECT 1
            FROM course_map_tasks task
            JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
            JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
            JOIN course_map_competencies competency ON competency.id = constituent.competency_id
            JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
            WHERE task.audio_asset_id = learning_audio_assets.id
        ) THEN 'task_no_longer_current'
        ELSE sqlc.arg(error_code)
    END,
    updated_at = now()
WHERE learning_audio_assets.id = sqlc.arg(id) AND status = 'processing' AND claim_token = sqlc.arg(claim_token);

-- name: LearningFindReadyTaskAudioByInstruction :one
-- A ready recording of the same instruction can be copied instead of synthesized.
SELECT id, instruction, object_key, bucket, storage_uri, audio_url, status, attempts
FROM learning_audio_assets
WHERE instruction = $1 AND id <> $2 AND status = 'ready'
  AND starts_with(object_key, sqlc.arg(object_prefix)::text)
ORDER BY updated_at DESC, id
LIMIT 1;

-- name: LearningReleaseTaskAudio :execrows
WITH current_task AS (
    SELECT EXISTS (
        SELECT 1
        FROM course_map_tasks task
        JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
        JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
        JOIN course_map_competencies competency ON competency.id = constituent.competency_id
        JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
        WHERE task.audio_asset_id = sqlc.arg(id)
    ) AS is_current
)
UPDATE learning_audio_assets asset
SET status = CASE WHEN current_task.is_current THEN 'pending' ELSE 'cancelled' END,
    attempts = greatest(asset.attempts - 1, 0),
    lease_until = NULL, claim_token = NULL, next_attempt_at = now(),
    last_error_code = CASE WHEN current_task.is_current THEN 'worker_interrupted' ELSE 'task_no_longer_current' END,
    updated_at = now()
FROM current_task
WHERE asset.id = sqlc.arg(id) AND asset.status = 'processing' AND asset.claim_token = sqlc.arg(claim_token);

-- name: LearningPrioritizeTaskAudio :execrows
UPDATE learning_audio_assets asset
SET priority_requested_at = now(), next_attempt_at = now(), updated_at = now()
WHERE asset.id = $1 AND asset.status = 'pending'
  AND (asset.priority_requested_at IS NULL OR EXISTS (
      SELECT 1 FROM learning_audio_assets other
      WHERE other.status = 'pending' AND other.priority_requested_at > asset.priority_requested_at
  ))
  AND EXISTS (
      SELECT 1 FROM course_map_tasks task
      JOIN course_map_outcomes outcome ON outcome.id = task.outcome_id
      JOIN course_map_constituents constituent ON constituent.id = outcome.constituent_id
      JOIN course_map_competencies competency ON competency.id = constituent.competency_id
      JOIN learning_subject_state subject_state ON subject_state.active_revision = competency.revision
      JOIN subjects subject ON subject.id = subject_state.subject_id
      WHERE task.audio_asset_id = asset.id
  );

-- name: LearningInsertTaskAudioAsset :one
INSERT INTO learning_audio_assets(id, instruction, object_key)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO UPDATE SET instruction = learning_audio_assets.instruction
RETURNING instruction;

-- name: LearningLinkTaskAudioAsset :execrows
UPDATE course_map_tasks SET audio_asset_id = $2 WHERE id = $1;
