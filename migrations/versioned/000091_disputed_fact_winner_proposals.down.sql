-- Migration rollback: 000091_disputed_fact_winner_proposals
DROP INDEX IF EXISTS idx_disputed_facts_kb_suggested_winner;

ALTER TABLE disputed_facts
    DROP COLUMN IF EXISTS winner_proposal_source_count,
    DROP COLUMN IF EXISTS winner_proposal_version,
    DROP COLUMN IF EXISTS winner_proposal_confidence,
    DROP COLUMN IF EXISTS winner_proposal_reason,
    DROP COLUMN IF EXISTS suggested_winner_knowledge_id;
