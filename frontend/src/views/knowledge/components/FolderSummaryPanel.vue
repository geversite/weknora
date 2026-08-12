<template>
  <div class="folder-summary-panel">
    <div class="summary-actions">
      <t-button variant="outline" size="small" :loading="refreshing" @click="refreshSummary">
        <template #icon><t-icon name="refresh" /></template>
        {{ $t('knowledge.refreshSummary') }}
      </t-button>
      <t-button theme="primary" size="small" :disabled="!canEdit" @click="editMode = !editMode">
        {{ editMode ? $t('common.cancel') : $t('knowledge.editSummary') }}
      </t-button>
    </div>

    <t-alert v-if="summary?.is_manual_edit" theme="info" :close="false" class="summary-alert">
      {{ $t('knowledge.manualEditNotice') }}
    </t-alert>

    <t-loading :loading="loading" class="summary-loading">
      <div v-if="!editMode" class="summary-content" v-html="renderedContent" @click="handleContentClick"></div>
      <div v-else class="summary-editor">
        <t-textarea v-model="editContent" :autosize="{ minRows: 12, maxRows: 30 }" />
        <div class="summary-editor-actions">
          <t-button theme="primary" size="small" :loading="saving" @click="saveEdit">
            {{ $t('common.save') }}
          </t-button>
        </div>
      </div>
    </t-loading>

    <div class="summary-meta" v-if="summary">
      <span v-if="summary.generated_at">{{ $t('knowledge.generatedAt') }}: {{ formatDate(summary.generated_at) }}</span>
      <span v-if="summary.edited_at">{{ $t('knowledge.editedAt') }}: {{ formatDate(summary.edited_at) }}</span>
      <span>{{ $t('knowledge.version') }}: v{{ summary.summary_version }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  getFolderSummary,
  editFolderSummary,
  refreshFolderSummary,
  type FolderSummary,
  type KnowledgeFolderNode,
} from '@/api/knowledge-folder'

const router = useRouter()
const route = useRoute()

const props = defineProps<{
  kbId: string
  folder: KnowledgeFolderNode
  canEdit?: boolean
}>()

const emit = defineEmits<{ (e: 'refreshed'): void }>()

const summary = ref<FolderSummary | null>(null)
const editMode = ref(false)
const editContent = ref('')
const loading = ref(false)
const refreshing = ref(false)
const saving = ref(false)

const renderedContent = computed(() => {
  if (!summary.value?.content) return '<p class="summary-none">暂无摘要内容</p>'
  let html = summary.value.content
  // Wiki links [[slug|name]] → clickable links that jump to the wiki tab.
  // Process them BEFORE regular markdown links so the [[...]] syntax isn't
  // mangled by the [text](url) pass.
  html = html.replace(/\[\[([^\]]+)\]\]/g, (_, inner: string) => {
    const pipeIdx = inner.indexOf('|')
    const slug = pipeIdx > 0 ? inner.substring(0, pipeIdx).trim() : inner.trim()
    const display = pipeIdx > 0 ? inner.substring(pipeIdx + 1).trim() : slug
    return `<a href="#" class="folder-summary-wiki-link" data-slug="${slug}">${display}</a>`
  })
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>')
  html = html.replace(/\n/g, '<br/>')
  return html
})

function handleContentClick(e: MouseEvent) {
  const target = (e.target as HTMLElement)?.closest('a.folder-summary-wiki-link') as HTMLAnchorElement | null
  if (!target) return
  e.preventDefault()
  const slug = target.dataset.slug
  if (!slug) return
  // Jump to the wiki tab with ?slug=... so WikiBrowser opens that page.
  router.replace({ query: { ...route.query, tab: 'wiki', slug } })
}

async function loadSummary() {
  loading.value = true
  try {
    const res = await getFolderSummary(props.kbId, props.folder.id)
    summary.value = res
    editContent.value = res?.content || ''
  } catch {
    summary.value = null
    editContent.value = ''
  } finally {
    loading.value = false
  }
}

async function refreshSummary() {
  refreshing.value = true
  try {
    await refreshFolderSummary(props.kbId, props.folder.id)
    MessagePlugin.success('摘要刷新已排队')
    setTimeout(loadSummary, 3000)
    emit('refreshed')
  } catch (e: any) {
    MessagePlugin.error(e?.message || '刷新摘要失败')
  } finally {
    refreshing.value = false
  }
}

async function saveEdit() {
  saving.value = true
  try {
    await editFolderSummary(props.kbId, props.folder.id, { content: editContent.value })
    MessagePlugin.success('已保存')
    editMode.value = false
    loadSummary()
  } catch (e: any) {
    MessagePlugin.error(e?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

function formatDate(iso?: string) {
  if (!iso) return '-'
  const d = new Date(iso)
  return isNaN(d.getTime()) ? '-' : d.toLocaleString()
}

watch(() => props.folder.id, loadSummary, { immediate: true })
</script>

<style scoped>
.folder-summary-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.summary-actions {
  display: flex;
  gap: 8px;
}
.summary-alert {
  margin: 0;
}
.summary-loading {
  min-height: 60px;
}
.summary-content {
  line-height: 1.7;
  padding: 12px 16px;
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-component-stroke);
  border-radius: 6px;
}
.summary-editor {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.summary-editor-actions {
  display: flex;
  justify-content: flex-end;
}
.summary-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  color: var(--td-text-color-secondary);
  font-size: 13px;
}
.summary-none {
  color: var(--td-text-color-secondary);
}
:deep(.folder-summary-wiki-link) {
  color: var(--td-brand-color);
  text-decoration: none;
  cursor: pointer;
}
:deep(.folder-summary-wiki-link:hover) {
  text-decoration: underline;
}
</style>
