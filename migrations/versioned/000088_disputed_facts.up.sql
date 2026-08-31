-- Migration: 000088_disputed_facts
-- C4-Lite fact-level clustering for raw knowledge_conflicts rows.
DO $$ BEGIN RAISE NOTICE '[Migration 000088] Creating disputed_facts and conflict cluster anchors'; END $$;

ALTER TABLE knowledge_conflicts
    ADD COLUMN IF NOT EXISTS cluster_id VARCHAR(36) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fact_key VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fact_anchor_kind VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS claim_key VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fact_subject TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fact_predicate TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fact_value_a TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fact_value_b TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_conflicts_kb_fact_key
    ON knowledge_conflicts (knowledge_base_id, fact_key);
CREATE INDEX IF NOT EXISTS idx_conflicts_cluster_id
    ON knowledge_conflicts (cluster_id);
CREATE INDEX IF NOT EXISTS idx_conflicts_claim_key
    ON knowledge_conflicts (knowledge_base_id, claim_key);

CREATE TABLE IF NOT EXISTS disputed_facts (
    id                     VARCHAR(36) PRIMARY KEY,
    tenant_id              BIGINT       NOT NULL,
    knowledge_base_id      VARCHAR(36)  NOT NULL,
    clusterer_version      VARCHAR(32)  NOT NULL DEFAULT 'c4-v1',

    fact_key               VARCHAR(512) NOT NULL,
    anchor_kind            VARCHAR(32)  NOT NULL DEFAULT 'chunk_pair',
    claim_key              VARCHAR(512) NOT NULL DEFAULT '',
    subject                TEXT         NOT NULL DEFAULT '',
    predicate              TEXT         NOT NULL DEFAULT '',

    conflict_type          VARCHAR(32)  NOT NULL DEFAULT 'fact_contradiction',
    status                 VARCHAR(20)  NOT NULL DEFAULT 'pending',

    conflict_count         INT          NOT NULL DEFAULT 0,
    pending_conflict_count INT          NOT NULL DEFAULT 0,
    source_count           INT          NOT NULL DEFAULT 0,
    candidate_value_count  INT          NOT NULL DEFAULT 0,
    candidate_values       JSONB        NOT NULL DEFAULT '[]'::jsonb,
    source_refs            JSONB        NOT NULL DEFAULT '[]'::jsonb,

    created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_disputed_facts_tenant_kb_fact_key
        UNIQUE (tenant_id, knowledge_base_id, fact_key)
);

CREATE INDEX IF NOT EXISTS idx_disputed_facts_kb_status_updated
    ON disputed_facts (knowledge_base_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_disputed_facts_tenant
    ON disputed_facts (tenant_id);
CREATE INDEX IF NOT EXISTS idx_disputed_facts_claim_key
    ON disputed_facts (knowledge_base_id, claim_key);

COMMENT ON TABLE disputed_facts IS 'C4-Lite fact-level clusters of raw knowledge_conflicts';
COMMENT ON COLUMN knowledge_conflicts.fact_key IS 'Deterministic C4 anchor: claim_key / fuzzy_slot / chunk_pair';
COMMENT ON COLUMN knowledge_conflicts.cluster_id IS 'disputed_facts.id assigned by ConflictClusterService';

DO $$ BEGIN RAISE NOTICE '[Migration 000088] done'; END $$;
