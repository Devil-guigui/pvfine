<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { NModal, NProgress, NSpin } from "naive-ui";
import { useEditorStore } from "../stores/editor";

const editor = useEditorStore();
const elapsed = ref(0);
let timer: ReturnType<typeof setInterval> | undefined;

function stopTimer(): void {
  clearInterval(timer);
  timer = undefined;
}

watch(() => !!editor.archiveSaveStage, (active) => {
  stopTimer();
  elapsed.value = 0;
  if (active) {
    const started = performance.now();
    timer = setInterval(() => {
      elapsed.value = Math.floor((performance.now() - started) / 1000);
    }, 1000);
  }
}, { immediate: true });
onUnmounted(stopTimer);

const stageLabel = computed(() => {
  if (editor.archiveSaveStage !== "finishing" && editor.archiveSaveProgress) {
    const labels: Record<string, string> = {
      backup: "正在备份源文件…",
      "backup-sync": "正在同步备份到磁盘…",
      preparing: "正在检查归档并整理字符串表…",
      packing: "正在打包归档内容…",
      metadata: "正在生成文件索引和归档结构…",
      writing: "正在写入临时文件…",
      syncing: "正在同步归档到磁盘…",
      replacing: "正在替换目标文件…",
      done: "归档已写入，正在更新工作区…",
    };
    if (labels[editor.archiveSaveProgress.phase]) return labels[editor.archiveSaveProgress.phase];
  }
  switch (editor.archiveSaveStage) {
    case "preparing": return "正在同步待保存的修改…";
    case "saving": return "正在保存到源文件…";
    case "saveAs": return "正在另存为归档…";
    case "finishing": return "正在更新保存后的工作区…";
    default: return "";
  }
});
const progress = computed(() => editor.archiveSaveStage === "finishing" || editor.archiveSaveProgress?.phase === "done"
  ? null : editor.archiveSaveProgress);
const percentage = computed(() => progress.value?.total
  ? Math.min(100, Math.floor(progress.value.done / progress.value.total * 100))
  : null);
const progressDetail = computed(() => {
  const value = progress.value;
  if (!value?.total) return "";
  if (value.unit === "bytes") {
    return `${(value.done / 1048576).toFixed(1)} / ${(value.total / 1048576).toFixed(1)} MiB`;
  }
  const unit = value.unit === "chunks" ? "个数据块" : value.unit === "files" ? "个文件" : "";
  return unit ? `${value.done.toLocaleString()} / ${value.total.toLocaleString()} ${unit}` : "";
});
const elapsedLabel = computed(() => elapsed.value < 60
  ? `${elapsed.value} 秒`
  : `${Math.floor(elapsed.value / 60)} 分 ${elapsed.value % 60} 秒`);
</script>

<template>
  <NModal
    :show="!!editor.archiveSaveStage"
    :mask-closable="false"
    :close-on-esc="false"
    :z-index="3000"
    :auto-focus="true"
  >
    <section class="archive-save-feedback" role="dialog" aria-modal="true" aria-labelledby="archive-save-title" aria-busy="true" tabindex="-1">
      <NSpin size="large" />
      <h2 id="archive-save-title">正在保存归档</h2>
      <p class="save-stage" role="status" aria-live="polite">{{ stageLabel }}</p>
      <div v-if="percentage !== null" class="save-progress">
        <NProgress type="line" :percentage="percentage" />
        <p class="save-progress-detail">当前阶段 · {{ progressDetail }}</p>
      </div>
      <p v-if="editor.archiveSaveStage === 'saveAs' && !editor.archiveSaveProgress" class="save-hint">若出现文件对话框，请先选择保存位置。</p>
      <p class="save-hint">大归档可能需要较长时间，请保持应用打开，等待保存完成。</p>
      <p class="save-elapsed">已用时间 · {{ elapsedLabel }}</p>
    </section>
  </NModal>
</template>

<style scoped>
.archive-save-feedback {
  width: min(440px, calc(100vw - 48px));
  box-sizing: border-box;
  padding: 32px;
  border: 1px solid var(--pvf-border-normal);
  border-radius: 12px;
  background: var(--pvf-surface-elevated);
  color: var(--pvf-text-primary);
  text-align: center;
  outline: none;
}
h2 { margin: 20px 0 12px; font-size: 20px; }
p { margin: 0; }
.save-stage { color: var(--pvf-text-secondary); }
.save-progress { margin-top: 20px; text-align: left; }
.save-progress-detail { margin-top: 6px; font-size: 12px; font-variant-numeric: tabular-nums; color: var(--pvf-text-secondary); }
.save-hint { margin-top: 12px; line-height: 1.7; color: var(--pvf-text-tertiary); }
.save-elapsed { margin-top: 24px; font-variant-numeric: tabular-nums; color: var(--pvf-text-secondary); }
</style>
