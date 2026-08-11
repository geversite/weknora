package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
)

// KnowledgeFolderHandler handles file-level folder governance (M4) endpoints.
type KnowledgeFolderHandler struct {
	folderService    interfaces.KnowledgeFolderService
	summaryService   interfaces.FolderSummaryService
	kbService        interfaces.KnowledgeBaseService
	knowledgeService interfaces.KnowledgeService
}

func NewKnowledgeFolderHandler(
	folderService interfaces.KnowledgeFolderService,
	summaryService interfaces.FolderSummaryService,
	kbService interfaces.KnowledgeBaseService,
	knowledgeService interfaces.KnowledgeService,
) *KnowledgeFolderHandler {
	return &KnowledgeFolderHandler{
		folderService:    folderService,
		summaryService:   summaryService,
		kbService:        kbService,
		knowledgeService: knowledgeService,
	}
}

// validateKBFolderGovernance checks the KB exists and folder governance is enabled.
func (h *KnowledgeFolderHandler) validateKBFolderGovernance(c *gin.Context) (string, error) {
	kbID := secutils.SanitizeForLog(c.Param("kb_id"))
	if kbID == "" {
		kbID = secutils.SanitizeForLog(c.Param("id"))
	}
	if kbID == "" {
		return "", errors.NewBadRequestError("Knowledge base ID is required")
	}
	kb, err := h.kbService.GetKnowledgeBaseByID(c.Request.Context(), kbID)
	if err != nil {
		return "", errors.NewNotFoundError("Knowledge base not found")
	}
	if !kb.IsFolderGovernanceEnabled() {
		return "", errors.NewBadRequestError("Folder governance is not enabled for this knowledge base")
	}
	return kbID, nil
}

// ListFolders godoc
// @Summary      List folder tree
// @Description  List the file-level folder tree with file counts for a KB
// @Tags         KnowledgeFolder
// @Produce      json
// @Param        kb_id  path  string  true  "Knowledge base ID"
// @Success      200    {object} types.KnowledgeFolderTreeResponse
// @Router       /knowledge-bases/{kb_id}/folders [get]
func (h *KnowledgeFolderHandler) ListFolders(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	tree, err := h.folderService.ListTree(c.Request.Context(), kbID)
	if err != nil {
		logger.ErrorWithFields(c.Request.Context(), err, nil)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list folders"})
		return
	}
	c.JSON(http.StatusOK, tree)
}

// CreateFolder godoc
// @Summary      Create a folder
// @Description  Create a folder under a parent (empty parent_id = root)
// @Tags         KnowledgeFolder
// @Accept       json
// @Produce      json
// @Param        kb_id  path  string              true  "Knowledge base ID"
// @Param        body   body  types.KnowledgeFolderCreateRequest true "Folder payload"
// @Success      201    {object} types.KnowledgeFolder
// @Router       /knowledge-bases/{kb_id}/folders [post]
func (h *KnowledgeFolderHandler) CreateFolder(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var req types.KnowledgeFolderCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}
	folder, err := h.folderService.Create(c.Request.Context(), kbID, &req)
	if err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	c.JSON(http.StatusCreated, folder)
}

// CreateOrGetFolder godoc
// @Summary      Create or reuse a folder (merge semantics)
// @Description  Creates a folder, or if a same-name folder already exists under
// @Description  the same parent, returns the existing one. Used for folder upload
// @Description  merge: uploading folder A (with file C) when A (with file B)
// @Description  already exists reuses A, resulting in A containing both B and C.
// @Tags         KnowledgeFolder
// @Accept       json
// @Produce      json
// @Param        kb_id  path  string  true  "Knowledge base ID"
// @Param        body   body  types.KnowledgeFolderCreateRequest true "Folder create"
// @Success      200    {object} types.KnowledgeFolder
// @Router       /knowledge-bases/{kb_id}/folders/or-get [post]
func (h *KnowledgeFolderHandler) CreateOrGetFolder(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var req types.KnowledgeFolderCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}
	folder, err := h.folderService.CreateOrGet(c.Request.Context(), kbID, &req)
	if err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	// 200 OK for both created and reused (caller can't tell the difference,
	// which is the point of merge semantics).
	c.JSON(http.StatusOK, folder)
}

// UpdateFolder godoc
// @Summary      Rename / reparent a folder
// @Description  Rename and/or reparent a folder; subtree paths are recomputed
// @Tags         KnowledgeFolder
// @Accept       json
// @Produce      json
// @Param        kb_id      path  string  true  "Knowledge base ID"
// @Param        folder_id  path  string  true  "Folder ID"
// @Param        body       body  types.KnowledgeFolderUpdateRequest true "Folder update"
// @Success      200  {object} types.KnowledgeFolder
// @Router       /knowledge-bases/{kb_id}/folders/{folder_id} [put]
func (h *KnowledgeFolderHandler) UpdateFolder(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	folderID := secutils.SanitizeForLog(c.Param("folder_id"))
	if folderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Folder ID is required"})
		return
	}
	var req types.KnowledgeFolderUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}
	folder, err := h.folderService.Update(c.Request.Context(), kbID, folderID, &req)
	if err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	c.JSON(http.StatusOK, folder)
}

