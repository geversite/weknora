<template>
  <div class="section-content kb-conflict-queue">
    <div class="section-header">
      <div class="kb-conflict-title-row">
        <h3 class="section-title">{{ t('knowledgeEditor.conflict.title') }}</h3>
        <button
          type="button"
          class="kb-conflict-refresh"
          :disabled="loading"
          :title="t('knowledgeEditor.conflict.refresh')"
          :aria-label="t('knowledgeEditor.conflict.refresh')"
          @click="reload"
        >
          <t-icon :name="loading ? 'loading' : 'refresh'" :class="{ 'sq-refresh-spin': loading }" />
        </button>
      </div>
      <p class="section-desc">{{ t('knowledgeEditor.conflict.description') }}</p>
    </div>

    <div v-if="error" class="kb-conflict-branch kb-conflict-branch--error">
      <t-alert theme="error" :message="error">
        <template #operation>
          <t-button size="small" @click="reload">{{ t('common.retry') }}</t-button>
        </template>
      </t-alert>
    </div>

    <div class="kb-conflict-stats" v-if="stats">
      <div class="kb-conflict-stat">
        <span class="kb-conflict-stat-value">{{ stats.pending }}</span>
        <span class="kb-conflict-stat-label">{{ t('knowledgeEditor.conflict.statPending') }}</span>
      </div>
      <div class="kb-conflict-stat">
        <span class="kb-conflict-stat-value">{{ stats.not_conflict }}</span>
        <span class="kb-conflict-stat-label">{{ t('knowledgeEditor.conflict.statNotConflict') }}</span>
      </div>
      <div class="kb-conflict-stat">
        <span class="kb-conflict-stat-value">{{ stats.newer_wins + stats.older_wins + stats.keep_both }}</span>
        <span class="kb-conflict-stat-label">{{ t('knowledgeEditor.conflict.statResolved') }}</span>
      </div>
    </div>

    <div class="kb-conflict-filter">
      <t-radio-group v-model="activeStatus" variant="default-filled" @change="onStatusChange">
        <t-radio-button value="pending">{{ t('knowledgeEditor.conflict.filterPending') }}</t-radio-button>
        <t-radio-button value="">{{ t('knowledgeEditor.conflict.filterAll') }}</t-radio-button>
      </t-radio-group>
      <span class="kb-conflict-tip">{{ t('knowledgeEditor.conflict.cellTip') }}</span>
    </div>

    <div class="data-table-shell kb-conflict-table-shell">
      <t-table
        row-key="id"
        :data="conflicts"
        :columns="columns"
        size="medium"
        hover
        :loading="loading"
        :pagination="pagination"
        @page-change="onPageChange"
        @cell-click="onCellClick"
      >
        <template #type="{ row }">
          <t-tag :theme="typeTheme(row.conflict_type)" variant="light">
            {{ typeLabel(row.conflict_type) }}
          </t-tag>
        </template>

        <template #contentA="{ row }">
          <div class="kb-conflict-content" tabindex="0">{{ row.content_a }}</div>
        </template>

        <template #contentB="{ row }">
          <div class="kb-conflict-content" tabindex="0">{{ row.content_b }}</div>
        </template>

        <template #reason="{ row }">
          <div class="kb-conflict-reason" tabindex="0">{{ row.llm_reason || '-' }}</div>
        </template>

        <template #actions="{ row }">
          <t-button size="small" variant="text" @click.stop="openDetail(row)">
            {{ t('knowledgeEditor.conflict.viewDetail') }}
          </t-button>
          <template v-if="row.status === 'pending'">
            <t-popconfirm
              :content="t('knowledgeEditor.conflict.confirmKeepBoth')"
              @confirm="doResolve(row, 'resolved_keep_both')"
            >
              <t-button size="small" variant="outline">{{ t('knowledgeEditor.conflict.keepBoth') }}</t-button>
            </t-popconfirm>
            <t-popconfirm
              :content="t('knowledgeEditor.conflict.confirmNewer')"
              @confirm="doResolve(row, 'resolved_newer_wins')"
            >
              <t-button size="small" theme="primary" variant="outline">{{ t('knowledgeEditor.conflict.newerWins') }}</t-button>
            </t-popconfirm>
            <t-popconfirm
              :content="t('knowledgeEditor.conflict.confirmOlder')"
              @confirm="doResolve(row, 'resolved_older_wins')"
            >
              <t-button size="small" theme="warning" variant="outline">{{ t('knowledgeEditor.conflict.olderWins') }}</t-button>
            </t-popconfirm>
            <t-popconfirm
              :content="t('knowledgeEditor.conflict.confirmNotConflict')"
              @confirm="doResolve(row, 'resolved_not_conflict')"
            >
              <t-button size="small" theme="success" variant="text">{{ t('knowledgeEditor.conflict.notConflict') }}</t-button>
            </t-popconfirm>
          </template>
          <span v-else class="kb-conflict-resolved">{{ statusLabel(row.status) }}</span>
        </template>
      </t-table>
    </div>

    <t-dialog
      v-model:visible="detailVisible"
      :header="t('knowledgeEditor.conflict.detailTitle')"
      :width="760"
      :footer="false"
      placement="center"
      class="kb-conflict-detail-dialog"
    >
      <div v-if="detailRow" class="kb-conflict-detail">
        <div class="kb-conflict-detail-meta">
          <div class="kb-conflict-detail-meta-row">
            <span class="kb-conflict-detail-meta-key">{{ t('knowledgeEditor.conflict.detailStatus') }}:</span>
            <t-tag v-if="detailRow.status === 'pending'" theme="warning" variant="light">
              {{ t('knowledgeEditor.conflict.filterPending') }}
            </t-tag>
            <span v-else>{{ statusLabel(detailRow.status) }}</span>
          </div>
          <div class="kb-conflict-detail-meta-row">
            <span class="kb-conflict-detail-meta-key">{{ t('knowledgeEditor.conflict.detailType') }}:</span>
            <t-tag :theme="typeTheme(detailRow.conflict_type)" variant="light">
              {{ typeLabel(detailRow.conflict_type) }}
            </t-tag>
          </div>
          <div class="kb-conflict-detail-meta-row">
            <span class="kb-conflict-detail-meta-key">{{ t('knowledgeEditor.conflict.detailReason') }}:</span>
            <div class="kb-conflict-detail-reason">{{ detailRow.llm_reason || '-' }}</div>
          </div>
          <div class="kb-conflict-detail-meta-row kb-conflict-detail-meta-row--ids">
            <div>
              <span class="kb-conflict-detail-meta-key">{{ t('knowledgeEditor.conflict.detailKnowledgeA') }}:</span>
              <code class="kb-conflict-detail-code">{{ detailRow.knowledge_id_a }}</code>
            </div>
            <div>
              <span class="kb-conflict-detail-meta-key">{{ t('knowledgeEditor.conflict.detailChunkA') }}:</span>
              <code class="kb-conflict-detail-code">{{ detailRow.chunk_id_a }}</code>
            </div>
          </div>
          <div class="kb-conflict-detail-meta-row kb-conflict-detail-meta-row--ids">
            <div>
              <span class="kb-conflict-detail-meta-key">{{ t('knowledgeEditor.conflict.detailKnowledgeB') }}:</span>
              <code class="kb-conflict-detail-code">{{ detailRow.knowledge_id_b }}</code>
            </div>
            <div>
              <span class="kb-conflict-detail-meta-key">{{ t('knowledgeEditor.conflict.detailChunkB') }}:</span>
              <code class="kb-conflict-detail-code">{{ detailRow.chunk_id_b }}</code>
            </div>
          </div>
          <div class="kb-conflict-detail-meta-row kb-conflict-detail-meta-row--ids">
            <div>
              <span class="kb-conflict-detail-meta-key">{{ t('knowledgeEditor.conflict.detailDetectedAt') }}:</span>
              <span>{{ detailRow.created_at }}</span>
            </div>
            <div v-if="detailRow.resolved_at">
              <span class="kb-conflict-detail-meta-key">{{ t('knowledgeEditor.conflict.detailResolvedAt') }}:</span>
              <span>{{ detailRow.resolved_at }}</span>
            </div>
          </div>
        </div>

        <div class="kb-conflict-detail-section">
          <div class="kb-conflict-detail-section-title">
            {{ t('knowledgeEditor.conflict.colContentA') }}
          </div>
          <pre class="kb-conflict-detail-content">{{ detailRow.content_a }}</pre>
        </div>

        <div class="kb-conflict-detail-section">
          <div class="kb-conflict-detail-section-title">
            {{ t('knowledgeEditor.conflict.colContentB') }}
          </div>
          <pre class="kb-conflict-detail-content">{{ detailRow.content_b }}</pre>
        </div>
      </div>
    </t-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  getKnowledgeConflicts,
  getKnowledgeConflictStats,
  resolveKnowledgeConflict,
  type KnowledgeConflict,
  type KnowledgeConflictStatus,
  type KnowledgeConflictType,
} from '@/api/knowledge-base'

