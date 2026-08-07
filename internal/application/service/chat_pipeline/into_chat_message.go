package chatpipeline

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/Tencent/WeKnora/internal/searchutil"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
)

// PluginIntoChatMessage handles the transformation of search results into chat messages
type PluginIntoChatMessage struct {
	messageService interfaces.MessageService
	folderRepo     interfaces.KnowledgeFolderRepository // M4: folder context injection
	summaryRepo    interfaces.FolderSummaryRepository   // M4: folder summary lookup
}

// NewPluginIntoChatMessage creates and registers a new PluginIntoChatMessage instance
func NewPluginIntoChatMessage(eventManager *EventManager, messageService interfaces.MessageService,
	folderRepo interfaces.KnowledgeFolderRepository, summaryRepo interfaces.FolderSummaryRepository,
) *PluginIntoChatMessage {
	res := &PluginIntoChatMessage{
		messageService: messageService,
		folderRepo:     folderRepo,
		summaryRepo:    summaryRepo,
	}
	eventManager.Register(res)
	return res
}

// ActivationEvents returns the event types this plugin handles
func (p *PluginIntoChatMessage) ActivationEvents() []types.EventType {
	return []types.EventType{types.INTO_CHAT_MESSAGE}
}

// OnEvent processes the INTO_CHAT_MESSAGE event to format chat message content
func (p *PluginIntoChatMessage) OnEvent(ctx context.Context,
	eventType types.EventType, chatManage *types.ChatManage, next func() *PluginError,
) *PluginError {
	pipelineInfo(ctx, "IntoChatMessage", "input", map[string]interface{}{
		"session_id":       chatManage.SessionID,
		"merge_result_cnt": len(chatManage.MergeResult),
		"template_len":     len(chatManage.SummaryConfig.ContextTemplate),
	})

	// Separate FAQ and document results when FAQ priority is enabled
	var faqResults, docResults []*types.SearchResult
	var hasHighConfidenceFAQ bool

	if chatManage.FAQPriorityEnabled {
		for _, result := range chatManage.MergeResult {
			if result.ChunkType == string(types.ChunkTypeFAQ) {
				faqResults = append(faqResults, result)
				// Check if this FAQ has high confidence (above direct answer threshold)
				if result.Score >= chatManage.FAQDirectAnswerThreshold && !hasHighConfidenceFAQ {
					hasHighConfidenceFAQ = true
					pipelineInfo(ctx, "IntoChatMessage", "high_confidence_faq", map[string]interface{}{
						"chunk_id":  result.ID,
						"score":     fmt.Sprintf("%.4f", result.Score),
						"threshold": chatManage.FAQDirectAnswerThreshold,
					})
				}
			} else {
				docResults = append(docResults, result)
			}
		}
		pipelineInfo(ctx, "IntoChatMessage", "faq_separation", map[string]interface{}{
			"faq_count":           len(faqResults),
			"doc_count":           len(docResults),
			"has_high_confidence": hasHighConfidenceFAQ,
		})
	}

	// 验证用户查询的安全性
	safeQuery, isValid := utils.ValidateInput(chatManage.Query)
	if !isValid {
		pipelineWarn(ctx, "IntoChatMessage", "invalid_query", map[string]interface{}{
			"session_id": chatManage.SessionID,
		})
		return ErrTemplateExecute.WithError(fmt.Errorf("user query contains invalid content"))
	}

	// Intent-based no-search path: no retrieval results, but still render
	// through the context template so runtime metadata (current_time, etc.) is injected.
	if !chatManage.NeedsRetrieval() {
		userContent := safeQuery
		if rewrite := strings.TrimSpace(chatManage.RewriteQuery); rewrite != "" {
			if safeRewrite, ok := utils.ValidateInput(rewrite); ok {
				userContent = safeRewrite
			} else {
				pipelineWarn(ctx, "IntoChatMessage", "invalid_rewrite_query_fallback", map[string]interface{}{
					"session_id": chatManage.SessionID,
				})
			}
		}
		if chatManage.ImageDescription != "" && !chatManage.ChatModelSupportsVision {
			userContent += "\n\n[用户上传图片内容]\n" + chatManage.ImageDescription
		}
		if chatManage.QuotedContext != "" {
			userContent += "\n\n" + chatManage.QuotedContext
		}
		// Inject attachment content (documents, audio transcripts, etc.)
		if len(chatManage.Attachments) > 0 {
			userContent += chatManage.Attachments.BuildPrompt()
		}

		if tpl := chatManage.SummaryConfig.ContextTemplate; tpl != "" {
			chatManage.UserContent = types.RenderPromptPlaceholders(tpl, types.PlaceholderValues{
				"query":    userContent,
				"contexts": "",
				"language": chatManage.Language,
			})
		} else {
			chatManage.UserContent = userContent
		}

		pipelineInfo(ctx, "IntoChatMessage", "no_search_with_template", map[string]interface{}{
			"session_id":       chatManage.SessionID,
			"user_content_len": len(chatManage.UserContent),
			"has_template":     chatManage.SummaryConfig.ContextTemplate != "",
		})
		return next()
	}

	var contextsBuilder strings.Builder

	// Collect unique document metadata (title + description), once per knowledge
	allResults := chatManage.MergeResult
	if chatManage.FAQPriorityEnabled && len(faqResults) > 0 {
		allResults = append(faqResults, docResults...)
	}
	// M4-fix2: resolve each result's FolderID to its materialized path so
	// <document folder="..."> can be rendered. Best-effort.
	p.populateFolderPaths(ctx, allResults)
	docHeader := buildDocumentHeader(allResults)
	if docHeader != "" {
		contextsBuilder.WriteString(docHeader)
		contextsBuilder.WriteString("\n")
	}

	// M4: inject folder-level summaries for folders hit by retrieval, giving
	// the LLM sub-domain context (folder_contexts). Best-effort.
	if folderCtx := p.buildFolderContexts(ctx, allResults); folderCtx != "" {
		contextsBuilder.WriteString(folderCtx)
		contextsBuilder.WriteString("\n")
	}

	// M4-fix2: when retrieval returns zero hits, inject a folder overview so the
	// LLM understands what sub-domains exist in the KB and can guide the user
	// (e.g. "we have /产品线A/ — try narrowing to that folder"). Best-effort.
	if len(allResults) == 0 {
		if folderOverview := p.buildFolderOverview(ctx, chatManage.KnowledgeBaseIDs); folderOverview != "" {
			contextsBuilder.WriteString(folderOverview)
			contextsBuilder.WriteString("\n")
		}
	}

	// Build contexts string based on FAQ priority strategy
	if chatManage.FAQPriorityEnabled && len(faqResults) > 0 {
		contextsBuilder.WriteString("<source type=\"faq\" priority=\"high\">\n")
		for i, result := range faqResults {
			passage := getEnrichedPassageForChat(ctx, result)
			if hasHighConfidenceFAQ && i == 0 {
				contextsBuilder.WriteString(fmt.Sprintf("<context id=\"FAQ-%d\" match=\"exact\">%s</context>\n", i+1, passage))
			} else {
				contextsBuilder.WriteString(fmt.Sprintf("<context id=\"FAQ-%d\">%s</context>\n", i+1, passage))
			}
		}
		contextsBuilder.WriteString("</source>\n")

		if len(docResults) > 0 {
			contextsBuilder.WriteString("<source type=\"document\" priority=\"supplementary\">\n")
			for i, result := range docResults {
				passage := getEnrichedPassageForChat(ctx, result)
				contextsBuilder.WriteString(fmt.Sprintf("<context id=\"DOC-%d\">%s</context>\n", i+1, passage))
			}
			contextsBuilder.WriteString("</source>")
		}
	} else {
		for i, result := range chatManage.MergeResult {
			passage := getEnrichedPassageForChat(ctx, result)
			if i > 0 {
				contextsBuilder.WriteString("\n")
			}
			contextsBuilder.WriteString(fmt.Sprintf("<context id=\"%d\">%s</context>", i+1, passage))
		}
	}

	chatManage.RenderedContexts = contextsBuilder.String()

	// Replace placeholders in context template
	userContent := types.RenderPromptPlaceholders(chatManage.SummaryConfig.ContextTemplate, types.PlaceholderValues{
		"query":    safeQuery,
		"contexts": chatManage.RenderedContexts,
		"language": chatManage.Language,
	})

	// Append image description as text fallback only when the chat model cannot
	// process images directly. Vision-capable models see images via MultiContent.
	if chatManage.ImageDescription != "" && !chatManage.ChatModelSupportsVision {
		userContent += "\n\n[用户上传图片内容]\n" + chatManage.ImageDescription
	}
	if chatManage.QuotedContext != "" {
		userContent += "\n\n" + chatManage.QuotedContext
	}
	// Inject attachment content (documents, audio transcripts, etc.)
	if len(chatManage.Attachments) > 0 {
		userContent += chatManage.Attachments.BuildPrompt()
	}

	// Set formatted content back to chat management
	chatManage.UserContent = userContent
	pipelineInfo(ctx, "IntoChatMessage", "output", map[string]interface{}{
		"session_id":                 chatManage.SessionID,
		"user_content_len":           len(chatManage.UserContent),
		"faq_priority":               chatManage.FAQPriorityEnabled,
		"intent":                     chatManage.Intent,
		"image_description":          chatManage.ImageDescription,
		"chat_model_supports_vision": chatManage.ChatModelSupportsVision,
	})

	p.persistRenderedContent(ctx, chatManage)
	return next()
}

