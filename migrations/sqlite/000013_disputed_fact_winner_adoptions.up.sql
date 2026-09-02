-- Migration: 000013_disputed_fact_winner_adoptions (sqlite / Lite mode)
-- C4.8 durable, explicit reopen support for C4.7 global-winner adoptions.
ALTER TABLE knowledge_conflicts ADD COLUMN winner_adoption_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_conflicts_winner_adoption_id
    ON knowledge_conflicts (winner_adoption_id);

ALTER TABLE disputed_facts ADD COLUMN active_winner_adoption_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_disputed_facts_active_winner_adoption
    ON disputed_facts (knowledge_base_id, active_winner_adoption_id);

CREATE TABLE IF NOT EXISTS disputed_fact_winner_adoptions (
    id                        TEXT PRIMARY KEY,
    tenant_id                 INTEGER NOT NULL,
    knowledge_base_id         TEXT    NOT NULL,
    disputed_fact_id          TEXT    NOT NULL,
    fact_key                  TEXT    NOT NULL DEFAULT '',

    winner_knowledge_id       TEXT    NOT NULL,
    proposal_version          TEXT    NOT NULL,
    proposal_confidence       REAL    NOT NULL DEFAULT 0,
    proposal_source_count     INTEGER NOT NULL DEFAULT 0,

    member_conflict_ids       TEXT    NOT NULL DEFAULT '[]',
    disabled_chunk_ids        TEXT    NOT NULL DEFAULT '[]',
    disabled_knowledge_ids    TEXT    NOT NULL DEFAULT '[]',

    status                    TEXT    NOT NULL DEFAULT 'adopted',
    adopted_by                TEXT    NOT NULL DEFAULT '',
    adopted_at                DATETIME NOT NULL,
    adoption_note             TEXT    NOT NULL DEFAULT '',
    revoked_by                TEXT    NOT NULL DEFAULT '',
    revoked_at                DATETIME,
    revoke_note               TEXT    NOT NULL DEFAULT '',
    created_at                DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_disputed_fact_active_winner_adoption
    ON disputed_fact_winner_adoptions (tenant_id, knowledge_base_id, disputed_fact_id)
    WHERE status = 'adopted';
CREATE INDEX IF NOT EXISTS idx_disputed_fact_winner_adoptions_kb_status
    ON disputed_fact_winner_adoptions (knowledge_base_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_disputed_fact_winner_adoptions_fact
    ON disputed_fact_winner_adoptions (tenant_id, knowledge_base_id, disputed_fact_id);
