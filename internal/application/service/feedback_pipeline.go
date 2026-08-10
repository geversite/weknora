package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// FeedbackPipelineService runs the M5 automatic user-feedback-to-wiki
// pipeline: given a completed assistant turn it scans the user message for
// new factual info and, if found, appends a "用户补充" section to the most
// relevant wiki page and records an audit issue. Fully async, non-blocking,
// best-effort — any failure is logged and never affects the conversation.
type FeedbackPipelineService interface {
	RunFeedbackPipeline(ctx context.Context, params FeedbackParams)
}

// FeedbackParams carries the context needed to attribute one feedback write.
type FeedbackParams struct {
	TenantID        uint64
	KnowledgeBaseID string
	UserQuery       string         // 用户消息原文
	AssistantMsg    *types.Message // Agent 回答（供上下文参考）
	UserID          string
	SessionID       string
	MessageID       string
}

// feedbackContributionSection is the markdown heading appended to a wiki page
// by the feedback pipeline.
const feedbackContributionSection = "## 用户补充"

// feedbackReportedByPrefix marks issues created by the automatic feedback
// pipeline so admins can recognize and treat them as audit markers.
const feedbackReportedByPrefix = "user-feedback-pipeline:"

// feedbackSummaryNamespacePrefix protects auto-generated document-level
// summary pages from feedback pollution.
const feedbackSummaryNamespacePrefix = "summary/"

type feedbackPipelineService struct {
	wikiPageService interfaces.WikiPageService
	kbService       interfaces.KnowledgeBaseService
	modelService    interfaces.ModelService
}

// NewFeedbackPipelineService builds the M5 feedback pipeline service.
func NewFeedbackPipelineService(
	wikiPageService interfaces.WikiPageService,
	kbService interfaces.KnowledgeBaseService,
	modelService interfaces.ModelService,
) FeedbackPipelineService {
	return &feedbackPipelineService{
		wikiPageService: wikiPageService,
		kbService:       kbService,
		modelService:    modelService,
	}
}