// persistRenderedContent asynchronously writes the RAG-augmented UserContent back
// to the user message so that subsequent conversation turns can see the full
// retrieval context in history.
func (p *PluginIntoChatMessage) persistRenderedContent(ctx context.Context, chatManage *types.ChatManage) {
	if chatManage.UserMessageID == "" || chatManage.UserContent == "" {
		pipelineInfo(ctx, "IntoChatMessage", "persist_rendered_content_skip", map[string]interface{}{
			"session_id":       chatManage.SessionID,
			"user_message_id":  chatManage.UserMessageID,
			"has_user_content": chatManage.UserContent != "",
			"reason":           "empty_id_or_content",
		})
		return
	}
	if chatManage.UserContent == chatManage.Query {
		return
	}
	pipelineInfo(ctx, "IntoChatMessage", "persist_rendered_content", map[string]interface{}{
		"session_id":           chatManage.SessionID,
		"user_message_id":      chatManage.UserMessageID,
		"rendered_content_len": len(chatManage.UserContent),
	})
	bgCtx := context.WithoutCancel(ctx)
	go func() {
		if err := p.messageService.UpdateMessageRenderedContent(
			bgCtx, chatManage.SessionID, chatManage.UserMessageID, chatManage.UserContent,
		); err != nil {
			pipelineWarn(bgCtx, "IntoChatMessage", "persist_rendered_content_error", map[string]interface{}{
				"session_id":      chatManage.SessionID,
				"user_message_id": chatManage.UserMessageID,
				"error":           err.Error(),
			})
		}
	}()
}

