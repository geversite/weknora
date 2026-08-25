-- Migration: 000006_claims (sqlite / Lite mode)
-- Atomic claims extracted from document chunks and wiki pages (C1, Conflict V2).
CREATE TABLE IF NOT EXISTS claims (
    id                TEXT PRIMARY KEY,
    tenant_id         INTEGER NOT NULL,
    knowledge_base_id TEXT    NOT NULL,

    source_type       TEXT    NOT NULL,
    source_id         TEXT    NOT NULL,
    knowledge_id      TEXT    NOT NULL DEFAULT '',
    span_start        INTEGER NOT NULL DEFAULT 0,
    span_end          INTEGER NOT NULL DEFAULT 0,

    subject           TEXT    NOT NULL,
    predicate         TEXT    NOT NULL,
    value             TEXT    NOT NULL,
    qualifiers        TEXT,

    claim_key         TEXT    NOT NULL,
    value_norm        TEXT    NOT NULL DEFAULT '',
    value_kind        TEXT    NOT NULL DEFAULT 'text',

    extractor_version INTEGER NOT NULL DEFAULT 1,
    extract_batch_id  TEXT    NOT NULL DEFAULT '',

    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_claims_kb_key    ON claims (knowledge_base_id, claim_key);
CREATE INDEX IF NOT EXISTS idx_claims_source    ON claims (source_type, source_id);
CREATE INDEX IF NOT EXISTS idx_claims_knowledge ON claims (knowledge_id);
CREATE INDEX IF NOT EXISTS idx_claims_tenant    ON claims (tenant_id);
