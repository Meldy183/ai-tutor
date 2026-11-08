-- +goose Up
-- One upload of a two-sheet competency map stores the course structure once
-- (decision of 08.11.2026): competencies, constituents and outcomes belong to
-- neither mode. Each mode keeps its own tasks of an outcome, marked by mode.
-- A revision of the map stays while a published Geek graph or a started
-- session still uses it, so a course may have several.
ALTER TABLE learning_map_imports RENAME TO course_map_imports;
ALTER TABLE course_map_imports DROP CONSTRAINT competency_map_imports_subject_id_key;
ALTER TABLE learning_competencies RENAME TO course_map_competencies;
ALTER TABLE learning_constituents RENAME TO course_map_constituents;
ALTER TABLE learning_outcomes RENAME TO course_map_outcomes;
ALTER TABLE learning_tasks RENAME TO course_map_tasks;

-- Tasks of the learning sheet and generated ones are the diagnostic's; the
-- Geek sheet's are inserted as exam tasks.
ALTER TABLE course_map_tasks ADD COLUMN mode text NOT NULL DEFAULT 'diagnostic'
    CHECK (mode IN ('diagnostic', 'exam'));
-- The prepared rephrase of an exam task; empty until Geek prepares it.
ALTER TABLE course_map_tasks ADD COLUMN rephrase text NOT NULL DEFAULT '';
-- The order of the variants of an exam task in its outcome.
ALTER TABLE course_map_tasks ADD COLUMN position integer NOT NULL DEFAULT 0 CHECK (position >= 0);
CREATE INDEX course_map_tasks_by_mode ON course_map_tasks(outcome_id, mode, position, id);

-- The revision of the map a saved and a published Geek graph were built from.
ALTER TABLE course_graph_maps ADD COLUMN map_revision bigint REFERENCES course_map_imports(revision);
ALTER TABLE course_graphs ADD COLUMN map_revision bigint REFERENCES course_map_imports(revision);

-- +goose Down
ALTER TABLE course_graphs DROP COLUMN map_revision;
ALTER TABLE course_graph_maps DROP COLUMN map_revision;
DROP INDEX course_map_tasks_by_mode;
DELETE FROM course_map_tasks WHERE mode = 'exam';
ALTER TABLE course_map_tasks DROP COLUMN position;
ALTER TABLE course_map_tasks DROP COLUMN rephrase;
ALTER TABLE course_map_tasks DROP COLUMN mode;
ALTER TABLE course_map_tasks RENAME TO learning_tasks;
ALTER TABLE course_map_outcomes RENAME TO learning_outcomes;
ALTER TABLE course_map_constituents RENAME TO learning_constituents;
ALTER TABLE course_map_competencies RENAME TO learning_competencies;
ALTER TABLE course_map_imports ADD CONSTRAINT competency_map_imports_subject_id_key UNIQUE(subject_id);
ALTER TABLE course_map_imports RENAME TO learning_map_imports;
