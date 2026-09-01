-- Migration: 000011_conflict_version_suggestions (sqlite / Lite mode)
ALTER TABLE knowledge_conflicts ADD COLUMN doc_meta_a TEXT NOT NULL DEFAULT '{}';
ALTER TABLE knowledge_conflicts ADD COLUMN doc_meta_b TEXT NOT NULL DEFAULT '{}';
ALTER TABLE knowledge_conflicts ADD COLUMN suggested_resolution TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN suggestion_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN suggestion_confidence REAL NOT NULL DEFAULT 0;
ALTER TABLE knowledge_conflicts ADD COLUMN suggestion_version TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN auto_resolved INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_conflicts_kb_suggested_resolution
    ON knowledge_conflicts (knowledge_base_id, suggested_resolution);
