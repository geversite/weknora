-- Migration: 000090_conflict_version_suggestions
-- C3-Lite source-grounded document metadata snapshots and advisory version
-- suggestions. Suggestions intentionally do not alter raw conflict status.
DO $$ BEGIN RAISE NOTICE '[Migration 000090] Adding C3 conflict version suggestions'; END $$;

ALTER TABLE knowledge_conflicts
    ADD COLUMN IF NOT EXISTS doc_meta_a JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS doc_meta_b JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS suggested_resolution VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS suggestion_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS suggestion_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS suggestion_version VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS auto_resolved BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_conflicts_kb_suggested_resolution
    ON knowledge_conflicts (knowledge_base_id, suggested_resolution)
    WHERE suggested_resolution <> '';

COMMENT ON COLUMN knowledge_conflicts.doc_meta_a IS 'C3 source-grounded title/header metadata snapshot for conflict A';
COMMENT ON COLUMN knowledge_conflicts.doc_meta_b IS 'C3 source-grounded title/header metadata snapshot for conflict B';
COMMENT ON COLUMN knowledge_conflicts.suggested_resolution IS 'Advisory resolved_newer_wins / resolved_older_wins only; no side effect';
COMMENT ON COLUMN knowledge_conflicts.auto_resolved IS 'False in C3-Lite; retained for later explicit authority policy';

DO $$ BEGIN RAISE NOTICE '[Migration 000090] done'; END $$;
