package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type referenceEventRepo struct {
	db *gorm.DB
}

func NewReferenceEventRepository(db *gorm.DB) interfaces.ReferenceEventRepository {
	return &referenceEventRepo{db: db}
}

func (r *referenceEventRepo) BatchCreate(ctx context.Context, events []*types.ReferenceEvent) error {
	if len(events) == 0 {
		return nil
	}
	for _, e := range events {
		if e == nil {
			continue
		}
		if e.ID == "" {
			e.ID = uuid.New().String()
		}
		if e.ReferenceType == "" {
			e.ReferenceType = types.ReferenceTypeRAG
		}
	}
	return r.db.WithContext(ctx).CreateInBatches(events, 100).Error
}

func (r *referenceEventRepo) CountByKB(ctx context.Context, tenantID uint64, kbID string, since *time.Time) (int64, error) {
	q := r.db.WithContext(ctx).Model(&types.ReferenceEvent{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
	if since != nil {
		q = q.Where("created_at >= ?", *since)
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

func (r *referenceEventRepo) CountByKnowledge(ctx context.Context, tenantID uint64, kbID string, since *time.Time) (map[string]int64, error) {
	q := r.db.WithContext(ctx).Model(&types.ReferenceEvent{}).
		Select("knowledge_id, COUNT(*) as cnt").
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Group("knowledge_id")
	if since != nil {
		q = q.Where("created_at >= ?", *since)
	}
	var rows []struct {
		KnowledgeID string
		Cnt         int64
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(rows))
	for _, row := range rows {
		result[row.KnowledgeID] = row.Cnt
	}
	return result, nil
}

func (r *referenceEventRepo) TopCited(ctx context.Context, tenantID uint64, kbID string, limit int, since *time.Time) ([]types.KnowledgeCitationCount, error) {
	q := r.db.WithContext(ctx).Model(&types.ReferenceEvent{}).
		Select("knowledge_id, COUNT(*) as count, MAX(created_at) as last_cited_at").
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Group("knowledge_id").
		Order("count DESC").
		Limit(limit)
	if since != nil {
		q = q.Where("created_at >= ?", *since)
	}
	var results []types.KnowledgeCitationCount
	err := q.Scan(&results).Error
	return results, err
}

func (r *referenceEventRepo) ZeroCited(ctx context.Context, tenantID uint64, kbID string) ([]string, error) {
	// 找出 KB 内所有 knowledge_id，减去被引用过的
	var allIDs []string
	err := r.db.WithContext(ctx).Table("knowledges").
		Select("id").
		Where("knowledge_base_id = ? AND deleted_at IS NULL", kbID).
		Pluck("id", &allIDs).Error
	if err != nil {
		return nil, err
	}
	var citedIDs []string
	err = r.db.WithContext(ctx).Model(&types.ReferenceEvent{}).
		Distinct("knowledge_id").
		Where("knowledge_base_id = ?", kbID).
		Pluck("knowledge_id", &citedIDs).Error
	if err != nil {
		return nil, err
	}
	citedSet := make(map[string]bool, len(citedIDs))
	for _, id := range citedIDs {
		citedSet[id] = true
	}
	var zeroCited []string
	for _, id := range allIDs {
		if !citedSet[id] {
			zeroCited = append(zeroCited, id)
		}
	}
	return zeroCited, nil
}

func (r *referenceEventRepo) DeleteByKnowledge(ctx context.Context, knowledgeID string) error {
	return r.db.WithContext(ctx).Where("knowledge_id = ?", knowledgeID).
		Delete(&types.ReferenceEvent{}).Error
}

func (r *referenceEventRepo) DeleteBySession(ctx context.Context, sessionID string) error {
	return r.db.WithContext(ctx).Where("session_id = ?", sessionID).
		Delete(&types.ReferenceEvent{}).Error
}

func (r *referenceEventRepo) DeleteByKB(ctx context.Context, kbID string) error {
	return r.db.WithContext(ctx).Where("knowledge_base_id = ?", kbID).
		Delete(&types.ReferenceEvent{}).Error
}
