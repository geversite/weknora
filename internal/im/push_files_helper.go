package im

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// appendPushedFileLinks appends markdown download links for the files pushed by
// the push_files tool to the end of the IM message content. IM channels do not
// render the structured file_push cards, so the download URLs are surfaced as
// plain links in the message body. Returns the input content unchanged when
// there are no pushed files or they cannot be resolved.
func appendPushedFileLinks(
	ctx context.Context,
	content string,
	msg *types.Message,
	knowledgeService interfaces.KnowledgeService,
	fileService interfaces.FileService,
) string {
	if msg == nil || len(msg.PushedKnowledgeIDs) == 0 || knowledgeService == nil || fileService == nil {
		return content
	}
	var ids []string
	if err := json.Unmarshal(msg.PushedKnowledgeIDs, &ids); err != nil || len(ids) == 0 {
		return content
	}

	var links []string
	for _, kid := range ids {
		k, err := knowledgeService.GetKnowledgeByIDOnly(ctx, kid)
		if err != nil || k == nil || k.FilePath == "" {
			continue
		}
		// 文档所有者关闭了推送权限则跳过（不向 IM 追加下载链接）
		if k.PushAllowed != nil && !*k.PushAllowed {
			continue
		}
		url, err := fileService.GetFileURL(ctx, k.FilePath)
		if err != nil || url == "" || strings.HasPrefix(url, "local://") {
			continue
		}
		title := k.Title
		if title == "" {
			title = k.FileName
		}
		if title == "" {
			title = kid
		}
		links = append(links, fmt.Sprintf("- [%s](%s)", title, url))
	}

	if len(links) == 0 {
		return content
	}

	suffix := "\n\n" + strings.Join(links, "\n")
	if content == "" {
		return strings.TrimPrefix(suffix, "\n")
	}
	return content + suffix
}
