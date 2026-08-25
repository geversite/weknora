-- Migration rollback: 000085_claims
DROP INDEX IF EXISTS idx_claims_tenant;
DROP INDEX IF EXISTS idx_claims_knowledge;
DROP INDEX IF EXISTS idx_claims_source;
DROP INDEX IF EXISTS idx_claims_kb_key;
DROP TABLE IF EXISTS claims;
