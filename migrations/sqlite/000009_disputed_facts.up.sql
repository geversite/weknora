-- Migration: 000009_disputed_facts (sqlite / Lite mode)
-- C4-Lite fact-level clustering for raw knowledge_conflicts rows.
ALTER TABLE knowledge_conflicts ADD COLUMN cluster_id TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN fact_key TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN fact_anchor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN claim_key TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN fact_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN fact_predicate TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN fact_value_a TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_conflicts ADD COLUMN fact_value_b TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_conflicts_kb_fact_key
    ON knowledge_conflicts (knowledge_base_id, fact_key);
CREATE INDEX IF NOT EXISTS idx_conflicts_cluster_id
    ON knowledge_conflicts (cluster_id);
CREATE INDEX IF NOT EXISTS idx_conflicts_claim_key
    ON knowledge_conflicts (knowledge_base_id, claim_key);

CREATE TABLE IF NOT EXISTS disputed_facts (
    id                     TEXT PRIMARY KEY,
    tenant_id              INTEGER NOT NULL,
    knowledge_base_id      TEXT    NOT NULL,
    clusterer_version      TEXT    NOT NULL DEFAULT 'c4-v1',

    fact_key               TEXT    NOT NULL,
    anchor_kind            TEXT    NOT NULL DEFAULT 'chunk_pair',
    claim_key              TEXT    NOT NULL DEFAULT '',
    subject                TEXT    NOT NULL DEFAULT '',
    predicate              TEXT    NOT NULL DEFAULT '',

    conflict_type          TEXT    NOT NULL DEFAULT 'fact_contradiction',
    status                 TEXT    NOT NULL DEFAULT 'pending',

    conflict_count         INTEGER NOT NULL DEFAULT 0,
    pending_conflict_count INTEGER NOT NULL DEFAULT 0,
    source_count           INTEGER NOT NULL DEFAULT 0,
    candidate_value_count  INTEGER NOT NULL DEFAULT 0,
    candidate_values       TEXT    NOT NULL DEFAULT '[]',
    source_refs            TEXT    NOT NULL DEFAULT '[]',

    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_disputed_facts_tenant_kb_fact_key
    ON disputed_facts (tenant_id, knowledge_base_id, fact_key);
CREATE INDEX IF NOT EXISTS idx_disputed_facts_kb_status_updated
    ON disputed_facts (knowledge_base_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_disputed_facts_tenant
    ON disputed_facts (tenant_id);
CREATE INDEX IF NOT EXISTS idx_disputed_facts_claim_key
    ON disputed_facts (knowledge_base_id, claim_key);
