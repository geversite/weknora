-- Migration rollback: 000013_disputed_fact_winner_adoptions (sqlite / Lite mode)
DROP INDEX IF EXISTS idx_disputed_fact_winner_adoptions_fact;
DROP INDEX IF EXISTS idx_disputed_fact_winner_adoptions_kb_status;
DROP INDEX IF EXISTS uq_disputed_fact_active_winner_adoption;
DROP TABLE IF EXISTS disputed_fact_winner_adoptions;

DROP INDEX IF EXISTS idx_disputed_facts_active_winner_adoption;
ALTER TABLE disputed_facts DROP COLUMN active_winner_adoption_id;

DROP INDEX IF EXISTS idx_conflicts_winner_adoption_id;
ALTER TABLE knowledge_conflicts DROP COLUMN winner_adoption_id;
