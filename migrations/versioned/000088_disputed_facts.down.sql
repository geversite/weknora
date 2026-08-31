-- Migration rollback: 000088_disputed_facts
DROP TABLE IF EXISTS disputed_facts;

DROP INDEX IF EXISTS idx_conflicts_claim_key;
DROP INDEX IF EXISTS idx_conflicts_cluster_id;
DROP INDEX IF EXISTS idx_conflicts_kb_fact_key;

ALTER TABLE knowledge_conflicts
    DROP COLUMN IF EXISTS fact_value_b,
    DROP COLUMN IF EXISTS fact_value_a,
    DROP COLUMN IF EXISTS fact_predicate,
    DROP COLUMN IF EXISTS fact_subject,
    DROP COLUMN IF EXISTS claim_key,
    DROP COLUMN IF EXISTS fact_anchor_kind,
    DROP COLUMN IF EXISTS fact_key,
    DROP COLUMN IF EXISTS cluster_id;
