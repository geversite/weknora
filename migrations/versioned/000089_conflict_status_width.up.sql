-- Migration: 000089_conflict_status_width
-- resolved_not_conflict is 21 characters; the original M3 VARCHAR(20) column
-- made an otherwise valid safe C4.5 resolution fail transactionally.
DO $$ BEGIN RAISE NOTICE '[Migration 000089] Widening knowledge_conflicts.status'; END $$;

ALTER TABLE knowledge_conflicts
    ALTER COLUMN status TYPE VARCHAR(32);

COMMENT ON COLUMN knowledge_conflicts.status IS 'pending / resolved_keep_both / resolved_newer_wins / resolved_older_wins / resolved_not_conflict';

DO $$ BEGIN RAISE NOTICE '[Migration 000089] done'; END $$;
