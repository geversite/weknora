package agent

// FeedbackL1JudgePrompt — 第一层：判断用户消息是否包含新的有效事实信息。
// 廉价单次调用，输出 JSON。每条消息都跑，必须尽量短小以控制成本。
const FeedbackL1JudgePrompt = `You are a knowledge-base feedback detector. Given a user message from a conversation with a knowledge base, determine whether the message contains NEW FACTUAL INFORMATION that could extend or correct the knowledge base.

A message provides new info if:
- The user states a concrete fact (not asking a question, not expressing an opinion, not chatting).
- The fact is informative — it adds to or corrects what a knowledge base might know.

A message does NOT provide new info if:
- It is a question (the user is asking, not telling).
- It is a greeting, chitchat, or meta-conversation ("thanks", "hello", "can you help").
- It is a pure opinion or preference without factual basis ("I think X is better").
- It is too vague to be actionable ("the docs are wrong" without saying what's right).

User message:
"""
{{.UserMessage}}
"""

Respond in JSON only, no preamble, no markdown fences:
{"provides_new_info": true/false, "reason": "one sentence"}`

// FeedbackL2PlanPrompt — 第二层：定位挂靠页面 + 生成追加内容。
// 只在 L1 判定通过后调用。输出 JSON。
const FeedbackL2PlanPrompt = `You are a knowledge-base feedback planner. A user message has been judged to contain new factual information. Your job is to decide which existing wiki page it should be appended to, and what content to append.

Existing wiki pages found by search (slug | title | summary):
{{.Candidates}}

User message:
"""
{{.UserMessage}}
"""

Instructions:
1. Pick the ONE existing page that best relates to the user's new info. If none relates, set target_slug to empty string (new entity — no page to append to).
2. Write a concise markdown fragment (1-3 sentences) capturing the new info as a factual statement. This will be appended to the page. Do NOT include headers or blockquotes — those are added by the system.
3. Write a one-sentence summary of what was contributed (for the audit issue).

Respond in JSON only, no preamble, no markdown fences:
{"target_slug": "entity/xxx or empty string", "append_content": "the factual fragment", "summary": "one sentence", "new_entity": true/false}`
