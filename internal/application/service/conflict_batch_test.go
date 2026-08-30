package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

type scriptedBatchChat struct {
	responses []string
	errors    []error
	calls     int
	last      []chat.Message
}

func (m *scriptedBatchChat) Chat(_ context.Context, messages []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
	m.last = append([]chat.Message(nil), messages...)
	index := m.calls
	m.calls++
	if index < len(m.errors) && m.errors[index] != nil {
		return nil, m.errors[index]
	}
	if index >= len(m.responses) {
		return nil, errors.New("unexpected batch chat call")
	}
	return &types.ChatResponse{Content: m.responses[index]}, nil
}

func (m *scriptedBatchChat) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	stream := make(chan types.StreamResponse)
	close(stream)
	return stream, nil
}

func (m *scriptedBatchChat) GetModelName() string { return "scripted-batch" }
func (m *scriptedBatchChat) GetModelID() string   { return "scripted-batch" }

func batchTestPair(key, newer, older string) conflictPair {
	return conflictPair{
		NewChunk:      &types.Chunk{ID: "new-" + key, KnowledgeID: "new-doc", Content: "新片段中的明确事实陈述"},
		ExistingChunk: &types.Chunk{ID: "old-" + key, KnowledgeID: "old-doc", Content: "旧片段中的明确事实陈述"},
		NewTitle:      "new-title",
		ExistingTitle: "old-title",
		ClaimKeyHit:   key,
		NewClaimEvidence: []claimEvidence{{
			ClaimKey: key, Subject: "主体", Predicate: "属性", Value: newer, ValueNorm: newer,
			ValueKind: types.ClaimValueKindText, Qualifiers: "{}",
		}},
		ExistClaimEvidence: []claimEvidence{{
			ClaimKey: key, Subject: "主体", Predicate: "属性", Value: older, ValueNorm: older,
			ValueKind: types.ClaimValueKindText, Qualifiers: "{}",
		}},
	}
}

func TestParseConflictBatchVerdictsAcceptsCompleteEnvelope(t *testing.T) {
	verdicts, err := parseConflictBatchVerdicts(`{"results":[
		{"id":"pair-000","conflict":true,"type":"version_update","reason":"更新"},
		{"id":"pair-001","conflict":false,"reason":"一致"}
	]}`, 2)
	if err != nil {
		t.Fatalf("parse batch verdicts: %v", err)
	}
	if len(verdicts) != 2 || verdicts["pair-000"].Type != types.ConflictTypeVersionUpdate || verdicts["pair-001"].Conflict {
		t.Fatalf("unexpected verdicts: %+v", verdicts)
	}
}

func TestParseConflictBatchVerdictsRejectsMissingOrDuplicateID(t *testing.T) {
	if _, err := parseConflictBatchVerdicts(`{"results":[{"id":"pair-000","conflict":true}]}`, 2); err == nil {
		t.Fatal("missing result must fail so caller falls back to per-pair adjudication")
	}
	if _, err := parseConflictBatchVerdicts(`[{"id":"pair-000","conflict":true},{"id":"pair-000","conflict":false}]`, 2); err == nil {
		t.Fatal("duplicate id must fail")
	}
}

func TestBuildConflictBatchPromptCarriesClaimEvidence(t *testing.T) {
	prompt := buildConflictBatchAdjudicationPrompt([]conflictPair{batchTestPair("工业级星晶供应实体", "两家", "唯一")})
	for _, want := range []string{"pair-000", "候选声明证据", "工业级星晶供应实体", "新片段", "旧片段"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("batch prompt missing %q: %s", want, prompt)
		}
	}
}

func TestBuildConflictBatchPromptMarksFallbackAsRetrievalOnly(t *testing.T) {
	pair := conflictPair{
		NewChunk:      &types.Chunk{ID: "new-fallback", KnowledgeID: "new-doc", Content: "新文件中的事实描述"},
		ExistingChunk: &types.Chunk{ID: "old-fallback", KnowledgeID: "old-doc", Content: "旧文件中的事实描述"},
		NewTitle:      "new-title",
		ExistingTitle: "old-title",
	}
	prompt := buildConflictBatchAdjudicationPrompt([]conflictPair{pair})
	for _, want := range []string{"semantic_fallback", "仅语义检索相关", "默认应判 conflict=false"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("fallback batch prompt missing %q: %s", want, prompt)
		}
	}
}

