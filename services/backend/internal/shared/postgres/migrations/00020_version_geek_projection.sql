-- +goose Up
-- A Geek projection can be regenerated (for example after excluding tasks)
-- without changing the canonical learning graph or the currently published
-- Geek version.
CREATE TABLE course_graph_exam_versions (
    id bigserial PRIMARY KEY,
    map_revision bigint NOT NULL REFERENCES learning_map_imports(revision) ON DELETE CASCADE,
    created_at bigint NOT NULL
);

ALTER TABLE course_graph_exam_topics ADD COLUMN exam_revision bigint;
ALTER TABLE course_graph_exam_tasks ADD COLUMN exam_revision bigint;

-- Give each already imported map one immutable Geek projection version.
-- +goose StatementBegin
DO $$
DECLARE
    map_row record;
    version_id bigint;
BEGIN
    FOR map_row IN SELECT subject_id,learning_revision,graph FROM course_graph_maps
                   WHERE graph->>'canonical'='true' AND learning_revision IS NOT NULL
    LOOP
        INSERT INTO course_graph_exam_versions(map_revision,created_at)
        SELECT map_row.learning_revision,COALESCE(map_row.graph->>'uploaded_at','0')::bigint
        RETURNING id INTO version_id;
        UPDATE course_graph_exam_topics SET exam_revision=version_id
        WHERE map_revision=map_row.learning_revision;
        UPDATE course_graph_exam_tasks SET exam_revision=version_id
        WHERE map_revision=map_row.learning_revision;
        UPDATE course_graph_maps SET graph=graph || jsonb_build_object('exam_revision',version_id)
        WHERE subject_id=map_row.subject_id;
        UPDATE course_graphs SET graph=graph || jsonb_build_object('exam_revision',version_id)
        WHERE subject_id=map_row.subject_id AND source='competency_map'
          AND map_revision=map_row.learning_revision AND graph->>'canonical'='true';
    END LOOP;
END $$;
-- +goose StatementEnd

ALTER TABLE course_graph_exam_topics ALTER COLUMN exam_revision SET NOT NULL;
ALTER TABLE course_graph_exam_tasks ALTER COLUMN exam_revision SET NOT NULL;
ALTER TABLE course_graph_exam_tasks DROP CONSTRAINT course_graph_exam_tasks_pkey;
ALTER TABLE course_graph_exam_tasks DROP CONSTRAINT course_graph_exam_tasks_map_revision_outcome_id_fkey;
ALTER TABLE course_graph_exam_topics DROP CONSTRAINT course_graph_exam_topics_pkey;
ALTER TABLE course_graph_exam_topics DROP CONSTRAINT course_graph_exam_topics_map_revision_topic_order_key;
ALTER TABLE course_graph_exam_topics
    ADD CONSTRAINT course_graph_exam_topics_pkey PRIMARY KEY(exam_revision,outcome_id),
    ADD CONSTRAINT course_graph_exam_topics_exam_order_key UNIQUE(exam_revision,topic_order),
    ADD CONSTRAINT course_graph_exam_topics_version_fkey FOREIGN KEY(exam_revision)
        REFERENCES course_graph_exam_versions(id) ON DELETE CASCADE,
    ADD CONSTRAINT course_graph_exam_topics_version_outcome_key UNIQUE(exam_revision,outcome_id);
ALTER TABLE course_graph_exam_tasks
    ADD CONSTRAINT course_graph_exam_tasks_pkey PRIMARY KEY(exam_revision,outcome_id,position),
    ADD CONSTRAINT course_graph_exam_tasks_topic_fkey FOREIGN KEY(exam_revision,outcome_id)
        REFERENCES course_graph_exam_topics(exam_revision,outcome_id) ON DELETE CASCADE;