// DeleteFolder godoc
// @Summary      Delete a folder
// @Description  Delete a folder; its files cascade to the parent folder
// @Tags         KnowledgeFolder
// @Param        kb_id      path  string  true  "Knowledge base ID"
// @Param        folder_id  path  string  true  "Folder ID"
// @Success      204
// @Router       /knowledge-bases/{kb_id}/folders/{folder_id} [delete]
func (h *KnowledgeFolderHandler) DeleteFolder(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	folderID := secutils.SanitizeForLog(c.Param("folder_id"))
	if folderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Folder ID is required"})
		return
	}
	if err := h.folderService.Delete(c.Request.Context(), kbID, folderID); err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// MoveFilesToFolder godoc
// @Summary      Move files into a folder
// @Description  Assign knowledge entries to a folder (folder_id empty = root)
// @Tags         KnowledgeFolder
// @Accept       json
// @Param        kb_id      path  string  true  "Knowledge base ID"
// @Param        folder_id  path  string  true  "Folder ID"
// @Param        body       body  moveFilesRequest true "Knowledge IDs to move"
// @Success      200
// @Router       /knowledge-bases/{kb_id}/folders/{folder_id}/files [post]
func (h *KnowledgeFolderHandler) MoveFilesToFolder(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	folderID := secutils.SanitizeForLog(c.Param("folder_id"))
	if folderID == types.FolderRootID {
		folderID = ""
	}
	var req moveFilesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}
	if err := h.folderService.MoveFilesToFolder(c.Request.Context(), kbID, folderID, req.KnowledgeIDs); err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetFolderSummary godoc
