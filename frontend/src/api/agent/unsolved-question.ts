import { get, post, put } from '@/utils/request'

// 智能体未解决问题记录
export interface AgentUnsolvedQuestion {
  id: string
  tenant_id: number
  agent_id: string
  session_id: string
  assistant_message_id: string
  user_question: string
  answer_summary: string
  reason?: string
  status: 'pending' | 'resolved' | 'unsolved' | 'failed'
  model_id?: string
  prompt_tokens?: number
  completion_tokens?: number
  latency_ms?: number
  error_code?: string
  resolved: boolean
  generated_at?: string
  created_at: string
  updated_at: string
}

export interface AgentUnsolvedQuestionListResult {
  items: AgentUnsolvedQuestion[]
  total: number
  unsolved_count: number
}

/**
 * 触发未解决问题判定。在一次助手回复完成后由前端调用，
 * 后端用 LLM 判断回答是否完善解决了用户问题，若未完善则记录到该智能体名下。
 */
export function ensureUnsolvedJudge(sessionId: string, messageId: string, regenerate = false) {
  return post<{ data: AgentUnsolvedQuestion }>(
    `/api/v1/sessions/${sessionId}/messages/${messageId}/unsolved-judge`,
    { regenerate },
  )
}

/** 获取智能体的未解决问题列表 */
export function listAgentUnsolvedQuestions(
  agentId: string,
  params?: { only_unsolved?: boolean; limit?: number; offset?: number },
) {
  const query = new URLSearchParams()
  query.set('only_unsolved', String(params?.only_unsolved ?? true))
  if (params?.limit) query.set('limit', String(params.limit))
  if (params?.offset) query.set('offset', String(params.offset))
  return get<{ data: AgentUnsolvedQuestionListResult }>(
    `/api/v1/agents/${agentId}/unsolved-questions?${query.toString()}`,
  )
}

/** 标记未解决问题为已处理/未处理 */
export function markUnsolvedQuestionResolved(agentId: string, questionId: string, resolved: boolean) {
  return put<{ success: boolean }>(
    `/api/v1/agents/${agentId}/unsolved-questions/${questionId}/resolve`,
    { resolved },
  )
}
