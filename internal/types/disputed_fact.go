package types

import (
	"errors"
	"time"
)

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

	// C3/C4.6 computes an advisory unique winner across all source metadata
	// in this cluster. It is never applied automatically in this milestone.
	SuggestedWinnerKnowledgeID string  `json:"suggested_winner_knowledge_id" gorm:"type:varchar(36);index"`
	WinnerProposalReason       string  `json:"winner_proposal_reason" gorm:"type:text"`
	WinnerProposalConfidence   float64 `json:"winner_proposal_confidence"`
	WinnerProposalVersion      string  `json:"winner_proposal_version" gorm:"type:varchar(32)"`
	WinnerProposalSourceCount  int     `json:"winner_proposal_source_count"`

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
	// ConflictFactAnchorDocumentSingleton is a post-verdict C4 fallback for
	// two documents that each contain exactly one usable claim. It is used
	// when schema wording has no safe lexical fuzzy-slot match.
	ConflictFactAnchorDocumentSingleton = "document_singleton"
	// ConflictFactAnchorChunkPair is the conservative legacy fallback when no
	// usable claim evidence exists. It deliberately does not merge across
	// different chunk pairs.
	ConflictFactAnchorChunkPair = "chunk_pair"

	DisputedFactStatusPending      = "pending"
	DisputedFactStatusResolved     = "resolved"
	DisputedFactStatusMixed        = "mixed"
	DisputedFactConflictTypeMixed  = "mixed"

	// ConflictClustererVersion identifies deterministic clustering semantics.
	// c4-v6 adds advisory global winner proposal aggregation over C3 metadata.
	ConflictClustererVersion = "c4-v6"

	// DisputedFactWinnerProposalVersion identifies C3/C4.6's unique-max source
	// proposal semantics. It is distinct from the per-raw-conflict C3 parser.
	DisputedFactWinnerProposalVersion = "c3-c4-v1"

	// DisputedFactWinnerAdoptionVersion identifies the explicit C4.7 action
	// that applies a currently-proposed global winner. It never represents an
	// automatic model decision.
	DisputedFactWinnerAdoptionVersion = "c4-winner-adopt-v1"
)

// ErrDisputedFactWinnerAdoptionConflict marks a current-state/precondition
// failure that must be refreshed before the caller retries adoption. The HTTP
// handler maps it to 409, rather than treating a stale proposal as a generic
// malformed request.
var ErrDisputedFactWinnerAdoptionConflict = errors.New("disputed fact winner proposal cannot be adopted in its current state")

// DisputedFactRebuildResult is returned by the C4-Lite rebuild endpoint and
// exported by experiment scripts. It quantifies how much raw chunk-pair work
// compresses into human-reviewable fact clusters.
type DisputedFactRebuildResult struct {
	KnowledgeBaseID          string         `json:"knowledge_base_id"`
	ClustererVersion         string         `json:"clusterer_version"`
	RawConflictCount         int            `json:"raw_conflict_count"`
	DisputedFactCount        int            `json:"disputed_fact_count"`
	AssignedConflictCount    int            `json:"assigned_conflict_count"`
	UnanchoredConflictCount  int            `json:"unanchored_conflict_count"`
	WinnerProposalCount      int            `json:"winner_proposal_count"`
	AnchorKinds              map[string]int `json:"anchor_kinds"`
}

// DisputedFactResolution is a C4.5 cluster-level adjudication request. The
// first research-safe implementation deliberately permits only resolutions
// that disable no chunk: keep_both and not_conflict. C4.7 winner adoption is
// a separate explicit endpoint with its own optimistic proposal checks.
type DisputedFactResolution struct {
	DisputedFactID string `json:"disputed_fact_id"`
	Resolution      string `json:"resolution"`
	Note            string `json:"note,omitempty"`
}

// DisputedFactAdjudicationResult records one propagated safe resolution. It
// makes the reduction from raw-pair actions to one fact-level action explicit
// for C4.5 experiments.
type DisputedFactAdjudicationResult struct {
	DisputedFactID      string                      `json:"disputed_fact_id"`
	Resolution           string                      `json:"resolution"`
	UpdatedConflictIDs   []string                    `json:"updated_conflict_ids"`
	UpdatedConflictCount int                         `json:"updated_conflict_count"`
	ClearPenaltyChunkIDs []string                    `json:"clear_penalty_chunk_ids"`
	Rebuild              *DisputedFactRebuildResult `json:"rebuild,omitempty"`
}

// DisputedFactWinnerAdoption is an explicit human/API acceptance of a current
// C4.6 global-winner proposal. Every expected_* field is required: it makes the
// action optimistic and fails closed when a rebuild, new raw member, or changed
// proposal makes the review snapshot stale.
type DisputedFactWinnerAdoption struct {
	DisputedFactID              string    `json:"disputed_fact_id"`
	ExpectedWinnerKnowledgeID    string    `json:"expected_winner_knowledge_id"`
	ExpectedProposalVersion       string    `json:"expected_proposal_version"`
	ExpectedProposalUpdatedAt     time.Time `json:"expected_proposal_updated_at"`
	ExpectedProposalSourceCount   int       `json:"expected_proposal_source_count"`
	Note                         string    `json:"note,omitempty"`
}

// DisputedFactWinnerAdoptionResult is the durable, global-winner propagation
// result. Every affected raw member receives resolved_global_winner, and only
// chunks belonging to non-winner sources are disabled. The per-member status is
// intentionally direction-free because a raw pair may not include the global
// winner at all.
type DisputedFactWinnerAdoptionResult struct {
	DisputedFactID          string                      `json:"disputed_fact_id"`
	Resolution               string                      `json:"resolution"`
	WinnerKnowledgeID        string                      `json:"winner_knowledge_id"`
	ProposalVersion          string                      `json:"proposal_version"`
	ProposalConfidence       float64                     `json:"proposal_confidence"`
	ProposalSourceCount      int                         `json:"proposal_source_count"`
	AdoptionVersion          string                      `json:"adoption_version"`
	AdoptedAt                time.Time                   `json:"adopted_at"`
	ResolutionNote           string                      `json:"resolution_note"`
	UpdatedConflictIDs       []string                    `json:"updated_conflict_ids"`
	UpdatedConflictCount     int                         `json:"updated_conflict_count"`
	DisabledChunkIDs         []string                    `json:"disabled_chunk_ids"`
	DisabledKnowledgeIDs     []string                    `json:"disabled_knowledge_ids"`
	ClearPenaltyChunkIDs     []string                    `json:"clear_penalty_chunk_ids"`
	Rebuild                  *DisputedFactRebuildResult `json:"rebuild,omitempty"`
}
