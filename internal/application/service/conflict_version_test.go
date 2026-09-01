package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestExtractConflictDocumentMetaUsesExplicitHeaderEvidence(t *testing.T) {
	meta := extractConflictDocumentMeta(
		nil,
		"doc-v2",
		"国内出差餐费补贴标准（V2.0）",
		"# 国内出差餐费补贴标准（V2.0）\n\n**发布机构**：天穹财团\n**生效日期**：2148年6月1日\n**版本号**：V2.0\n\n每日标准为 150 元。",
	)
	if meta.ParserVersion != types.ConflictVersionSuggestionVersion || meta.KnowledgeID != "doc-v2" {
		t.Fatalf("metadata identity = %+v", meta)
	}
	if meta.Issuer != "天穹财团" || meta.EffectiveDate != "2148-06-01" || meta.EffectiveDatePrecision != "day" || meta.Version != "2.0" {
		t.Fatalf("header metadata = %+v", meta)
	}
	if meta.EffectiveDateEvidence == "" || meta.IssuerEvidence == "" || meta.VersionEvidence == "" {
		t.Fatalf("explicit metadata evidence must be retained: %+v", meta)
	}
}

func TestExtractConflictDocumentMetaPrefersManualSourceContent(t *testing.T) {
	knowledge := &types.Knowledge{ID: "manual-doc", Title: "manual title"}
	if err := knowledge.SetManualMetadata(types.NewManualKnowledgeMetadata(
		"# 标准\n\n发布机构：天穹财团\n生效日期：2148年6月1日\n版本号：V2.0\n", "publish", 1,
	)); err != nil {
		t.Fatalf("SetManualMetadata: %v", err)
	}
	meta := extractConflictDocumentMeta(knowledge, "manual-doc", "fallback", "正文提到 2153 年")
	if meta.Issuer != "天穹财团" || meta.EffectiveDate != "2148-06-01" || meta.Version != "2.0" {
		t.Fatalf("manual source metadata = %+v", meta)
	}
}

func TestExtractConflictDocumentMetaDoesNotPromoteBodyFactDate(t *testing.T) {
	meta := extractConflictDocumentMeta(
		nil,
		"doc",
		"幽能引擎备忘录",
		"# 幽能引擎备忘录\n\n项目将在 2153 年进入原型机测试阶段。",
	)
	if meta.EffectiveDate != "" {
		t.Fatalf("arbitrary body fact date must not become document metadata: %+v", meta)
	}
}

func TestExtractConflictDocumentMetaReadsEditionDateFromTitle(t *testing.T) {
	meta := extractConflictDocumentMeta(nil, "doc", "员工行政事务 FAQ（2148年10月版）", "# FAQ")
	if meta.EffectiveDate != "2148-10" || meta.EffectiveDatePrecision != "month" {
		t.Fatalf("title edition date = %+v", meta)
	}
}

func TestSuggestConflictVersionResolutionUsesAlignedDateAndVersion(t *testing.T) {
	newer := types.ConflictDocumentMeta{
		Issuer: "天穹财团", EffectiveDate: "2148-06-01", EffectiveDatePrecision: "day", Version: "2.0",
	}
	older := types.ConflictDocumentMeta{
		Issuer: "天穹财团", EffectiveDate: "2148-01-01", EffectiveDatePrecision: "day", Version: "1.0",
	}
	suggestion := suggestConflictVersionResolution(newer, older)
	if suggestion.Resolution != types.ConflictStatusResolvedNewer || suggestion.Confidence < 0.95 {
		t.Fatalf("newer suggestion = %+v", suggestion)
	}
	if suggestion.Reason == "" {
		t.Fatal("suggestion must retain source-grounded reason")
	}
}

func TestSuggestConflictVersionResolutionRejectsIssuerMismatchAndMetadataDisagreement(t *testing.T) {
	issuerMismatch := suggestConflictVersionResolution(
		types.ConflictDocumentMeta{Issuer: "天穹财团", EffectiveDate: "2149-01-01", Version: "3"},
		types.ConflictDocumentMeta{Issuer: "新弦工业", EffectiveDate: "2148-01-01", Version: "1"},
	)
	if issuerMismatch.Resolution != "" {
		t.Fatalf("issuer mismatch must not suggest a winner: %+v", issuerMismatch)
	}

	disagreement := suggestConflictVersionResolution(
		types.ConflictDocumentMeta{Issuer: "天穹财团", EffectiveDate: "2149-01-01", Version: "1"},
		types.ConflictDocumentMeta{Issuer: "天穹财团", EffectiveDate: "2148-01-01", Version: "2"},
	)
	if disagreement.Resolution != "" {
		t.Fatalf("date/version disagreement must not suggest a winner: %+v", disagreement)
	}
}

func TestConflictVersionDateIntervalsAreConservative(t *testing.T) {
	// A full date inside an imprecise year overlaps that year interval, so it
	// is not enough to declare either document newer.
	if direction, _, ok := compareConflictEffectiveDates(
		types.ConflictDocumentMeta{EffectiveDate: "2148"},
		types.ConflictDocumentMeta{EffectiveDate: "2148-06-01"},
	); ok || direction != 0 {
		t.Fatalf("overlapping date intervals must remain incomparable: direction=%d ok=%t", direction, ok)
	}

	if direction, confidence, ok := compareConflictEffectiveDates(
		types.ConflictDocumentMeta{EffectiveDate: "2149"},
		types.ConflictDocumentMeta{EffectiveDate: "2148-06-01"},
	); !ok || direction != 1 || confidence < 0.85 {
		t.Fatalf("non-overlapping year/day intervals should compare: direction=%d confidence=%f ok=%t", direction, confidence, ok)
	}
}

func TestConflictAnchorSupportsVersionSuggestionExcludesUnanchoredChunkPair(t *testing.T) {
	if !conflictAnchorSupportsVersionSuggestion(conflictFactAnchor{AnchorKind: types.ConflictFactAnchorClaimKey}) {
		t.Fatal("claim-key anchor should support advisory version suggestion")
	}
	if conflictAnchorSupportsVersionSuggestion(conflictFactAnchor{AnchorKind: types.ConflictFactAnchorChunkPair}) {
		t.Fatal("unanchored chunk pair must not receive a version winner suggestion")
	}
}
