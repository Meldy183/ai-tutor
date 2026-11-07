-- +goose Up
ALTER TABLE learning_map_imports DROP CONSTRAINT competency_map_imports_subject_id_key;
ALTER TABLE learning_subject_state
    ADD COLUMN pending_revision bigint REFERENCES learning_map_imports(revision) ON DELETE SET NULL;

ALTER TABLE course_graph_maps ADD COLUMN learning_revision bigint;

-- +goose Down
ALTER TABLE course_graph_maps DROP COLUMN learning_revision;
ALTER TABLE learning_subject_state DROP COLUMN pending_revision;
ALTER TABLE learning_map_imports ADD CONSTRAINT competency_map_imports_subject_id_key UNIQUE(subject_id);
