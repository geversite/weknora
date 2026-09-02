package handler

import (
	stderrors "errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// KnowledgeConflictHandler processes HTTP requests related to the M3 conflict
// adjudication queue.
type KnowledgeConflictHandler struct {
	conflictService interfaces.ConflictAdjudicateService
	clusterService  interfaces.ConflictClusterService
	kbService       interfaces.KnowledgeBaseService
}

// NewKnowledgeConflictHandler creates a new conflict handler instance.
func NewKnowledgeConflictHandler(
	conflictService interfaces.ConflictAdjudicateService,
	clusterService interfaces.ConflictClusterService,
	kbService interfaces.KnowledgeBaseService,
) *KnowledgeConflictHandler {
	return &KnowledgeConflictHandler{
		conflictService: conflictService,
		clusterService:  clusterService,
		kbService:       kbService,
	}
}

// ListConflicts godoc
// @Summary      List content conflicts for a knowledge base
// @Description  Returns a paged list of file-level content conflicts for a KB,
//
//	optionally filtered by status (pending / resolved_*).
//
// @Tags         知识冲突
// @Param        id       path      string  true  "Knowledge base ID"
// @Param        status   query     string  false "Filter by status: pending|resolved_keep_both|resolved_newer_wins|resolved_older_wins|resolved_not_conflict|resolved_global_winner"
// @Param        page     query     int     false "Page number (default 1)"
// @Param        page_size query    int     false "Page size (default 20, max 200)"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/conflicts [get]
func (h *KnowledgeConflictHandler) ListConflicts(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := c.Param("id")
	tenantID := c.GetUint64(types.TenantIDContextKey.String())

	status := c.Query("status")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}

	conflicts, total, err := h.conflictService.ListConflicts(ctx, tenantID, kbID, status, pageSize, (page-1)*pageSize)
	if err != nil {
		logger.Errorf(ctx, "List conflicts for KB %s failed: %v", kbID, err)
		c.Error(errors.NewInternalServerError("failed to list conflicts"))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"list":      conflicts,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// GetConflictStats godoc
// @Summary      Get conflict counts for a knowledge base
// @Description  Returns pending/resolved counts grouped by status.
// @Tags         知识冲突
// @Param        id  path  string  true  "Knowledge base ID"
// @Success      200 {object} map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/conflicts/stats [get]
func (h *KnowledgeConflictHandler) GetConflictStats(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := c.Param("id")
	tenantID := c.GetUint64(types.TenantIDContextKey.String())

	stats, err := h.conflictService.GetConflictStats(ctx, tenantID, kbID)
	if err != nil {
		logger.Errorf(ctx, "Get conflict stats for KB %s failed: %v", kbID, err)
		c.Error(errors.NewInternalServerError("failed to get conflict stats"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": stats})
}

// ListDisputedFacts godoc
// @Summary      List C4-Lite fact-level conflict clusters for a knowledge base
// @Description  Returns a paged list of deterministic DisputedFact aggregates.
//
//	Each item may represent multiple raw chunk-pair conflict rows.
//
// @Tags         知识冲突
// @Param        id       path      string  true  "Knowledge base ID"
// @Param        status   query     string  false "Filter by pending|resolved"
// @Param        page     query     int     false "Page number (default 1)"
// @Param        page_size query    int     false "Page size (default 20, max 200)"
// @Success      200      {object}  map[string]interface{}
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/conflicts/clusters [get]
func (h *KnowledgeConflictHandler) ListDisputedFacts(c *gin.Context) {
	if h.clusterService == nil {
		c.Error(errors.NewInternalServerError("conflict cluster service is not configured"))
		return
	}
	ctx := c.Request.Context()
	kbID := c.Param("id")
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	status := c.Query("status")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}

	facts, total, err := h.clusterService.ListDisputedFacts(ctx, tenantID, kbID, status, pageSize, (page-1)*pageSize)
	if err != nil {
		logger.Errorf(ctx, "List disputed facts for KB %s failed: %v", kbID, err)
		c.Error(errors.NewInternalServerError("failed to list disputed facts"))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"list":      facts,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// RebuildDisputedFacts godoc
// @Summary      Rebuild C4-Lite fact-level conflict clusters
// @Description  Deterministically re-clusters current raw conflicts in a KB.
// @Tags         知识冲突
// @Param        id path string true "Knowledge base ID"
// @Success      200 {object} map[string]interface{}
// @Failure      500 {object} errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/conflicts/clusters/rebuild [post]
func (h *KnowledgeConflictHandler) RebuildDisputedFacts(c *gin.Context) {
	if h.clusterService == nil {
		c.Error(errors.NewInternalServerError("conflict cluster service is not configured"))
		return
	}
	ctx := c.Request.Context()
	kbID := c.Param("id")
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	result, err := h.clusterService.Rebuild(ctx, tenantID, kbID)
	if err != nil {
		logger.Errorf(ctx, "Rebuild disputed facts for KB %s failed: %v", kbID, err)
		c.Error(errors.NewInternalServerError("failed to rebuild disputed facts"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// ResolveDisputedFact godoc
// @Summary      Safely resolve every pending raw member of a DisputedFact
// @Description  C4.5 permits only keep_both and not_conflict. C4.7 global
//
//	winner adoption is a separate explicit endpoint with snapshot checks.
//
// @Tags         知识冲突
// @Param        id   path string true "Knowledge base ID"
// @Param        body body types.DisputedFactResolution true "Cluster resolution payload"
// @Success      200  {object} map[string]interface{}
// @Failure      400  {object} errors.AppError
// @Failure      500  {object} errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/conflicts/clusters/resolve [post]
func (h *KnowledgeConflictHandler) ResolveDisputedFact(c *gin.Context) {
	if h.clusterService == nil {
		c.Error(errors.NewInternalServerError("conflict cluster service is not configured"))
		return
	}
	ctx := c.Request.Context()
	kbID := c.Param("id")
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	resolverUserID := c.GetString(types.UserIDContextKey.String())
	var req types.DisputedFactResolution
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid disputed fact resolution payload: " + err.Error()))
		return
	}
	result, err := h.clusterService.ResolveDisputedFact(ctx, tenantID, resolverUserID, kbID, req)
	if err != nil {
		logger.Errorf(ctx, "Resolve disputed fact %s in KB %s failed: %v", req.DisputedFactID, kbID, err)
		c.Error(errors.NewValidationError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// AdoptDisputedFactWinner godoc
// @Summary      Explicitly adopt a current C4.6 global winner proposal
// @Description  Re-reads and locks the reviewed DisputedFact proposal and all
//
//	current raw members. The caller must echo the winner, proposal version,
//
//	source count and updated_at snapshot. On success, every pending member
//
//	becomes resolved_global_winner and only non-winner source chunks are
//
//	disabled. This endpoint never derives a decision from local raw A/B order.
// @Tags         知识冲突
// @Param        id   path string true "Knowledge base ID"
// @Param        body body types.DisputedFactWinnerAdoption true "Winner proposal adoption payload"
// @Success      200  {object} map[string]interface{}
// @Failure      400  {object} errors.AppError
// @Failure      409  {object} errors.AppError
// @Failure      500  {object} errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/conflicts/clusters/adopt-winner [post]
func (h *KnowledgeConflictHandler) AdoptDisputedFactWinner(c *gin.Context) {
	if h.clusterService == nil {
		c.Error(errors.NewInternalServerError("conflict cluster service is not configured"))
		return
	}
	ctx := c.Request.Context()
	kbID := c.Param("id")
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	resolverUserID := c.GetString(types.UserIDContextKey.String())
	var req types.DisputedFactWinnerAdoption
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid winner proposal adoption payload: " + err.Error()))
		return
	}
	result, err := h.clusterService.AdoptDisputedFactWinner(ctx, tenantID, resolverUserID, kbID, req)
	if err != nil {
		logger.Errorf(ctx, "Adopt disputed fact %s winner in KB %s failed: %v", req.DisputedFactID, kbID, err)
		if stderrors.Is(err, types.ErrDisputedFactWinnerAdoptionConflict) {
			c.Error(errors.NewConflictError(err.Error()))
			return
		}
		c.Error(errors.NewValidationError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// Resolve godoc
// @Summary      Adjudicate a content conflict
// @Description  Resolves a pending conflict, applying the disable/penalty side-effects.
// @Tags         知识冲突
// @Param        id   path  string  true  "Knowledge base ID"
// @Param        body body types.ConflictResolution true "Resolution payload"
// @Success      200  {object} map[string]interface{}
// @Failure      400  {object} errors.AppError
// @Failure      404  {object} errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/conflicts/resolve [post]
func (h *KnowledgeConflictHandler) Resolve(c *gin.Context) {
	ctx := c.Request.Context()
	userID := c.GetString(types.UserIDContextKey.String())

	var req types.ConflictResolution
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid resolution payload: " + err.Error()))
		return
	}
	result, err := h.conflictService.Resolve(ctx, userID, req)
	if err != nil {
		logger.Errorf(ctx, "Resolve conflict %s failed: %v", req.ConflictID, err)
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}
