package types

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAppendUserFeedback_MergesAndPreserves(t *testing.T) {
	page := &WikiPage{Slug: "entity/acme", Title: "Acme"}

	// Pre-seed an unrelated metadata key to prove we don't clobber it.
	seed := map[string]json.RawMessage{"source": json.RawMessage(`"manual"`)}
	raw, _ := json.Marshal(seed)
	page.PageMetadata = raw

	now := time.Now()
	AppendUserFeedback(page, WikiFeedbackContribution{
		ID:                "fb-1",
		SectionAnchor:     "## 用户补充",
		SourceSessionID:   "s1",
		SourceMessageID:   "m1",
		ContributorUserID: "u1",
		ContributedAt:     now,
		Summary:           "added fact",
		IssueID:           "i1",
	})

	fb := UserFeedbackFromMetadata(page)
	require.NotNil(t, fb)
	require.Len(t, fb.Contributions, 1)
	require.Equal(t, "fb-1", fb.Contributions[0].ID)
	require.Equal(t, "i1", fb.Contributions[0].IssueID)

	// The pre-existing key is preserved.
	var meta map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(page.PageMetadata, &meta))
	require.Equal(t, json.RawMessage(`"manual"`), meta["source"])
}

func TestAppendUserFeedback_AppendsMultiple(t *testing.T) {
	page := &WikiPage{Slug: "entity/acme"}
	AppendUserFeedback(page, WikiFeedbackContribution{ID: "fb-1", Summary: "one"})
	AppendUserFeedback(page, WikiFeedbackContribution{ID: "fb-2", Summary: "two"})

	fb := UserFeedbackFromMetadata(page)
	require.NotNil(t, fb)
	require.Len(t, fb.Contributions, 2)
	require.Equal(t, "fb-1", fb.Contributions[0].ID)
	require.Equal(t, "fb-2", fb.Contributions[1].ID)
}

func TestAppendUserFeedback_NilPage(t *testing.T) {
	require.NotPanics(t, func() { AppendUserFeedback(nil, WikiFeedbackContribution{}) })
}

func TestUserFeedbackFromMetadata_Absent(t *testing.T) {
	require.Nil(t, UserFeedbackFromMetadata(&WikiPage{}))
	require.Nil(t, UserFeedbackFromMetadata(nil))

	page := &WikiPage{PageMetadata: []byte(`{"other":"value"}`)}
	require.Nil(t, UserFeedbackFromMetadata(page))
}

func TestKnowledgeBase_IsUserFeedbackEnabled(t *testing.T) {
	kb := &KnowledgeBase{}
	require.False(t, kb.IsUserFeedbackEnabled())

	// Feedback without wiki is never enabled.
	kb.IndexingStrategy = IndexingStrategy{UserFeedbackEnabled: true, WikiEnabled: false}
	require.False(t, kb.IsUserFeedbackEnabled())

	// Wiki + feedback = enabled.
	kb.IndexingStrategy = IndexingStrategy{UserFeedbackEnabled: true, WikiEnabled: true}
	require.True(t, kb.IsUserFeedbackEnabled())

	// Wiki alone is not enough.
	kb.IndexingStrategy = IndexingStrategy{UserFeedbackEnabled: false, WikiEnabled: true}
	require.False(t, kb.IsUserFeedbackEnabled())

	require.False(t, (*KnowledgeBase)(nil).IsUserFeedbackEnabled())
}
