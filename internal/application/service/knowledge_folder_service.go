package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// folderService implements KnowledgeFolderService.
type folderService struct {
	folderRepo      interfaces.KnowledgeFolderRepository
	summaryRepo     interfaces.FolderSummaryRepository
	summarySvc      interfaces.FolderSummaryService
	chunkService    interfaces.ChunkService
	kbService       interfaces.KnowledgeBaseService
	repo            interfaces.KnowledgeRepository
	wikiPageService interfaces.WikiPageService // [M6] optional; nil on non-wiki deployments
}

func NewKnowledgeFolderService(
	folderRepo interfaces.KnowledgeFolderRepository,
	summaryRepo interfaces.FolderSummaryRepository,
	summarySvc interfaces.FolderSummaryService,
	chunkService interfaces.ChunkService,
	kbService interfaces.KnowledgeBaseService,
	repo interfaces.KnowledgeRepository,
	wikiPageService interfaces.WikiPageService, // [M6]
) *folderService {
	return &folderService{
		folderRepo:      folderRepo,
		summaryRepo:     summaryRepo,
		summarySvc:      summarySvc,
		chunkService:    chunkService,
		kbService:       kbService,
		repo:            repo,
		wikiPageService: wikiPageService,
	}
}

func (s *folderService) ListTree(ctx context.Context, kbID string) (*types.KnowledgeFolderTreeResponse, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	folders, err := s.folderRepo.ListByKB(ctx, tenantID, kbID)
	if err != nil {
		return nil, err
	}
	// build children map: parentID -> []folder
	children := make(map[string][]*types.KnowledgeFolder)
	for _, f := range folders {
		children[f.ParentID] = append(children[f.ParentID], f)
	}
	// build root list (parentID == "")
	roots := children[""]

	var build func(parents []*types.KnowledgeFolder) ([]types.KnowledgeFolderNode, error)
	build = func(parents []*types.KnowledgeFolder) ([]types.KnowledgeFolderNode, error) {
		nodes := make([]types.KnowledgeFolderNode, 0, len(parents))
		for _, f := range parents {
			node := types.KnowledgeFolderNode{KnowledgeFolder: *f}
			count, err := s.folderRepo.CountFilesInFolder(ctx, kbID, f.ID)
			if err != nil {
				return nil, err
			}
			node.FileCount = count
			node.HasChildren = len(children[f.ID]) > 0
			nodes = append(nodes, node)
			// Recurse into direct children so the returned Nodes is a true
			// flattened tree (every folder at every depth), not just the root
			// level. Callers (browse_folders, <folders> prompt, wiki governance,
			// knowledge_search folder scoping) rely on every folder being
			// present so they can iterate the tree without N+1 lookups.
			childNodes, cerr := build(children[f.ID])
			if cerr != nil {
				return nil, cerr
			}
			nodes = append(nodes, childNodes...)
		}
		return nodes, nil
	}

	nodes, err := build(roots)
	if err != nil {
		return nil, err
	}
	return &types.KnowledgeFolderTreeResponse{Nodes: nodes}, nil
}

func (s *folderService) Create(ctx context.Context, kbID string, req *types.KnowledgeFolderCreateRequest) (*types.KnowledgeFolder, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, ErrInvalidFolderName
	}

	var path string
	var depth int
	if req.ParentID != "" {
		p, err := s.folderRepo.GetByID(ctx, req.ParentID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrFolderNotFound
			}
			return nil, err
		}
		if p.KnowledgeBaseID != kbID || p.TenantID != tenantID {
			return nil, ErrFolderNotFound
		}
		path = buildFolderPath(p.Path, name)
		depth = p.Depth + 1
	} else {
		path = buildFolderPath("/", name)
		depth = 0
	}

	// 同路径下不允许同名文件夹
	exists, err := s.folderRepo.ExistsByName(ctx, tenantID, kbID, req.ParentID, name, "")
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrFolderNameConflict
	}

	folder := &types.KnowledgeFolder{
		ID:              uuid.New().String(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		ParentID:        req.ParentID,
		Name:            name,
		Path:            path,
		Depth:           depth,
		SortOrder:       0,
		SummaryStatus:   types.FolderSummaryStatusNone,
	}
	if err := s.folderRepo.Create(ctx, folder); err != nil {
		return nil, err
	}
	return folder, nil
}

