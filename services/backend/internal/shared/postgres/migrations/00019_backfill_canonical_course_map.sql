-- +goose Up
-- Preserve the exact previous paired-map payload so the migration can be
-- rolled back without reconstructing or losing the imported source data.
CREATE TABLE course_graph_map_legacy_backup (
    subject_id text PRIMARY KEY REFERENCES subjects(id) ON DELETE CASCADE,
    graph jsonb NOT NULL
);

-- Carry forward paired maps from migration 17. Match by competency and outcome
-- only when the result is unique; ambiguity or a missing outcome stops the
-- migration instead of silently creating unrelated vertices.
-- +goose StatementBegin
DO $$
DECLARE
    imported record;
    topic jsonb;
    variant jsonb;
    edge jsonb;
    canonical_id text;
    mapping jsonb;
    topic_position integer;
    task_position integer;
    match_count integer;
BEGIN
    FOR imported IN
        SELECT map.subject_id, map.learning_revision, map.graph
        FROM course_graph_maps map
        WHERE map.learning_revision IS NOT NULL AND map.graph->>'canonical' IS DISTINCT FROM 'true'
    LOOP
        IF NOT EXISTS (SELECT 1 FROM learning_map_imports item
                       WHERE item.subject_id=imported.subject_id AND item.revision=imported.learning_revision) THEN
            RAISE EXCEPTION 'paired course map revision does not belong to subject %', imported.subject_id;
        END IF;
        mapping := '{}'::jsonb;
        topic_position := 0;
        FOR topic IN SELECT value FROM jsonb_array_elements(imported.graph->'topics') AS item(value)
        LOOP
            topic_position := topic_position + 1;
            SELECT count(*) INTO match_count
            FROM learning_outcomes outcome
            JOIN learning_constituents constituent ON constituent.id=outcome.constituent_id
            JOIN learning_competencies competency ON competency.id=constituent.competency_id
            WHERE competency.revision=imported.learning_revision
              AND competency.name=topic->>'parent_title'
              AND outcome.name=topic->>'title';
            IF match_count <> 1 THEN
                RAISE EXCEPTION 'paired Geek outcome has % canonical matches for subject %, competency %, outcome %',
                    match_count, imported.subject_id, topic->>'parent_title', topic->>'title';
            END IF;
            SELECT outcome.id INTO canonical_id
            FROM learning_outcomes outcome
            JOIN learning_constituents constituent ON constituent.id=outcome.constituent_id
            JOIN learning_competencies competency ON competency.id=constituent.competency_id
            WHERE competency.revision=imported.learning_revision
              AND competency.name=topic->>'parent_title'
              AND outcome.name=topic->>'title';
            mapping := mapping || jsonb_build_object(topic->>'id', canonical_id);
            INSERT INTO course_graph_exam_topics(map_revision,outcome_id,topic_order,role)
            VALUES(imported.learning_revision,canonical_id,topic_position,COALESCE(NULLIF(topic->>'role',''),'main'));
            task_position := 0;
            FOR variant IN SELECT value FROM jsonb_array_elements(COALESCE(topic->'variants','[]'::jsonb)) AS item(value)
            LOOP
                task_position := task_position + 1;
                INSERT INTO course_graph_exam_tasks(map_revision,outcome_id,position,question,content,assessment_guidance,rephrase)
                VALUES(imported.learning_revision,canonical_id,task_position,variant->>'question',
                       COALESCE(variant->>'content',''),COALESCE(variant->>'assessment_guidance',''),
                       COALESCE(variant->>'rephrase',''));
            END LOOP;
        END LOOP;
        FOR edge IN SELECT value FROM jsonb_array_elements(COALESCE(imported.graph->'prerequisites','[]'::jsonb)) AS item(value)
        LOOP
            IF mapping->>(edge->>'source') IS NULL OR mapping->>(edge->>'target') IS NULL THEN
                RAISE EXCEPTION 'paired Geek relation references a missing topic for subject %', imported.subject_id;
            END IF;
            INSERT INTO learning_graph_prerequisites(map_revision,source_outcome_id,target_outcome_id,reason)
            VALUES(imported.learning_revision,mapping->>(edge->>'source'),mapping->>(edge->>'target'),COALESCE(edge->>'reason',''));
        END LOOP;
        INSERT INTO course_graph_map_legacy_backup(subject_id,graph)
        VALUES(imported.subject_id,imported.graph);
        UPDATE course_graph_maps
        SET graph=(graph - 'topics' - 'prerequisites' - 'hierarchy') || jsonb_build_object('canonical',true)
        WHERE subject_id=imported.subject_id;
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- A map uploaded after this migration has no legacy copy to restore. Refuse a
-- lossy rollback and preserve its data until it can be exported or migrated.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM course_graph_maps map
               WHERE map.graph->>'canonical'='true'
                 AND NOT EXISTS (SELECT 1 FROM course_graph_map_legacy_backup backup WHERE backup.subject_id=map.subject_id)) THEN
        RAISE EXCEPTION 'canonical competency-map imports exist without legacy rollback copies';
    END IF;
END $$;
-- +goose StatementEnd
UPDATE course_graph_maps map SET graph=backup.graph
FROM course_graph_map_legacy_backup backup WHERE backup.subject_id=map.subject_id;
DROP TABLE IF EXISTS course_graph_map_legacy_backup;
