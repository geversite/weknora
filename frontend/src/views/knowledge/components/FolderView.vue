<template>
  <div class="folder-view">
    <!-- 面包屑导航（操作入口统一由右上角全局下拉处理，M4-fix1） -->
    <div class="folder-toolbar">
      <div class="folder-breadcrumb">
        <t-breadcrumb>
          <t-breadcrumb-item @click="navigateTo('')">
            {{ $t('knowledge.allDocuments') }}
          </t-breadcrumb-item>
          <t-breadcrumb-item v-for="crumb in breadcrumb" :key="crumb.id" @click="navigateTo(crumb.id)">
            {{ crumb.name }}
          </t-breadcrumb-item>
        </t-breadcrumb>
      </div>
    </div>

    <t-loading :loading="loading">
      <!-- 文件夹 + 文件混合网格 -->
      <div class="folder-content-grid">
        <!-- 子文件夹卡片 -->
        <div
          v-for="folder in folders"
          :key="'folder-' + folder.id"
          class="folder-card"
          :class="{ 'folder-card--drag-over': dragOverFolderId === folder.id }"
          @click="enterFolder(folder.id)"
          @dragover.prevent="onDragOver(folder.id)"
          @dragleave="onDragLeave(folder.id)"
          @drop.prevent="onDrop(folder.id)"
        >
          <div class="folder-card-icon">
            <t-icon name="folder" size="40px" />
          </div>
          <div class="folder-card-body">
            <div class="folder-card-name" :title="folder.name">{{ folder.name }}</div>
            <div class="folder-card-meta">
              <span v-if="folder.file_count > 0">{{ folder.file_count }} {{ $t('knowledge.files') }}</span>
              <t-tag v-if="folder.summary_status === 'completed'" size="small" theme="success" variant="light">
                {{ $t('knowledge.summaryReady') }}
              </t-tag>
              <t-tag v-else-if="folder.summary_status === 'processing'" size="small" theme="warning" variant="light">
                {{ $t('knowledge.summaryPending') }}
              </t-tag>
            </div>
          </div>
          <span @click.stop>
            <t-dropdown trigger="click" :options="folderMenuOptions(folder)" @click="(opt) => onFolderMenu(opt, folder)">
              <t-icon name="more" class="folder-card-more" />
            </t-dropdown>
          </span>
        </div>

        <!-- 文件卡片 -->
        <div
          v-for="file in files"
          :key="'file-' + file.id"
          class="file-card"
          draggable="true"
          @dragstart="onFileDragStart(file, $event)"
          @click="openFile(file)"
        >
          <div class="file-card-icon">
            <t-icon :name="getFileIcon(file.file_type)" size="40px" />
          </div>
          <div class="file-card-body">
            <div class="file-card-name" :title="file.title">{{ file.title }}</div>
            <div class="file-card-meta">
              <span>{{ formatDate(file.created_at) }}</span>
            </div>
          </div>
          <t-dropdown :options="fileMenuOptions(file)" @click="(opt) => onFileMenu(opt, file)">
            <t-icon name="more" class="file-card-more" @click.stop />
          </t-dropdown>
        </div>

        <!-- 空文件夹提示 -->
        <div v-if="folders.length === 0 && files.length === 0 && !loading" class="folder-empty">
          <t-icon name="folder-open" size="48px" />
          <p>{{ $t('knowledge.emptyFolder') }}</p>
        </div>
      </div>
    </t-loading>

    <!-- 文件夹摘要查看/编辑抽屉 -->
    <t-drawer v-model:visible="summaryDrawerVisible" :header="summaryTitle" size="520px">
      <FolderSummaryPanel v-if="currentSummaryFolder" :kb-id="kbId" :folder="currentSummaryFolder" @refreshed="loadContent" />
    </t-drawer>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, h } from 'vue'
import { MessagePlugin, DialogPlugin, Input as TInput } from 'tdesign-vue-next'
import {
  listFolderContent,
  deleteFolder,
  moveKnowledgeToFolder,
  updateFolder,
  type KnowledgeFolderNode,
  type KnowledgeFileItem,
  type KnowledgeFolder,
} from '@/api/knowledge-folder'
import FolderSummaryPanel from './FolderSummaryPanel.vue'

