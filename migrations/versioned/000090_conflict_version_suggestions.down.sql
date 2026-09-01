-- Migration rollback: 000090_conflict_version_suggestions
DROP INDEX IF EXISTS idx_conflicts_kb_suggested_resolution;

ALTER TABLE knowledge_conflicts
    DROP COLUMN IF EXISTS auto_resolved,
    DROP COLUMN IF EXISTS suggestion_version,
    DROP COLUMN IF EXISTS suggestion_confidence,
    DROP COLUMN IF EXISTS suggestion_reason,
    DROP COLUMN IF EXISTS suggested_resolution,
    DROP COLUMN IF EXISTS doc_meta_b,
    DROP COLUMN IF EXISTS doc_meta_a;