// @Summary      Get folder summary
// @Description  Return the LLM-generated summary for a folder
// @Tags         KnowledgeFolder
// @Produce      json
// @Param        kb_id      path  string  true  "Knowledge base ID"
// @Param        folder_id  path  string  true  "Folder ID"
// @Success      200  {object} types.FolderSummary
// @Router       /knowledge-bases/{kb_id}/folders/{folder_id}/summary [get]
func (h *KnowledgeFolderHandler) GetFolderSummary(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	folderID := secutils.SanitizeForLog(c.Param("folder_id"))
	summary, err := h.summaryService.Get(c.Request.Context(), kbID, folderID)
	if err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

// GenerateFolderSummary godoc
// @Summary      Generate a folder summary
// @Description  Enqueue an async folder summary generation
// @Tags         KnowledgeFolder
// @Param        kb_id      path  string  true  "Knowledge base ID"
// @Param        folder_id  path  string  true  "Folder ID"
// @Success      202
// @Router       /knowledge-bases/{kb_id}/folders/{folder_id}/summary/generate [post]
func (h *KnowledgeFolderHandler) GenerateFolderSummary(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	folderID := secutils.SanitizeForLog(c.Param("folder_id"))
	if err := h.summaryService.Generate(c.Request.Context(), kbID, folderID); err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

// RefreshFolderSummary godoc
// @Summary      Force-regenerate a folder summary
// @Description  Regenerate even if the summary was manually edited
// @Tags         KnowledgeFolder
// @Param        kb_id      path  string  true  "Knowledge base ID"
// @Param        folder_id  path  string  true  "Folder ID"
// @Success      202
// @Router       /knowledge-bases/{kb_id}/folders/{folder_id}/summary/refresh [post]
func (h *KnowledgeFolderHandler) RefreshFolderSummary(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	folderID := secutils.SanitizeForLog(c.Param("folder_id"))
	if err := h.summaryService.Refresh(c.Request.Context(), kbID, folderID); err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

// EditFolderSummary godoc
// @Summary      Manually edit a folder summary
// @Description  Store a manually-authored summary (suppresses auto-regeneration)
// @Tags         KnowledgeFolder
// @Accept       json
// @Produce      json
// @Param        kb_id      path  string  true  "Knowledge base ID"
// @Param        folder_id  path  string  true  "Folder ID"
// @Param        body       body  types.FolderSummaryEditRequest true "Summary content"
// @Success      200  {object} types.FolderSummary
// @Router       /knowledge-bases/{kb_id}/folders/{folder_id}/summary [put]
func (h *KnowledgeFolderHandler) EditFolderSummary(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	folderID := secutils.SanitizeForLog(c.Param("folder_id"))
	var req types.FolderSummaryEditRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}
	summary, err := h.summaryService.Edit(c.Request.Context(), kbID, folderID, &req)
	if err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

// GetGovernanceReport godoc
// @Summary      Get folder governance report
// @Description  Return empty/imbalanced/stale/duplicate/deep folder diagnostics
// @Tags         KnowledgeFolder
// @Produce      json
// @Param        kb_id  path  string  true  "Knowledge base ID"
// @Success      200  {object} types.FolderGovernanceReport
// @Router       /knowledge-bases/{kb_id}/folders/governance [get]
func (h *KnowledgeFolderHandler) GetGovernanceReport(c *gin.Context) {
	kbID, err := h.validateKBFolderGovernance(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	report, err := h.folderService.GetGovernanceReport(c.Request.Context(), kbID)
	if err != nil {
		logger.ErrorWithFields(c.Request.Context(), err, nil)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build governance report"})
		return
	}
	c.JSON(http.StatusOK, report)
}

// ListFolderContent godoc
// @Summary      List folder content (sub-folders + files)
// @Description  Returns the direct child folders and paged files of a folder
// (folder_id omitted = root). Core API for the folder view.
// @Tags         KnowledgeFolder
// @Produce      json
// @Param        id        path  string  true  "Knowledge base ID"
// @Param        folder_id query string  false "Folder ID (empty = root)"
// @Param        page      query int     false "Page (default 1)"
// @Param        page_size query int     false "Page size (default 50)"
// @Param        files_only query bool  false "Only files, no sub-folders"
// @Success      200  {object} folderContentResponse
// @Router       /knowledge-bases/{id}/folder-content [get]
func (h *KnowledgeFolderHandler) ListFolderContent(c *gin.Context) {
	kbID := secutils.SanitizeForLog(c.Param("id"))
	if kbID == "" {
		kbID = secutils.SanitizeForLog(c.Param("kb_id"))
	}
	if kbID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Knowledge base ID is required"})
		return
	}
	ctx := c.Request.Context()

	folderID := secutils.SanitizeForLog(c.DefaultQuery("folder_id", ""))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if pageSize < 1 {
		pageSize = 50
	}
	filesOnly := c.Query("files_only") == "true"

	// 1. child folders (unless files_only)
	var folders []*types.KnowledgeFolderNode
	var err error
	if !filesOnly {
		folders, err = h.folderService.ListChildrenWithMeta(ctx, kbID, folderID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	// 2. files directly in the folder (paged)
	files, total, err := h.folderService.ListByFolderPaged(ctx, kbID, folderID, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 3. current folder info (for breadcrumb)
	var currentFolder *types.KnowledgeFolder
	if folderID != "" {
		if f, ferr := h.folderService.GetByID(ctx, folderID); ferr == nil {
			currentFolder = f
		}
	}

	c.JSON(http.StatusOK, folderContentResponse{
		Folders:       folders,
		Files:         files,
		TotalFiles:    total,
		CurrentFolder: currentFolder,
	})
}

// MoveKnowledgeToFolder godoc
// @Summary      Move a single knowledge entry to a folder
// @Description  Move one file into a folder (folder_id empty = root)
// @Tags         KnowledgeFolder
// @Accept       json
// @Param        id            path  string  true  "Knowledge base ID"
// @Param        knowledge_id  path  string  true  "Knowledge ID"
// @Param        body          body  moveSingleRequest true "Target folder"
// @Success      200
// @Router       /knowledge-bases/{id}/knowledge/{knowledge_id}/move [post]
func (h *KnowledgeFolderHandler) MoveKnowledgeToFolder(c *gin.Context) {
	kbID := secutils.SanitizeForLog(c.Param("id"))
	if kbID == "" {
		kbID = secutils.SanitizeForLog(c.Param("kb_id"))
	}
	knowledgeID := secutils.SanitizeForLog(c.Param("knowledge_id"))
	if knowledgeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Knowledge ID is required"})
		return
	}
	var req moveSingleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}
	if err := h.folderService.MoveKnowledge(c.Request.Context(), knowledgeID, req.FolderID); err != nil {
		writeKnowledgeFolderError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// folderContentResponse is the payload of ListFolderContent.
type folderContentResponse struct {
	Folders       []*types.KnowledgeFolderNode `json:"folders"`
	Files         []*types.Knowledge           `json:"files"`
	TotalFiles    int64                        `json:"total_files"`
	CurrentFolder *types.KnowledgeFolder       `json:"current_folder"`
}

// moveSingleRequest is the request body for moving a single knowledge entry.
type moveSingleRequest struct {
	FolderID string `json:"folder_id"`
}

// moveFilesRequest is the request body for moving files into a folder.
type moveFilesRequest struct {
	KnowledgeIDs []string `json:"knowledge_ids"`
}

func writeKnowledgeFolderError(c *gin.Context, err error) {
	switch err.Error() {
	case "folder not found":
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case "folder summary not ready":
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case "invalid folder name", "invalid folder parent", "knowledge not in knowledge base":
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case "folder name already exists in the same parent folder":
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		logger.ErrorWithFields(c.Request.Context(), err, nil)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
