-- Migration rollback: 000011_conflict_version_suggestions (sqlite / Lite mode)
DROP INDEX IF EXISTS idx_conflicts_kb_suggested_resolution;

ALTER TABLE knowledge_conflicts DROP COLUMN auto_resolved;
ALTER TABLE knowledge_conflicts DROP COLUMN suggestion_version;
ALTER TABLE knowledge_conflicts DROP COLUMN suggestion_confidence;
ALTER TABLE knowledge_conflicts DROP COLUMN suggestion_reason;
ALTER TABLE knowledge_conflicts DROP COLUMN suggested_resolution;
ALTER TABLE knowledge_conflicts DROP COLUMN doc_meta_b;
ALTER TABLE knowledge_conflicts DROP COLUMN doc_meta_a;
