package session

import (
	"context"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// openAIStorageSchemeRe matches the storage references WeKnora may embed in
// LLM output (markdown images etc.) that third-party OpenAI-compatible
// clients such as Dify cannot fetch: canonical resource:// catalog aliases
// plus raw provider:// schemes. Mirrors im.storageSchemeRe.
var openAIStorageSchemeRe = regexp.MustCompile(
	`\b(?:resource://[0-9A-Za-z_-]+|` +
		`(?:storage://[0-9A-Za-z_-]+/)?` +
		`(?:local|minio|s3|cos|tos|oss|obs|ks3)://[^\s)\]>"]+)`,
)

// openAIIncompleteURLSuffixRe matches a storage URL that reaches the end of a
// streamed chunk — it may continue in the next chunk, so the streaming
// rewriter holds it back. Mirrors im.incompleteURLSuffixRe.
var openAIIncompleteURLSuffixRe = regexp.MustCompile(
	`\b(?:resource|storage|local|minio|s3|cos|tos|oss|obs|ks3)://[^\s)\]>"]*$`,
)

func isOpenAIHTTPURL(s string) bool {
	return len(s) >= 7 && strings.EqualFold(s[:7], "http://") ||
		len(s) >= 8 && strings.EqualFold(s[:8], "https://")
}

// resolveStorageRefForOpenAI resolves one storage reference via
// fileService.GetFileURL, returning an http(s) URL when possible. References
// that fail to resolve — or resolve to a non-HTTP value (local storage with
// APP_EXTERNAL_URL unset) — are returned unchanged with a WARN log, the most
// common cause of "image broken in Dify" reports.
func resolveStorageRefForOpenAI(ctx context.Context, fileService interfaces.FileService, cache map[string]string, ref string) string {
	if u, ok := cache[ref]; ok {
		return u
	}
	resolved := ref
	httpURL, err := fileService.GetFileURL(ctx, ref)
	if err != nil {
		logger.Warnf(ctx, "[openai-compat] rewrite storage URL failed (kept %s): %v", ref, err)
	} else if isOpenAIHTTPURL(httpURL) {
		resolved = httpURL
	} else {
		logger.Warnf(ctx,
			"[openai-compat] storage URL %s resolved to non-HTTP %q (set APP_EXTERNAL_URL to expose local storage via /r/); kept original",
			ref, httpURL)
	}
	cache[ref] = resolved
	return resolved
}

// rewriteResourceURLsToString performs a one-shot replacement of every storage
// reference in content with an http(s) URL. Used on the non-streaming path
// where the full answer is available at once.
func (h *Handler) rewriteResourceURLsToString(ctx context.Context, content string) string {
	if h == nil || h.fileService == nil || content == "" || !openAIStorageSchemeRe.MatchString(content) {
		return content
	}
	cache := make(map[string]string)
	return openAIStorageSchemeRe.ReplaceAllStringFunc(content, func(match string) string {
		return resolveStorageRefForOpenAI(ctx, h.fileService, cache, match)
	})
}

// openAIResourceRewriter is an incremental streaming rewriter that replaces
// storage references with http(s) URLs while never emitting a reference that
// may be split across chunk boundaries (a trailing fragment that could be an
// incomplete storage URL is held back until the rest arrives or the stream
// finishes).
type openAIResourceRewriter struct {
	ctx         context.Context
	fileService interfaces.FileService
	cache       map[string]string
	pending     string
}

func newOpenAIResourceRewriter(ctx context.Context, fileService interfaces.FileService) *openAIResourceRewriter {
	if fileService == nil {
		return nil
	}
	return &openAIResourceRewriter{
		ctx:         ctx,
		fileService: fileService,
		cache:       make(map[string]string),
	}
}

// feed rewrites complete references in input and holds back a trailing
// fragment that may be an incomplete storage URL.
func (r *openAIResourceRewriter) feed(input string) string {
	if r == nil {
		return input
	}
	combined := r.pending + input
	r.pending = ""
	safe := combined
	if loc := openAIIncompleteURLSuffixRe.FindStringIndex(combined); loc != nil {
		safe = combined[:loc[0]]
		r.pending = combined[loc[0]:]
	}
	return r.rewrite(safe)
}

// flush emits any held-back tail once the stream has finished.
func (r *openAIResourceRewriter) flush() string {
	if r == nil {
		return ""
	}
	p := r.pending
	r.pending = ""
	return r.rewrite(p)
}

func (r *openAIResourceRewriter) rewrite(s string) string {
	if s == "" || !openAIStorageSchemeRe.MatchString(s) {
		return s
	}
	return openAIStorageSchemeRe.ReplaceAllStringFunc(s, func(match string) string {
		return resolveStorageRefForOpenAI(r.ctx, r.fileService, r.cache, match)
	})
}
