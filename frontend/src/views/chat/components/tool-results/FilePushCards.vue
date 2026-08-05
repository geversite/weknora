<template>
  <div class="file-push-cards">
    <div
      v-for="file in files"
      :key="file.knowledge_id"
      class="file-push-card"
      :class="{ 'push-failed': !!file.push_failed_reason }"
    >
      <div class="card-icon">
        <t-icon :name="getFileIcon(file)" size="24px" />
      </div>

      <div class="card-body">
        <div class="card-title" :title="file.title">{{ file.title || file.file_name || file.knowledge_id }}</div>
        <div class="card-meta">
          <span v-if="file.file_name" class="meta-name">{{ file.file_name }}</span>
          <span class="meta-type">{{ (file.file_type || 'file').toUpperCase() }}</span>
          <span v-if="file.file_size" class="meta-size">{{ formatFileSize(file.file_size) }}</span>
          <span v-if="file.push_failed_reason" class="meta-failed">
            {{ t('chat.pushFailed') }}: {{ file.push_failed_reason }}
          </span>
          <span v-else-if="file.expires_in_hours" class="meta-expiry">
            {{ t('chat.expiresIn', { hours: file.expires_in_hours }) }}
          </span>
        </div>
      </div>

      <div v-if="!file.push_failed_reason && file.download_url" class="card-action">
        <a
          :href="file.download_url"
          target="_blank"
          rel="noopener noreferrer"
          class="download-btn"
          :download="file.file_name || true"
        >
          <t-icon name="download" size="16px" />
          <span>{{ t('chat.download') }}</span>
        </a>
      </div>
    </div>

    <div v-if="!files.length" class="empty-state">
      {{ t('chat.filePushEmpty') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';
import type { FilePushData } from '@/types/tool-results';
import { formatFileSize, getFileIcon } from '@/utils/files';

const props = defineProps<{
  data: FilePushData;
}>();

const { t } = useI18n();

const files = computed(() => props.data?.files ?? []);
</script>

<style lang="less" scoped>
@import './tool-results.less';

.file-push-cards {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0 8px 8px 8px;
}

.file-push-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid @card-border;
  border-radius: @card-radius;
  background: var(--td-bg-color-container);

  &.push-failed {
    opacity: 0.75;
    background: var(--td-bg-color-secondarycontainer);
  }

  .card-icon {
    flex-shrink: 0;
    color: var(--td-brand-color);
    display: flex;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    border-radius: 6px;
    background: var(--td-brand-color-light);
  }

  .card-body {
    flex: 1;
    min-width: 0;
  }

  .card-title {
    font-size: 13px;
    font-weight: 500;
    color: var(--td-text-color-primary);
    line-height: 1.4;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .card-meta {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 3px;
    font-size: 11px;
    color: var(--td-text-color-secondary);
    line-height: 1.5;
    flex-wrap: wrap;

    .meta-name {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      max-width: 160px;
    }

    .meta-failed {
      color: var(--td-error-color);
    }

    .meta-expiry {
      color: var(--td-warning-color);
    }
  }

  .card-action {
    flex-shrink: 0;
  }

  .download-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 4px 10px;
    font-size: 12px;
    color: var(--td-brand-color);
    background: var(--td-brand-color-light);
    border: 1px solid var(--td-brand-color-light);
    border-radius: 6px;
    text-decoration: none;
    transition: opacity 0.15s ease;

    &:hover {
      opacity: 0.85;
    }
  }
}

.empty-state {
  font-size: 12px;
  color: var(--td-text-color-placeholder);
  text-align: center;
  padding: 14px;
  border: 1px dashed @card-border;
  border-radius: @card-radius;
  background: var(--td-bg-color-secondarycontainer);
}
</style>