// CreateOrGet creates a folder, or if a same-name folder already exists
// under the same parent, returns the existing one. This enables folder-merge
// semantics on upload: uploading folder A (with file C) when A (with file B)
// already exists reuses the existing A, resulting in A containing both B and C.
func (s *folderService) CreateOrGet(ctx context.Context, kbID string, req *types.KnowledgeFolderCreateRequest) (*types.KnowledgeFolder, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, ErrInvalidFolderName
	}

	// Check if a same-name folder already exists under the same parent.
	existing, err := s.folderRepo.GetByNameInParent(ctx, tenantID, kbID, req.ParentID, name)
	if err == nil && existing != nil {
		// Folder already exists — reuse it (merge semantics).
		return existing, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Not found — create a new folder (delegates to Create, which also
	// handles parent validation and path/depth computation).
	return s.Create(ctx, kbID, req)
}

func (s *folderService) Update(ctx context.Context, kbID, folderID string, req *types.KnowledgeFolderUpdateRequest) (*types.KnowledgeFolder, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	folder, err := s.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrFolderNotFound
		}
		return nil, err
	}
	if folder.KnowledgeBaseID != kbID || folder.TenantID != tenantID {
		return nil, ErrFolderNotFound
	}

	// 1. rename updates name + path leaf
	newName := folder.Name
	if req.Name != "" {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return nil, ErrInvalidFolderName
		}
		newName = name
		oldPath := folder.Path
		folder.Name = name
		folder.Path = buildFolderPath(parentPathOf(oldPath), name)
	}

	// 2. optional reparent (cascades path + depth for subtree)
	newParentID := folder.ParentID
	if req.MoveParent {
		newParent := &types.KnowledgeFolder{
			ParentID: "", Path: "/", Depth: -1,
		}
		if req.ParentID != "" {
			p, err := s.folderRepo.GetByID(ctx, req.ParentID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, ErrFolderNotFound
				}
				return nil, err
			}
			if p.KnowledgeBaseID != kbID || p.TenantID != tenantID || p.ID == folderID {
				return nil, ErrInvalidFolderParent
			}
			newParent = p
		}
		// guard against moving into own subtree
		if strings.HasPrefix(req.ParentID, folder.Path) {
			return nil, ErrInvalidFolderParent
		}
		newParentID = req.ParentID
		folder.ParentID = req.ParentID
		folder.Path = buildFolderPath(newParent.Path, folder.Name)
		folder.Depth = newParent.Depth + 1
	}

	// 同路径下不允许同名文件夹（rename 或 reparent 都可能触发）
	if req.Name != "" || req.MoveParent {
		exists, err := s.folderRepo.ExistsByName(ctx, tenantID, kbID, newParentID, newName, folderID)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, ErrFolderNameConflict
		}
	}

	if err := s.folderRepo.Update(ctx, folder); err != nil {
		return nil, err
	}

	// 3. cascade path/depth to descendants after rename/reparent
	if err := s.cascadePaths(ctx, folderID, folder.Path, folder.Depth); err != nil {
		logger.Warnf(ctx, "failed to cascade folder paths under %s: %v", folderID, err)
	}
	return folder, nil
}

// cascadePaths recomputes path+depth for all descendants of folderID.
func (s *folderService) cascadePaths(ctx context.Context, parentID, parentPath string, parentDepth int) error {
	tenantID := types.MustTenantIDFromContext(ctx)
	children, err := s.folderRepo.ListChildren(ctx, tenantID, "", parentID)
	if err != nil {
		return err
	}
	if len(children) == 0 {
		return nil
	}
	var batch []*types.KnowledgeFolder
	for _, child := range children {
		child.Path = buildFolderPath(parentPath, child.Name)
		child.Depth = parentDepth + 1
		batch = append(batch, child)
		// recurse into grandchildren (small trees, acceptable)
		if err := s.cascadePaths(ctx, child.ID, child.Path, child.Depth); err != nil {
			return err
		}
	}
	return s.folderRepo.UpdatePathBatch(ctx, batch)
}