DROP FUNCTION course_graph_map_projection(text,bigint,jsonb,boolean);
CREATE FUNCTION course_graph_map_projection(p_subject_id text, p_revision bigint, p_metadata jsonb, p_prepared boolean)
RETURNS jsonb LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object(
        'version', 'competency-map-canonical-v1',
        'source', 'competency_map',
        'canonical', true,
        'learning_revision', p_revision,
        'exam_revision', (p_metadata->>'exam_revision')::bigint,
        'topics', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                'id', outcome.id,'title', outcome.name,'text', first_task.content,'content', first_task.content,
                'question', first_task.question,
                'rephrase', CASE WHEN p_prepared THEN first_task.rephrase ELSE '' END,
                'assessment_guidance', first_task.assessment_guidance,'variants', task_data.variants,
                'provides', jsonb_build_array(outcome.name),
                'requires', COALESCE((
                    SELECT jsonb_agg(edge.source_outcome_id ORDER BY edge.source_outcome_id)
                    FROM learning_graph_prerequisites edge
                    JOIN course_graph_exam_topics source_topic
                      ON source_topic.exam_revision=(p_metadata->>'exam_revision')::bigint
                     AND source_topic.outcome_id=edge.source_outcome_id
                    WHERE edge.map_revision=p_revision AND edge.target_outcome_id=outcome.id
                ), '[]'::jsonb),
                'parent_title', competency.name,'constituent_title', constituent.name,
                'competency_id', competency.id,'constituent_id', constituent.id,'role', topic.role
            ) ORDER BY topic.topic_order)
            FROM course_graph_exam_topics topic
            JOIN learning_outcomes outcome ON outcome.id=topic.outcome_id
            JOIN learning_constituents constituent ON constituent.id=outcome.constituent_id
            JOIN learning_competencies competency ON competency.id=constituent.competency_id
            JOIN LATERAL (
                SELECT jsonb_agg(jsonb_build_object('question',task.question,
                    'rephrase',CASE WHEN p_prepared THEN task.rephrase ELSE '' END,
                    'content',task.content,'assessment_guidance',task.assessment_guidance)
                    ORDER BY task.position) AS variants
                FROM course_graph_exam_tasks task
                WHERE task.exam_revision=topic.exam_revision AND task.outcome_id=outcome.id
            ) task_data ON true
            JOIN LATERAL (
                SELECT task.question,task.content,task.assessment_guidance,task.rephrase
                FROM course_graph_exam_tasks task
                WHERE task.exam_revision=topic.exam_revision AND task.outcome_id=outcome.id
                ORDER BY task.position LIMIT 1
            ) first_task ON true
            WHERE topic.map_revision=p_revision
              AND topic.exam_revision=(p_metadata->>'exam_revision')::bigint
              AND EXISTS (SELECT 1 FROM learning_map_imports imported
                          WHERE imported.revision=p_revision AND imported.subject_id=p_subject_id)
        ), '[]'::jsonb),
        'prerequisites', COALESCE((
            SELECT jsonb_agg(jsonb_build_object('source',edge.source_outcome_id,
                'target',edge.target_outcome_id,'reason',edge.reason)
                ORDER BY edge.target_outcome_id,edge.source_outcome_id)
            FROM learning_graph_prerequisites edge
            JOIN course_graph_exam_topics source_topic
              ON source_topic.exam_revision=(p_metadata->>'exam_revision')::bigint
             AND source_topic.outcome_id=edge.source_outcome_id
            JOIN course_graph_exam_topics target_topic
              ON target_topic.exam_revision=(p_metadata->>'exam_revision')::bigint
             AND target_topic.outcome_id=edge.target_outcome_id
            WHERE edge.map_revision=p_revision
              AND EXISTS (SELECT 1 FROM learning_map_imports imported
                          WHERE imported.revision=p_revision AND imported.subject_id=p_subject_id)
        ), '[]'::jsonb),
        'hierarchy', COALESCE((
            SELECT jsonb_agg(jsonb_build_object('source',edge.target_outcome_id,'target',edge.source_outcome_id)
                ORDER BY edge.target_outcome_id,edge.source_outcome_id)
            FROM learning_graph_prerequisites edge
            JOIN course_graph_exam_topics source_topic
              ON source_topic.exam_revision=(p_metadata->>'exam_revision')::bigint
             AND source_topic.outcome_id=edge.source_outcome_id
            JOIN course_graph_exam_topics target_topic
              ON target_topic.exam_revision=(p_metadata->>'exam_revision')::bigint
             AND target_topic.outcome_id=edge.target_outcome_id
            WHERE edge.map_revision=p_revision
              AND EXISTS (SELECT 1 FROM learning_map_imports imported
                          WHERE imported.revision=p_revision AND imported.subject_id=p_subject_id)
        ), '[]'::jsonb),
        'skipped', COALESCE(p_metadata->'skipped','[]'::jsonb),
        'assessment_warnings', COALESCE(p_metadata->'assessment_warnings','[]'::jsonb),
        'quality', COALESCE(p_metadata->'quality',jsonb_build_object('acyclic',true))
    )