const props = defineProps<{
  kbId: string
  active: boolean
}>()

const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const conflicts = ref<KnowledgeConflict[]>([])
const stats = ref<{ pending: number; not_conflict: number; newer_wins: number; older_wins: number; keep_both: number } | null>(null)
const activeStatus = ref<'pending' | ''>('pending')
const page = ref(1)
const pageSize = ref(20)

const pagination = computed(() => ({
  current: page.value,
  pageSize: pageSize.value,
  total: total.value,
  showJumper: true,
}))

const total = ref(0)

const typeTheme = (type: KnowledgeConflictType): 'danger' | 'warning' | 'primary' | 'default' => {
  switch (type) {
    case 'fact_contradiction': return 'danger'
    case 'partial_contradiction': return 'warning'
    case 'version_update': return 'primary'
    default: return 'default'
  }
}

const typeLabel = (type: KnowledgeConflictType) => {
  switch (type) {
    case 'fact_contradiction': return t('knowledgeEditor.conflict.typeFactContradiction')
    case 'partial_contradiction': return t('knowledgeEditor.conflict.typePartialContradiction')
    case 'version_update': return t('knowledgeEditor.conflict.typeVersionUpdate')
    default: return type
  }
}

const statusLabel = (status: KnowledgeConflictStatus) => {
  switch (status) {
    case 'resolved_keep_both': return t('knowledgeEditor.conflict.statusKeepBoth')
    case 'resolved_newer_wins': return t('knowledgeEditor.conflict.statusNewer')
    case 'resolved_older_wins': return t('knowledgeEditor.conflict.statusOlder')
    case 'resolved_not_conflict': return t('knowledgeEditor.conflict.statusNotConflict')
    default: return status
  }
}