func (s *folderService) Delete(ctx context.Context, kbID, folderID string) error {
	tenantID := types.MustTenantIDFromContext(ctx)
	folder, err := s.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrFolderNotFound
		}
		return err
	}
	if folder.KnowledgeBaseID != kbID || folder.TenantID != tenantID {
		return ErrFolderNotFound
	}

	// 1. cascade files to parent folder
	if _, err := s.folderRepo.MoveFiles(ctx, kbID, folderID, folder.ParentID); err != nil {
		return err
	}
	// 2. delete subtree folders (soft delete via path prefix)
	subtree, err := s.folderRepo.GetSubtree(ctx, tenantID, kbID, []string{folderID})
	if err != nil {
		return err
	}
	for _, sf := range subtree {
		// drop folder_summary rows + summary chunks before soft-deleting
		if err := s.summaryRepo.DeleteByFolder(ctx, sf.ID); err != nil {
			return err
		}
		if err := s.chunkService.DeleteByFolderAndType(ctx, sf.ID, types.ChunkTypeFolderSummary); err != nil {
			return err
		}
		// [M6] 同步删除该文件夹对应的 wiki 摘要投影页
		if s.wikiPageService != nil {
			slug := types.WikiFolderSummarySlug(sf.ID)
			if err := s.wikiPageService.DeletePage(ctx, kbID, slug); err != nil {
				logger.Warnf(ctx, "[M6] failed to delete wiki folder-summary page %s: %v", slug, err)
				// best-effort: continue cleanup
			}
		}
		if err := s.folderRepo.Delete(ctx, sf.ID); err != nil {
			return err
		}
	}
	// M4: files cascaded into the parent folder; refresh it + ancestor chain.
	if s.summarySvc != nil && folder.ParentID != "" {
		if parent, err := s.folderRepo.GetByID(ctx, folder.ParentID); err == nil {
			s.summarySvc.ScheduleRefreshForFolderAndAncestors(ctx, parent)
		}
	}
	return nil
}

func (s *folderService) MoveFilesToFolder(ctx context.Context, kbID, folderID string, knowledgeIDs []string) error {
	if len(knowledgeIDs) == 0 {
		return nil
	}
	// validate target folder ownership (folderID may be "" = root)
	if folderID != "" {
		folder, err := s.folderRepo.GetByID(ctx, folderID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrFolderNotFound
			}
			return err
		}
		if folder.KnowledgeBaseID != kbID {
			return ErrFolderNotFound
		}
	}
	// ensure each knowledge belongs to this KB before moving
	tenantID := types.MustTenantIDFromContext(ctx)
	for _, id := range knowledgeIDs {
		k, err := s.repo.GetKnowledgeByID(ctx, tenantID, id)
		if err != nil {
			return fmt.Errorf("knowledge %s: %w", id, err)
		}
		if k.KnowledgeBaseID != kbID {
			return ErrKnowledgeNotInKB
		}
		if err := s.repo.UpdateFolderID(ctx, id, folderID); err != nil {
			return err
		}
	}
	// M4: debounced refresh for the target folder and its ancestor chain since
	// membership just changed. Fire-and-forget.
	if s.summarySvc != nil && folderID != "" {
		if folder, err := s.folderRepo.GetByID(ctx, folderID); err == nil {
			s.summarySvc.ScheduleRefreshForFolderAndAncestors(ctx, folder)
		}
	}
	return nil
}

