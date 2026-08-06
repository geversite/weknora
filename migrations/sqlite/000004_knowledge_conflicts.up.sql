CREATE TABLE IF NOT EXISTS knowledge_conflicts (
    id TEXT PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    knowledge_id_a TEXT NOT NULL,
    knowledge_id_b TEXT NOT NULL,
    chunk_id_a TEXT NOT NULL,
    chunk_id_b TEXT NOT NULL,
    content_a TEXT NOT NULL,
    content_b TEXT NOT NULL,
    conflict_type TEXT NOT NULL DEFAULT 'fact_contradiction',
    llm_reason TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    resolved_by TEXT,
    resolved_at DATETIME,
    resolution_note TEXT,
    detected_by TEXT NOT NULL DEFAULT 'upload',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_conflicts_tenant ON knowledge_conflicts (tenant_id);
CREATE INDEX IF NOT EXISTS idx_conflicts_kb ON knowledge_conflicts (knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_conflicts_kb_status ON knowledge_conflicts (knowledge_base_id, status);
CREATE INDEX IF NOT EXISTS idx_conflicts_knowledge_a ON knowledge_conflicts (knowledge_id_a);
CREATE INDEX IF NOT EXISTS idx_conflicts_knowledge_b ON knowledge_conflicts (knowledge_id_b);
CREATE INDEX IF NOT EXISTS idx_conflicts_chunk_a ON knowledge_conflicts (chunk_id_a);
CREATE INDEX IF NOT EXISTS idx_conflicts_chunk_b ON knowledge_conflicts (chunk_id_b);
CREATE INDEX IF NOT EXISTS idx_conflicts_created ON knowledge_conflicts (created_at);
