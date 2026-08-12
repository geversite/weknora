package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// OpenAI-compatible request/response types
// ---------------------------------------------------------------------------

// OpenAIChatRequest mirrors the OpenAI /v1/chat/completions request schema.
// Dify sends a subset of these fields; extra fields are accepted but ignored.
type OpenAIChatRequest struct {
	Model       string          `json:"model" binding:"required"`
	Messages    []OpenAIMessage `json:"messages" binding:"required,min=1"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	// WeKnora-specific extensions (passed via extra_body in Dify):
	//   agent_id            — explicit WeKnora agent ID (overrides model mapping)
	//   knowledge_base_ids  — list of KB IDs to scope the RAG retrieval
	//   knowledge_ids       — list of specific knowledge (file) IDs
	//   web_search_enabled  — enable web search in this turn
	AgentID          string   `json:"agent_id,omitempty"`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids,omitempty"`
	KnowledgeIDs     []string `json:"knowledge_ids,omitempty"`
	WebSearchEnabled bool     `json:"web_search_enabled,omitempty"`
}

// OpenAIMessage represents a single message in the OpenAI chat schema.
type OpenAIMessage struct {
	Role    string `json:"role" binding:"required"`
	Content string `json:"content"`
}

// openaiChatChoice is one choice in a non-streaming response.
type openaiChatChoice struct {
	Index   int `json:"index"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

// openaiChatResponse is the full non-streaming response.
type openaiChatResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"` // "chat.completion"
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []openaiChatChoice `json:"choices"`
	// SessionID is returned so Dify can echo it back in the next request's
	// first message line to maintain conversation continuity.
	SessionID string `json:"session_id,omitempty"`
}

