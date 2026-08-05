<template>
  <div class="citation-stats-panel">
    <div class="section-header">
      <h3>{{ t('knowledgeEditor.citationStats.title') }}</h3>
      <p class="section-desc">{{ t('knowledgeEditor.citationStats.desc') }}</p>
    </div>

    <div v-if="loading" class="stats-loading">
      <t-loading size="small" />
    </div>

    <div v-else-if="error" class="stats-error">
      <t-alert theme="warning" :message="error" />
    </div>

    <div v-else-if="stats" class="stats-content">
      <!-- 总览卡片 -->
      <div class="overview-cards">
        <div class="overview-card">
          <div class="overview-card__value">{{ stats.total_count }}</div>
          <div class="overview-card__label">{{ t('knowledgeEditor.citationStats.totalCount') }}</div>
        </div>
        <div class="overview-card">
          <div class="overview-card__value">{{ stats.recent_count }}</div>
          <div class="overview-card__label">{{ t('knowledgeEditor.citationStats.recentCount') }}</div>
        </div>
        <div class="overview-card">
          <div class="overview-card__value">{{ stats.zero_cited_ids?.length ?? 0 }}</div>
          <div class="overview-card__label">{{ t('knowledgeEditor.citationStats.zeroCitedCount') }}</div>
        </div>
      </div>

      <!-- 最常引用 -->
      <div class="stats-section">
        <h4>{{ t('knowledgeEditor.citationStats.topCitedTitle') }}</h4>
        <t-empty v-if="!stats.top_cited || stats.top_cited.length === 0" :description="t('knowledgeEditor.citationStats.noTopCited')" />
        <t-list v-else size="small">
          <t-list-item v-for="(item, index) in stats.top_cited" :key="item.knowledge_id" class="top-cited-item">
            <div class="top-cited-rank" :class="`rank-${index + 1}`">{{ index + 1 }}</div>
            <div class="top-cited-info">
              <span class="top-cited-title">{{ fileTitle(item.knowledge_id) }}</span>
              <span class="top-cited-id">{{ item.knowledge_id }}</span>
            </div>
            <t-tag theme="primary" variant="light" class="top-cited-count">
              {{ item.count }} {{ t('knowledgeEditor.citationStats.times') }}
            </t-tag>
          </t-list-item>
        </t-list>
      </div>

      <!-- 零引用文件 -->
      <div class="stats-section">
        <h4>{{ t('knowledgeEditor.citationStats.zeroCitedTitle') }}</h4>
        <t-empty v-if="!stats.zero_cited_ids || stats.zero_cited_ids.length === 0" :description="t('knowledgeEditor.citationStats.noZeroCited')" />
        <t-list v-else size="small" :split="false">
          <t-list-item v-for="id in stats.zero_cited_ids" :key="id" class="zero-cited-item">
            <t-icon name="delete" class="zero-cited-icon" />
            <div class="zero-cited-info">
              <span class="zero-cited-title">{{ fileTitle(id) }}</span>
              <span class="zero-cited-id">{{ id }}</span>
            </div>
          </t-list-item>
        </t-list>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getCitationStats, listKnowledgeFiles, type CitationStats } from '@/api/knowledge-base'

const props = defineProps<{
  kbId: string
}>()

const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const stats = ref<CitationStats | null>(null)
// knowledge_id -> title 映射（用于把引用到的文件 ID 展示为标题）
const fileTitleMap = ref<Record<string, string>>({})

async function loadFileTitles() {
  if (!props.kbId) return
  try {
    const pageSize = 500
    const res = await listKnowledgeFiles(props.kbId, { page: 1, page_size: pageSize })
    const items = res?.data?.list || res?.data?.items || res?.data || []
    const map: Record<string, string> = {}
    for (const f of items) {
      if (f && f.id) map[f.id] = f.title || f.name || f.id
    }
    fileTitleMap.value = map
  } catch (e) {
    // 标题解析失败不影响统计展示，仅降级显示 ID
    fileTitleMap.value = {}
  }
}

function fileTitle(id: string) {
  return fileTitleMap.value[id] || id
}

async function loadStats() {
  if (!props.kbId) return
  loading.value = true
  error.value = ''
  try {
    const res = await getCitationStats(props.kbId)
    if (res?.success) {
      stats.value = res.data
    } else {
      error.value = t('knowledgeEditor.citationStats.loadFailed')
    }
  } catch (e: any) {
    error.value = e?.message || t('knowledgeEditor.citationStats.loadFailed')
  } finally {
    loading.value = false
  }
}

watch(
  () => props.kbId,
  () => {
    stats.value = null
    if (props.kbId) {
      loadFileTitles()
      loadStats()
    }
  },
  { immediate: true },
)
</script>

<style scoped>
.citation-stats-panel {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.section-header h3 {
  margin: 0 0 4px;
  font-size: 16px;
  font-weight: 600;
}
.section-desc {
  margin: 0;
  font-size: 13px;
  color: var(--td-text-color-secondary);
}
.stats-loading,
.stats-error {
  padding: 24px 0;
  display: flex;
  justify-content: center;
}
.stats-error {
  width: 100%;
}
.overview-cards {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
}
.overview-card {
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-component-border);
  border-radius: var(--td-radius-medium);
  padding: 16px;
  text-align: center;
}
.overview-card__value {
  font-size: 28px;
  font-weight: 700;
  color: var(--td-brand-color);
}
.overview-card__label {
  margin-top: 4px;
  font-size: 13px;
  color: var(--td-text-color-secondary);
}
.stats-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.stats-section h4 {
  margin: 8px 0 0;
  font-size: 14px;
  font-weight: 600;
}
.top-cited-item {
  display: flex;
  align-items: center;
  gap: 10px;
}
.top-cited-rank {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: var(--td-bg-color-component);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  font-weight: 600;
  flex-shrink: 0;
}
.top-cited-rank.rank-1 {
  background: #ffd666;
  color: #614700;
}
.top-cited-rank.rank-2 {
  background: #d9d9d9;
  color: #514b3c;
}
.top-cited-rank.rank-3 {
  background: #d7a97e;
  color: #4b2d12;
}
.top-cited-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.top-cited-title {
  font-size: 14px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.top-cited-id,
.zero-cited-id {
  font-size: 12px;
  color: var(--td-text-color-placeholder);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.zero-cited-item {
  display: flex;
  align-items: center;
  gap: 10px;
}
.zero-cited-icon {
  color: var(--td-warning-color);
  flex-shrink: 0;
}
.zero-cited-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.zero-cited-title {
  font-size: 14px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
