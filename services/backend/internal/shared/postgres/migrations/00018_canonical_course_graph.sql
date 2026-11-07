-- +goose Up
-- The learning map is the canonical owner of course vertices. Geek-specific
-- task data and traversal edges refer to those vertices by ID and revision.
CREATE TABLE learning_graph_prerequisites (
    map_revision bigint NOT NULL REFERENCES learning_map_imports(revision) ON DELETE CASCADE,
    source_outcome_id text NOT NULL REFERENCES learning_outcomes(id) ON DELETE CASCADE,
    target_outcome_id text NOT NULL REFERENCES learning_outcomes(id) ON DELETE CASCADE,
    reason text NOT NULL DEFAULT '',
    PRIMARY KEY (map_revision, source_outcome_id, target_outcome_id),
    CHECK (source_outcome_id <> target_outcome_id)
);
CREATE INDEX learning_graph_prerequisites_by_target
    ON learning_graph_prerequisites(map_revision, target_outcome_id);

CREATE TABLE course_graph_exam_topics (
    map_revision bigint NOT NULL REFERENCES learning_map_imports(revision) ON DELETE CASCADE,
    outcome_id text NOT NULL REFERENCES learning_outcomes(id) ON DELETE CASCADE,
    topic_order integer NOT NULL CHECK (topic_order > 0),
    role text NOT NULL CHECK (role IN ('main', 'basic')),
    PRIMARY KEY (map_revision, outcome_id),
    UNIQUE (map_revision, topic_order)
);

CREATE TABLE course_graph_exam_tasks (
    map_revision bigint NOT NULL REFERENCES learning_map_imports(revision) ON DELETE CASCADE,
    outcome_id text NOT NULL REFERENCES learning_outcomes(id) ON DELETE CASCADE,
    position integer NOT NULL CHECK (position > 0),
    question text NOT NULL CHECK (length(btrim(question)) > 0),
    content text NOT NULL,
    assessment_guidance text NOT NULL DEFAULT '',
    rephrase text NOT NULL DEFAULT '',
    PRIMARY KEY (map_revision, outcome_id, position),
    FOREIGN KEY (map_revision, outcome_id)
        REFERENCES course_graph_exam_topics(map_revision, outcome_id) ON DELETE CASCADE
);

-- Build the Geek view from canonical vertices, stored prerequisite edges, and
-- mode-specific tasks. The JSON is a read projection; it is never the source
-- of the map vertices or their relationships.
CREATE FUNCTION course_graph_map_projection(p_subject_id text, p_revision bigint, p_metadata jsonb, p_prepared boolean)
RETURNS jsonb LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object(
        'version', 'competency-map-canonical-v1',
        'source', 'competency_map',
        'canonical', true,
        'learning_revision', p_revision,
        'topics', COALESCE((
            SELECT jsonb_agg(
                jsonb_build_object(
                    'id', outcome.id,
                    'title', outcome.name,
                    'text', first_task.content,
                    'content', first_task.content,
                    'question', first_task.question,
                    'rephrase', CASE WHEN p_prepared THEN first_task.rephrase ELSE '' END,
                    'assessment_guidance', first_task.assessment_guidance,
                    'variants', task_data.variants,
                    'provides', jsonb_build_array(outcome.name),
                    'requires', COALESCE((
                        SELECT jsonb_agg(edge.source_outcome_id ORDER BY edge.source_outcome_id)
                        FROM learning_graph_prerequisites edge
                        WHERE edge.map_revision=p_revision AND edge.target_outcome_id=outcome.id
                    ), '[]'::jsonb),
                    'parent_title', competency.name,
                    'constituent_title', constituent.name,
                    'competency_id', competency.id,
                    'constituent_id', constituent.id,
                    'role', topic.role
                ) ORDER BY topic.topic_order
            )
            FROM course_graph_exam_topics topic
            JOIN learning_outcomes outcome ON outcome.id=topic.outcome_id
            JOIN learning_constituents constituent ON constituent.id=outcome.constituent_id
            JOIN learning_competencies competency ON competency.id=constituent.competency_id
            JOIN LATERAL (
                SELECT jsonb_agg(jsonb_build_object(
                    'question', task.question,
                    'rephrase', CASE WHEN p_prepared THEN task.rephrase ELSE '' END,
                    'content', task.content,
                    'assessment_guidance', task.assessment_guidance
                ) ORDER BY task.position) AS variants
                FROM course_graph_exam_tasks task
                WHERE task.map_revision=p_revision AND task.outcome_id=outcome.id
            ) task_data ON true
            JOIN LATERAL (
                SELECT task.question, task.content, task.assessment_guidance, task.rephrase
                FROM course_graph_exam_tasks task
                WHERE task.map_revision=p_revision AND task.outcome_id=outcome.id
                ORDER BY task.position LIMIT 1
            ) first_task ON true
            WHERE topic.map_revision=p_revision
              AND EXISTS (SELECT 1 FROM learning_map_imports imported
                          WHERE imported.revision=p_revision AND imported.subject_id=p_subject_id)
        ), '[]'::jsonb),
        'prerequisites', COALESCE((
            SELECT jsonb_agg(jsonb_build_object('source', edge.source_outcome_id,
                                                'target', edge.target_outcome_id,
                                                'reason', edge.reason)
                             ORDER BY edge.target_outcome_id, edge.source_outcome_id)
            FROM learning_graph_prerequisites edge
            WHERE edge.map_revision=p_revision
              AND EXISTS (SELECT 1 FROM learning_map_imports imported
                          WHERE imported.revision=p_revision AND imported.subject_id=p_subject_id)
        ), '[]'::jsonb),
        'hierarchy', COALESCE((
            SELECT jsonb_agg(jsonb_build_object('source', edge.target_outcome_id,
                                                'target', edge.source_outcome_id)
                             ORDER BY edge.target_outcome_id, edge.source_outcome_id)
            FROM learning_graph_prerequisites edge
            WHERE edge.map_revision=p_revision
              AND EXISTS (SELECT 1 FROM learning_map_imports imported
                          WHERE imported.revision=p_revision AND imported.subject_id=p_subject_id)
        ), '[]'::jsonb),
        'skipped', COALESCE(p_metadata->'skipped', '[]'::jsonb),
        'assessment_warnings', COALESCE(p_metadata->'assessment_warnings', '[]'::jsonb),
        'quality', COALESCE(p_metadata->'quality', jsonb_build_object('acyclic', true))
    )
$$;

-- +goose Down
DROP TABLE IF EXISTS course_graph_exam_tasks, course_graph_exam_topics,
    learning_graph_prerequisites;
DROP FUNCTION IF EXISTS course_graph_map_projection(text, bigint, jsonb, boolean);
