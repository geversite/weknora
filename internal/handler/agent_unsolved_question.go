package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AgentUnsolvedQuestionHandler exposes the agent-level "unsolved questions"
// surface: a list endpoint for the agent editor and a resolve toggle.
//
// The judgement itself is triggered via the message-scoped
// /sessions/{session_id}/messages/{message_id}/unsolved-judge endpoint so the
// frontend can fire it right after a reply completes (mirroring the
// message-suggestion Ensure flow).
type AgentUnsolvedQuestionHandler struct {
	service interfaces.AgentUnsolvedQuestionService
}

func NewAgentUnsolvedQuestionHandler(service interfaces.AgentUnsolvedQuestionService) *AgentUnsolvedQuestionHandler {
	return &AgentUnsolvedQuestionHandler{service: service}
}

type ensureUnsolvedJudgeRequest struct {
	Regenerate bool `json:"regenerate"`
}

// EnsureJudgement godoc
// @Summary      判定回答是否未解决问题
// @Description  对已完成的助手消息运行 LLM 判定，若回答未完善解决问题则记录为该智能体的未解决问题
// @Tags         会话
// @Accept       json
// @Produce      json
// @Param        session_id  path  string  true  "会话 ID"
// @Param        message_id  path  string  true  "助手消息 ID"
// @Param        request     body  ensureUnsolvedJudgeRequest  false  "判定选项"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /sessions/{session_id}/messages/{message_id}/unsolved-judge [post]
func (h *AgentUnsolvedQuestionHandler) EnsureJudgement(c *gin.Context) {
	var request ensureUnsolvedJudgeRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&request); err != nil {
			c.Error(apperrors.NewBadRequestError("invalid request body"))
			return
		}
	}
	record, err := h.service.EnsureJudgement(
		c.Request.Context(),
		secutils.SanitizeForLog(c.Param("session_id")),
		secutils.SanitizeForLog(c.Param("message_id")),
		request.Regenerate,
	)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": record})
}

// ListByAgent godoc
// @Summary      获取智能体的未解决问题列表
// @Description  返回该智能体名下的未解决问题（可筛选仅未解决）
// @Tags         智能体
// @Produce      json
// @Param        id             path   string  true   "智能体 ID"
// @Param        only_unsolved  query  bool    false  "仅返回未解决（resolved=false 且 status=unsolved）的记录"
// @Param        limit          query  int     false  "每页数量，默认 50，最大 200"
// @Param        offset         query  int     false  "偏移量"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /agents/{id}/unsolved-questions [get]
func (h *AgentUnsolvedQuestionHandler) ListByAgent(c *gin.Context) {
	agentID := secutils.SanitizeForLog(c.Param("id"))
	if agentID == "" {
		c.Error(apperrors.NewBadRequestError("agent id is required"))
		return
	}
	onlyUnsolved, _ := strconv.ParseBool(c.DefaultQuery("only_unsolved", "true"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	result, err := h.service.ListByAgent(c.Request.Context(), agentID, onlyUnsolved, limit, offset)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

type resolveUnsolvedRequest struct {
	Resolved bool `json:"resolved"`
}

// MarkResolved godoc
// @Summary      标记未解决问题为已处理/未处理
// @Description  切换单条未解决问题的 resolved 标记
// @Tags         智能体
// @Accept       json
// @Produce      json
// @Param        id          path  string                  true  "智能体 ID"
// @Param        question_id path  string                  true  "未解决问题 ID"
// @Param        request     body  resolveUnsolvedRequest  true  "目标状态"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /agents/{id}/unsolved-questions/{question_id}/resolve [put]
func (h *AgentUnsolvedQuestionHandler) MarkResolved(c *gin.Context) {
	agentID := secutils.SanitizeForLog(c.Param("id"))
	questionID := secutils.SanitizeForLog(c.Param("question_id"))
	if agentID == "" || questionID == "" {
		c.Error(apperrors.NewBadRequestError("agent id and question id are required"))
		return
	}
	var request resolveUnsolvedRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewBadRequestError("invalid request body"))
		return
	}
	if err := h.service.MarkResolved(c.Request.Context(), agentID, questionID, request.Resolved); err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *AgentUnsolvedQuestionHandler) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.Error(apperrors.NewNotFoundError("unsolved question not found"))
	case strings.Contains(err.Error(), "completed assistant"),
		strings.Contains(err.Error(), "agent-bound message"),
		strings.Contains(err.Error(), "non-empty answer"),
		strings.Contains(err.Error(), "agent id"):
		c.Error(apperrors.NewBadRequestError(err.Error()))
	default:
		logger.Error(c.Request.Context(), "agent unsolved question operation failed", err)
		c.Error(apperrors.NewInternalServerError("agent unsolved question operation failed"))
	}
}
