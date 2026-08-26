package types

import "time"

// Claim is one atomic factual claim extracted from a document chunk or a wiki
// page (C1, Conflict V2). Subject / Predicate are stable extractor labels for
// display and cross-document pairing; Value preserves the asserted conclusion.
// SpanStart / SpanEnd anchor each row back to a verbatim source quote. ClaimKey
// / ValueNorm carry normalized forms used for conflict candidate pairing (see
// claim_normalize.go).
type Claim struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36)"`

	// SourceType is ClaimSourceChunk or ClaimSourceWikiPage; SourceID is the
	// chunk ID / wiki page ID respectively. KnowledgeID is redundantly kept
	// for chunk sources ("" for wiki pages) so document-level cleanup and
	// pairing filters avoid an extra join.
	SourceType  string `json:"source_type" gorm:"type:varchar(16)"`
	SourceID    string `json:"source_id" gorm:"type:varchar(36)"`
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);default:''"`
	// SpanStart / SpanEnd are rune offsets of the supporting quote inside the
	// ORIGINAL source text (machine-managed wiki blocks included). 0,0 means
	// "location failed" — the claim is still usable for pairing.
	SpanStart int `json:"span_start"`
	SpanEnd   int `json:"span_end"`

	Subject    string `json:"subject"`
	Predicate  string `json:"predicate"`
	Value      string `json:"value"`
	Qualifiers JSON   `json:"qualifiers" gorm:"type:json"`

	// ClaimKey is the fused normalized key norm(subject)+normPredicate(pred)
	// (v1.1: boundary-insensitive). ValueNorm is the normalized value used
	// for equality checks; ValueKind routes C2 rule-layer comparisons.
	ClaimKey  string `json:"claim_key" gorm:"type:varchar(512);index"`
	ValueNorm string `json:"value_norm" gorm:"type:varchar(512)"`
	ValueKind string `json:"value_kind" gorm:"type:varchar(16)"`

	// ExtractorVersion tags rows with the extractor generation that produced
	// them so sweeps (C6) can re-extract stale sources. ExtractBatchID groups
	// one extraction run; replace-style upserts delete rows of other batches.
	ExtractorVersion int    `json:"extractor_version"`
	ExtractBatchID   string `json:"extract_batch_id" gorm:"type:varchar(36)"`

	CreatedAt time.Time `json:"created_at"`
}

// TableName returns the table name for the Claim model.
func (Claim) TableName() string { return "claims" }

// Claim source types.
const (
	ClaimSourceChunk    = "chunk"
	ClaimSourceWikiPage = "wiki_page"
)

// Claim value kinds.
const (
	ClaimValueKindNumber = "number"
	ClaimValueKindEnum   = "enum"
	ClaimValueKindDate   = "date"
	ClaimValueKindText   = "text"
)

// ClaimExtractorVersion is the current extractor generation. Bump it whenever
// the extraction prompt or the normalization rules change in a way that makes
// previously stored claims incomparable with fresh ones.
const ClaimExtractorVersion = 2