func TestValidateConflictBatchVerdictEvidenceRequiresVerbatimQuotes(t *testing.T) {
	pairs := []conflictPair{batchTestPair("供应实体", "两家", "唯一")}

	invalid := map[string]conflictBatchVerdict{
		"pair-000": {
			ID: "pair-000", Conflict: true,
			EvidenceA: "不存在的原文引用", EvidenceB: "旧片段中的明确事实陈述",
		},
	}
	if err := validateConflictBatchVerdictEvidence(invalid, pairs); err == nil {
		t.Fatal("positive verdict with invented evidence must be rejected")
	}

	valid := map[string]conflictBatchVerdict{
		"pair-000": {
			ID: "pair-000", Conflict: true,
			// Formatting is intentionally allowed to differ from the source.
			EvidenceA: "新片段中的 明确事实，陈述", EvidenceB: "旧片段中的明确事实陈述",
		},
	}
	if err := validateConflictBatchVerdictEvidence(valid, pairs); err != nil {
		t.Fatalf("verbatim evidence with formatting differences should pass: %v", err)
	}
}

func TestAdjudicateConflictBatchReturnsSelectiveEvidenceErrorAfterRetry(t *testing.T) {
	invalidBatch := `{"results":[
		{"id":"pair-000","conflict":true,"type":"fact_contradiction","reason":"未引用原文"},
		{"id":"pair-001","conflict":false,"reason":"没有冲突"}
	]}`
	model := &scriptedBatchChat{responses: []string{invalidBatch, invalidBatch}}
	var stats conflictCascadeExecutionStats
	verdicts, err := adjudicateConflictBatch(
		context.Background(), model,
		[]conflictPair{batchTestPair("A", "新值", "旧值"), batchTestPair("B", "新值", "旧值")},
		&stats,
	)
	if err == nil {
		t.Fatal("expected final ungrounded positive evidence error")
	}
	var evidenceErr *conflictBatchEvidenceValidationError
	if !errors.As(err, &evidenceErr) {
		t.Fatalf("error type = %T, want *conflictBatchEvidenceValidationError: %v", err, err)
	}
	if len(verdicts) != 2 || verdicts["pair-001"].Conflict {
		t.Fatalf("structurally valid batch verdicts must be retained: %+v", verdicts)
	}
	if _, ok := evidenceErr.InvalidIDs["pair-000"]; !ok || len(evidenceErr.InvalidIDs) != 1 {
		t.Fatalf("invalid ids = %+v, want pair-000 only", evidenceErr.InvalidIDs)
	}
	if model.calls != 2 || stats.LLMBatchCallCount != 2 {
		t.Fatalf("expected bounded retry before selective fallback: calls=%d stats=%+v", model.calls, stats)
	}
}

func TestFineAdjudicateBatchFallsBackOnlyForUngroundedPositive(t *testing.T) {
	invalidBatch := `{"results":[
		{"id":"pair-000","conflict":true,"type":"fact_contradiction","reason":"未引用原文"},
		{"id":"pair-001","conflict":false,"reason":"没有冲突"}
	]}`
	model := &scriptedBatchChat{responses: []string{
		invalidBatch,
		invalidBatch,
		`{"conflict":true,"type":"fact_contradiction","reason":"单对复核确认互斥"}`,
	}}
	service := &KnowledgeConflictService{modelService: &stubModelService{chatModel: model}}
	kb := &types.KnowledgeBase{ID: "kb", SummaryModelID: "model"}
	var stats conflictCascadeExecutionStats
	got := service.fineAdjudicateBatch(
		context.Background(), kb,
		[]conflictPair{batchTestPair("A", "新值", "旧值"), batchTestPair("B", "新值", "旧值")},
		&stats,
	)
	if len(got) != 1 || got[0].Reason != "单对复核确认互斥" {
		t.Fatalf("selective fallback result = %+v, want one single-verified pair", got)
	}
	if model.calls != 3 {
		t.Fatalf("chat calls=%d, want two batch attempts plus one single fallback", model.calls)
	}
	if stats.LLMPairCount != 2 || stats.LLMBatchCallCount != 2 ||
		stats.LLMSingleCallCount != 1 || stats.LLMSingleFallbackCount != 1 {
		t.Fatalf("selective fallback stats = %+v", stats)
	}
}

