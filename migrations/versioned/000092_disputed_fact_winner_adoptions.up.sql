-- Migration: 000092_disputed_fact_winner_adoptions
-- C4.8 durable, explicit reopen support for C4.7 global-winner adoptions.
-- A row records exactly which raw members and chunks one adoption changed, so
-- reopening never guesses from a current proposal or raw A/B orientation.
DO $$ BEGIN RAISE NOTICE '[Migration 000092] Adding durable winner adoption audit state'; END $$;

ALTER TABLE knowledge_conflicts
    ADD COLUMN IF NOT EXISTS winner_adoption_id VARCHAR(36) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_conflicts_winner_adoption_id
    ON knowledge_conflicts (winner_adoption_id)
    WHERE winner_adoption_id <> '';

ALTER TABLE disputed_facts
    ADD COLUMN IF NOT EXISTS active_winner_adoption_id VARCHAR(36) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_disputed_facts_active_winner_adoption
    ON disputed_facts (knowledge_base_id, active_winner_adoption_id)
    WHERE active_winner_adoption_id <> '';

CREATE TABLE IF NOT EXISTS disputed_fact_winner_adoptions (
    id                        VARCHAR(36) PRIMARY KEY,
    tenant_id                 BIGINT       NOT NULL,
    knowledge_base_id         VARCHAR(36)  NOT NULL,
    disputed_fact_id          VARCHAR(36)  NOT NULL,
    fact_key                  VARCHAR(512) NOT NULL DEFAULT '',

    winner_knowledge_id       VARCHAR(36)  NOT NULL,
    proposal_version          VARCHAR(32)  NOT NULL,
    proposal_confidence       DOUBLE PRECISION NOT NULL DEFAULT 0,
    proposal_source_count     INT          NOT NULL DEFAULT 0,

    member_conflict_ids       JSONB        NOT NULL DEFAULT '[]'::jsonb,
    disabled_chunk_ids        JSONB        NOT NULL DEFAULT '[]'::jsonb,
    disabled_knowledge_ids    JSONB        NOT NULL DEFAULT '[]'::jsonb,

    status                    VARCHAR(32)  NOT NULL DEFAULT 'adopted',
    adopted_by                VARCHAR(64)  NOT NULL DEFAULT '',
    adopted_at                TIMESTAMPTZ  NOT NULL,
    adoption_note             TEXT         NOT NULL DEFAULT '',
    revoked_by                VARCHAR(64)  NOT NULL DEFAULT '',
    revoked_at                TIMESTAMPTZ,
    revoke_note               TEXT         NOT NULL DEFAULT '',
    created_at                TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- At most one currently active adoption owns a disputed fact. Historical
-- revoked rows remain for audit and do not consume the active uniqueness slot.
CREATE UNIQUE INDEX IF NOT EXISTS uq_disputed_fact_active_winner_adoption
    ON disputed_fact_winner_adoptions (tenant_id, knowledge_base_id, disputed_fact_id)
    WHERE status = 'adopted';
CREATE INDEX IF NOT EXISTS idx_disputed_fact_winner_adoptions_kb_status
    ON disputed_fact_winner_adoptions (knowledge_base_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_disputed_fact_winner_adoptions_fact
    ON disputed_fact_winner_adoptions (tenant_id, knowledge_base_id, disputed_fact_id);

COMMENT ON TABLE disputed_fact_winner_adoptions IS 'C4.7 explicit global-winner adoption audit; C4.8 can revoke only a durable active row';
COMMENT ON COLUMN knowledge_conflicts.winner_adoption_id IS 'Active C4.7 adoption record owning this resolved_global_winner raw member';
COMMENT ON COLUMN disputed_facts.active_winner_adoption_id IS 'Derived current active C4.7 adoption id; blank unless every member is owned by it';

DO $$ BEGIN RAISE NOTICE '[Migration 000092] done'; END $$;