// buildDocumentHeader generates a document metadata section listing each unique
// knowledge document (by KnowledgeID) with its title and description.
// Returns an empty string when no meaningful metadata is available.
// populateFolderPaths resolves each result's FolderID to its materialized
// path (e.g. "/产品线A/协议规范/") using a batched, per-KB lookup (M4-fix2).
// It's best-effort: lookup failures leave FolderPath empty and the <document>
// tag simply omits the folder attribute.
func (p *PluginIntoChatMessage) populateFolderPaths(ctx context.Context, results []*types.SearchResult) {
	if p.folderRepo == nil || len(results) == 0 {
		return
	}
	tenantID := types.MustTenantIDFromContext(ctx)

	// Build distinct folder_id -> kb_id mapping.
	folderToKB := make(map[string]string)
	for _, r := range results {
		if r.FolderID == "" {
			continue
		}
		kbID := r.KnowledgeBaseID
		if kbID == "" {
			// KnowledgeBaseID may not be set on SearchResult; leave folder
			// unresolved rather than guessing.
			continue
		}
		folderToKB[r.FolderID] = kbID
	}
	if len(folderToKB) == 0 {
		return
	}

	// Group folder IDs by KB for one ListByKB call each.
	byKB := make(map[string][]string)
	for folderID, kbID := range folderToKB {
		byKB[kbID] = append(byKB[kbID], folderID)
	}

	// Map folder_id -> path across the involved KBs.
	pathByFolder := make(map[string]string, len(folderToKB))
	for kbID, folderIDs := range byKB {
		folders, err := p.folderRepo.ListByKB(ctx, tenantID, kbID)
		if err != nil {
			pipelineWarn(ctx, "IntoChatMessage", "folder_path_lookup_failed", map[string]interface{}{
				"kb_id": kbID, "err": err.Error(),
			})
			continue
		}
		want := make(map[string]struct{}, len(folderIDs))
		for _, id := range folderIDs {
			want[id] = struct{}{}
		}
		for _, f := range folders {
			if _, ok := want[f.ID]; ok {
				pathByFolder[f.ID] = f.Path
			}
		}
	}

	for _, r := range results {
		if p, ok := pathByFolder[r.FolderID]; ok {
			r.FolderPath = p
		}
	}
}

