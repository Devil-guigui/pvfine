<script setup lang="ts">
import { Folder20Regular } from "@vicons/fluent";
import { computed, h } from "vue";
import { NAlert, NButton, NEmpty, NModal, NProgress, NSpin, NTree, useMessage, type TreeOption } from "naive-ui";
import { useArchiveDiffStore, type DiffTreeNode } from "../stores/archiveDiff";
import { useSettingsStore } from "../stores/settings";
import { useExportStore } from "../stores/export";
import { archiveName, diffOperation } from "../archiveDiff";
import { computeDiffLines } from "../textDiff";
import UnifiedDiff from "./UnifiedDiff.vue";

const diff = useArchiveDiffStore();
const settings = useSettingsStore();
const exporter = useExportStore();
const message = useMessage();
const handledClickEvents = new WeakSet<MouseEvent>();
const directoryToggleKeys = new Set<string>();
const oldPath = computed(() => diff.reverse ? diff.status?.currentPath : diff.status?.otherPath);
const newPath = computed(() => diff.reverse ? diff.status?.otherPath : diff.status?.currentPath);
const operationLabels: Record<string, string> = { add: "新增", modify: "变更", delete: "删除" };
const operationMarks: Record<string, string> = { add: "A", modify: "M", delete: "D" };
const parsedFile = computed(() => diff.displayedFile);
const parsedLines = computed(() => computeDiffLines(parsedFile.value));
// A phase may finish while the next phase is still running. Only a ready,
// displayed result represents completion; never round pending work to 100%.
const progress = computed(() => diff.status?.total ? Math.min(diff.busy ? 99 : 100, Math.floor(diff.status.processed / diff.status.total * 100)) : 0);
const progressLabel = computed(() => {
  const state = diff.rootLoading ? "building" : diff.status?.state;
  const labels: Record<string, string> = {
    loading: "正在加载归档", comparing: "正在比较文件", classifying: "正在整理差异",
    verifying: "正在复核文本内容", sorting: "正在排序差异", building: "正在生成差异树", "sorting-tree": "正在排序差异树",
  };
  const label = labels[state ?? "loading"] ?? "正在处理";
  return diff.status?.total && !diff.rootLoading ? `${label} ${diff.status.processed.toLocaleString()} / ${diff.status.total.toLocaleString()}` : `${label}…`;
});
async function exportDiff() {
  const status = diff.status;
  if (!status || status.state !== "ready" || diff.busy) return;
  try {
    const path = await exporter.open({ source: "archive-diff", scopes: [], commitId: "", diffId: status.id, reverse: diff.reverse },
      `导出差异（${archiveName(oldPath.value || "")} → ${archiveName(newPath.value || "")}）`);
    if (path) message.success(`已导出差异到 ${path}`);
  } catch (error: any) {
    message.error(`导出差异失败: ${error?.message ?? error}`);
  }
}
function renderLabel({ option }: { option: TreeOption }) {
  const node = (option as DiffTreeNode).node;
  const op = diffOperation(node.operation, diff.reverse);
  return h("span", { class: ["tree-label", node.isDir ? "" : `operation-${op}`], title: node.path }, [
    h("span", { class: "tree-mark" }, node.isDir ? h(Folder20Regular, { width: 14, height: 14 }) : operationMarks[op]),
    h("span", { class: "tree-name" }, node.name),
    node.isDir ? h("span", { class: "tree-count" }, String(node.count)) : null,
  ]);
}
async function openNode(option: TreeOption) {
  const node = (option as DiffTreeNode).node;
  if (!node.isDir) { await diff.openFile(node); return; }
  const key = String(option.key);
  if (directoryToggleKeys.has(key)) return;
  directoryToggleKeys.add(key);
  try {
    const id = diff.status?.id;
    await diff.loadChildren(option);
    if (!id || diff.status?.id !== id) return;
    diff.expandedKeys = diff.expandedKeys.includes(key)
      ? diff.expandedKeys.filter(item => item !== key) : [...diff.expandedKeys, key];
  } finally { directoryToggleKeys.delete(key); }
}
function isTreeControl(event: MouseEvent) {
  return event.target instanceof Element && !!event.target.closest("[data-switcher], [data-checkbox]");
}
function nodeProps({ option }: { option: TreeOption }) {
  const node = (option as DiffTreeNode).node;
  return {
    onClick: (event: MouseEvent) => {
      // NTree block-line can invoke this handler twice for the same event.
      if (handledClickEvents.has(event)) return;
      handledClickEvents.add(event);
      if (isTreeControl(event) || settings.explorerOpenMode !== "single-click") return;
      event.stopPropagation();
      void openNode(option);
    },
    onDblclick: (event: MouseEvent) => {
      if (isTreeControl(event) || settings.explorerOpenMode !== "double-click") return;
      event.stopPropagation();
      void openNode(option);
    },
    onKeydown: (event: KeyboardEvent) => {
      if (event.key === "Enter" && !node.isDir) { event.preventDefault(); void diff.openFile(node); }
    },
  };
}
</script>
<template>
  <NModal :show="diff.visible" :mask-closable="false" @update:show="value => { if (!value) diff.close(); }">
    <section class="archive-diff-modal" role="dialog" aria-modal="true" aria-label="归档对比">
      <header class="modal-heading"><h2>归档对比</h2><NButton quaternary aria-label="关闭归档对比" @click="diff.close">关闭</NButton></header>
      <div class="compare-toolbar">
        <NButton :disabled="diff.busy || diff.selecting" :loading="diff.selecting" @click="diff.selectArchive">{{ diff.otherPath ? '更换对比归档' : '选择对比归档' }}</NButton>
        <span v-if="diff.otherPath" class="archive-filename" :title="diff.otherPath">{{ archiveName(diff.otherPath) }}</span>
        <NButton :disabled="!diff.status || diff.status.state !== 'ready'" @click="diff.reverse = !diff.reverse">切换方向</NButton>
        <NButton :disabled="!diff.otherPath || diff.busy || diff.selecting" @click="diff.refresh">刷新</NButton>
        <NButton v-if="diff.busy" :disabled="diff.starting" @click="diff.cancel">取消比较</NButton>
        <NButton :disabled="diff.busy || diff.status?.state !== 'ready' || !diff.tree.length || exporter.visible" @click="exportDiff">导出差异</NButton>
      </div>
      <div v-if="diff.status" class="compare-direction">
        <span :title="oldPath"><small>基准{{ diff.reverse ? '（当前归档）' : '（另一份归档）' }}</small>{{ archiveName(oldPath || '') }}</span>
        <b aria-hidden="true">→</b>
        <span :title="newPath"><small>目标{{ diff.reverse ? '（另一份归档）' : '（当前归档）' }}</small>{{ archiveName(newPath || '') }}</span>
      </div>
      <NAlert v-if="diff.error" type="error" class="compare-error">{{ diff.error }}</NAlert>
      <div v-if="diff.busy" class="compare-progress">
        <span>{{ progressLabel }}</span>
        <NProgress v-if="diff.status?.total && !diff.rootLoading" type="line" :percentage="progress" :show-indicator="false" />
      </div>
      <div class="compare-summary">
        <span class="operation-add">A 新增 {{ diff.counts.added }}</span>
        <span class="operation-modify">M 变更 {{ diff.counts.modified }}</span>
        <span class="operation-delete">D 删除 {{ diff.counts.deleted }}</span>
        <small>结果为快照，点击刷新更新</small>
      </div>
      <div class="compare-body">
        <aside class="compare-tree">
          <NTree v-if="diff.tree.length" v-model:selected-keys="diff.selectedKeys" v-model:expanded-keys="diff.expandedKeys"
            :data="diff.tree" :on-load="diff.loadChildren" :render-label="renderLabel" :node-props="nodeProps"
            virtual-scroll block-line :expand-on-click="false" :selectable="true" class="difference-tree" />
          <NEmpty v-else :description="diff.busy ? progressLabel : diff.status?.state === 'ready' ? '两份归档内容一致' : diff.status?.state === 'cancelled' ? '比较已取消，点击刷新重试' : '选择另一份归档开始比较'" />
        </aside>
        <main class="compare-detail">
          <NSpin v-if="diff.fileLoading" class="detail-loading" />
          <NAlert v-else-if="diff.fileError" type="error">{{ diff.fileError }}</NAlert>
          <template v-else-if="parsedFile">
            <div class="file-heading"><span :class="`operation-${parsedFile.operation}`">{{ operationLabels[parsedFile.operation] }}</span><strong :title="parsedFile.path">{{ parsedFile.path }}</strong></div>
            <div class="file-metadata">
              <div><small>基准</small><span v-if="parsedFile.beforeExists">类型 {{ parsedFile.beforeType }}，{{ parsedFile.beforeSize.toLocaleString() }} 字节</span><span v-else>不存在</span><code v-if="parsedFile.beforeExists && parsedFile.beforeHash" :title="parsedFile.beforeHash">{{ parsedFile.beforeHash }}</code></div>
              <div><small>目标</small><span v-if="parsedFile.afterExists">类型 {{ parsedFile.afterType }}，{{ parsedFile.afterSize.toLocaleString() }} 字节</span><span v-else>不存在</span><code v-if="parsedFile.afterExists && parsedFile.afterHash" :title="parsedFile.afterHash">{{ parsedFile.afterHash }}</code></div>
            </div>
            <UnifiedDiff v-if="parsedFile.textAvailable && parsedLines.lines.length" :diff="parsedFile" />
            <NEmpty v-else :description="parsedFile.message || (!parsedFile.beforeText && !parsedFile.afterText ? `${operationLabels[parsedFile.operation]}空文件，没有文本行级差异` : '没有文本行级差异')" class="detail-empty" />
          </template>
          <NEmpty v-else description="双击左侧文件查看内容 diff" class="detail-empty" />
        </main>
      </div>
    </section>
  </NModal>
