CREATE TABLE IF NOT EXISTS reference_events (
    id TEXT PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    knowledge_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    reference_type TEXT NOT NULL DEFAULT 'rag',
    score REAL NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_reference_events_tenant ON reference_events (tenant_id);
CREATE INDEX IF NOT EXISTS idx_reference_events_kb ON reference_events (knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_reference_events_knowledge ON reference_events (knowledge_id);
CREATE INDEX IF NOT EXISTS idx_reference_events_created ON reference_events (created_at);
CREATE INDEX IF NOT EXISTS idx_reference_events_kb_knowledge_created ON reference_events (knowledge_base_id, knowledge_id, created_at);
CREATE INDEX IF NOT EXISTS idx_reference_events_session ON reference_events (session_id);
