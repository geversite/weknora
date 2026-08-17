package tools

import "strings"

// normalizeKnowledgeID strips wiki-style page-slug prefixes so a slug such as
// "summary/<uuid>" can be resolved back to the underlying document's raw
// knowledge_id.
//
// The wiki subsystem addresses pages by slug (summary/<id>, folder_summary/<id>,
// folder/<id>, wiki/...), whereas push_files / get_document_info expect the raw
// knowledge_id. When the model reuses a wiki slug it saw in a tool result as a
// knowledge_ids value (instead of the bare document id), the prefix would make
// the lookup fail with "knowledge not found". Stripping the known prefixes lets
// the exact document still be reached.
func normalizeKnowledgeID(raw string) string {
	s := strings.TrimSpace(raw)
	for _, prefix := range []string{
		"summary/",
		"folder_summary/",
		"folder/",
		"wiki/",
	} {
		if len(s) > len(prefix) && strings.HasPrefix(s, prefix) {
			return strings.TrimSpace(s[len(prefix):])
		}
	}
	return s
}
