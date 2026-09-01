-- Migration: 000091_disputed_fact_winner_proposals
-- C3/C4.6 advisory unique-global-winner proposal fields. They never execute
-- a resolution automatically; adoption remains a later explicit operation.
DO $$ BEGIN RAISE NOTICE '[Migration 000091] Adding DisputedFact winner proposals'; END $$;

ALTER TABLE disputed_facts
    ADD COLUMN IF NOT EXISTS suggested_winner_knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS winner_proposal_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS winner_proposal_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS winner_proposal_version VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS winner_proposal_source_count INT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_disputed_facts_kb_suggested_winner
    ON disputed_facts (knowledge_base_id, suggested_winner_knowledge_id)
    WHERE suggested_winner_knowledge_id <> '';

COMMENT ON COLUMN disputed_facts.suggested_winner_knowledge_id IS 'C3/C4.6 advisory unique global winner; never auto-applied';
COMMENT ON COLUMN disputed_facts.winner_proposal_confidence IS 'Conservative minimum pairwise metadata comparison confidence';

DO $$ BEGIN RAISE NOTICE '[Migration 000091] done'; END $$;