// buildFolderOverview renders a compact folder outline of the given KBs so the
// LLM has structural awareness even when retrieval returns zero hits (M4-fix2).
// Best-effort — returns "" when no KBs / no folders are available.
func (p *PluginIntoChatMessage) buildFolderOverview(ctx context.Context, kbIDs []string) string {
	if p.folderRepo == nil || len(kbIDs) == 0 {
		return ""
	}
	tenantID := types.MustTenantIDFromContext(ctx)

	var b strings.Builder
	b.WriteString("<folder_overview>\n")
	written := false
	for _, kbID := range kbIDs {
		folders, err := p.folderRepo.ListByKB(ctx, tenantID, kbID)
		if err != nil || len(folders) == 0 {
			continue
		}
		written = true
		b.WriteString(fmt.Sprintf("<kb id=\"%s\">\n", html.EscapeString(kbID)))
		// Keep it shallow & token-efficient: root-level dirs plus one level down.
		for _, f := range folders {
			if f.Depth > 2 {
				continue
			}
			indent := strings.Repeat("  ", f.Depth)
			b.WriteString(fmt.Sprintf("%s- %s\n", indent, html.EscapeString(f.Name)))
		}
		b.WriteString("</kb>\n")
	}
	b.WriteString("</folder_overview>")
	if !written {
		return ""
	}
	return b.String()
}

func buildDocumentHeader(results []*types.SearchResult) string {
	type docMeta struct {
		title       string
		description string
		metadata    string
		folder      string // M4-fix2: materialized path of the file's folder
	}

	seen := make(map[string]struct{})
	var docs []docMeta

	for _, r := range results {
		if r.KnowledgeID == "" {
			continue
		}
		if _, ok := seen[r.KnowledgeID]; ok {
			continue
		}
		seen[r.KnowledgeID] = struct{}{}

		title := r.KnowledgeTitle
		if title == "" {
			title = r.KnowledgeFilename
		}
		if title == "" {
			continue
		}

		docs = append(docs, docMeta{
			title:       title,
			description: r.KnowledgeDescription,
			metadata:    r.KnowledgeCustomMetadata,
			folder:      r.FolderPath,
		})
	}

	if len(docs) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<documents>\n")
	for _, d := range docs {
		// M4-fix2: include the file's folder path as an attribute so the LLM
		// can reason about which folder each document lives in.
		if d.folder != "" {
			b.WriteString(fmt.Sprintf("<document folder=\"%s\">\n", html.EscapeString(d.folder)))
		} else {
			b.WriteString("<document>\n")
		}
		b.WriteString(fmt.Sprintf("<title>%s</title>\n", html.EscapeString(d.title)))
		if d.description != "" {
			b.WriteString(fmt.Sprintf("<description>%s</description>\n", html.EscapeString(d.description)))
		}
		if d.metadata != "" {
			b.WriteString(fmt.Sprintf("<metadata>%s</metadata>\n", html.EscapeString(d.metadata)))
		}
		b.WriteString("</document>\n")
	}
	b.WriteString("</documents>")
	return b.String()
}