// RunFeedbackPipeline scans one user message and, if it carries new factual
// info, appends it to the relevant wiki page and records an audit issue.
// It is designed to be invoked in a background goroutine.
func (s *feedbackPipelineService) RunFeedbackPipeline(ctx context.Context, p FeedbackParams) {
	// Short-circuit: nothing to scan, or the KB is not configured for feedback.
	if strings.TrimSpace(p.UserQuery) == "" {
		return
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, p.KnowledgeBaseID)
	if err != nil || kb == nil || !kb.IsUserFeedbackEnabled() {
		logger.Infof(ctx, "feedback pipeline: skip kb=%s (err=%v, enabled=%v)", p.KnowledgeBaseID, err, kb != nil && kb.IsUserFeedbackEnabled())
		return
	}

	// Resolve the LLM used for judging / planning. Falls back to the KB's
	// summary model, then the default conversation model is unavailable.
	model, err := s.resolveChatModel(ctx, kb)
	if err != nil {
		logger.Warnf(ctx, "feedback pipeline: chat model resolve failed for kb %s: %v", p.KnowledgeBaseID, err)
		return
	}

	// ── 第一层：有效性判断 ──
	verdict, err := s.judgeNewInfo(ctx, model, p.UserQuery)
	if err != nil {
		logger.Warnf(ctx, "feedback pipeline L1 failed for msg %s: %v", p.MessageID, err)
		return
	}
	logger.Infof(ctx, "feedback pipeline L1 verdict for msg %s: providesNewInfo=%v reason=%q", p.MessageID, verdict.ProvidesNewInfo, verdict.Reason)
	if !verdict.ProvidesNewInfo {
		return
	}

	// ── 第二层：定位挂靠页面 ──
	// SearchPages is a regex/term matcher and often returns nothing for a full
	// sentence of new facts, so when it misses we fall back to a bounded list of
	// the KB's authored pages (concept/entity) and let the L2 model pick the
	// target slug. This keeps feedback working on KBs whose lexical search is
	// weaker than the semantic intent of the user's contribution.
	candidates, err := s.wikiPageService.SearchPages(ctx, p.KnowledgeBaseID, p.UserQuery, 5)
	if err != nil {
		logger.Warnf(ctx, "feedback pipeline wiki_search failed for kb %s: %v", p.KnowledgeBaseID, err)
		return
	}
	if len(candidates) == 0 {
		candidates, err = s.feedbackFallbackCandidates(ctx, p.KnowledgeBaseID, 30)
		if err != nil {
			logger.Warnf(ctx, "feedback pipeline candidate fallback failed for kb %s: %v", p.KnowledgeBaseID, err)
			return
		}
	}
	// 无候选页面可挂靠时短路，省一次 LLM 调用。
	if len(candidates) == 0 {
		logger.Infof(ctx, "feedback pipeline: no wiki page candidates for kb %s, query=%q", p.KnowledgeBaseID, p.UserQuery)
		return
	}
	logger.Infof(ctx, "feedback pipeline: found %d wiki page candidates for kb %s", len(candidates), p.KnowledgeBaseID)

	plan, err := s.planContribution(ctx, model, p.UserQuery, candidates)
	if err != nil {
		logger.Warnf(ctx, "feedback pipeline L2 failed for msg %s: %v", p.MessageID, err)
		return
	}
	if plan.TargetSlug == "" {
		// 新实体，无既有页面可挂靠 → MVP 跳过（见设计 §七.4）。
		return
	}
	// 保护 summary 命名空间页面（文档级自动生成，不应被反哺污染）。
	if strings.HasPrefix(plan.TargetSlug, feedbackSummaryNamespacePrefix) {
		logger.Infof(ctx, "feedback pipeline: skip append to protected summary page %s", plan.TargetSlug)
		return
	}
	appendContent := strings.TrimSpace(plan.AppendContent)
	if appendContent == "" {
		return
	}

	// ── 第三层：写入 wiki 页面（复用 UpdatePage）──
	page, err := s.wikiPageService.GetPageBySlug(ctx, p.KnowledgeBaseID, plan.TargetSlug)
	if err != nil || page == nil {
		logger.Warnf(ctx, "feedback pipeline: page %s not found in kb %s: %v", plan.TargetSlug, p.KnowledgeBaseID, err)
		return
	}

	contributionID := "fb-" + uuid.NewString()
	appendBlock := fmt.Sprintf("\n\n%s\n\n> [反哺 @ session-%s msg-%s by user-%s · %s]\n> %s\n",
		feedbackContributionSection,
		p.SessionID, p.MessageID, p.UserID, time.Now().Format("2006-01-02"),
		appendContent)
	page.Content = page.Content + appendBlock
	// 在 Content 写入前预生成 contribution 溯源（issue_id 留空，后续回填）。
	types.AppendUserFeedback(page, types.WikiFeedbackContribution{
		ID:                contributionID,
		SectionAnchor:     feedbackContributionSection,
		SourceSessionID:   p.SessionID,
		SourceMessageID:   p.MessageID,
		ContributorUserID: p.UserID,
		ContributedAt:     time.Now(),
		Summary:           plan.Summary,
	})

	// 注入用户编辑身份：UpdatePage 据此记录 LastEditSource=user / LastEditorID。
	writeCtx := types.WithWikiEditSource(ctx, types.WikiEditSourceUser)
	writeCtx = context.WithValue(writeCtx, types.UserIDContextKey, p.UserID)

	updatedPage, err := s.wikiPageService.UpdatePage(writeCtx, page)
	if err != nil {
		logger.Warnf(ctx, "feedback pipeline UpdatePage failed for %s: %v", plan.TargetSlug, err)
		return
	}

	// ── 第四层：提交 issue（审计标记）──
	issue, err := s.wikiPageService.CreateIssue(ctx, &types.WikiPageIssue{
		TenantID:        p.TenantID,
		KnowledgeBaseID: p.KnowledgeBaseID,
		Slug:            plan.TargetSlug,
		IssueType:       "other",
		Description: fmt.Sprintf("对话反哺自动写入。来源: session-%s msg-%s by user-%s。摘要: %s",
			p.SessionID, p.MessageID, p.UserID, plan.Summary),
		ReportedBy: feedbackReportedByPrefix + p.UserID,
		Status:     "pending",
	})
	if err != nil {
		// issue 失败不回滚 wiki 写入（写入已生效，只是缺审计标记）。
		logger.Warnf(ctx, "feedback pipeline CreateIssue failed: %v", err)
	}

	// 回填 issue_id 到 PageMetadata（仅 metadata 变化，不触发版本递增）。
	if issue != nil {
		fb := types.UserFeedbackFromMetadata(updatedPage)
		if fb != nil {
			for i := range fb.Contributions {
				if fb.Contributions[i].ID == contributionID {
					fb.Contributions[i].IssueID = issue.ID
					break
				}
			}
			if metaJSON, mErr := marshalFeedbackMeta(updatedPage, fb); mErr == nil {
				updatedPage.PageMetadata = metaJSON
				_, _ = s.wikiPageService.UpdatePage(writeCtx, updatedPage)
			}
		}
	}

	logger.Infof(ctx, "feedback pipeline: appended to wiki page %s, contribution %s, issue %s",
		plan.TargetSlug, contributionID, func() string {
			if issue == nil {
				return "<none>"
			}
			return issue.ID
		}())
}

// resolveChatModel picks the chat model used for the L1 / L2 LLM calls. It
// reuses the KB's summary model (the same one the wiki ingest uses), since
// the feedback judging/planning work is summarization-shaped. When the KB has
// no summary model configured, the pipeline is skipped rather than guessed.
func (s *feedbackPipelineService) resolveChatModel(ctx context.Context, kb *types.KnowledgeBase) (chat.Chat, error) {
	modelID := ""
	if kb != nil {
		modelID = kb.SummaryModelID
	}
	if modelID == "" {
		return nil, fmt.Errorf("no summary model configured for kb %s", kbIDForError(kb))
	}
	return s.modelService.GetChatModel(ctx, modelID)
}

func kbIDForError(kb *types.KnowledgeBase) string {
	if kb == nil {
		return ""
	}
	return kb.ID
}

