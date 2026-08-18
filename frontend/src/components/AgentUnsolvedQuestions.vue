<template>
  <div class="unsolved-questions-panel">
    <div class="unsolved-panel-header">
      <div class="unsolved-panel-titlewrap">
        <h2 class="unsolved-panel-title">{{ $t('agentEditor.unsolvedQuestions.title') }}</h2>
        <t-popup placement="bottom-start" trigger="hover">
          <button type="button" class="unsolved-hint-trigger-btn" :aria-label="$t('agentEditor.unsolvedQuestions.title')">
            <t-icon name="info-circle" size="16px" />
          </button>
          <template #content>
            <div class="unsolved-hint-popover">
              <p class="unsolved-hint-title">{{ $t('agentEditor.unsolvedQuestions.title') }}</p>
              <p class="unsolved-hint-desc">{{ $t('agentEditor.unsolvedQuestions.description') }}</p>
            </div>
          </template>
        </t-popup>
      </div>
      <div class="unsolved-panel-actions">
        <t-radio-group v-model="filter" variant="default-filled" size="small">
          <t-radio-button value="unsolved">{{ $t('agentEditor.unsolvedQuestions.filterUnsolved') }} ({{ result?.unsolved_count ?? 0 }})</t-radio-button>
          <t-radio-button value="all">{{ $t('agentEditor.unsolvedQuestions.filterAll') }} ({{ result?.total ?? 0 }})</t-radio-button>
        </t-radio-group>
        <t-button variant="outline" size="small" @click="fetchList" :loading="loading">
          <template #icon><t-icon name="refresh" /></template>
        </t-button>
        <t-button variant="outline" size="small" @click="exportCsv" :loading="exporting">
          <template #icon><t-icon name="download" /></template>
          {{ $t('agentEditor.unsolvedQuestions.exportCsv') }}
        </t-button>
      </div>
    </div>

    <div v-if="loading && items.length === 0" class="unsolved-panel-loading">
      <t-loading size="small" />
      <span>{{ $t('common.loading') }}</span>
    </div>
    <div v-else-if="items.length === 0" class="unsolved-panel-empty">
      <t-empty :description="$t('agentEditor.unsolvedQuestions.empty')" />
    </div>
    <div v-else class="unsolved-panel-list">
      <div v-for="item in items" :key="item.id" class="unsolved-item" :class="{ 'is-resolved': item.resolved }">
        <div class="unsolved-item-main">
          <div class="unsolved-item-question">
            <t-icon name="help-circle" size="16px" class="unsolved-item-icon" />
            <span class="unsolved-item-text">{{ item.user_question }}</span>
          </div>
          <div v-if="item.reason" class="unsolved-item-reason">
            <t-icon name="info-circle" size="14px" class="unsolved-item-reason-icon" />
            <span>{{ item.reason }}</span>
          </div>
          <div class="unsolved-item-meta">
            <span class="unsolved-item-time">{{ formatTime(item.created_at) }}</span>
            <span v-if="item.status === 'failed'" class="unsolved-item-badge unsolved-item-badge--failed">
              {{ $t('agentEditor.unsolvedQuestions.statusFailed') }}
            </span>
            <span v-else-if="item.resolved" class="unsolved-item-badge unsolved-item-badge--resolved">
              {{ $t('agentEditor.unsolvedQuestions.statusResolved') }}
            </span>
            <span v-else-if="item.status === 'unsolved'" class="unsolved-item-badge unsolved-item-badge--unsolved">
              {{ $t('agentEditor.unsolvedQuestions.statusUnsolved') }}
            </span>
          </div>
        </div>
        <div class="unsolved-item-actions">
          <t-button
            v-if="!readOnly"
            variant="text"
            size="small"
            :theme="item.resolved ? 'default' : 'primary'"
            @click="toggleResolve(item)"
          >
            {{ item.resolved ? $t('agentEditor.unsolvedQuestions.markUnresolved') : $t('agentEditor.unsolvedQuestions.markResolved') }}
          </t-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, onMounted } from 'vue';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  exportAgentUnsolvedQuestions,
  listAgentUnsolvedQuestions,
  markUnsolvedQuestionResolved,
  type AgentUnsolvedQuestion,
  type AgentUnsolvedQuestionListResult,
} from '@/api/agent/unsolved-question';