</template>
<style scoped>
.archive-diff-modal { width: min(1320px, 94vw); height: 88vh; display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--pvf-border-normal); border-radius: 10px; background: var(--pvf-surface-card); color: var(--pvf-text-primary); }
.modal-heading { display: flex; align-items: center; justify-content: space-between; padding: 12px 20px; border-bottom: 1px solid var(--pvf-border-subtle); }
h2 { margin: 0; font-size: 17px; font-weight: 600; }
.compare-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; padding: 12px 20px; }
.archive-filename { flex: 1; min-width: 80px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--pvf-text-secondary); }
.compare-direction { display: flex; align-items: center; gap: 24px; padding: 10px 20px; background: var(--pvf-surface-subtle); }
.compare-direction > span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.compare-direction small { display: block; margin-bottom: 4px; color: var(--pvf-text-muted); }
.compare-error { margin: 8px 20px; }
.compare-progress { display: grid; gap: 6px; padding: 8px 20px; }
.compare-summary { display: flex; gap: 18px; align-items: center; padding: 10px 20px; border-bottom: 1px solid var(--pvf-border-subtle); }
.compare-summary small { margin-left: auto; color: var(--pvf-text-muted); }
.operation-add, :deep(.operation-add) { color: var(--pvf-success-hover); }
.operation-modify, :deep(.operation-modify) { color: var(--pvf-warning-hover); }
.operation-delete, :deep(.operation-delete) { color: var(--pvf-error-hover); }
.compare-body { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(220px, 28%) minmax(0, 1fr); }
.compare-tree { min-height: 0; display: flex; flex-direction: column; justify-content: center; padding: 8px; border-right: 1px solid var(--pvf-border-subtle); }
.difference-tree { flex: 1; min-height: 0; height: 100%; }
:deep(.tree-label) { display: flex; align-items: center; gap: 6px; min-width: 0; }
:deep(.tree-mark) { width: 16px; font-family: monospace; flex-shrink: 0; }
:deep(.tree-name) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
:deep(.tree-count) { margin-left: auto; padding-left: 6px; color: var(--pvf-text-muted); font-size: 11px; }
.compare-detail { display: flex; flex-direction: column; min-width: 0; min-height: 0; overflow: hidden; }
.file-heading { display: flex; gap: 10px; padding: 12px; border-bottom: 1px solid var(--pvf-border-subtle); }
.file-heading strong { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-metadata { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; padding: 10px 12px; color: var(--pvf-text-secondary); font-size: 12px; }
.file-metadata > div { display: grid; gap: 3px; min-width: 0; }
.file-metadata small { color: var(--pvf-text-muted); }
.file-metadata code { font-size: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.detail-empty, .detail-loading { margin: auto; padding: 24px; }
@media (max-width: 700px) { .compare-body { grid-template-columns: 190px minmax(0, 1fr); } .compare-summary { gap: 10px; flex-wrap: wrap; } .compare-summary small { width: 100%; margin: 0; } }
</style>
