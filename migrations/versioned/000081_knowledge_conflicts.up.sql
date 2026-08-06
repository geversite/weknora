-- Migration: 000081_knowledge_conflicts
-- Records file-level content conflicts detected post-upload between different
-- knowledge files in the same KB, pending human adjudication (M3).
DO $$ BEGIN RAISE NOTICE '[Migration 000081] Creating knowledge_conflicts table'; END $$;

CREATE TABLE IF NOT EXISTS knowledge_conflicts (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    knowledge_id_a VARCHAR(36) NOT NULL,
    knowledge_id_b VARCHAR(36) NOT NULL,
    chunk_id_a VARCHAR(36) NOT NULL,
    chunk_id_b VARCHAR(36) NOT NULL,
    content_a TEXT NOT NULL,
    content_b TEXT NOT NULL,
    conflict_type VARCHAR(32) NOT NULL DEFAULT 'fact_contradiction',
    llm_reason TEXT,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    resolved_by VARCHAR(64),
    resolved_at TIMESTAMP WITH TIME ZONE,
    resolution_note TEXT,
    detected_by VARCHAR(20) NOT NULL DEFAULT 'upload',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_conflicts_tenant ON knowledge_conflicts (tenant_id);
CREATE INDEX IF NOT EXISTS idx_conflicts_kb ON knowledge_conflicts (knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_conflicts_kb_status ON knowledge_conflicts (knowledge_base_id, status);
CREATE INDEX IF NOT EXISTS idx_conflicts_knowledge_a ON knowledge_conflicts (knowledge_id_a);
CREATE INDEX IF NOT EXISTS idx_conflicts_knowledge_b ON knowledge_conflicts (knowledge_id_b);
CREATE INDEX IF NOT EXISTS idx_conflicts_chunk_a ON knowledge_conflicts (chunk_id_a);
CREATE INDEX IF NOT EXISTS idx_conflicts_chunk_b ON knowledge_conflicts (chunk_id_b);
CREATE INDEX IF NOT EXISTS idx_conflicts_created ON knowledge_conflicts (created_at);

COMMENT ON TABLE knowledge_conflicts IS 'File-level content conflicts detected post-upload, pending adjudication';
COMMENT ON COLUMN knowledge_conflicts.conflict_type IS 'fact_contradiction / partial_contradiction / version_update';
COMMENT ON COLUMN knowledge_conflicts.status IS 'pending / resolved_keep_both / resolved_newer_wins / resolved_older_wins / resolved_not_conflict';

DO $$ BEGIN RAISE NOTICE '[Migration 000081] done'; END $$;
