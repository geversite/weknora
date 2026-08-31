-- Migration rollback: 000009_disputed_facts (sqlite / Lite mode)
DROP TABLE IF EXISTS disputed_facts;

DROP INDEX IF EXISTS idx_conflicts_claim_key;
DROP INDEX IF EXISTS idx_conflicts_cluster_id;
DROP INDEX IF EXISTS idx_conflicts_kb_fact_key;

ALTER TABLE knowledge_conflicts DROP COLUMN fact_value_b;
ALTER TABLE knowledge_conflicts DROP COLUMN fact_value_a;
ALTER TABLE knowledge_conflicts DROP COLUMN fact_predicate;
ALTER TABLE knowledge_conflicts DROP COLUMN fact_subject;
ALTER TABLE knowledge_conflicts DROP COLUMN claim_key;
ALTER TABLE knowledge_conflicts DROP COLUMN fact_anchor_kind;
ALTER TABLE knowledge_conflicts DROP COLUMN fact_key;
ALTER TABLE knowledge_conflicts DROP COLUMN cluster_id;
