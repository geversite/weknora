package types

// ConflictVersionSuggestionVersion identifies C3-Lite's deterministic document
// metadata parser and advisory winner policy. It is stored with every new raw
// conflict suggestion so later C3 revisions remain comparable.
const ConflictVersionSuggestionVersion = "c3-v1"

// ConflictDocumentMeta is the compact, source-grounded document metadata
// snapshot attached to a raw conflict. It deliberately records only evidence
// available from title/header text; it does not infer effective dates from an
// arbitrary fact in the body.
type ConflictDocumentMeta struct {
	ParserVersion string `json:"parser_version"`
	KnowledgeID   string `json:"knowledge_id,omitempty"`
	Title         string `json:"title,omitempty"`
	Issuer        string `json:"issuer,omitempty"`

	// EffectiveDate is normalized as YYYY, YYYY-MM, or YYYY-MM-DD. Precision
	// explains which components were explicitly present in source evidence.
	EffectiveDate          string `json:"effective_date,omitempty"`
	EffectiveDatePrecision string `json:"effective_date_precision,omitempty"`
	EffectiveDateEvidence  string `json:"effective_date_evidence,omitempty"`

	// Version is a dotted numeric sequence extracted from an explicit version
	// marker (for example V2.1 or 第2版), not merely any year in a title.
	Version         string `json:"version,omitempty"`
	VersionEvidence string `json:"version_evidence,omitempty"`
	IssuerEvidence  string `json:"issuer_evidence,omitempty"`
}

// ConflictVersionSuggestion is C3-Lite's advisory result. Resolution uses
// existing raw-conflict status strings but does not mutate Status or disable
// chunks; callers persist it in SuggestedResolution only.
type ConflictVersionSuggestion struct {
	Resolution string  `json:"resolution,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}