// GetGovernanceReport builds the folder governance health report (M4):
// empty / imbalanced / stale-summary / duplicate / over-deep folders.
func (s *folderService) GetGovernanceReport(ctx context.Context, kbID string) (*types.FolderGovernanceReport, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	folders, err := s.folderRepo.ListByKB(ctx, tenantID, kbID)
	if err != nil {
		return nil, err
	}
	report := &types.FolderGovernanceReport{}
	const maxFilesPerFolder = 200
	const maxDepth = 5

	// 1. empty / imbalanced / over-deep
	for _, f := range folders {
		count, cerr := s.folderRepo.CountFilesInFolder(ctx, kbID, f.ID)
		if cerr != nil {
			return nil, cerr
		}
		hasChildren, herr := s.folderRepo.HasChildren(ctx, tenantID, kbID, f.ID)
		if herr != nil {
			return nil, herr
		}
		if count == 0 && !hasChildren {
			report.EmptyFolders = append(report.EmptyFolders, types.FolderEmptyInfo{
				FolderID: f.ID, Name: f.Name, Path: f.Path,
			})
		}
		if count > maxFilesPerFolder {
			report.ImbalancedFolders = append(report.ImbalancedFolders, types.FolderImbalancedInfo{
				FolderID: f.ID, Name: f.Name, Path: f.Path,
				FileCount: count, Suggestion: "consider_split",
			})
		}
		if f.Depth > maxDepth {
			report.DeepFolders = append(report.DeepFolders, types.FolderDeepInfo{
				FolderID: f.ID, Name: f.Name, Path: f.Path, Depth: f.Depth,
			})
		}
	}

	// 2. stale summaries: use the summary service's IsStale which detects both
	// direct-file changes and child-folder summary version drift (parent
	// folders). Falls back to skip when the summary service is unavailable.
	summaries, serr := s.summaryRepo.ListByKB(ctx, tenantID, kbID)
	if serr != nil {
		return nil, serr
	}
	for _, sum := range summaries {
		if sum.IsManualEdit || sum.GeneratedAt == nil {
			continue
		}
		folder, ferr := s.folderRepo.GetByID(ctx, sum.FolderID)
		if ferr != nil {
			continue
		}
		stale, stlErr := s.summarySvc.IsStale(ctx, kbID, sum.FolderID)
		if stlErr != nil || !stale {
			continue
		}
		// Best-effort last file change time for the report display; parents
		// may have no direct files so this can be zero.
		latest, _ := s.repo.LatestFileChangeTime(ctx, kbID, sum.FolderID)
		var lastChange time.Time
		if latest != nil {
			lastChange = *latest
		} else {
			lastChange = *sum.GeneratedAt // no direct file change to show
		}
		report.StaleSummaries = append(report.StaleSummaries, types.FolderStaleSummaryInfo{
			FolderID: sum.FolderID, Name: folder.Name,
			GeneratedAt: *sum.GeneratedAt, LastFileChange: lastChange,
		})
	}

	// 3. duplicate files across folders (same file_hash, different folder_id)
	dups, derr := s.repo.ListFolderDuplicates(ctx, kbID)
	if derr != nil {
		return nil, derr
	}
	for _, d := range dups {
		if d == nil {
			continue
		}
		report.DuplicateFiles = append(report.DuplicateFiles, *d)
	}

	return report, nil
}

// GetByID returns a folder by its ID.
func (s *folderService) GetByID(ctx context.Context, folderID string) (*types.KnowledgeFolder, error) {
	return s.folderRepo.GetByID(ctx, folderID)
}

// GetPathChain returns the ancestor chain from root to the given folder
// (inclusive). Used for breadcrumb navigation so each crumb can carry a
// clickable folder id (not just a name).
func (s *folderService) GetPathChain(ctx context.Context, kbID, folderID string) ([]*types.KnowledgeFolder, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	return s.folderRepo.GetPathChain(ctx, tenantID, kbID, folderID)
}