// openaiToolCallDelta mirrors the OpenAI streaming tool_call fragment schema.
type openaiToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"` // always "function"
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// openaiChunkChoice is one choice in a streaming response chunk.
type openaiChunkChoice struct {
	Index int `json:"index"`
	Delta struct {
		Role      string                `json:"role,omitempty"`
		Content   string                `json:"content,omitempty"`
		ToolCalls []openaiToolCallDelta `json:"tool_calls,omitempty"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

// openaiChatChunk is a single SSE chunk sent to the client.
type openaiChatChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"` // "chat.completion.chunk"
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []openaiChunkChoice `json:"choices"`
	// SessionID is included in the first chunk so Dify can echo it back
	// in subsequent requests to maintain conversation continuity.
	SessionID string `json:"session_id,omitempty"`
}

// openaiModelListResponse is the response for /v1/models.
type openaiModelListResponse struct {
	Object string        `json:"object"` // "list"
	Data   []openaiModel `json:"data"`
}

type openaiModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"` // "model"
	OwnedBy string `json:"owned_by"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// OpenAIChatCompletions implements the POST /v1/chat/completions endpoint.
//
// It accepts an OpenAI-compatible request body, resolves the WeKnora agent
// (from the `model` field or the explicit `agent_id` extension), creates a
// session on-the-fly, runs KnowledgeQA/AgentQA, and translates the internal
// SSE event stream into OpenAI-format chunks (stream=true) or a single JSON
// response (stream=false).
//
// Authentication is handled by the global Auth middleware (X-API-Key or
// Bearer token), so the tenant context is already populated when we enter.
func (h *Handler) OpenAIChatCompletions(c *gin.Context) {
	ctx := logger.CloneContext(c.Request.Context())

	var req OpenAIChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Errorf(ctx, "[openai] invalid request body: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Invalid request body: " + err.Error(),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// Resolve agent ID: explicit agent_id > model field
	agentID := req.AgentID
	if agentID == "" {
		agentID = req.Model
	}

	// Extract session ID from the first line of the last user message.
	// Convention: the first line may be "[sid:xxx]" where xxx is the session ID.
	// - If xxx is non-empty and the session exists → reuse it (multi-turn).
	// - If xxx is non-empty but session not found → create a new session.
	// - If no [sid:...] marker is present → create a new session.
	// The response always returns the session_id so Dify can echo it back.
	rawQuery, sessionIDHint := extractSessionIDFromMessages(req.Messages)
	if strings.TrimSpace(rawQuery) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "No user message found in request",
				"type":    "invalid_request_error",
			},
		})
		return
	}
	query := rawQuery

	requestID := secutils.SanitizeForLog(c.GetString(types.RequestIDContextKey.String()))
	if requestID == "" {
		requestID = uuid.New().String()
	}

	// Resolve tenant from context (set by Auth middleware)
	tenantID, exists := c.Get(types.TenantIDContextKey.String())
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"message": "Unauthorized", "type": "authentication_error"},
		})
		return
	}

	// Resolve or create session.
	// If sessionIDHint is provided and the session exists, reuse it so
	// conversation history is preserved across turns. Otherwise create a
	// new session (Dify will get the new session_id back in the response).
	var session *types.Session
	if sessionIDHint != "" {
		if existing, err := h.sessionService.GetOwnedSession(ctx, sessionIDHint); err == nil && existing != nil {
			session = existing
			logger.Infof(ctx, "[openai] reusing existing session: %s", session.ID)
		}
	}
	if session == nil {
		sessionTitle := truncateString(query, 50)
		createdSession := &types.Session{
			TenantID: tenantID.(uint64),
			Title:    sessionTitle,
		}
		// If the caller provided a session ID hint, use it as the session's
		// primary key so subsequent requests with the same hint can find it.
		// BeforeCreate only auto-generates when ID is empty.
		if sessionIDHint != "" {
			createdSession.ID = sessionIDHint
		}
		if ownerID := types.SessionOwnerIDFromContext(ctx); ownerID != "" {
			createdSession.UserID = ownerID
		}
		var err error
		session, err = h.sessionService.CreateSession(ctx, createdSession)
		if err != nil {
			logger.Errorf(ctx, "[openai] failed to create session: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{
					"message": "Failed to create session",
					"type":    "server_error",
				},
			})
			return
		}
		logger.Infof(ctx, "[openai] created new session: %s (hint=%s)", session.ID, sessionIDHint)
	}
	sessionID := session.ID

	// Resolve custom agent
	customAgent, effectiveTenantID, sharedAgentReadOnly := h.resolveAgent(ctx, c, agentID, 0)
	if agentID != "" && customAgent == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": fmt.Sprintf("Agent not found: %s", agentID),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// Use agent mode when a custom agent is resolved; otherwise normal RAG mode.
	useAgentMode := customAgent != nil && customAgent.IsAgentMode()

	// Create user message
	userMsg, err := h.createUserMessage(ctx, sessionID, query, requestID, nil, nil, nil, "api", nil)
	if err != nil {
		logger.Errorf(ctx, "[openai] failed to create user message: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": "Failed to create user message",
				"type":    "server_error",
			},
		})
		return
	}

	// Create assistant message (placeholder)
	assistantMsg := &types.Message{
		SessionID:   sessionID,
		Role:        "assistant",
		RequestID:   requestID,
		CreatedAt:   time.Now(),
		IsCompleted: false,
	}
	assistantMessagePtr, err := h.createAssistantMessage(ctx, assistantMsg)
	if err != nil {
		logger.Errorf(ctx, "[openai] failed to create assistant message: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": "Failed to create assistant message",
				"type":    "server_error",
			},
		})
		return
	}

	// Build the QA request context (reuse existing infrastructure)
	reqCtx := &qaRequestContext{
		ctx:                 ctx,
		c:                   c,
		sessionID:           sessionID,
		requestID:           requestID,
		query:               query,
		session:             session,
		customAgent:         customAgent,
		assistantMessage:    assistantMessagePtr,
		knowledgeBaseIDs:    req.KnowledgeBaseIDs,
		knowledgeIDs:        req.KnowledgeIDs,
		webSearchEnabled:    req.WebSearchEnabled,
		effectiveTenantID:   effectiveTenantID,
		sharedAgentReadOnly: sharedAgentReadOnly,
		channel:             "api",
		userMessageID:       userMsg.ID,
	}

	// Resolve KB IDs from agent config if not explicitly provided
	if customAgent != nil && len(reqCtx.knowledgeBaseIDs) == 0 {
		switch customAgent.Config.KBSelectionMode {
		case "all":
			// Agent will resolve all KBs internally; leave empty
		case "selected", "":
			reqCtx.knowledgeBaseIDs = customAgent.Config.KnowledgeBases
		case "none":
			// No KBs
		default:
			reqCtx.knowledgeBaseIDs = customAgent.Config.KnowledgeBases
		}
	}

	// For non-agent mode (normal RAG), we need a default agent config.
	// If no agent is resolved, we still need to run KnowledgeQA with default settings.
	mode := qaModeNormal
	if useAgentMode {
		mode = qaModeAgent
	}

	if req.Stream {
		h.openAIStreamQA(ctx, c, reqCtx, mode, req.Model)
	} else {
		h.openAINonStreamQA(ctx, c, reqCtx, mode, req.Model)
	}
}

// OpenAIModels implements the GET /v1/models endpoint.
// It lists all custom agents available to the caller, mapped as "models".
func (h *Handler) OpenAIModels(c *gin.Context) {
	ctx := c.Request.Context()

	agents, err := h.customAgentService.ListAgents(ctx)
	if err != nil {
		logger.Errorf(ctx, "[openai] failed to list agents: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": "Failed to list models",
				"type":    "server_error",
			},
		})
		return
	}

	models := make([]openaiModel, 0, len(agents))
	for _, agent := range agents {
		models = append(models, openaiModel{
			ID:      agent.ID,
			Object:  "model",
			OwnedBy: "weknora",
		})
	}

	c.JSON(http.StatusOK, openaiModelListResponse{
		Object: "list",
		Data:   models,
	})
}

// ---------------------------------------------------------------------------
// Internal: streaming QA
// ---------------------------------------------------------------------------

// openAIStreamQA runs the QA pipeline and translates the internal SSE event
// stream into OpenAI-compatible chat.completion.chunk SSE frames.
func (h *Handler) openAIStreamQA(
	ctx context.Context,
	c *gin.Context,
	reqCtx *qaRequestContext,
	mode qaMode,
	modelName string,
) {
	sessionID := reqCtx.sessionID
	assistantMessageID := reqCtx.assistantMessage.ID
	requestID := reqCtx.requestID

	// Set SSE headers
	setSSEHeaders(c)

	// Write initial agent_query event to StreamManager
	h.writeAgentQueryEvent(ctx, sessionID, assistantMessageID)

	// Build base context for async work
	baseCtx := ctx
	if reqCtx.effectiveTenantID != 0 && h.tenantService != nil {
		if tenant, err := h.tenantService.GetTenantByID(ctx, reqCtx.effectiveTenantID); err == nil && tenant != nil {
			baseCtx = context.WithValue(
				context.WithValue(ctx, types.TenantIDContextKey, reqCtx.effectiveTenantID),
				types.TenantInfoContextKey, tenant,
			)
		}
	}
	// Mark the context so the agent engine appends the <!FINAL_ANSWER>
	// constraint to the system prompt. This lets the OpenAI streaming
	// handler split reasoning (wrapped in <think>...</think> as delta.content)
	// from the final answer (delta.content directly) by detecting the marker.
	baseCtx = types.WithFinalAnswerMarker(baseCtx)

	eventBus := event.NewEventBus()
	asyncCtx, cancel := context.WithCancel(logger.CloneContext(baseCtx))
	defer cancel()

	// Setup stop event handler
	h.setupStopEventHandler(eventBus, sessionID, reqCtx.session.TenantID, reqCtx.assistantMessage, cancel)

	// Start stop watcher (survives client disconnect, self-terminates on complete)
	h.startStopWatcher(logger.CloneContext(baseCtx), sessionID, assistantMessageID, eventBus)

	// Setup stream handler: bridges EventBus events → StreamManager so
	// the pull-based polling loop can observe answer/thinking/complete events.
	h.setupStreamHandler(asyncCtx, sessionID, assistantMessageID, requestID,
		time.Now(), reqCtx.assistantMessage, eventBus)

	// Normal mode: register completion handler on EventAgentFinalAnswer
	if mode == qaModeNormal {
		var completionHandled bool
		eventBus.On(event.EventAgentFinalAnswer, func(ctx context.Context, evt event.Event) error {
			data, ok := evt.Data.(event.AgentFinalAnswerData)
			if !ok {
				return nil
			}
			reqCtx.assistantMessage.Content += data.Content
			if data.IsFallback {
				reqCtx.assistantMessage.IsFallback = true
			}
			if data.Done {
				if completionHandled {
					return nil
				}
				completionHandled = true
				updateCtx := context.WithValue(asyncCtx, types.TenantIDContextKey, reqCtx.session.TenantID)
				h.completeAssistantMessage(updateCtx, reqCtx.assistantMessage, reqCtx.query, reqCtx.knowledgeBaseIDs)
				eventBus.Emit(asyncCtx, event.Event{
					Type:      event.EventAgentComplete,
					SessionID: sessionID,
					Data:      event.AgentCompleteData{FinalAnswer: reqCtx.assistantMessage.Content},
				})
			}
			return nil
		})
	}

	// Send initial role chunk (includes session_id so Dify can echo it back)
	completionID := "chatcmpl-" + uuid.New().String()
	now := time.Now().Unix()
	h.writeOpenAIChunkWithSession(c, completionID, now, modelName, "assistant", "", nil, sessionID)

	// Execute QA asynchronously
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf(asyncCtx, "[openai] QA panicked: %v", r)
				eventBus.Emit(asyncCtx, event.Event{
					Type:      event.EventError,
					SessionID: sessionID,
					Data: event.ErrorData{
						Error:     fmt.Sprintf("internal error: %v", r),
						SessionID: sessionID,
					},
				})
			}
			if mode == qaModeAgent {
				updateCtx := context.WithValue(
					context.WithoutCancel(asyncCtx),
					types.TenantIDContextKey, reqCtx.session.TenantID,
				)
				h.completeAssistantMessage(updateCtx, reqCtx.assistantMessage, reqCtx.query, reqCtx.knowledgeBaseIDs)
			}
		}()

		qaReq := reqCtx.buildQARequest()
		var serviceErr error
		if mode == qaModeNormal {
			serviceErr = h.sessionService.KnowledgeQA(asyncCtx, qaReq, eventBus)
		} else {
			serviceErr = h.sessionService.AgentQA(asyncCtx, qaReq, eventBus)
		}
		if serviceErr != nil {
			if asyncCtx.Err() != nil {
				logger.Infof(asyncCtx, "[openai] QA cancelled for session: %s", sessionID)
			} else {
				logger.Errorf(asyncCtx, "[openai] QA service error: %v", serviceErr)
				eventBus.Emit(asyncCtx, event.Event{
					Type:      event.EventError,
					SessionID: sessionID,
					Data: event.ErrorData{
						Error:     serviceErr.Error(),
						SessionID: sessionID,
					},
				})
			}
		}
	}()

	// Poll StreamManager and translate events to OpenAI chunks (blocking)
	h.streamEventsToOpenAIChunks(c, ctx, sessionID, assistantMessageID, requestID, completionID, modelName, eventBus)
}

// finalAnswerMarker is the sentinel the LLM emits (constrained by the system
// prompt) immediately before the user-facing final answer. Everything before
// it is wrapped inside a <think>...</think> block and streamed as
// delta.content; everything after it is streamed as delta.content directly
// (the final answer).
//
// The marker itself is stripped — it must never reach the client.
// Alias for types.FinalAnswerMarker kept for readability within this file.
const finalAnswerMarker = types.FinalAnswerMarker

// streamEventsToOpenAIChunks polls the StreamManager and converts WeKnora
// stream events into OpenAI chat.completion.chunk SSE frames.
//
// Mapping strategy (optimistic <think> → marker → content):
//   - Before the <!FINAL_ANSWER> marker, all content (thinking, answer
//     preamble, tool-call narration) is wrapped inside a single
//     <think>...</think> block and streamed as delta.content.
//   - The opening <think> tag is emitted lazily (only when the first
//     reasoning chunk arrives) so we don't emit an empty think block.
//   - When the <!FINAL_ANSWER> marker is detected:
//     1. flush remaining buffered reasoning
//     2. emit </think>\n as delta.content (closes the think block)
//     3. switch: subsequent answer events → delta.content (the final answer)
//   - tool_call events → delta.tool_calls (still emitted separately)
//
// Because the LLM only emits the marker in the terminal round (system prompt
// constrains it to do so), all intermediate-round preamble text correctly
// lands inside the <think> block.
func (h *Handler) streamEventsToOpenAIChunks(
	c *gin.Context,
	ctx context.Context,
	sessionID, assistantMessageID, requestID, completionID, modelName string,
	eventBus *event.EventBus,
) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	lastOffset := 0
	now := time.Now().Unix()
	log := logger.GetLogger(ctx)

	// State machine
	finalAnswerStarted := false // becomes true after we see <!FINAL_ANSWER>
	thinkOpened := false        // becomes true after we emit the opening <think>

	// Reasoning buffer: accumulates content destined for the <think> block.
	// We flush it in two cases:
	//   (a) when we detect the <!FINAL_ANSWER> marker (split around it)
	//   (b) at the very end if the marker never appeared (graceful fallback)
	var reasoningBuf strings.Builder
	// fullAnswer accumulates only the final-answer text (post-marker) for logging.
	var fullAnswer strings.Builder

	// flushReasoning sends the accumulated reasoningBuf as delta.content.
	// On the very first flush it also prepends the opening <think> tag.
	flushReasoning := func() {
		if reasoningBuf.Len() == 0 && thinkOpened {
			return
		}
		prefix := ""
		if !thinkOpened {
			prefix = "<think>"
			thinkOpened = true
		}
		h.writeOpenAIChunk(c, completionID, now, modelName, "", prefix+reasoningBuf.String(), nil)
		reasoningBuf.Reset()
	}

	// closeThink flushes any remaining reasoning buffer, then emits
	// </think>\n as delta.content to close the think block.
	closeThink := func() {
		flushReasoning()
		if thinkOpened {
			h.writeOpenAIChunk(c, completionID, now, modelName, "", "</think>\n", nil)
		}
	}

	// emitAnswerChunk routes content to the <think> block (pre-marker,
	// optimistic) or delta.content (post-marker). It also scans for the
	// <!FINAL_ANSWER> marker which may be split across streaming chunks.
	var markerScanBuf strings.Builder // accumulates content for marker scanning

	emitAnswerChunk := func(content string) {
		if content == "" {
			return
		}
		if finalAnswerStarted {
			// Post-marker: route straight to content
			fullAnswer.WriteString(content)
			h.writeOpenAIChunk(c, completionID, now, modelName, "", content, nil)
			return
		}
		// Pre-marker (optimistic): buffer and scan for the marker.
		markerScanBuf.WriteString(content)
		scanned := markerScanBuf.String()
		markerScanBuf.Reset()

		idx := strings.Index(scanned, finalAnswerMarker)
		if idx < 0 {
			// No marker found. But the marker could be split across chunks:
			// hold back a tail that could be the start of a partial marker
			// and re-inject it into markerScanBuf for the next chunk.
			keep, hold := splitPotentialPartialMarker(scanned, finalAnswerMarker)
			if keep != "" {
				reasoningBuf.WriteString(keep)
			}
			if hold != "" {
				markerScanBuf.WriteString(hold)
			}
			// Flush reasoning in reasonable batches to keep latency low.
			if reasoningBuf.Len() >= 256 {
				flushReasoning()
			}
			return
		}
		// Marker found! Split: [before-marker] → reasoning, [after-marker] → content.
		before := scanned[:idx]
		after := scanned[idx+len(finalAnswerMarker):]
		if before != "" {
			reasoningBuf.WriteString(before)
		}
		// Flush all accumulated reasoning and close the think block.
		closeThink()
		finalAnswerStarted = true
		log.Infof("[openai] <!FINAL_ANSWER> marker detected, switching to content mode, session=%s", sessionID)
		if after != "" {
			fullAnswer.WriteString(after)
			h.writeOpenAIChunk(c, completionID, now, modelName, "", after, nil)
		}
	}

	for {
		select {
		case <-c.Request.Context().Done():
			log.Infof("[openai] client disconnected, session=%s", sessionID)
			return

		case <-ticker.C:
			events, newOffset, err := h.streamManager.GetEvents(ctx, sessionID, assistantMessageID, lastOffset)
			if err != nil {
				log.Warnf("[openai] failed to get events: %v", err)
				continue
			}

			streamCompleted := false
			for _, evt := range events {
				// Check for stop event
				if evt.Type == types.ResponseType(event.EventStop) {
					if eventBus != nil {
						eventBus.Emit(ctx, event.Event{
							Type:      event.EventStop,
							SessionID: sessionID,
							Data: event.StopData{
								SessionID: sessionID,
								MessageID: assistantMessageID,
								Reason:    "user_requested",
							},
						})
					}
					// Graceful close: flush any pending reasoning
					if !finalAnswerStarted {
						closeThink()
					}
					finishReason := "stop"
					h.writeOpenAIChunk(c, completionID, now, modelName, "", "", &finishReason)
					c.Writer.Flush()
					return
				}

				// Check for completion
				if evt.Type == "complete" {
					streamCompleted = true
					continue
				}

				// Check for error
				if evt.Type == types.ResponseTypeError {
					errMsg := evt.Content
					if errMsg == "" {
						if d, ok := evt.Data["error"]; ok {
							errMsg = fmt.Sprintf("%v", d)
						}
					}
					// Send error as a chunk with finish_reason
					h.writeOpenAIChunkError(c, completionID, now, modelName, errMsg)
					c.Writer.Flush()
					return
				}

				// thinking events (LLM内生 reasoning_content) → <think> block
				if evt.Type == types.ResponseTypeThinking {
					if evt.Content != "" {
						reasoningBuf.WriteString(evt.Content)
						if reasoningBuf.Len() >= 256 {
							flushReasoning()
						}
					}
					continue
				}

				// tool_call events → delta.tool_calls (+ brief narration to <think>)
				if evt.Type == types.ResponseTypeToolCall {
					toolName, _ := evt.Data["tool_name"].(string)
					toolCallID, _ := evt.Data["tool_call_id"].(string)
					arguments, _ := evt.Data["arguments"].(string)
					// Emit tool_calls delta (index 0, single function call per event)
					tcDelta := openaiToolCallDelta{
						Index: 0,
						ID:    toolCallID,
						Type:  "function",
					}
					tcDelta.Function.Name = toolName
					tcDelta.Function.Arguments = arguments
					h.writeOpenAIChunkDelta(c, completionID, now, modelName, openaiChunkDelta{
						ToolCalls: []openaiToolCallDelta{tcDelta},
					}, nil, "")
					// Also add a brief narration to the <think> block so the
					// receiving client shows it inside the think section.
					narration := fmt.Sprintf("\n[calling tool: %s]\n", toolName)
					reasoningBuf.WriteString(narration)
					continue
				}

				// tool_result events → <think> block narration
				if evt.Type == types.ResponseTypeToolResult {
					toolName, _ := evt.Data["tool_name"].(string)
					success := true
					if s, ok := evt.Data["success"].(bool); ok {
						success = s
					}
					narration := fmt.Sprintf("[tool result: %s (success=%v)]\n", toolName, success)
					reasoningBuf.WriteString(narration)
					continue
				}

				// answer / final_answer events → optimistic <think> block
				// until <!FINAL_ANSWER> marker detected, then content.
				if evt.Type == types.ResponseTypeAnswer || evt.Type == types.ResponseType(event.EventAgentFinalAnswer) {
					emitAnswerChunk(evt.Content)
				}

				// Ignore other event types (references, reflection, etc.)
			}

			lastOffset = newOffset

			if streamCompleted {
				// Flush any tail reasoning content (marker never appeared)
				if !finalAnswerStarted {
					closeThink()
					// Fallback: if no final answer was extracted, the
					// reasoning content is all we have. Leave it inside
					// the <think> block — the client will display it.
					// This is a safety net — the system prompt should
					// prevent this in normal operation.
					if fullAnswer.Len() == 0 {
						log.Warnf("[openai] no <!FINAL_ANSWER> marker found; reasoning left in <think>, session=%s", sessionID)
					}
				} else {
					// Flush any tail content from the marker scan buffer
					tail := markerScanBuf.String()
					if tail != "" && finalAnswerStarted {
						fullAnswer.WriteString(tail)
						h.writeOpenAIChunk(c, completionID, now, modelName, "", tail, nil)
					}
				}
				// Send final chunk with finish_reason=stop
				finishReason := "stop"
				h.writeOpenAIChunk(c, completionID, now, modelName, "", "", &finishReason)
				c.Writer.Flush()
				log.Infof("[openai] stream completed, session=%s, final_answer_len=%d, marker_found=%v",
					sessionID, fullAnswer.Len(), finalAnswerStarted)
				return
			}
		}
	}
}

// splitPotentialPartialMarker checks if the tail of `content` could be the
// beginning of a partial `marker` (i.e. the marker is split across streaming
// chunks). Returns (keep, hold) where:
//   - keep: the portion that is safe to flush as reasoning content
//   - hold: the tail portion that might be the start of the marker and
//     should be buffered until the next chunk arrives
//
// If the content does not end with a prefix of the marker, keep=content and
// hold="".
func splitPotentialPartialMarker(content, marker string) (keep, hold string) {
	if len(marker) == 0 || len(content) == 0 {
		return content, ""
	}
	// Maximum overlap is len(marker)-1 characters.
	maxOverlap := len(marker) - 1
	if maxOverlap > len(content) {
		maxOverlap = len(content)
	}
	// Check from longest possible overlap down to 1.
	for n := maxOverlap; n >= 1; n-- {
		tail := content[len(content)-n:]
		if strings.HasPrefix(marker, tail) {
			// The tail is a prefix of the marker — hold it back.
			return content[:len(content)-n], tail
		}
	}
	return content, ""
}

// openaiChunkDelta carries the delta payload for a single OpenAI streaming chunk.
// It decouples callers from the inline struct literal so we can set
// tool_calls without rewriting the struct tags everywhere.
type openaiChunkDelta struct {
	Role      string
	Content   string
	ToolCalls []openaiToolCallDelta
}

// writeOpenAIChunk writes a single OpenAI chat.completion.chunk SSE frame.
func (h *Handler) writeOpenAIChunk(c *gin.Context, completionID string, created int64, model, role, content string, finishReason *string) {
	h.writeOpenAIChunkWithSession(c, completionID, created, model, role, content, finishReason, "")
}

// writeOpenAIChunkWithSession is like writeOpenAIChunk but also includes a
// session_id field in the JSON (used for the first chunk of a stream so
// Dify can echo it back for multi-turn conversations).
func (h *Handler) writeOpenAIChunkWithSession(c *gin.Context, completionID string, created int64, model, role, content string, finishReason *string, sessionID string) {
	delta := openaiChunkDelta{Role: role, Content: content}
	h.writeOpenAIChunkDelta(c, completionID, created, model, delta, finishReason, sessionID)
}

// writeOpenAIChunkDelta writes a single OpenAI chat.completion.chunk SSE frame
// using a full openaiChunkDelta, allowing tool_calls.
func (h *Handler) writeOpenAIChunkDelta(c *gin.Context, completionID string, created int64, model string, delta openaiChunkDelta, finishReason *string, sessionID string) {
	chunk := openaiChatChunk{
		ID:        completionID,
		Object:    "chat.completion.chunk",
		Created:   created,
		Model:     model,
		SessionID: sessionID,
		Choices: []openaiChunkChoice{
			{
				Index: 0,
				Delta: struct {
					Role      string                `json:"role,omitempty"`
					Content   string                `json:"content,omitempty"`
					ToolCalls []openaiToolCallDelta `json:"tool_calls,omitempty"`
				}{
					Role:      delta.Role,
					Content:   delta.Content,
					ToolCalls: delta.ToolCalls,
				},
				FinishReason: finishReason,
			},
		},
	}
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(c.Writer, "data: %s\n\n", string(data))
	c.Writer.Flush()
}

// writeOpenAIChunkError writes an error chunk in OpenAI format.
func (h *Handler) writeOpenAIChunkError(c *gin.Context, completionID string, created int64, model, errMsg string) {
	// OpenAI doesn't have a standard error chunk; we send the error message
	// as content and finish_reason=stop, then a data: [DONE] frame.
	h.writeOpenAIChunk(c, completionID, created, model, "", errMsg, nil)
	finishReason := "stop"
	h.writeOpenAIChunk(c, completionID, created, model, "", "", &finishReason)
	fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}

// ---------------------------------------------------------------------------
// Internal: non-streaming QA
// ---------------------------------------------------------------------------

// openAINonStreamQA runs the QA pipeline, collects the full response, and
// returns a single JSON object matching the OpenAI chat.completion schema.
func (h *Handler) openAINonStreamQA(
	ctx context.Context,
	c *gin.Context,
	reqCtx *qaRequestContext,
	mode qaMode,
	modelName string,
) {
	sessionID := reqCtx.sessionID
	assistantMessageID := reqCtx.assistantMessage.ID

	// Write initial event to StreamManager
	h.writeAgentQueryEvent(ctx, sessionID, assistantMessageID)

	// Build base context
	baseCtx := ctx
	if reqCtx.effectiveTenantID != 0 && h.tenantService != nil {
		if tenant, err := h.tenantService.GetTenantByID(ctx, reqCtx.effectiveTenantID); err == nil && tenant != nil {
			baseCtx = context.WithValue(
				context.WithValue(ctx, types.TenantIDContextKey, reqCtx.effectiveTenantID),
				types.TenantInfoContextKey, tenant,
			)
		}
	}
	// Mark context for <!FINAL_ANSWER> marker (same as streaming path).
	baseCtx = types.WithFinalAnswerMarker(baseCtx)

	eventBus := event.NewEventBus()
	asyncCtx, cancel := context.WithCancel(logger.CloneContext(baseCtx))
	defer cancel()

	h.setupStopEventHandler(eventBus, sessionID, reqCtx.session.TenantID, reqCtx.assistantMessage, cancel)

	// Start stop watcher
	h.startStopWatcher(logger.CloneContext(baseCtx), sessionID, assistantMessageID, eventBus)

	// Setup stream handler: bridges EventBus events → StreamManager
	h.setupStreamHandler(asyncCtx, sessionID, assistantMessageID, reqCtx.requestID,
		time.Now(), reqCtx.assistantMessage, eventBus)

	// Collect the full answer content
	var fullContent strings.Builder
	var hasError bool
	var errorMsg string

	if mode == qaModeNormal {
		var completionHandled bool
		eventBus.On(event.EventAgentFinalAnswer, func(ctx context.Context, evt event.Event) error {
			data, ok := evt.Data.(event.AgentFinalAnswerData)
			if !ok {
				return nil
			}
			reqCtx.assistantMessage.Content += data.Content
			if data.IsFallback {
				reqCtx.assistantMessage.IsFallback = true
			}
			if data.Done {
				if completionHandled {
					return nil
				}
				completionHandled = true
				updateCtx := context.WithValue(asyncCtx, types.TenantIDContextKey, reqCtx.session.TenantID)
				h.completeAssistantMessage(updateCtx, reqCtx.assistantMessage, reqCtx.query, reqCtx.knowledgeBaseIDs)
				eventBus.Emit(asyncCtx, event.Event{
					Type:      event.EventAgentComplete,
					SessionID: sessionID,
					Data:      event.AgentCompleteData{FinalAnswer: reqCtx.assistantMessage.Content},
				})
			}
			return nil
		})
	}

	eventBus.On(event.EventError, func(ctx context.Context, evt event.Event) error {
		if data, ok := evt.Data.(event.ErrorData); ok {
			errorMsg = data.Error
			hasError = true
		}
		return nil
	})

	// Execute QA synchronously (block until done)
	qaReq := reqCtx.buildQARequest()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf(asyncCtx, "[openai] QA panicked: %v", r)
				eventBus.Emit(asyncCtx, event.Event{
					Type:      event.EventError,
					SessionID: sessionID,
					Data: event.ErrorData{
						Error:     fmt.Sprintf("internal error: %v", r),
						SessionID: sessionID,
					},
				})
			}
			if mode == qaModeAgent {
				updateCtx := context.WithValue(
					context.WithoutCancel(asyncCtx),
					types.TenantIDContextKey, reqCtx.session.TenantID,
				)
				h.completeAssistantMessage(updateCtx, reqCtx.assistantMessage, reqCtx.query, reqCtx.knowledgeBaseIDs)
			}
		}()

		var serviceErr error
		if mode == qaModeNormal {
			serviceErr = h.sessionService.KnowledgeQA(asyncCtx, qaReq, eventBus)
		} else {
			serviceErr = h.sessionService.AgentQA(asyncCtx, qaReq, eventBus)
		}
		if serviceErr != nil {
			if asyncCtx.Err() != nil {
				logger.Infof(asyncCtx, "[openai] QA cancelled for session: %s", sessionID)
			} else {
				logger.Errorf(asyncCtx, "[openai] QA service error: %v", serviceErr)
				eventBus.Emit(asyncCtx, event.Event{
					Type:      event.EventError,
					SessionID: sessionID,
					Data: event.ErrorData{
						Error:     serviceErr.Error(),
						SessionID: sessionID,
					},
				})
			}
		}
	}()

	// Poll StreamManager until complete or error
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	lastOffset := 0
	log := logger.GetLogger(ctx)
pollLoop:
	for {
		select {
		case <-c.Request.Context().Done():
			log.Infof("[openai] non-stream client disconnected, session=%s", sessionID)
			return
		case <-ticker.C:
			events, newOffset, err := h.streamManager.GetEvents(ctx, sessionID, assistantMessageID, lastOffset)
			if err != nil {
				continue
			}
			for _, evt := range events {
				if evt.Type == types.ResponseTypeAnswer || evt.Type == types.ResponseType(event.EventAgentFinalAnswer) {
					fullContent.WriteString(evt.Content)
				}
				if evt.Type == types.ResponseTypeError {
					if data, ok := evt.Data["error"]; ok {
						errorMsg = fmt.Sprintf("%v", data)
					}
					if evt.Content != "" {
						errorMsg = evt.Content
					}
					hasError = true
				}
			}
			lastOffset = newOffset

			// Check for completion
			done := false
			for _, evt := range events {
				if evt.Type == types.ResponseTypeComplete || evt.Type == types.ResponseType(event.EventStop) {
					done = true
					break
				}
			}
			if done || hasError {
				log.Infof("[openai] non-stream done, content_len=%d", fullContent.Len())
				break pollLoop
			}
		}
	}

	// Build response
	completionID := "chatcmpl-" + uuid.New().String()
	finishReason := "stop"

	if hasError {
		resp := openaiChatResponse{
			ID:        completionID,
			Object:    "chat.completion",
			Created:   time.Now().Unix(),
			Model:     modelName,
			SessionID: sessionID,
			Choices: []openaiChatChoice{
				{
					Index: 0,
					Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{
						Role:    "assistant",
						Content: fmt.Sprintf("[Error] %s", errorMsg),
					},
					FinishReason: finishReason,
				},
			},
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	// Split the accumulated content at the <!FINAL_ANSWER> marker.
	// Everything before it is wrapped in <think>...</think>; everything
	// after is the final answer. Both go into delta.content.
	rawContent := fullContent.String()
	reasoningPart := ""
	answerPart := ""
	idx := strings.Index(rawContent, finalAnswerMarker)
	if idx >= 0 {
		reasoningPart = rawContent[:idx]
		answerPart = rawContent[idx+len(finalAnswerMarker):]
		log.Infof("[openai] non-stream marker found, reasoning_len=%d, answer_len=%d",
			len(reasoningPart), len(answerPart))
	} else {
		// No marker — treat everything as the answer (safety fallback).
		answerPart = rawContent
		log.Warnf("[openai] non-stream no <!FINAL_ANSWER> marker found; using full content as answer, len=%d",
			len(answerPart))
	}

	// Combine: <think>reasoning</think>\n + answer
	var content string
	if reasoningPart != "" {
		content = "<think>" + reasoningPart + "</think>\n" + answerPart
	} else {
		content = answerPart
	}

	log.Infof("[openai] non-stream response ready, reasoning_len=%d, answer_len=%d, total_len=%d",
		len(reasoningPart), len(answerPart), len(content))

	resp := openaiChatResponse{
		ID:        completionID,
		Object:    "chat.completion",
		Created:   time.Now().Unix(),
		Model:     modelName,
		SessionID: sessionID,
		Choices: []openaiChatChoice{
			{
				Index: 0,
				Message: struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				}{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: finishReason,
			},
		},
	}

	c.JSON(http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildQueryFromMessages extracts the latest user message from the OpenAI
// messages array. If the last message is not from the user role, it falls
// back to concatenating all user messages.
func buildQueryFromMessages(messages []OpenAIMessage) string {
	// Find the last user message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	// Fallback: concatenate all user messages
	var parts []string
	for _, m := range messages {
		if m.Role == "user" {
			parts = append(parts, m.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// extractSessionIDFromMessages parses the last user message to extract an
// optional session ID marker and the actual query content.
//
// Convention: the first line of the user message may be "[sid:xxx]" where
// xxx is the session ID to reuse. The marker line is stripped from the query.
// If no marker is found, the full message content is returned as the query
// and the session ID hint is empty (a new session will be created).
//
// Examples:
//
//	"[sid:abc123]\nWhat is RAG?"  →  query="What is RAG?", hint="abc123"
//	"What is RAG?"                →  query="What is RAG?", hint=""
//	"[sid:]\nHello"               →  query="Hello",          hint=""
func extractSessionIDFromMessages(messages []OpenAIMessage) (query, sessionIDHint string) {
	raw := buildQueryFromMessages(messages)

	// Check if the first line is a [sid:xxx] marker.
	// We only look at the first line, not any subsequent line, so a "[sid:...]"
	// appearing mid-message is treated as regular content.
	lines := strings.SplitN(raw, "\n", 2)
	firstLine := strings.TrimSpace(lines[0])

	const prefix = "[sid:"
	const suffix = "]"
	if strings.HasPrefix(firstLine, prefix) && strings.HasSuffix(firstLine, suffix) {
		hint := strings.TrimSpace(firstLine[len(prefix) : len(firstLine)-len(suffix)])
		if len(lines) > 1 {
			query = strings.TrimSpace(lines[1])
		} else {
			query = ""
		}
		return query, hint
	}

	return raw, ""
}

// truncateString truncates a string to maxLen characters, appending "..." if truncated.
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// eventTypes returns a slice of event type strings for logging.
func eventTypes(events []interfaces.StreamEvent) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = string(e.Type)
	}
	return types
}