$$;

-- +goose Down
-- Reverting would collapse multiple filtered Geek projections into one map
-- revision. Refuse that data loss; a single-version dataset is reversible.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT map_revision FROM course_graph_exam_versions GROUP BY map_revision HAVING count(*)>1) THEN
        RAISE EXCEPTION 'multiple Geek projection versions exist for a learning revision';
    END IF;
END $$;
-- +goose StatementEnd
DROP FUNCTION course_graph_map_projection(text,bigint,jsonb,boolean);
CREATE FUNCTION course_graph_map_projection(p_subject_id text, p_revision bigint, p_metadata jsonb, p_prepared boolean)
RETURNS jsonb LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object(
        'version', 'competency-map-canonical-v1',
        'source', 'competency_map',
        'canonical', true,
        'learning_revision', p_revision,
        'topics', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
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
            ) ORDER BY topic.topic_order)
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
                'target', edge.target_outcome_id,'reason', edge.reason)
                ORDER BY edge.target_outcome_id, edge.source_outcome_id)
            FROM learning_graph_prerequisites edge
            WHERE edge.map_revision=p_revision
              AND EXISTS (SELECT 1 FROM learning_map_imports imported
                          WHERE imported.revision=p_revision AND imported.subject_id=p_subject_id)
        ), '[]'::jsonb),
        'hierarchy', COALESCE((
            SELECT jsonb_agg(jsonb_build_object('source',edge.target_outcome_id,
                'target',edge.source_outcome_id)
                ORDER BY edge.target_outcome_id,edge.source_outcome_id)
            FROM learning_graph_prerequisites edge
            WHERE edge.map_revision=p_revision
              AND EXISTS (SELECT 1 FROM learning_map_imports imported
                          WHERE imported.revision=p_revision AND imported.subject_id=p_subject_id)
        ), '[]'::jsonb),
        'skipped', COALESCE(p_metadata->'skipped','[]'::jsonb),
        'assessment_warnings', COALESCE(p_metadata->'assessment_warnings','[]'::jsonb),
        'quality', COALESCE(p_metadata->'quality',jsonb_build_object('acyclic',true))
    )
$$;
ALTER TABLE course_graph_exam_tasks DROP CONSTRAINT course_graph_exam_tasks_topic_fkey;
ALTER TABLE course_graph_exam_tasks DROP CONSTRAINT course_graph_exam_tasks_pkey;
ALTER TABLE course_graph_exam_topics DROP CONSTRAINT course_graph_exam_topics_version_fkey,
    DROP CONSTRAINT course_graph_exam_topics_version_outcome_key,
    DROP CONSTRAINT course_graph_exam_topics_exam_order_key,
    DROP CONSTRAINT course_graph_exam_topics_pkey;
ALTER TABLE course_graph_exam_topics
    ADD CONSTRAINT course_graph_exam_topics_pkey PRIMARY KEY(map_revision,outcome_id),
    ADD CONSTRAINT course_graph_exam_topics_map_revision_topic_order_key UNIQUE(map_revision,topic_order);
ALTER TABLE course_graph_exam_tasks
    ADD CONSTRAINT course_graph_exam_tasks_pkey PRIMARY KEY(map_revision,outcome_id,position),
    ADD CONSTRAINT course_graph_exam_tasks_map_revision_outcome_id_fkey
        FOREIGN KEY(map_revision,outcome_id) REFERENCES course_graph_exam_topics(map_revision,outcome_id) ON DELETE CASCADE;
UPDATE course_graph_maps map SET graph=graph-'exam_revision' WHERE graph->>'canonical'='true';
UPDATE course_graphs graph SET graph=graph-'exam_revision' WHERE graph->>'canonical'='true';
ALTER TABLE course_graph_exam_tasks DROP COLUMN exam_revision;
ALTER TABLE course_graph_exam_topics DROP COLUMN exam_revision;
DROP TABLE course_graph_exam_versions;