// ListChildrenWithMeta returns the direct child folders of a folder (or root when
// folderID="") enriched with file counts and has-children flags.
func (s *folderService) ListChildrenWithMeta(ctx context.Context, kbID, folderID string) ([]*types.KnowledgeFolderNode, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	children, err := s.folderRepo.ListChildren(ctx, tenantID, kbID, folderID)
	if err != nil {
		return nil, err
	}
	nodes := make([]*types.KnowledgeFolderNode, 0, len(children))
	for _, f := range children {
		count, cerr := s.folderRepo.CountFilesInFolder(ctx, kbID, f.ID)
		if cerr != nil {
			return nil, cerr
		}
		hasChildren, herr := s.folderRepo.HasChildren(ctx, tenantID, kbID, f.ID)
		if herr != nil {
			return nil, herr
		}
		nodes = append(nodes, &types.KnowledgeFolderNode{
			KnowledgeFolder: *f,
			FileCount:       count,
			HasChildren:     hasChildren,
		})
	}
	return nodes, nil
}

// ListByFolderPaged returns knowledge entries directly in a folder (folderID=""
// for root), paged. Reuses the existing filtered paged query with FolderIDs.
func (s *folderService) ListByFolderPaged(ctx context.Context, kbID, folderID string, page, pageSize int) ([]*types.Knowledge, int64, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	p := &types.Pagination{Page: page, PageSize: pageSize}
	filter := types.KnowledgeListFilter{}
	if folderID == "" {
		filter.FolderIDs = []string{types.FolderRootID}
	} else {
		filter.FolderIDs = []string{folderID}
	}
	return s.repo.ListPagedKnowledgeByKnowledgeBaseID(ctx, tenantID, kbID, p, filter)
}

// MoveKnowledge moves a single knowledge entry to a folder (folderID="" = root),
// triggering debounced summary refresh for old+new folder ancestor chains.
func (s *folderService) MoveKnowledge(ctx context.Context, knowledgeID, targetFolderID string) error {
	tenantID := types.MustTenantIDFromContext(ctx)
	// validate target folder ownership (may be "" = root)
	if targetFolderID != "" {
		folder, err := s.folderRepo.GetByID(ctx, targetFolderID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrFolderNotFound
			}
			return err
		}
		_ = folder // ownership re-checked via the knowledge's KB below
	}
	knowledge, err := s.repo.GetKnowledgeByID(ctx, tenantID, knowledgeID)
	if err != nil {
		return err
	}
	if targetFolderID != "" {
		folder, ferr := s.folderRepo.GetByID(ctx, targetFolderID)
		if ferr != nil {
			return ErrFolderNotFound
		}
		if folder.KnowledgeBaseID != knowledge.KnowledgeBaseID {
			return ErrKnowledgeNotInKB
		}
	}
	oldFolderID := knowledge.FolderID
	knowledge.FolderID = targetFolderID
	if err := s.repo.UpdateFolderID(ctx, knowledgeID, targetFolderID); err != nil {
		return err
	}
	// trigger debounced refresh for old + new folder ancestor chains
	if oldFolderID != "" {
		if oldFolder, err := s.folderRepo.GetByID(ctx, oldFolderID); err == nil {
			s.summarySvc.ScheduleRefreshForFolderAndAncestors(ctx, oldFolder)
		}
	}
	if targetFolderID != "" {
		if newFolder, err := s.folderRepo.GetByID(ctx, targetFolderID); err == nil {
			s.summarySvc.ScheduleRefreshForFolderAndAncestors(ctx, newFolder)
		}
	}
	return nil
}

// buildFolderPath joins a parent path and a name into the child's materialized path.
func buildFolderPath(parentPath, name string) string {
	if parentPath == "" || parentPath == "/" {
		return "/" + name
	}
	return strings.TrimRight(parentPath, "/") + "/" + name
}

// parentPathOf returns the parent of a leaf materialized path.
func parentPathOf(path string) string {
	if path == "" || path == "/" {
		return "/"
	}
	idx := strings.LastIndex(strings.TrimRight(path, "/"), "/")
	if idx <= 0 {
		return "/"
	}
	return path[:idx]
}
