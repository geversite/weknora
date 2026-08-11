package repository

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

type agentUnsolvedQuestionRepository struct {
	db *gorm.DB
}

// NewAgentUnsolvedQuestionRepository creates the GORM-backed repository.
func NewAgentUnsolvedQuestionRepository(db *gorm.DB) interfaces.AgentUnsolvedQuestionRepository {
	return &agentUnsolvedQuestionRepository{db: db}
}

func (r *agentUnsolvedQuestionRepository) GetByAssistantMessageID(
	ctx context.Context,
	tenantID uint64,
	assistantMessageID string,
) (*types.AgentUnsolvedQuestion, error) {
	var record types.AgentUnsolvedQuestion
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND assistant_message_id = ?", tenantID, assistantMessageID).
		First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *agentUnsolvedQuestionRepository) Create(
	ctx context.Context,
	record *types.AgentUnsolvedQuestion,
) error {
	if record == nil {
		return errors.New("agent unsolved question record is nil")
	}
	return r.db.WithContext(ctx).Create(record).Error
}

func (r *agentUnsolvedQuestionRepository) Update(
	ctx context.Context,
	record *types.AgentUnsolvedQuestion,
) error {
	if record == nil {
		return errors.New("agent unsolved question record is nil")
	}
	return r.db.WithContext(ctx).Save(record).Error
}

func (r *agentUnsolvedQuestionRepository) ListByAgent(
	ctx context.Context,
	tenantID uint64,
	agentID string,
	onlyUnsolved bool,
	limit, offset int,
) ([]types.AgentUnsolvedQuestion, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	query := r.db.WithContext(ctx).Model(&types.AgentUnsolvedQuestion{}).
		Where("tenant_id = ? AND agent_id = ?", tenantID, agentID)
	if onlyUnsolved {
		query = query.Where("resolved = ? AND status = ?", false, types.UnsolvedStatusUnsolved)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []types.AgentUnsolvedQuestion
	err := query.Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&records).Error
	if err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

func (r *agentUnsolvedQuestionRepository) CountByAgent(
	ctx context.Context,
	tenantID uint64,
	agentID string,
) (int64, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&types.AgentUnsolvedQuestion{}).
		Where("tenant_id = ? AND agent_id = ?", tenantID, agentID).
		Count(&total).Error; err != nil {
		return 0, 0, err
	}
	var unsolved int64
	if err := r.db.WithContext(ctx).Model(&types.AgentUnsolvedQuestion{}).
		Where("tenant_id = ? AND agent_id = ? AND resolved = ? AND status = ?",
			tenantID, agentID, false, types.UnsolvedStatusUnsolved).
		Count(&unsolved).Error; err != nil {
		return 0, 0, err
	}
	return total, unsolved, nil
}

func (r *agentUnsolvedQuestionRepository) MarkResolved(
	ctx context.Context,
	tenantID uint64,
	agentID, id string,
	resolved bool,
) error {
	result := r.db.WithContext(ctx).Model(&types.AgentUnsolvedQuestion{}).
		Where("tenant_id = ? AND agent_id = ? AND id = ?", tenantID, agentID, id).
		Update("resolved", resolved)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *agentUnsolvedQuestionRepository) DeleteByAgent(
	ctx context.Context,
	tenantID uint64,
	agentID string,
) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND agent_id = ?", tenantID, agentID).
		Delete(&types.AgentUnsolvedQuestion{}).Error
}

func (r *agentUnsolvedQuestionRepository) DeleteBySession(
	ctx context.Context,
	tenantID uint64,
	sessionID string,
) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND session_id = ?", tenantID, sessionID).
		Delete(&types.AgentUnsolvedQuestion{}).Error
}
