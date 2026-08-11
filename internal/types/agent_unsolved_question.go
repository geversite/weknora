package types

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	// UnsolvedStatusPending 表示判定仍在生成中
	UnsolvedStatusPending = "pending"
	// UnsolvedStatusResolved 表示回答完善地解决了问题
	UnsolvedStatusResolved = "resolved"
	// UnsolvedStatusUnsolved 表示回答未能完善解决问题（记录为未解决问题）
	UnsolvedStatusUnsolved = "unsolved"
	// UnsolvedStatusFailed 表示判定调用失败
	UnsolvedStatusFailed = "failed"
)

// AgentUnsolvedQuestion 记录一个智能体在某次对话中未能完善回答的用户问题。
//
// 设计上参考 MessageSuggestionSet：每次助手回答完成后由前端触发判定，
// 当 LLM 判定回答未能完善解决问题时，以 status=unsolved 持久化一行。
// 智能体编辑页通过 ListAgentUnsolvedQuestions 展示该 agent 名下的未解决问题。
type AgentUnsolvedQuestion struct {
	// 唯一主键
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// 租户 ID
	TenantID uint64 `json:"tenant_id" gorm:"not null;index"`
	// 智能体 ID
	AgentID string `json:"agent_id" gorm:"type:varchar(36);not null;index"`
	// 智能体所属租户（共享智能体场景下可能与 TenantID 不同）
	AgentTenantID uint64 `json:"-" gorm:"not null;default:0"`
	// 关联的会话 ID
	SessionID string `json:"session_id" gorm:"type:varchar(36);not null;index"`
	// 触发本次判定的助手消息 ID
	AssistantMessageID string `json:"assistant_message_id" gorm:"type:varchar(36);not null;index"`
	// 用户原始问题（截断后存储，便于直接展示）
	UserQuestion string `json:"user_question" gorm:"type:text;not null"`
	// 助手回答摘要（截断后存储，便于运营人员复核）
	AnswerSummary string `json:"answer_summary" gorm:"type:text;not null;default:''"`
	// LLM 给出的判定理由（为什么认为未解决）
	Reason string `json:"reason,omitempty" gorm:"type:text;not null;default:''"`
	// 判定状态：pending / resolved / unsolved / failed
	Status string `json:"status" gorm:"type:varchar(16);not null;index"`
	// 判定使用的模型 ID
	ModelID string `json:"model_id,omitempty" gorm:"type:varchar(64);not null;default:''"`
	// 判定消耗的 prompt token 数
	PromptTokens int `json:"prompt_tokens,omitempty" gorm:"not null;default:0"`
	// 判定消耗的 completion token 数
	CompletionTokens int `json:"completion_tokens,omitempty" gorm:"not null;default:0"`
	// 判定耗时（毫秒）
	LatencyMs int64 `json:"latency_ms,omitempty" gorm:"not null;default:0"`
	// 错误码（status=failed 时填写）
	ErrorCode string `json:"error_code,omitempty" gorm:"type:varchar(64);not null;default:''"`
	// 是否已被运营人员标记为"已处理"
	Resolved bool `json:"resolved" gorm:"not null;default:false;index"`
	// 生成时间
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	// 创建时间
	CreatedAt time.Time `json:"created_at"`
	// 更新时间
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 返回表名
func (AgentUnsolvedQuestion) TableName() string { return "agent_unsolved_questions" }

// BeforeCreate 在创建前自动生成 ID
func (q *AgentUnsolvedQuestion) BeforeCreate(_ *gorm.DB) error {
	if q.ID == "" {
		q.ID = uuid.NewString()
	}
	return nil
}

// AgentUnsolvedQuestionListResult 列表查询结果
type AgentUnsolvedQuestionListResult struct {
	Items      []AgentUnsolvedQuestion `json:"items"`
	Total      int64                   `json:"total"`
	UnsolvedCount int64                `json:"unsolved_count"`
}

// UnsolvedJudgeRequest LLM 判定请求的输入参数
type UnsolvedJudgeInput struct {
	UserQuestion  string
	Answer        string
	History       string
}

// UnsolvedJudgeResult LLM 判定结果
type UnsolvedJudgeResult struct {
	// Resolved=true 表示回答完善解决了问题；false 表示未解决
	Resolved bool `json:"resolved"`
	// 判定理由
	Reason string `json:"reason"`
}

// Value / Scan 让 UnsolvedJudgeResult 可作为 JSON 列存储（备用）
func (u UnsolvedJudgeResult) Value() (driver.Value, error) {
	return json.Marshal(u)
}

func (u *UnsolvedJudgeResult) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	var b []byte
	switch v := value.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return nil
	}
	return json.Unmarshal(b, u)
}
