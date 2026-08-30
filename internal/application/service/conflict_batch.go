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
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	conflictBatchSize       = 8
	conflictBatchMaxTokens  = 2200
	conflictBatchMaxRetries = 1

	// A positive batched verdict must quote a substantive source excerpt from
	// both sides. Small limits avoid turning the batch response into a second
	// copy of every chunk while still requiring grounded evidence.
	conflictBatchEvidenceQuoteMinRunes = 6
	conflictBatchEvidenceQuoteMaxRunes = 160
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

	// EvidenceA/EvidenceB are required only for a positive batch verdict.
	// They must be short, verbatim excerpts from the matching A/B source
	// chunks. This makes a batch verdict auditable and blocks the common
	// open-world error where an LLM treats a file's silence as a contradiction.
	EvidenceA string `json:"evidence_a"`
	EvidenceB string `json:"evidence_b"`
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
		if err := validateConflictBatchVerdictEvidence(verdicts, pairs); err != nil {
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

// validateConflictBatchVerdictEvidence makes positive batch results
// proof-carrying. A model may still decide whether two propositions conflict,
// but it may not infer a contradiction from an omitted statement, a document
// title, or facts belonging to a different batch item. Invalid evidence makes
// the entire batch take the already-proven C1 single-pair fallback path.
func validateConflictBatchVerdictEvidence(
	verdicts map[string]conflictBatchVerdict,
	pairs []conflictPair,
) error {
	for index, pair := range pairs {
		id := batchCandidateID(index)
		verdict, ok := verdicts[id]
		if !ok {
			return fmt.Errorf("missing verdict %q during evidence validation", id)
		}
		if !verdict.Conflict {
			continue
		}
		if pair.NewChunk == nil || pair.ExistingChunk == nil {
			return fmt.Errorf("positive batch verdict %q has an incomplete chunk pair", id)
		}
		if !batchEvidenceQuoteMatches(pair.NewChunk.Content, verdict.EvidenceA) {
			return fmt.Errorf("positive batch verdict %q lacks a valid evidence_a quote", id)
		}
		if !batchEvidenceQuoteMatches(pair.ExistingChunk.Content, verdict.EvidenceB) {
			return fmt.Errorf("positive batch verdict %q lacks a valid evidence_b quote", id)
		}
	}
	return nil
}

func batchEvidenceQuoteMatches(source, quote string) bool {
	normalizedQuote := normalizeBatchEvidenceText(quote)
	if runeCount(normalizedQuote) < conflictBatchEvidenceQuoteMinRunes ||
		runeCount(normalizedQuote) > conflictBatchEvidenceQuoteMaxRunes {
		return false
	}
	return strings.Contains(normalizeBatchEvidenceText(source), normalizedQuote)
}

// normalizeBatchEvidenceText allows harmless formatting differences in a
// quoted source span (Markdown markers, whitespace, full-width punctuation)
// but deliberately retains words and numbers. It is only a grounding check,
// not a semantic contradiction classifier.
func normalizeBatchEvidenceText(value string) string {
	value = strings.ToLower(norm.NFKC.String(strings.TrimSpace(value)))
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func runeCount(value string) int {
	return len([]rune(value))
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

const conflictBatchAdjudicationSystemPrompt = `你是知识库一致性批量审查助手。你会收到多个候选对，每个都有唯一 id。候选仅表示检索相关，绝不预设为矛盾；多数语义召回候选应为 conflict=false。

每个 id 必须完全独立判断：只能比较该 id 内片段 A 与片段 B 的明确陈述，严禁借用其他 id、标题、常识或隐含价值判断的事实。

规则：
1. 只有 A 和 B 都明确陈述“同一主体 + 同一事实维度 + 适用范围不明显互斥”的原子事实，且两个取值、结论或状态不能同时成立时，才可返回 conflict=true。
2. 一侧没有提及某件事、较为笼统、未预留某个前提、没有说明风险，均不是否定，必须返回 conflict=false。禁止把沉默、推断、组织形象、伦理评价或常识补全当作矛盾。
3. 标有“候选声明证据”的条目有直接同槽事实锚点；该锚点的 value/value_norm 不同且限定词不明显互斥时，应在 fact_contradiction 与 version_update 之间选择，不能因片段存在其他背景而判 false。
4. 标有“semantic_fallback”的条目只有语义检索相关性、没有直接同槽锚点。计划与建议、风险/副作用与安全措施、原因与结果、不同阶段或不同时间的记录、互补技术事实，都不是矛盾；无法指出两侧明确互斥的原子事实时必须返回 conflict=false。
5. 明确的更新、修订、推迟、替代关系用 version_update；同槽位互斥且无替代关系用 fact_contradiction；大体一致但某一点直接互斥用 partial_contradiction。
6. 对每个 conflict=true，必须提供 evidence_a 和 evidence_b：分别是片段 A/B 中连续的原文短引（每段 6 至 160 个字，不能改写）。reason 必须基于这两段引文说明互斥点。conflict=false 时 evidence_a/evidence_b 留空即可。
7. 仅输出 JSON：{"results":[{"id":"pair-000","conflict":true,"type":"fact_contradiction","reason":"中文一句话","evidence_a":"A 中原文短引","evidence_b":"B 中原文短引"}]}。results 必须恰好包含每个输入 id 一次。`

func buildConflictBatchAdjudicationPrompt(pairs []conflictPair) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "候选数量：%d。请按输入顺序逐项返回。\n\n", len(pairs))
	for index, pair := range pairs {
		fmt.Fprintf(&builder, "=== id: %s ===\n", batchCandidateID(index))
		if evidence := renderClaimEvidence(pair); evidence != "" {
			builder.WriteString("候选来源：claim_key（有直接同槽声明证据）。\n")
			builder.WriteString(evidence)
		} else {
			builder.WriteString("候选来源：semantic_fallback（仅语义检索相关，不是冲突证据；默认应判 conflict=false）。\n")
		}
		fmt.Fprintf(&builder, "片段 A（新文件：%s）：\n\"\"\"\n%s\n\"\"\"\n",
			pair.NewTitle, conflictBatchChunkContent(pair.NewChunk))
		fmt.Fprintf(&builder, "片段 B（已有文件：%s）：\n\"\"\"\n%s\n\"\"\"\n\n",
			pair.ExistingTitle, conflictBatchChunkContent(pair.ExistingChunk))
	}
	return builder.String()
}

func conflictBatchChunkContent(chunk *types.Chunk) string {
	if chunk == nil {
		return ""
	}
	return conflictTruncateRunes(chunk.Content, 1200)
}