// feedbackFallbackCandidates returns a bounded list of authored (non-summary)
// wiki pages for a KB when term-based SearchPages comes up empty. It pulls
// concept + entity pages first (the most common contribution targets), then
// tops up with any other non-summary pages up to the cap.
func (s *feedbackPipelineService) feedbackFallbackCandidates(ctx context.Context, kbID string, cap int) ([]*types.WikiPage, error) {
	seen := make(map[string]struct{})
	var out []*types.WikiPage
	appendType := func(pageType string) {
		pages, err := s.wikiPageService.ListByType(ctx, kbID, pageType)
		if err != nil {
			return // best-effort: a missing type must not abort the whole scan
		}
		for _, p := range pages {
			if p == nil || len(out) >= cap {
				return
			}
			if _, ok := seen[p.Slug]; ok {
				continue
			}
			seen[p.Slug] = struct{}{}
			out = append(out, p)
		}
	}
	appendType(types.WikiPageTypeConcept)
	appendType(types.WikiPageTypeEntity)
	// Top-up with remaining authored types if under the cap. Summary pages are
	// deliberately excluded (they are auto-generated and protected from
	// feedback pollution).
	for _, pt := range []string{types.WikiPageTypeSynthesis, types.WikiPageTypeComparison} {
		if len(out) >= cap {
			break
		}
		pages, err := s.wikiPageService.ListByType(ctx, kbID, pt)
		if err != nil {
			continue
		}
		for _, p := range pages {
			if p == nil || len(out) >= cap {
				break
			}
			if _, ok := seen[p.Slug]; ok {
				continue
			}
			seen[p.Slug] = struct{}{}
			out = append(out, p)
		}
	}
	return out, nil
}

// l1Verdict is the JSON verdict emitted by the L1 judge prompt.
type l1Verdict struct {
	ProvidesNewInfo bool   `json:"provides_new_info"`
	Reason          string `json:"reason"`
}

func (s *feedbackPipelineService) judgeNewInfo(ctx context.Context, model chat.Chat, userMsg string) (*l1Verdict, error) {
	prompt := strings.ReplaceAll(agent.FeedbackL1JudgePrompt, "{{.UserMessage}}", userMsg)
	raw, err := s.generate(ctx, model, prompt)
	if err != nil {
		return nil, err
	}
	var v l1Verdict
	if err := json.Unmarshal([]byte(extractJSON(raw)), &v); err != nil {
		return nil, fmt.Errorf("parse L1 verdict: %w", err)
	}
	return &v, nil
}

// l2Plan is the JSON plan emitted by the L2 planning prompt.
type l2Plan struct {
	TargetSlug    string `json:"target_slug"`
	AppendContent string `json:"append_content"`
	Summary       string `json:"summary"`
	NewEntity     bool   `json:"new_entity"`
}

func (s *feedbackPipelineService) planContribution(ctx context.Context, model chat.Chat, userMsg string, candidates []*types.WikiPage) (*l2Plan, error) {
	var candText strings.Builder
	for _, p := range candidates {
		if p == nil {
			continue
		}
		fmt.Fprintf(&candText, "- %s | %s | %s\n", p.Slug, p.Title, p.Summary)
	}
	prompt := strings.ReplaceAll(agent.FeedbackL2PlanPrompt, "{{.Candidates}}", candText.String())
	prompt = strings.ReplaceAll(prompt, "{{.UserMessage}}", userMsg)
	raw, err := s.generate(ctx, model, prompt)
	if err != nil {
		return nil, err
	}
	var plan l2Plan
	if err := json.Unmarshal([]byte(extractJSON(raw)), &plan); err != nil {
		return nil, fmt.Errorf("parse L2 plan: %w", err)
	}
	return &plan, nil
}

// generate performs a single, non-streaming LLM call.
func (s *feedbackPipelineService) generate(ctx context.Context, model chat.Chat, prompt string) (string, error) {
	messages := []chat.Message{{Role: "user", Content: prompt}}
	resp, err := model.Chat(ctx, messages, &chat.ChatOptions{Temperature: 0.0})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("chat model returned nil response")
	}
	return resp.Content, nil
}

// marshalFeedbackMeta rewrites a page's PageMetadata with the supplied
// feedback block, preserving all other keys.
func marshalFeedbackMeta(page *types.WikiPage, fb *types.WikiUserFeedback) (types.JSON, error) {
	var meta map[string]json.RawMessage
	if len(page.PageMetadata) > 0 {
		_ = json.Unmarshal(page.PageMetadata, &meta)
	}
	if meta == nil {
		meta = map[string]json.RawMessage{}
	}
	fbRaw, err := json.Marshal(fb)
	if err != nil {
		return nil, err
	}
	meta["user_feedback"] = fbRaw
	return json.Marshal(meta)
}

// extractJSON returns the first balanced JSON object substring found in s.
// LLM outputs frequently wrap JSON in prose or code fences; this isolates the
// object so json.Unmarshal has a clean payload. Returns "{}" when none found.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "{}"
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return "{}"
}