// buildFolderContexts collects folder_ids from search results and injects the
// folders' summaries as <folder_contexts> XML (M4). Returns "" when no folders
// with summaries are hit.
func (p *PluginIntoChatMessage) buildFolderContexts(ctx context.Context, results []*types.SearchResult) string {
	if p.folderRepo == nil || p.summaryRepo == nil {
		return ""
	}
	seen := make(map[string]struct{})
	var folderIDs []string
	// M4-fix2: track which knowledge files were hit per folder so <folder_contexts>
	// can map each summary back to the concrete documents that surfaced it.
	hitsByFolder := make(map[string][]*types.SearchResult)
	for _, r := range results {
		if r.FolderID == "" {
			continue
		}
		if _, ok := seen[r.FolderID]; !ok {
			seen[r.FolderID] = struct{}{}
			folderIDs = append(folderIDs, r.FolderID)
		}
		hitsByFolder[r.FolderID] = append(hitsByFolder[r.FolderID], r)
	}
	if len(folderIDs) == 0 {
		return ""
	}
	summaries, err := p.summaryRepo.GetByFolderIDs(ctx, folderIDs)
	if err != nil || len(summaries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<folder_contexts>\n")
	for _, fs := range summaries {
		if fs == nil || fs.Content == "" {
			continue
		}
		folder, ferr := p.folderRepo.GetByID(ctx, fs.FolderID)
		if ferr != nil || folder == nil {
			continue
		}
		b.WriteString("<folder>\n")
		b.WriteString(fmt.Sprintf("<name>%s</name>\n", html.EscapeString(folder.Name)))
		b.WriteString(fmt.Sprintf("<path>%s</path>\n", html.EscapeString(folder.Path)))
		b.WriteString(fmt.Sprintf("<summary>%s</summary>\n", html.EscapeString(fs.Content)))
		// M4-fix2: list the concrete files that hit this folder so the LLM can
		// tie the summary to specific documents.
		if hits := hitsByFolder[folder.ID]; len(hits) > 0 {
			b.WriteString("<hit_documents>\n")
			docSeen := make(map[string]struct{})
			for _, hit := range hits {
				if hit.KnowledgeID == "" {
					continue
				}
				if _, ok := docSeen[hit.KnowledgeID]; ok {
					continue
				}
				docSeen[hit.KnowledgeID] = struct{}{}
				title := hit.KnowledgeTitle
				if title == "" {
					title = hit.KnowledgeFilename
				}
				if title == "" {
					continue
				}
				b.WriteString(fmt.Sprintf("<doc knowledge_id=\"%s\" title=\"%s\" />\n",
					html.EscapeString(hit.KnowledgeID), html.EscapeString(title)))
			}
			b.WriteString("</hit_documents>\n")
		}
		b.WriteString("</folder>\n")
	}
	b.WriteString("</folder_contexts>")
	return b.String()
}

// getEnrichedPassageForChat 合并Content和ImageInfo的文本内容，为聊天消息准备
func getEnrichedPassageForChat(ctx context.Context, result *types.SearchResult) string {
	// 如果没有图片信息，直接返回内容
	if result.Content == "" && result.ImageInfo == "" {
		return ""
	}

	// 如果只有内容，没有图片信息
	if result.ImageInfo == "" {
		return result.Content
	}

	// 处理图片信息并与内容合并
	return enrichContentWithImageInfo(ctx, result.Content, result.ImageInfo)
}

// enrichContentWithImageInfo delegates to the shared searchutil implementation.
func enrichContentWithImageInfo(_ context.Context, content string, imageInfoJSON string) string {
	return searchutil.EnrichContentWithImageInfoForChat(content, imageInfoJSON)
}
