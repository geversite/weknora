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
		NewChunk:      &types.Chunk{ID: "new-" + key, KnowledgeID: "new-doc", Content: "新片段"},
		ExistingChunk: &types.Chunk{ID: "old-" + key, KnowledgeID: "old-doc", Content: "旧片段"},
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

func TestAdjudicateConflictBatchRetriesMalformedResponse(t *testing.T) {
	model := &scriptedBatchChat{responses: []string{
		`not json`,
		`{"results":[{"id":"pair-000","conflict":true,"type":"fact_contradiction","reason":"互斥"}]}`,
	}}
	verdicts, err := adjudicateConflictBatch(context.Background(), model, []conflictPair{batchTestPair("供应实体", "两家", "唯一")})
	if err != nil {
		t.Fatalf("adjudicate batch: %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("batch calls=%d, want retry=2", model.calls)
	}
	if !verdicts["pair-000"].Conflict {
		t.Fatalf("verdict = %+v, want conflict", verdicts)
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