func TestFineAdjudicateBatchFallsBackWholeBatchForMalformedEnvelope(t *testing.T) {
	model := &scriptedBatchChat{responses: []string{
		`not json`,
		`not json`,
		`{"conflict":true,"type":"fact_contradiction","reason":"第一对单对复核"}`,
		`{"conflict":false,"reason":"第二对单对复核无冲突"}`,
	}}
	service := &KnowledgeConflictService{modelService: &stubModelService{chatModel: model}}
	kb := &types.KnowledgeBase{ID: "kb", SummaryModelID: "model"}
	var stats conflictCascadeExecutionStats
	got := service.fineAdjudicateBatch(
		context.Background(), kb,
		[]conflictPair{batchTestPair("A", "新值", "旧值"), batchTestPair("B", "新值", "旧值")},
		&stats,
	)
	if len(got) != 1 || got[0].Reason != "第一对单对复核" {
		t.Fatalf("whole-batch structural fallback result = %+v", got)
	}
	if model.calls != 4 {
		t.Fatalf("chat calls=%d, want two batch attempts plus two single fallbacks", model.calls)
	}
	if stats.LLMPairCount != 2 || stats.LLMBatchCallCount != 2 ||
		stats.LLMSingleCallCount != 2 || stats.LLMSingleFallbackCount != 2 {
		t.Fatalf("whole-batch fallback stats = %+v", stats)
	}
}

func TestAdjudicateConflictBatchRetriesMalformedResponse(t *testing.T) {
	model := &scriptedBatchChat{responses: []string{
		`not json`,
		`{"results":[{"id":"pair-000","conflict":true,"type":"fact_contradiction","reason":"两侧事实互斥","evidence_a":"新片段中的明确事实陈述","evidence_b":"旧片段中的明确事实陈述"}]}`,
	}}
	var stats conflictCascadeExecutionStats
	verdicts, err := adjudicateConflictBatch(context.Background(), model, []conflictPair{batchTestPair("供应实体", "两家", "唯一")}, &stats)
	if err != nil {
		t.Fatalf("adjudicate batch: %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("batch calls=%d, want retry=2", model.calls)
	}
	if stats.LLMBatchCallCount != 2 || stats.LLMSingleCallCount != 0 {
		t.Fatalf("batch stats=%+v, want two batch calls", stats)
	}
	if !verdicts["pair-000"].Conflict {
		t.Fatalf("verdict = %+v, want conflict", verdicts)
	}
}

func TestAdjudicateConflictBatchRetriesUngroundedPositive(t *testing.T) {
	model := &scriptedBatchChat{responses: []string{
		`{"results":[{"id":"pair-000","conflict":true,"type":"fact_contradiction","reason":"推断出的冲突"}]}`,
		`{"results":[{"id":"pair-000","conflict":true,"type":"fact_contradiction","reason":"两侧事实互斥","evidence_a":"新片段中的明确事实陈述","evidence_b":"旧片段中的明确事实陈述"}]}`,
	}}
	var stats conflictCascadeExecutionStats
	verdicts, err := adjudicateConflictBatch(
		context.Background(), model, []conflictPair{batchTestPair("供应实体", "两家", "唯一")}, &stats,
	)
	if err != nil {
		t.Fatalf("adjudicate batch after ungrounded retry: %v", err)
	}
	if model.calls != 2 || stats.LLMBatchCallCount != 2 {
		t.Fatalf("ungrounded positive should consume one bounded retry: calls=%d stats=%+v", model.calls, stats)
	}
	if !verdicts["pair-000"].Conflict {
		t.Fatalf("verdict = %+v, want grounded conflict", verdicts)
	}
}

func TestConflictPairWithVerdictPreservesClaimEvidence(t *testing.T) {
	pair := batchTestPair("供应实体", "两家", "唯一")
	got := conflictPairWithVerdict(pair, "unknown-type", "reason")
	if got.ConflictType != types.ConflictTypeFactContradiction {
		t.Fatalf("unknown type should safely normalize: %q", got.ConflictType)
	}
	if len(got.NewClaimEvidence) != 1 || len(got.ExistClaimEvidence) != 1 || got.NewTitle != "new-title" {
		t.Fatalf("pair evidence/title lost: %+v", got)
	}
}