const columns = computed(() => [
  { colKey: 'type', title: t('knowledgeEditor.conflict.colType'), width: 120 },
  { colKey: 'contentA', title: t('knowledgeEditor.conflict.colContentA'), minWidth: 240, ellipsis: true },
  { colKey: 'contentB', title: t('knowledgeEditor.conflict.colContentB'), minWidth: 240, ellipsis: true },
  { colKey: 'reason', title: t('knowledgeEditor.conflict.colReason'), minWidth: 180, ellipsis: true },
  { colKey: 'created_at', title: t('knowledgeEditor.conflict.colTime'), width: 160 },
  { colKey: 'actions', title: t('knowledgeEditor.conflict.colActions'), width: 300, fixed: 'right' as const },
])

const onStatusChange = () => {
  page.value = 1
  loadConflicts()
}

const onPageChange = (ctx: { current: number; pageSize: number }) => {
  page.value = ctx.current
  if (ctx.pageSize) pageSize.value = ctx.pageSize
  loadConflicts()
}

const loadStats = async () => {
  try {
    const res: any = await getKnowledgeConflictStats(props.kbId)
    stats.value = res?.data || res
  } catch (e) {
    // stats 是 best-effort，失败不阻断列表
  }
}

const loadConflicts = async () => {
  loading.value = true
  error.value = ''
  try {
    const res: any = await getKnowledgeConflicts(props.kbId, {
      status: activeStatus.value || undefined,
      page: page.value,
      page_size: pageSize.value,
    })
    const data = res?.data || res
    conflicts.value = data?.list || []
    total.value = data?.total || 0
  } catch (e: any) {
    error.value = e?.message || t('knowledgeEditor.conflict.loadError')
  } finally {
    loading.value = false
  }
}

const reload = () => {
  loadStats()
  loadConflicts()
}

const detailVisible = ref(false)
const detailRow = ref<KnowledgeConflict | null>(null)

const openDetail = (row: KnowledgeConflict) => {
  detailRow.value = row
  detailVisible.value = true
}

// Clicking a non-action cell opens the detail dialog for quick inspection.
// The actions column is excluded so the adjudication buttons and the popconfirms
// they host never trigger the dialog accidentally.
const onCellClick = (context: { row: KnowledgeConflict; col?: { colKey?: string } }) => {
  if (context?.col?.colKey === 'actions') return
  if (context?.row) openDetail(context.row)
}

