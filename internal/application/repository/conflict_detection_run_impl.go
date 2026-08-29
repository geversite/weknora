package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type conflictDetectionRunRepo struct {
	db *gorm.DB
}

func NewConflictDetectionRunRepository(db *gorm.DB) interfaces.ConflictDetectionRunRepository {
	return &conflictDetectionRunRepo{db: db}
}

func (r *conflictDetectionRunRepo) Create(ctx context.Context, run *types.ConflictDetectionRun) error {
	if run == nil {
		return nil
	}
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.DetectorVersion == "" {
		run.DetectorVersion = types.ConflictDetectorVersion
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	if run.FinishedAt.IsZero() {
		run.FinishedAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *conflictDetectionRunRepo) ListByKnowledgeBase(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.ConflictDetectionRun, error) {
	var runs []*types.ConflictDetectionRun
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Order("created_at ASC").
		Find(&runs).Error
	return runs, err
}
