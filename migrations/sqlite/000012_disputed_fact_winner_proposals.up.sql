-- Migration: 000012_disputed_fact_winner_proposals (sqlite / Lite mode)
ALTER TABLE disputed_facts ADD COLUMN suggested_winner_knowledge_id TEXT NOT NULL DEFAULT '';
ALTER TABLE disputed_facts ADD COLUMN winner_proposal_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE disputed_facts ADD COLUMN winner_proposal_confidence REAL NOT NULL DEFAULT 0;
ALTER TABLE disputed_facts ADD COLUMN winner_proposal_version TEXT NOT NULL DEFAULT '';
ALTER TABLE disputed_facts ADD COLUMN winner_proposal_source_count INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_disputed_facts_kb_suggested_winner
    ON disputed_facts (knowledge_base_id, suggested_winner_knowledge_id);
