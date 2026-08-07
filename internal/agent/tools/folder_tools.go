package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

var browseFoldersBaseTool = BaseTool{
	name: ToolBrowseFolders,
	description: `List the folder tree of a knowledge base, showing folder names, paths, file counts,
summary availability, AND the concrete files inside every folder — including files
in all subfolders (recursively). Use this when the user asks about the KB's
structure, wants to navigate folders, or you need to know which files live where
before deciding where to search.

The folder path uses "/" as separator (e.g. "/产品线A/协议规范/").
Files are indented under their folder; subfolders appear nested below their parent.
Folders marked "[summary available]" have an LLM-generated summary you can read
with the read_folder_summary tool.`,
	schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "knowledge_base_id": {
      "type": "string",
      "description": "The knowledge base ID to browse folders for (the bN value shown in runtime context)"
    }
  },
  "required": ["knowledge_base_id"]
}`),
}

// browseFoldersTool lists the folder tree of a KB as a readable outline.
type browseFoldersTool struct {
	BaseTool
	folderService        interfaces.KnowledgeFolderService
	knowledgeBaseService interfaces.KnowledgeBaseService
}

// NewBrowseFoldersTool creates a new browse_folders tool.
func NewBrowseFoldersTool(
	folderService interfaces.KnowledgeFolderService,
	knowledgeBaseService interfaces.KnowledgeBaseService,
) *browseFoldersTool {
	return &browseFoldersTool{
		BaseTool:             browseFoldersBaseTool,
		folderService:        folderService,
		knowledgeBaseService: knowledgeBaseService,
	}
}

func (t *browseFoldersTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	var params struct {
		KnowledgeBaseID string `json:"knowledge_base_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &types.ToolResult{Success: false, Error: "Invalid parameters: " + err.Error()}, nil
	}
	if params.KnowledgeBaseID == "" {
		return &types.ToolResult{Success: false, Error: "knowledge_base_id is required"}, nil
	}

	kb, err := t.knowledgeBaseService.GetKnowledgeBaseByID(ctx, params.KnowledgeBaseID)
	if err != nil || kb == nil {
		return &types.ToolResult{Success: false, Error: "Knowledge base not found"}, nil
	}
	if !kb.IsFolderGovernanceEnabled() {
		return &types.ToolResult{
			Success: true,
			Output:  "This knowledge base does not have folder governance enabled. Files are not organized into folders.",
		}, nil
	}

	tree, err := t.folderService.ListTree(ctx, params.KnowledgeBaseID)
	if err != nil {
		return &types.ToolResult{Success: false, Error: "Failed to list folders: " + err.Error()}, nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Folder Tree for %s\n\n", kb.Name))
	b.WriteString("Each folder line lists its `folder_id` in square brackets — pass that value to " +
		"`read_folder_summary` or to `knowledge_search`'s `folder_id` parameter. " +
		"Files are listed under their folder, including files in all subfolders.\n\n")

	if tree == nil || len(tree.Nodes) == 0 {
		b.WriteString("No folders. All files are at the root level.\n")
	} else {
		// ListTree returns the flattened tree ordered by path, so parent folders
		// precede their children. Listing each folder's direct files therefore
		// covers every file across the whole tree, including subfolders.
		for _, node := range tree.Nodes {
			indent := strings.Repeat("  ", node.Depth)
			summaryTag := ""
			switch node.SummaryStatus {
			case types.FolderSummaryStatusCompleted:
				summaryTag = " [summary available]"
			case types.FolderSummaryStatusPending, types.FolderSummaryStatusProcessing:
				summaryTag = " [summary pending]"
			}
			// Every node returned by ListTree — including the Depth-0 root folder
			// such as "/航信应知应会" — is a real folder with its own ID. Always
			// pass node.ID to ListByFolderPaged; do NOT treat Depth 0 as the empty
			// FolderRootID, or we would list un-filed files instead of the root
			// folder's actual contents.
			b.WriteString(fmt.Sprintf("%s- %s (%d files)%s [id: %s]\n",
				indent, node.Name, node.FileCount, summaryTag, node.ID))
			if f := t.listFolderFiles(ctx, params.KnowledgeBaseID, node.ID, indent); f != "" {
				b.WriteString(f)
			}
		}
	}

	return &types.ToolResult{Success: true, Output: b.String()}, nil
}

// listFolderFiles lists the names of the files directly inside a folder, one per
// line indented under the folder. It is best-effort and token-capped: at most
// browseFolderMaxFilesPerDir files are shown per folder, and the total number of
// files across the whole tree is capped to avoid blowing up the prompt.
func (t *browseFoldersTool) listFolderFiles(ctx context.Context, kbID, folderID, indent string) string {
	const perDir = 20
	files, _, err := t.folderService.ListByFolderPaged(ctx, kbID, folderID, 1, perDir)
	if err != nil || len(files) == 0 {
		return ""
	}
	fileIndent := indent + "    "
	var b strings.Builder
	for _, f := range files {
		name := f.Title
		if name == "" {
			name = f.FileName
		}
		if name == "" {
			continue
		}
		fmt.Fprintf(&b, "%s- %s\n", fileIndent, name)
	}
	return b.String()
}

var readFolderSummaryBaseTool = BaseTool{
	name: ToolReadFolderSummary,
	description: `Read the LLM-generated summary of a specific folder. The summary describes
what knowledge domain the folder covers, lists core files, key conclusions,
subfolder themes, and an activity profile.

Use this when:
- The user asks "what is folder X about?"
- You need a high-level overview of a sub-domain before searching specific files
- You want to understand whether a folder is relevant before retrieving chunks

Pass the folder_id from the browse_folders tool or the <folders> section in the context.`,
	schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "folder_id": {
      "type": "string",
      "description": "The folder ID to read the summary for"
    }
  },
  "required": ["folder_id"]
}`),
}

// readFolderSummaryTool reads the LLM-generated summary of a single folder.
type readFolderSummaryTool struct {
	BaseTool
	folderService  interfaces.KnowledgeFolderService
	summaryService interfaces.FolderSummaryService
}

// NewReadFolderSummaryTool creates a new read_folder_summary tool.
func NewReadFolderSummaryTool(
	folderService interfaces.KnowledgeFolderService,
	summaryService interfaces.FolderSummaryService,
) *readFolderSummaryTool {
	return &readFolderSummaryTool{
		BaseTool:       readFolderSummaryBaseTool,
		folderService:  folderService,
		summaryService: summaryService,
	}
}

func (t *readFolderSummaryTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	var params struct {
		FolderID string `json:"folder_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &types.ToolResult{Success: false, Error: "Invalid parameters: " + err.Error()}, nil
	}
	if params.FolderID == "" {
		return &types.ToolResult{Success: false, Error: "folder_id is required"}, nil
	}

	folder, err := t.folderService.GetByID(ctx, params.FolderID)
	if err != nil || folder == nil {
		return &types.ToolResult{Success: false, Error: "Folder not found"}, nil
	}

	summary, err := t.summaryService.Get(ctx, folder.KnowledgeBaseID, params.FolderID)
	if err != nil || summary == nil || strings.TrimSpace(summary.Content) == "" {
		return &types.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("Folder '%s' (path: %s) does not have a summary yet.", folder.Name, folder.Path),
		}, nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Folder Summary: %s\n", folder.Name))
	b.WriteString(fmt.Sprintf("**Path:** %s\n\n", folder.Path))
	if summary.IsManualEdit {
		b.WriteString("> Note: This summary was manually edited by a human.\n\n")
	}
	b.WriteString(summary.Content)

	return &types.ToolResult{Success: true, Output: b.String()}, nil
}
