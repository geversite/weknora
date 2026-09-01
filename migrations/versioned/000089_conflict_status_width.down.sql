-- Migration rollback: 000089_conflict_status_width
-- Do not silently truncate status values when rolling back. A database that
-- contains resolved_not_conflict rows cannot safely return to VARCHAR(20).
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM knowledge_conflicts WHERE char_length(status) > 20
    ) THEN
        RAISE EXCEPTION 'cannot roll back 000089: knowledge_conflicts.status contains values longer than VARCHAR(20)';
    END IF;
END $$;

ALTER TABLE knowledge_conflicts
    ALTER COLUMN status TYPE VARCHAR(20);
