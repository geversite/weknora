package repository

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type knowledgeFolderRepo struct {
	db *gorm.DB
}

func NewKnowledgeFolderRepository(db *gorm.DB) interfaces.KnowledgeFolderRepository {
	return &knowledgeFolderRepo{db: db}
}

func (r *knowledgeFolderRepo) Create(ctx context.Context, folder *types.KnowledgeFolder) error {
	return r.db.WithContext(ctx).Create(folder).Error
}

func (r *knowledgeFolderRepo) GetByID(ctx context.Context, id string) (*types.KnowledgeFolder, error) {
	var folder types.KnowledgeFolder
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&folder).Error; err != nil {
		return nil, err
	}
	return &folder, nil
}

func (r *knowledgeFolderRepo) Update(ctx context.Context, folder *types.KnowledgeFolder) error {
	return r.db.WithContext(ctx).Save(folder).Error
}

func (r *knowledgeFolderRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.KnowledgeFolder{}).Error
}

func (r *knowledgeFolderRepo) ListByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.KnowledgeFolder, error) {
	var folders []*types.KnowledgeFolder
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Order("sort_order ASC, name ASC").
		Find(&folders).Error
	return folders, err
}

func (r *knowledgeFolderRepo) ListChildren(ctx context.Context, tenantID uint64, kbID, parentID string) ([]*types.KnowledgeFolder, error) {
	var folders []*types.KnowledgeFolder
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND parent_id = ?", tenantID, kbID, parentID).
		Order("sort_order ASC, name ASC").
		Find(&folders).Error
	return folders, err
}

// GetSubtree returns all folders whose path starts with any of the given
// folders' paths (inclusive). Uses materialized path prefix match.
func (r *knowledgeFolderRepo) GetSubtree(ctx context.Context, tenantID uint64, kbID string, folderIDs []string) ([]*types.KnowledgeFolder, error) {
	if len(folderIDs) == 0 {
		return nil, nil
	}
	// 先取给定文件夹的 path
	var seedFolders []*types.KnowledgeFolder
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND id IN ?", tenantID, kbID, folderIDs).
		Find(&seedFolders).Error; err != nil {
		return nil, err
	}
	if len(seedFolders) == 0 {
		return nil, nil
	}
	// 用 path 前缀匹配查子树
	var conditions []string
	var args []interface{}
	for _, f := range seedFolders {
		conditions = append(conditions, "(path = ? OR path LIKE ?)")
		args = append(args, f.Path, f.Path+"%")
	}
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Where(strings.Join(conditions, " OR "), args...)
	var folders []*types.KnowledgeFolder
	err := query.Order("depth ASC, sort_order ASC, name ASC").Find(&folders).Error
	return folders, err
}

func (r *knowledgeFolderRepo) CountFilesInFolder(ctx context.Context, kbID, folderID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("knowledges").
		Where("knowledge_base_id = ? AND folder_id = ? AND deleted_at IS NULL", kbID, folderID).
		Count(&count).Error
	return count, err
}

func (r *knowledgeFolderRepo) HasChildren(ctx context.Context, tenantID uint64, kbID, folderID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.KnowledgeFolder{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND parent_id = ?", tenantID, kbID, folderID).
		Count(&count).Error
	return count > 0, err
}

// ExistsByName reports whether a non-deleted folder with the given name
// exists under parentID (empty = root) in the KB. excludeID allows the
// rename path to skip the folder itself.
func (r *knowledgeFolderRepo) ExistsByName(ctx context.Context, tenantID uint64, kbID, parentID, name, excludeID string) (bool, error) {
	q := r.db.WithContext(ctx).Model(&types.KnowledgeFolder{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND name = ?", tenantID, kbID, name)
	if parentID == "" {
		q = q.Where("parent_id = '' OR parent_id IS NULL")
	} else {
		q = q.Where("parent_id = ?", parentID)
	}
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *knowledgeFolderRepo) UpdateStatus(ctx context.Context, folderID, status string) error {
	return r.db.WithContext(ctx).Model(&types.KnowledgeFolder{}).
		Where("id = ?", folderID).
		Update("summary_status", status).Error
}

func (r *knowledgeFolderRepo) UpdatePathBatch(ctx context.Context, folders []*types.KnowledgeFolder) error {
	if len(folders) == 0 {
		return nil
	}
	tx := r.db.WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()
	for _, f := range folders {
		if err := tx.Model(&types.KnowledgeFolder{}).
			Where("id = ?", f.ID).
			Updates(map[string]interface{}{"path": f.Path, "depth": f.Depth}).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit().Error
}

func (r *knowledgeFolderRepo) MoveFiles(ctx context.Context, kbID, oldFolderID, newFolderID string) (int64, error) {
	result := r.db.WithContext(ctx).Table("knowledges").
		Where("knowledge_base_id = ? AND folder_id = ?", kbID, oldFolderID).
		Update("folder_id", newFolderID)
	return result.RowsAffected, result.Error
}

func (r *knowledgeFolderRepo) DeleteByKB(ctx context.Context, kbID string) error {
	return r.db.WithContext(ctx).Where("knowledge_base_id = ?", kbID).
		Delete(&types.KnowledgeFolder{}).Error
}
