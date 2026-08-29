package service

// conflict_batch.go implements C2-B's batched LLM verifier. C2-A rule
// decisions are applied first; only ambiguous candidates reach this code.
// A malformed/partial batch response deliberately falls back to C1's proven
// per-pair verifier so batching can reduce cost without silently losing recall.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	conflictBatchSize       = 8
	conflictBatchMaxTokens  = 2200
	conflictBatchMaxRetries = 1
)

type conflictBatchCandidate struct {
	ID   string
	Pair conflictPair
}

type conflictBatchVerdict struct {
	ID       string `json:"id"`
	Conflict bool   `json:"conflict"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
}

type conflictBatchEnvelope struct {
	Results []conflictBatchVerdict `json:"results"`
}

// fineAdjudicateBatch is the C2-B LLM branch. Batch failures are bounded to a
// single batch and then delegate to fineAdjudicateSingle, preserving C1's retry
// and evidence prompt semantics for every pair in that batch.
func (s *KnowledgeConflictService) fineAdjudicateBatch(
	ctx context.Context,
	kb *types.KnowledgeBase,
	pairs []conflictPair,
	stats *conflictCascadeExecutionStats,
) []conflictPair {
	if kb == nil || len(pairs) == 0 {
		return nil
	}
	if len(pairs) > conflictFineMaxPairs {
		pairs = pairs[:conflictFineMaxPairs]
	}
	if kb.SummaryModelID == "" || s.modelService == nil {
		logger.GetLogger(ctx).Infof("[ConflictCascade] batch LLM unavailable for kb %s, report rule-only results", kb.ID)
		return nil
	}
	chatModel, err := s.modelService.GetChatModel(ctx, kb.SummaryModelID)
	if err != nil || chatModel == nil {
		logger.GetLogger(ctx).Warnf("[ConflictCascade] GetChatModel %s failed for batch: %v", kb.SummaryModelID, err)
		return nil
	}
	if stats != nil {
		stats.LLMPairCount += len(pairs)
	}

	out := make([]conflictPair, 0, len(pairs))
	for start := 0; start < len(pairs); start += conflictBatchSize {
		end := start + conflictBatchSize
		if end > len(pairs) {
			end = len(pairs)
		}
		batch := pairs[start:end]
		verdicts, err := adjudicateConflictBatch(ctx, chatModel, batch, stats)
		if err != nil {
			logger.GetLogger(ctx).Warnf(
				"[ConflictCascade] Batch adjudication %d-%d failed; fallback to per-pair C1 verifier: %v",
				start, end, err,
			)
			out = append(out, s.fineAdjudicateSingle(ctx, kb, batch, stats, true, false)...)
			continue
		}
		for index, pair := range batch {
			id := batchCandidateID(index)
			verdict := verdicts[id]
			if !verdict.Conflict {
				logger.GetLogger(ctx).Infof(
					"[ConflictCascade] Batch verdict new_knowledge=%s existing_knowledge=%s channel=%s claim_key=%q verdict=not_conflict",
					pair.NewChunk.KnowledgeID, pair.ExistingChunk.KnowledgeID, conflictPairChannel(pair), pair.ClaimKeyHit,
				)
				continue
			}
			logger.GetLogger(ctx).Infof(
				"[ConflictCascade] Batch verdict new_knowledge=%s existing_knowledge=%s channel=%s claim_key=%q verdict=%s",
				pair.NewChunk.KnowledgeID, pair.ExistingChunk.KnowledgeID, conflictPairChannel(pair), pair.ClaimKeyHit, verdict.Type,
			)
			out = append(out, conflictPairWithVerdict(pair, verdict.Type, verdict.Reason))
		}
	}
	return out
}

func adjudicateConflictBatch(
	ctx context.Context,
	chatModel chat.Chat,
	pairs []conflictPair,
	stats *conflictCascadeExecutionStats,
) (map[string]conflictBatchVerdict, error) {
	if len(pairs) == 0 {
		return map[string]conflictBatchVerdict{}, nil
	}
	messages := []chat.Message{
		{Role: "system", Content: conflictBatchAdjudicationSystemPrompt},
		{Role: "user", Content: buildConflictBatchAdjudicationPrompt(pairs)},
	}
	var lastErr error
	for attempt := 0; attempt <= conflictBatchMaxRetries; attempt++ {
		response, err := chatModel.Chat(ctx, messages, &chat.ChatOptions{
			Temperature: 0.1,
			MaxTokens:   conflictBatchMaxTokens,
		})
		if stats != nil {
			stats.addLLMResponse(response, true, false)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if response == nil || strings.TrimSpace(response.Content) == "" {
			lastErr = errors.New("empty batch chat response")
			continue
		}
		verdicts, err := parseConflictBatchVerdicts(response.Content, len(pairs))
		if err != nil {
			lastErr = err
			continue
		}
		return verdicts, nil
	}
	return nil, lastErr
}

// parseConflictBatchVerdicts accepts either the documented {"results": [...]}
// envelope or a bare result array, but requires every expected id exactly once.
// Missing entries must trigger safe per-pair fallback rather than treating them
// as implicit not-conflict decisions.
func parseConflictBatchVerdicts(reply string, expectedCount int) (map[string]conflictBatchVerdict, error) {
	reply = strings.TrimSpace(stripJSONFences(reply))
	if reply == "" {
		return nil, errors.New("empty batch verdict JSON")
	}
	var results []conflictBatchVerdict
	if strings.HasPrefix(reply, "[") {
		if err := json.Unmarshal([]byte(reply), &results); err != nil {
			return nil, fmt.Errorf("parse batch verdict array: %w", err)
		}
	} else {
		var envelope conflictBatchEnvelope
		if err := json.Unmarshal([]byte(reply), &envelope); err != nil {
			return nil, fmt.Errorf("parse batch verdict envelope: %w", err)
		}
		results = envelope.Results
	}
	if expectedCount < 0 {
		expectedCount = 0
	}
	if len(results) != expectedCount {
		return nil, fmt.Errorf("batch verdict count=%d, want %d", len(results), expectedCount)
	}

	out := make(map[string]conflictBatchVerdict, len(results))
	for _, result := range results {
		if _, ok := out[result.ID]; ok {
			return nil, fmt.Errorf("duplicate batch verdict id %q", result.ID)
		}
		if !isExpectedBatchCandidateID(result.ID, expectedCount) {
			return nil, fmt.Errorf("unexpected batch verdict id %q", result.ID)
		}
		if result.Conflict {
			result.Type = normalizeConflictType(result.Type)
		}
		out[result.ID] = result
	}
	for index := 0; index < expectedCount; index++ {
		if _, ok := out[batchCandidateID(index)]; !ok {
			return nil, fmt.Errorf("missing batch verdict id %q", batchCandidateID(index))
		}
	}
	return out, nil
}

func batchCandidateID(index int) string {
	return fmt.Sprintf("pair-%03d", index)
}

func isExpectedBatchCandidateID(id string, expectedCount int) bool {
	for index := 0; index < expectedCount; index++ {
		if id == batchCandidateID(index) {
			return true
		}
	}
	return false
}

func normalizeConflictType(value string) string {
	switch value {
	case types.ConflictTypeFactContradiction, types.ConflictTypePartialContradiction, types.ConflictTypeVersionUpdate:
		return value
	default:
		return types.ConflictTypeFactContradiction
	}
}

func conflictPairWithVerdict(pair conflictPair, conflictType, reason string) conflictPair {
	return conflictPair{
		NewChunk:           pair.NewChunk,
		ExistingChunk:      pair.ExistingChunk,
		NewTitle:           pair.NewTitle,
		ExistingTitle:      pair.ExistingTitle,
		ConflictType:       normalizeConflictType(conflictType),
		Reason:             reason,
		ClaimKeyHit:        pair.ClaimKeyHit,
		NewClaimIDs:        pair.NewClaimIDs,
		ExistClaimIDs:      pair.ExistClaimIDs,
		NewClaimEvidence:   pair.NewClaimEvidence,
		ExistClaimEvidence: pair.ExistClaimEvidence,
		ExistWikiSlug:      pair.ExistWikiSlug,
	}
}

const conflictBatchAdjudicationSystemPrompt = `你是知识库一致性批量审查助手。你会收到多个候选对，每个都有唯一 id。严格逐项判断，不得遗漏任何 id。
规则：
1. 仅当同一主体与同一事实维度给出互斥数值、结论或状态时 conflict=true。
2. 若候选带有“候选声明证据”，它是本次配对的直接事实锚点；同一声明槽位、限定词不明显互斥且 value/value_norm 不同，应在 fact_contradiction 与 version_update 之间选择，不得因为片段有其他背景直接判 false。
3. 明确的更新、修订、推迟、替代关系用 version_update；同槽位互斥且无替代关系用 fact_contradiction；局部冲突用 partial_contradiction。
4. 不同主体、明确不相交的适用范围、同值或互补事实必须 conflict=false。
5. 仅输出 JSON：{"results":[{"id":"pair-000","conflict":true,"type":"fact_contradiction","reason":"中文一句话"}]}。results 必须恰好包含每个输入 id 一次。`

func buildConflictBatchAdjudicationPrompt(pairs []conflictPair) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "候选数量：%d。请按输入顺序逐项返回。\n\n", len(pairs))
	for index, pair := range pairs {
		fmt.Fprintf(&builder, "=== id: %s ===\n", batchCandidateID(index))
		if evidence := renderClaimEvidence(pair); evidence != "" {
			builder.WriteString(evidence)
		}
		fmt.Fprintf(&builder, "片段 A（新文件：%s）：\n\"\"\"\n%s\n\"\"\"\n",
			pair.NewTitle, conflictTruncateRunes(pair.NewChunk.Content, 1200))
		fmt.Fprintf(&builder, "片段 B（已有文件：%s）：\n\"\"\"\n%s\n\"\"\"\n\n",
			pair.ExistingTitle, conflictTruncateRunes(pair.ExistingChunk.Content, 1200))
	}
	return builder.String()
}
