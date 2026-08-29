package service

import "github.com/Tencent/WeKnora/internal/types"

// conflictCascadeExecutionStats is per conflict:detect task state. It is kept
// separate from the durable model so C1/C2 verifier code can be unit-tested
// without a database; Handle copies it into ConflictDetectionRun exactly once.
type conflictCascadeExecutionStats struct {
	RuleNoConflict     int
	RuleDirectConflict int
	RuleNeedsLLM       int

	LLMPairCount           int
	LLMBatchCallCount      int
	LLMSingleCallCount     int
	LLMSingleFallbackCount int
	LLMPromptTokens        int64
	LLMCompletionTokens    int64
}

func (s *conflictCascadeExecutionStats) addRuleStats(rule conflictCascadeRuleStats) {
	if s == nil {
		return
	}
	s.RuleNoConflict += rule.NoConflict
	s.RuleDirectConflict += rule.DirectConflict
	s.RuleNeedsLLM += rule.NeedsLLM
}

func (s *conflictCascadeExecutionStats) addLLMResponse(response *types.ChatResponse, batch, singleFallback bool) {
	if s == nil {
		return
	}
	if batch {
		s.LLMBatchCallCount++
	} else {
		s.LLMSingleCallCount++
		if singleFallback {
			s.LLMSingleFallbackCount++
		}
	}
	if response == nil {
		return
	}
	s.LLMPromptTokens += int64(response.Usage.PromptTokens)
	s.LLMCompletionTokens += int64(response.Usage.CompletionTokens)
}

func (s *conflictCascadeExecutionStats) llmCallCount() int {
	if s == nil {
		return 0
	}
	return s.LLMBatchCallCount + s.LLMSingleCallCount
}
