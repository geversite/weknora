package types

import "time"

// KnowledgeConflict records a content conflict detected between two knowledge
// files within the same KB. One row = one conflicting chunk pair. Rows are
// created post-upload by the async conflict-detection pipeline and are resolved
// by an Owner/Admin through the adjudication queue (M3/C4.5/C4.7).
type KnowledgeConflict struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	// KnowledgeIDA is typically the newly-uploaded file; KnowledgeIDB the
	// pre-existing file whose chunk semantically overlaps with A's.
	KnowledgeIDA string `json:"knowledge_id_a" gorm:"type:varchar(36);index"`
	KnowledgeIDB string `json:"knowledge_id_b" gorm:"type:varchar(36);index"`
	ChunkIDA     string `json:"chunk_id_a" gorm:"type:varchar(36);index"`
	ChunkIDB     string `json:"chunk_id_b" gorm:"type:varchar(36);index"`

	// C4-Lite provenance. FactKey / FactAnchorKind are deterministic at
	// detection time; ClusterID points at the fact-level aggregate created by
	// ConflictClusterService. ClaimKey is populated only for an exact C1 claim
	// anchor; fallback anchors retain empty ClaimKey rather than pretending to
	// be exact.
	ClusterID      string `json:"cluster_id" gorm:"type:varchar(36);index"`
	FactKey        string `json:"fact_key" gorm:"type:varchar(512);index"`
	FactAnchorKind string `json:"fact_anchor_kind" gorm:"type:varchar(32);index"`
	ClaimKey       string `json:"claim_key" gorm:"type:varchar(512);index"`
	FactSubject    string `json:"fact_subject" gorm:"type:text"`
	FactPredicate  string `json:"fact_predicate" gorm:"type:text"`
	FactValueA     string `json:"fact_value_a" gorm:"type:text"`
	FactValueB     string `json:"fact_value_b" gorm:"type:text"`

	// C3-Lite version metadata is captured at detection time from document
	// title/header evidence. Suggestions are advisory only; AutoResolved stays
	// false until a later C3 authority policy explicitly enables side effects.
	DocMetaA                 JSON    `json:"doc_meta_a" gorm:"type:json"`
	DocMetaB                 JSON    `json:"doc_meta_b" gorm:"type:json"`
	SuggestedResolution      string  `json:"suggested_resolution" gorm:"type:varchar(32)"`
	SuggestionReason         string  `json:"suggestion_reason" gorm:"type:text"`
	SuggestionConfidence     float64 `json:"suggestion_confidence"`
	SuggestionVersion        string  `json:"suggestion_version" gorm:"type:varchar(32)"`
	AutoResolved             bool    `json:"auto_resolved"`

	// ContentA / ContentB are snapshots of the conflicting chunks taken at
	// detection time. They remain stable even if the source chunk is later
	// edited (adjudication targets the concrete contradiction found).
	ContentA       string     `json:"content_a" gorm:"type:text"`
	ContentB       string     `json:"content_b" gorm:"type:text"`
	ConflictType   string     `json:"conflict_type" gorm:"type:varchar(32);default:'fact_contradiction'"`
	LLMReason      string     `json:"llm_reason" gorm:"type:text"`
	// VARCHAR(32) is required because resolved_not_conflict is 21 characters.
	Status         string     `json:"status" gorm:"type:varchar(32);default:'pending';index"`
	ResolvedBy     string     `json:"resolved_by" gorm:"type:varchar(64)"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	ResolutionNote string     `json:"resolution_note" gorm:"type:text"`
	DetectedBy     string     `json:"detected_by" gorm:"type:varchar(20);default:'upload'"`
	CreatedAt      time.Time  `json:"created_at" gorm:"index"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TableName returns the table name for the KnowledgeConflict model.
func (KnowledgeConflict) TableName() string { return "knowledge_conflicts" }

// ConflictType constants
const (
	// ConflictTypeFactContradiction: mutually exclusive claims about the SAME fact.
	ConflictTypeFactContradiction = "fact_contradiction"
	// ConflictTypePartialContradiction: largely agree but contradict on specific points.
	ConflictTypePartialContradiction = "partial_contradiction"
	// ConflictTypeVersionUpdate: one is a newer revision/supersession of the other (not a real conflict).
	ConflictTypeVersionUpdate = "version_update"
)

// ConflictStatus constants
const (
	ConflictStatusPending             = "pending"
	ConflictStatusResolvedKeepBoth    = "resolved_keep_both"
	ConflictStatusResolvedNewer       = "resolved_newer_wins"
	ConflictStatusResolvedOlder       = "resolved_older_wins"
	ConflictStatusResolvedNotConflict = "resolved_not_conflict"
	// ConflictStatusResolvedGlobalWinner records an explicit C4.7 adoption
	// of one C4.6 fact-level winner. It deliberately does not encode raw A/B
	// direction: a member pair need not contain the global winning source.
	ConflictStatusResolvedGlobalWinner = "resolved_global_winner"
)

// ConflictDetectedBy constants
const (
	ConflictDetectedByUpload = "upload"
	ConflictDetectedByManual = "manual"
)

// ConflictResolution is the payload for adjudicating a conflict.
type ConflictResolution struct {
	ConflictID string `json:"conflict_id"`
	Resolution string `json:"resolution"`
	Note       string `json:"note,omitempty"`
}

// ConflictAdjudicationResult describes the side-effects of a resolution so
// the service can disable / demote the adjudicated-losing chunks and clear
// their rerank penalty.
type ConflictAdjudicationResult struct {
	ConflictID           string   `json:"conflict_id"`
	DisabledChunkIDs     []string `json:"disabled_chunk_ids"`      // chunks disabled (adjudicated wrong/outdated side)
	DemotedKnowledgeIDs  []string `json:"demoted_knowledge_ids"`   // files to demote (non-disable)
	ClearPenaltyChunkIDs []string `json:"clear_penalty_chunk_ids"` // chunks no longer under penalty
}

// IsPending reports whether the conflict has not yet been adjudicated.
func (c *KnowledgeConflict) IsPending() bool {
	return c != nil && c.Status == ConflictStatusPending
}
