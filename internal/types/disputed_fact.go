package types

import "time"

// DisputedFact is the C4-Lite, fact-level aggregate of one or more raw
// KnowledgeConflict chunk-pair rows. FactKey is a stable, deterministic
// clustering key; ClusterID is written back to each member conflict.
//
// This is intentionally a research-oriented aggregate. Cluster-level
// resolution propagation and wiki writeback remain later C4/C7 work.
type DisputedFact struct {
	ID               string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID         uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID  string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	ClustererVersion string `json:"clusterer_version" gorm:"type:varchar(32)"`

	// FactKey is unique per tenant/KB and remains stable across incremental
	// cluster rebuilds. ClaimKey is set only for an exact claim-key anchor.
	FactKey    string `json:"fact_key" gorm:"type:varchar(512);index"`
	AnchorKind string `json:"anchor_kind" gorm:"type:varchar(32);index"`
	ClaimKey   string `json:"claim_key" gorm:"type:varchar(512)"`
	Subject    string `json:"subject" gorm:"type:text"`
	Predicate  string `json:"predicate" gorm:"type:text"`

	// ConflictType is the shared member type, or "mixed" when members use
	// different LLM types. Status is pending while any member is pending.
	ConflictType string `json:"conflict_type" gorm:"type:varchar(32)"`
	Status       string `json:"status" gorm:"type:varchar(20);index"`

	ConflictCount      int         `json:"conflict_count"`
	PendingConflictCount int       `json:"pending_conflict_count"`
	SourceCount        int         `json:"source_count"`
	CandidateValueCount int        `json:"candidate_value_count"`
	CandidateValues    StringArray `json:"candidate_values" gorm:"type:json"`
	SourceRefs         StringArray `json:"source_refs" gorm:"type:json"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (DisputedFact) TableName() string { return "disputed_facts" }

const (
	// ConflictFactAnchorClaimKey is an exact C1 claim-key anchor.
	ConflictFactAnchorClaimKey = "claim_key"
	// ConflictFactAnchorFuzzySlot is a C4 prompt-only fuzzy slot pairing that
	// was grounded by a final conflict verdict; it is never a direct rule.
	ConflictFactAnchorFuzzySlot = "fuzzy_slot"
	// ConflictFactAnchorChunkPair is the conservative legacy fallback when no
	// usable claim evidence exists. It deliberately does not merge across
	// different chunk pairs.
	ConflictFactAnchorChunkPair = "chunk_pair"

	DisputedFactStatusPending      = "pending"
	DisputedFactStatusResolved     = "resolved"
	DisputedFactStatusMixed        = "mixed"
	DisputedFactConflictTypeMixed  = "mixed"

	// ConflictClustererVersion identifies deterministic clustering semantics.
	// c4-v2 adds a post-verdict, singleton-document fallback for source chunks
	// that have no directly attached claim row (common with summary children).
	ConflictClustererVersion = "c4-v2"
)

// DisputedFactRebuildResult is returned by the C4-Lite rebuild endpoint and
// exported by experiment scripts. It quantifies how much raw chunk-pair work
// compresses into human-reviewable fact clusters.
type DisputedFactRebuildResult struct {
	KnowledgeBaseID         string         `json:"knowledge_base_id"`
	ClustererVersion        string         `json:"clusterer_version"`
	RawConflictCount       int            `json:"raw_conflict_count"`
	DisputedFactCount      int            `json:"disputed_fact_count"`
	AssignedConflictCount  int            `json:"assigned_conflict_count"`
	UnanchoredConflictCount int           `json:"unanchored_conflict_count"`
	AnchorKinds            map[string]int `json:"anchor_kinds"`
}
