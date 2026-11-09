-- +goose Up
-- Both sheets use the same schema; their subject matter need not coincide.
ALTER TABLE course_map_competencies ADD COLUMN source_mode text NOT NULL DEFAULT 'diagnostic'
    CHECK (source_mode IN ('diagnostic', 'exam'));
CREATE INDEX course_map_competencies_by_source ON course_map_competencies(revision, source_mode);

-- +goose Down
DELETE FROM course_map_competencies WHERE source_mode = 'exam';
DROP INDEX course_map_competencies_by_source;
ALTER TABLE course_map_competencies DROP COLUMN source_mode;