const doResolve = async (row: KnowledgeConflict, resolution: string) => {
  try {
    await resolveKnowledgeConflict(props.kbId, { conflict_id: row.id, resolution })
    MessagePlugin.success(t('knowledgeEditor.conflict.resolveSuccess'))
    reload()
  } catch (e: any) {
    MessagePlugin.error(e?.message || t('knowledgeEditor.conflict.resolveError'))
  }
}

watch(() => props.active, (val) => {
  if (val) reload()
})

onMounted(() => {
  if (props.active) reload()
})
</script>

<style lang="less" scoped>
.kb-conflict-queue {
  width: 100%;
}

.kb-conflict-title-row {
  display: flex;
  align-items: center;
  gap: 8px;

  .section-title {
    margin: 0;
  }

  .kb-conflict-refresh {
    border: none;
    background: transparent;
    cursor: pointer;
    color: var(--td-text-color-secondary);
    padding: 4px;

    &:hover {
      color: var(--td-brand-color);
    }
  }
}

.kb-conflict-stats {
  display: flex;
  gap: 16px;
  margin: 16px 0;

  .kb-conflict-stat {
    flex: 1;
    padding: 16px;
    background: var(--td-bg-color-container);
    border-radius: 8px;
    border: 1px solid var(--td-component-stroke);
    display: flex;
    flex-direction: column;
    gap: 4px;

    .kb-conflict-stat-value {
      font-size: 24px;
      font-weight: 600;
      color: var(--td-brand-color);
    }

    .kb-conflict-stat-label {
      font-size: 13px;
      color: var(--td-text-color-secondary);
    }
  }
}

.kb-conflict-filter {
  margin-bottom: 16px;
}

.kb-conflict-tip {
  margin-left: 12px;
  font-size: 12px;
  color: var(--td-text-color-placeholder);
}

.kb-conflict-content {
  max-height: 200px;
  overflow-y: auto;
  overflow-x: hidden;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 12px;
  line-height: 1.6;
  color: var(--td-text-color-secondary);
  padding: 4px 6px;
  background: var(--td-bg-color-container-hover);
  border-radius: 4px;
  outline: none;

  &:focus-visible {
    box-shadow: 0 0 0 2px var(--td-brand-color-light);
  }
}

.kb-conflict-reason {
  max-height: 120px;
  overflow-y: auto;
  white-space: pre-wrap;
  font-size: 12px;
  line-height: 1.5;
  color: var(--td-text-color-secondary);
  padding: 4px 6px;
  background: var(--td-bg-color-container-hover);
  border-radius: 4px;
  outline: none;

  &:focus-visible {
    box-shadow: 0 0 0 2px var(--td-brand-color-light);
  }
}

.kb-conflict-detail {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.kb-conflict-detail-meta {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 14px;
  background: var(--td-bg-color-container-hover);
  border-radius: 6px;
}

.kb-conflict-detail-meta-row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 13px;
  line-height: 1.6;

  &--ids {
    display: flex;
    flex-wrap: wrap;
    gap: 12px 24px;

    > div {
      display: flex;
      align-items: center;
      gap: 6px;
      min-width: 0;
    }
  }
}

.kb-conflict-detail-meta-key {
  color: var(--td-text-color-secondary);
  white-space: nowrap;
}

.kb-conflict-detail-code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-component-stroke);
  padding: 1px 6px;
  border-radius: 4px;
  word-break: break-all;
}

.kb-conflict-detail-reason {
  flex: 1;
  white-space: pre-wrap;
  font-size: 13px;
  color: var(--td-text-color-primary);
}

.kb-conflict-detail-section {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.kb-conflict-detail-section-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--td-text-color-secondary);
}

.kb-conflict-detail-content {
  margin: 0;
  padding: 12px 14px;
  max-height: 360px;
  overflow-y: auto;
  background: var(--td-bg-color-container-hover);
  border: 1px solid var(--td-component-stroke);
  border-radius: 6px;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 13px;
  line-height: 1.6;
  color: var(--td-text-color-primary);
}

.kb-conflict-resolved {
  color: var(--td-success-color);
  font-size: 12px;
}

.sq-refresh-spin {
  display: inline-block;
  animation: kb-conflict-spin 1s linear infinite;
}

@keyframes kb-conflict-spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
</style>