// 文件夹上传时保留的目录结构（M4-fix1）
export interface FolderStructureItem {
  file: File
  relativePath: string
}

const props = defineProps<{
  kbId: string
  canEdit: boolean
}>()

const emit = defineEmits<{
  (e: 'open-file', file: KnowledgeFileItem): void
  (e: 'upload-files', files: File[], folderId: string): void
  (e: 'upload-folder', files: File[], folderId: string, folderStructure: FolderStructureItem[]): void
  (e: 'create-folder', folderId: string): void
}>()

const loading = ref(false)
const currentFolderId = ref('') // 空 = 根目录
const folders = ref<KnowledgeFolderNode[]>([])
const files = ref<KnowledgeFileItem[]>([])
const breadcrumb = ref<{ id: string; name: string }[]>([])

// 拖拽状态
const draggingFile = ref<KnowledgeFileItem | null>(null)
const dragOverFolderId = ref('')

// 摘要抽屉状态
const summaryDrawerVisible = ref(false)
const currentSummaryFolder = ref<KnowledgeFolderNode | null>(null)

const summaryTitle = computed(() =>
  currentSummaryFolder.value ? `${currentSummaryFolder.value.name} - ${props.canEdit ? '' : ''}` : '',
)

async function loadContent() {
  loading.value = true
  try {
    const res = await listFolderContent(props.kbId, currentFolderId.value)
    folders.value = res?.folders || []
    files.value = res?.files || []
    // path_chain 从后端返回，每层都有 id 和 name，可点击跳转到对应文件夹
    updateBreadcrumb(res?.path_chain || [])
  } catch (e: any) {
    MessagePlugin.error(e?.message || '加载文件夹内容失败')
  } finally {
    loading.value = false
  }
}

function updateBreadcrumb(chain: KnowledgeFolder[]) {
  // 后端返回祖先链（root -> ... -> current），每项有 id 和 name。
  // 点击任意层级可跳转，避免总是回到根目录。
  breadcrumb.value = chain.map((f) => ({ id: f.id, name: f.name }))
}

function enterFolder(folderId: string) {
  currentFolderId.value = folderId
  loadContent()
}

function navigateTo(folderId: string) {
  currentFolderId.value = folderId
  loadContent()
}

// 拖拽文件到文件夹
function onFileDragStart(file: KnowledgeFileItem, e: DragEvent) {
  draggingFile.value = file
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', file.id)
  }
}
function onDragOver(folderId: string) {
  if (draggingFile.value) dragOverFolderId.value = folderId
}
function onDragLeave(folderId: string) {
  if (dragOverFolderId.value === folderId) dragOverFolderId.value = ''
}
async function onDrop(targetFolderId: string) {
  dragOverFolderId.value = ''
  const file = draggingFile.value
  draggingFile.value = null
  if (file) {
    try {
      await moveKnowledgeToFolder(props.kbId, file.id, targetFolderId)
      MessagePlugin.success('文件已移动')
      loadContent()
    } catch (e: any) {
      MessagePlugin.error(e?.message || '移动文件失败')
    }
  }
}

function openFile(file: KnowledgeFileItem) {
  emit('open-file', file)
}

function folderMenuOptions(folder: KnowledgeFolderNode) {
  return [
    { content: props.canEdit ? '查看摘要' : '查看摘要', value: 'summary' },
    { content: '重命名', value: 'rename', disabled: !props.canEdit },
    { content: '删除', value: 'delete', disabled: !props.canEdit },
  ]
}

function onFolderMenu(opt: any, folder: KnowledgeFolderNode) {
  switch (opt.value) {
    case 'summary':
      currentSummaryFolder.value = folder
      summaryDrawerVisible.value = true
      break
    case 'rename':
      renameFolder(folder)
      break
    case 'delete':
      removeFolder(folder)
      break
  }
}

