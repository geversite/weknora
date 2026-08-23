-- Migration: 000085_claims
-- Atomic claims extracted from document chunks and wiki pages (C1, Conflict V2).
-- One row = one atomic factual claim anchored to its source span. Claims power
-- claim-key based conflict candidate pairing and corpus sweeps.
DO $$ BEGIN RAISE NOTICE '[Migration 000085] Creating claims table'; END $$;

CREATE TABLE IF NOT EXISTS claims (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT       NOT NULL,
    knowledge_base_id VARCHAR(36)  NOT NULL,

    -- Source anchoring (dual source: document chunk or wiki page)
    source_type       VARCHAR(16)  NOT NULL,
    source_id         VARCHAR(36)  NOT NULL,
    knowledge_id      VARCHAR(36)  NOT NULL DEFAULT '',
    span_start        INT          NOT NULL DEFAULT 0,
    span_end          INT          NOT NULL DEFAULT 0,

    -- Claim quadruple (verbatim source phrasing)
    subject           TEXT         NOT NULL,
    predicate         TEXT         NOT NULL,
    value             TEXT         NOT NULL,
    qualifiers        JSON,

    -- Normalized pairing keys (see claim_normalize.go; fused form, v1.1)
    claim_key         VARCHAR(512) NOT NULL,
    value_norm        VARCHAR(512) NOT NULL DEFAULT '',
    value_kind        VARCHAR(16)  NOT NULL DEFAULT 'text',

    -- Extraction management
    extractor_version INT          NOT NULL DEFAULT 1,
    extract_batch_id  VARCHAR(36)  NOT NULL DEFAULT '',

    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_claims_kb_key    ON claims (knowledge_base_id, claim_key);
CREATE INDEX IF NOT EXISTS idx_claims_source    ON claims (source_type, source_id);
CREATE INDEX IF NOT EXISTS idx_claims_knowledge ON claims (knowledge_id);
CREATE INDEX IF NOT EXISTS idx_claims_tenant    ON claims (tenant_id);

COMMENT ON TABLE claims IS 'Atomic claims extracted from chunks and wiki pages (C1)';
COMMENT ON COLUMN claims.source_type IS 'chunk / wiki_page';
COMMENT ON COLUMN claims.claim_key IS 'Fused normalized key: norm(subject)+norm_predicate(predicate)';
COMMENT ON COLUMN claims.value_kind IS 'number / enum / date / text';

DO $$ BEGIN RAISE NOTICE '[Migration 000085] done'; END $$;
