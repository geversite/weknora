package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// AgentUnsolvedQuestionRepository persists LLM-judged "unsolved" questions
// attached to a custom agent after each assistant reply.
type AgentUnsolvedQuestionRepository interface {
	// GetByAssistantMessageID returns the judgement row for a given assistant
	// message, if any. Used for deduplication on retry.
	GetByAssistantMessageID(ctx context.Context, tenantID uint64, assistantMessageID string) (*types.AgentUnsolvedQuestion, error)
	// Create persists a new judgement row.
	Create(ctx context.Context, record *types.AgentUnsolvedQuestion) error
	// Update writes back an existing row (e.g. status transition / resolve flag).
	Update(ctx context.Context, record *types.AgentUnsolvedQuestion) error
	// ListByAgent returns the paginated, ordered list of unsolved questions
	// for a given agent. When onlyUnsolved is true, rows with resolved=true or
	// status!=unsolved are excluded.
	ListByAgent(ctx context.Context, tenantID uint64, agentID string, onlyUnsolved bool, limit, offset int) ([]types.AgentUnsolvedQuestion, int64, error)
	// CountByAgent returns the total and unsolved counts for an agent.
	CountByAgent(ctx context.Context, tenantID uint64, agentID string) (total int64, unsolved int64, err error)
	// MarkResolved flips the resolved flag of a single row owned by (tenant, agent).
	MarkResolved(ctx context.Context, tenantID uint64, agentID, id string, resolved bool) error
	// DeleteByAgent removes all rows for an agent (used when the agent is deleted).
	DeleteByAgent(ctx context.Context, tenantID uint64, agentID string) error
	// DeleteBySession removes all rows for a session (used when the session is deleted).
	DeleteBySession(ctx context.Context, tenantID uint64, sessionID string) error
}

// AgentUnsolvedQuestionService judges whether an assistant reply fully answers
// the user's question and persists the row when it does not.
type AgentUnsolvedQuestionService interface {
	// EnsureJudgement runs the LLM judgement for a completed assistant message.
	// If the judgement already exists it returns the existing row without
	// re-running the model (unless regenerate is true).
	EnsureJudgement(ctx context.Context, sessionID, assistantMessageID string, regenerate bool) (*types.AgentUnsolvedQuestion, error)
	// ListByAgent returns the paginated unsolved-question list for an agent.
	ListByAgent(ctx context.Context, agentID string, onlyUnsolved bool, limit, offset int) (*types.AgentUnsolvedQuestionListResult, error)
	// MarkResolved toggles the manually-resolved flag of a single record.
	MarkResolved(ctx context.Context, agentID, id string, resolved bool) error
}