function renameFolder(folder: KnowledgeFolderNode) {
  const inputValue = ref(folder.name)
  const dlg = DialogPlugin({
    header: '重命名文件夹',
    body: () => h('div', { style: 'padding: 8px 0;' }, [
      h('div', { style: 'margin-bottom: 8px; font-size: 14px; color: var(--td-text-color-primary);' }, '文件夹名称'),
      h(TInput, {
        modelValue: inputValue.value,
        'onUpdate:modelValue': (val: string) => { inputValue.value = val },
        placeholder: '请输入文件夹名称',
      }),
    ]),
    confirmBtn: '确定',
    onConfirm: async () => {
      const name = (inputValue.value || '').trim()
      if (!name) return
      try {
        await updateFolder(props.kbId, folder.id, { name })
        MessagePlugin.success('已重命名')
        dlg.destroy()
        loadContent()
      } catch (e: any) {
        MessagePlugin.error(e?.message || '重命名失败')
      }
    },
    onClose: () => dlg.destroy(),
  })
}

function removeFolder(folder: KnowledgeFolderNode) {
  DialogPlugin.confirm({
    header: '删除文件夹',
    body: `确定删除「${folder.name}」吗？其中的文件将移动到上级文件夹。`,
    theme: 'danger',
    confirmBtn: { content: '删除', theme: 'danger' },
    onConfirm: async () => {
      try {
        await deleteFolder(props.kbId, folder.id, true)
        MessagePlugin.success('已删除')
        loadContent()
      } catch (e: any) {
        MessagePlugin.error(e?.message || '删除失败')
      }
    },
  })
}

function fileMenuOptions(file: KnowledgeFileItem) {
  return [
    { content: '打开', value: 'open' },
    { content: '移到根目录', value: 'move-root', disabled: !props.canEdit },
  ]
}

function onFileMenu(opt: any, file: KnowledgeFileItem) {
  switch (opt.value) {
    case 'open':
      openFile(file)
      break
    case 'move-root':
      moveToRoot(file)
      break
  }
}

async function moveToRoot(file: KnowledgeFileItem) {
  try {
    await moveKnowledgeToFolder(props.kbId, file.id, '')
    MessagePlugin.success('已移到根目录')
    loadContent()
  } catch (e: any) {
    MessagePlugin.error(e?.message || '操作失败')
  }
}

function getFileIcon(fileType: string) {
  const t = (fileType || '').toLowerCase()
  if (t.includes('pdf')) return 'file-pdf'
  if (t.includes('word') || t.includes('doc')) return 'file'
  if (t.includes('excel') || t.includes('sheet') || t.includes('csv')) return 'file'
  if (t.includes('image')) return 'image'
  if (t.includes('txt')) return 'file'
  return 'file'
}

function formatDate(iso: string) {
  if (!iso) return ''
  const d = new Date(iso)
  return isNaN(d.getTime()) ? '' : d.toLocaleDateString()
}

onMounted(loadContent)

// 暴露当前文件夹 id 与刷新能力给父组件（视图联动，M4-fix1）
defineExpose({
  currentFolderId,
  refresh: loadContent,
})
</script>

<style scoped>
.folder-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 16px 4px;
}
.folder-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}
.folder-actions {
  display: flex;
  gap: 8px;
}
.folder-content-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 16px;
  justify-content: start;
}
.folder-card,
.file-card {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 20px 12px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 8px;
  cursor: pointer;
  transition: box-shadow 0.2s;
}
.folder-card:hover,
.file-card:hover {
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.08);
}
.folder-card--drag-over {
  border-color: var(--td-brand-color);
  background: var(--td-brand-color-light);
}
.folder-card-icon {
  color: var(--td-brand-color);
}
.file-card-icon {
  color: var(--td-text-color-secondary);
}
.folder-card-name,
.file-card-name {
  font-size: 14px;
  font-weight: 500;
  text-align: center;
  word-break: break-all;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.folder-card-meta,
.file-card-meta {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  color: var(--td-text-color-secondary);
  font-size: 12px;
}
.folder-card-more,
.file-card-more {
  position: absolute;
  top: 8px;
  right: 8px;
  color: var(--td-text-color-secondary);
}
.folder-empty {
  grid-column: 1 / -1;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 48px 0;
  color: var(--td-text-color-secondary);
}
</style>
