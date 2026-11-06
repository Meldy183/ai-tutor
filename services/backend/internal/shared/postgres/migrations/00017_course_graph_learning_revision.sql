-- +goose Up
ALTER TABLE course_graphs ADD COLUMN map_revision bigint;

UPDATE course_graphs graph
SET map_revision = map.learning_revision
FROM course_graph_maps map
WHERE graph.subject_id = map.subject_id
  AND graph.source = 'competency_map'
  AND graph.fingerprint = map.fingerprint
  AND map.learning_revision IS NOT NULL;

-- +goose Down
ALTER TABLE course_graphs DROP COLUMN map_revision;
