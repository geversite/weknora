-- Migration rollback: 000012_disputed_fact_winner_proposals (sqlite / Lite mode)
DROP INDEX IF EXISTS idx_disputed_facts_kb_suggested_winner;

ALTER TABLE disputed_facts DROP COLUMN winner_proposal_source_count;
ALTER TABLE disputed_facts DROP COLUMN winner_proposal_version;
ALTER TABLE disputed_facts DROP COLUMN winner_proposal_confidence;
ALTER TABLE disputed_facts DROP COLUMN winner_proposal_reason;
ALTER TABLE disputed_facts DROP COLUMN suggested_winner_knowledge_id;