const props = defineProps<{
  agentId: string;
  readOnly?: boolean;
}>();

const loading = ref(false);
const exporting = ref(false);
const items = ref<AgentUnsolvedQuestion[]>([]);
const result = ref<AgentUnsolvedQuestionListResult | null>(null);
const filter = ref<'unsolved' | 'all'>('unsolved');

const fetchList = async () => {
  loading.value = true;
  try {
    const res = await listAgentUnsolvedQuestions(props.agentId, {
      only_unsolved: filter.value === 'unsolved',
      limit: 100,
      offset: 0,
    });
    items.value = res?.data?.items ?? [];
    result.value = res?.data ?? null;
  } catch (err) {
    console.warn('[UnsolvedQuestions] Failed to load:', err);
    items.value = [];
  } finally {
    loading.value = false;
  }
};

const toggleResolve = async (item: AgentUnsolvedQuestion) => {  const next = !item.resolved;
  const prev = item.resolved;
  item.resolved = next;
  try {
    await markUnsolvedQuestionResolved(props.agentId, item.id, next);
    MessagePlugin.success(next
      ? '已标记为已处理'
      : '已标记为未处理');
    await fetchList();
  } catch (err) {
    item.resolved = prev;
    MessagePlugin.error('操作失败');
  }
};

const formatTime = (iso?: string) => {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

const exportCsv = async () => {
  exporting.value = true;
  try {
    const blob = await exportAgentUnsolvedQuestions(props.agentId, {
      only_unsolved: filter.value === 'unsolved',
    });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = `unsolved_questions_${props.agentId}_${Date.now()}.csv`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    URL.revokeObjectURL(url);
  } catch (err) {
    console.warn('[UnsolvedQuestions] Export failed:', err);
    MessagePlugin.error('导出失败');
  } finally {
    exporting.value = false;
  }
};

watch(filter, fetchList);
watch(() => props.agentId, fetchList);
onMounted(fetchList);
</script>

<style scoped>
.unsolved-questions-panel {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.unsolved-panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.unsolved-panel-titlewrap {
  display: flex;
  align-items: center;
  gap: 8px;
}

.unsolved-panel-title {
  font-size: 16px;
  font-weight: 600;
  margin: 0;
}

.unsolved-hint-trigger-btn {
  border: none;
  background: transparent;
  cursor: pointer;
  padding: 0;
  color: var(--td-text-color-placeholder);
  display: inline-flex;
  align-items: center;
}

.unsolved-panel-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.unsolved-panel-loading,
.unsolved-panel-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 48px 0;
  color: var(--td-text-color-placeholder);
}

.unsolved-panel-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.unsolved-item {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 14px;
  border: 1px solid var(--td-component-border);
  border-radius: 8px;
  background: var(--td-bg-color-container);
  transition: opacity 0.2s;
}

.unsolved-item.is-resolved {
  opacity: 0.6;
}

.unsolved-item-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.unsolved-item-question {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 14px;
  line-height: 1.5;
  color: var(--td-text-color-primary);
  word-break: break-word;
}

.unsolved-item-icon {
  flex-shrink: 0;
  margin-top: 2px;
  color: var(--td-warning-color);
}

.unsolved-item-text {
  flex: 1;
}

.unsolved-item-reason {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  font-size: 12px;
  color: var(--td-text-color-secondary);
  line-height: 1.5;
}

.unsolved-item-reason-icon {
  flex-shrink: 0;
  margin-top: 2px;
}

.unsolved-item-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--td-text-color-placeholder);
}

.unsolved-item-badge {
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 11px;
  line-height: 1.5;
}

.unsolved-item-badge--unsolved {
  background: var(--td-warning-color-1);
  color: var(--td-warning-color);
}

.unsolved-item-badge--resolved {
  background: var(--td-success-color-1);
  color: var(--td-success-color);
}

.unsolved-item-badge--failed {
  background: var(--td-error-color-1);
  color: var(--td-error-color);
}

.unsolved-item-actions {
  flex-shrink: 0;
}

.unsolved-hint-popover {
  max-width: 320px;
  padding: 4px 0;
}

.unsolved-hint-title {
  font-weight: 600;
  margin: 0 0 6px;
}

.unsolved-hint-desc {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
}
</style>
