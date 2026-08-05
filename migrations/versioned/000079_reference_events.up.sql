-- Migration: 000079_reference_events
-- Records which knowledge files were cited by each assistant message,
-- enabling citation-count aggregation for asset observability and
-- folder-summary activeness profiling.
DO $$ BEGIN RAISE NOTICE '[Migration 000079] Creating reference_events table'; END $$;

CREATE TABLE IF NOT EXISTS reference_events (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    message_id VARCHAR(36) NOT NULL,
    session_id VARCHAR(36) NOT NULL,
    reference_type VARCHAR(32) NOT NULL DEFAULT 'rag',
    -- 'rag' = RAG chunk citation, 'agent' = agent tool citation,
    -- 'push' = file push (M2), 'wiki' = wiki page source doc
    score DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_reference_events_tenant
    ON reference_events (tenant_id);
CREATE INDEX IF NOT EXISTS idx_reference_events_kb
    ON reference_events (knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_reference_events_knowledge
    ON reference_events (knowledge_id);
CREATE INDEX IF NOT EXISTS idx_reference_events_created
    ON reference_events (created_at);
CREATE INDEX IF NOT EXISTS idx_reference_events_kb_knowledge_created
    ON reference_events (knowledge_base_id, knowledge_id, created_at);
CREATE INDEX IF NOT EXISTS idx_reference_events_session
    ON reference_events (session_id);

COMMENT ON TABLE reference_events IS 'Citation events: each row = one file cited by one assistant message';
COMMENT ON COLUMN reference_events.reference_type IS 'rag/agent/push/wiki — how the file was cited';
COMMENT ON COLUMN reference_events.score IS 'Retrieval relevance score at time of citation';

DO $$ BEGIN RAISE NOTICE '[Migration 000079] reference_events created'; END $$;
