import { get, post, put, del } from '../../utils/request'

// ==================== M4 文件级文件夹治理 API ====================

export interface KnowledgeFolderNode {
  id: string
  knowledge_base_id: string
  parent_id: string
  name: string
  path: string
  depth: number
  sort_order: number
  summary_status: 'none' | 'pending' | 'processing' | 'completed' | 'failed'
  file_count: number
  has_children: boolean
  created_at: string
  updated_at: string
}

export interface KnowledgeFolder {
  id: string
  tenant_id: number
  knowledge_base_id: string
  parent_id: string
  name: string
  path: string
  depth: number
  sort_order: number
  summary_status: string
  created_at: string
  updated_at: string
}

export interface FolderSummary {
  id: string
  folder_id: string
  content: string
  content_format: string
  is_manual_edit: boolean
  summary_version: number
  generated_at?: string
  edited_at?: string
}

export interface GovernanceReport {
  empty_folders: Array<{ folder_id: string; name: string; path: string }>
  imbalanced_folders: Array<{ folder_id: string; name: string; path: string; file_count: number; suggestion: string }>
  stale_summaries: Array<{ folder_id: string; name: string; generated_at: string; last_file_change: string }>
  duplicate_files: Array<{ file_hash: string; file_name: string; folder_paths: string[]; knowledge_ids: string[] }>
  deep_folders: Array<{ folder_id: string; name: string; path: string; depth: number }>
}

// 文件夹内容（子文件夹 + 文件，分页）—— 文件夹视图核心 API
export interface KnowledgeFileItem {
  id: string
  title: string
  file_path: string
  file_type: string
  status: string
  summary_status: string
  folder_id: string
  tags: any[]
  created_at: string
  updated_at: string
}

export interface FolderContent {
  folders: KnowledgeFolderNode[]
  files: KnowledgeFileItem[]
  total_files: number
  current_folder: KnowledgeFolder | null
}

// 列出文件夹树（后端返回单层 nodes + file_count + has_children，前端自建层级）
export function listFolders(kbId: string) {
  return get<{ nodes: KnowledgeFolderNode[] }>(`/api/v1/knowledge-bases/${kbId}/folders`)
}

export function createFolder(kbId: string, payload: { parent_id: string; name: string }) {
  return post<KnowledgeFolder>(`/api/v1/knowledge-bases/${kbId}/folders`, payload)
}

export function updateFolder(kbId: string, folderId: string, payload: { name?: string; parent_id?: string; move_parent?: boolean }) {
  return put<KnowledgeFolder>(`/api/v1/knowledge-bases/${kbId}/folders/${folderId}`, payload)
}

export function deleteFolder(kbId: string, folderId: string, cascadeToParent = true) {
  return del<unknown>(`/api/v1/knowledge-bases/${kbId}/folders/${folderId}?cascade_files_to_parent=${cascadeToParent}`)
}

// 获取文件夹内容（子文件夹 + 文件，分页）；folderId 为空 = 根目录
export function listFolderContent(kbId: string, folderId: string, params?: {
  page?: number
  page_size?: number
  files_only?: boolean
}) {
  const query = new URLSearchParams()
  if (folderId) query.append('folder_id', folderId)
  if (params?.page) query.append('page', String(params.page))
  if (params?.page_size) query.append('page_size', String(params.page_size))
  if (params?.files_only) query.append('files_only', 'true')
  const qs = query.toString()
  return get<FolderContent>(`/api/v1/knowledge-bases/${kbId}/folder-content${qs ? `?${qs}` : ''}`)
}

// 移动单个文件到文件夹（folderId 为空 = 移到根目录）
export function moveKnowledgeToFolder(kbId: string, knowledgeId: string, folderId: string) {
  return post<unknown>(`/api/v1/knowledge-bases/${kbId}/knowledge/${knowledgeId}/move`, { folder_id: folderId })
}

// 移动多个文件到文件夹（folderId 为空 = 移到根目录）
export function moveKnowledgeFilesToFolder(kbId: string, folderId: string, knowledgeIds: string[]) {
  return post<unknown>(`/api/v1/knowledge-bases/${kbId}/folders/${folderId || '__root__'}/files`, { knowledge_ids: knowledgeIds })
}

// 文件夹摘要
export function getFolderSummary(kbId: string, folderId: string) {
  return get<FolderSummary>(`/api/v1/knowledge-bases/${kbId}/folders/${folderId}/summary`)
}

export function generateFolderSummary(kbId: string, folderId: string) {
  return post<unknown>(`/api/v1/knowledge-bases/${kbId}/folders/${folderId}/summary/generate`, {})
}

export function refreshFolderSummary(kbId: string, folderId: string) {
  return post<unknown>(`/api/v1/knowledge-bases/${kbId}/folders/${folderId}/summary/refresh`, {})
}

export function editFolderSummary(kbId: string, folderId: string, payload: { content: string; content_format?: string }) {
  return put<unknown>(`/api/v1/knowledge-bases/${kbId}/folders/${folderId}/summary`, payload)
}

// 治理报告
export function getGovernanceReport(kbId: string) {
  return get<GovernanceReport>(`/api/v1/knowledge-bases/${kbId}/folders/governance`)
}
