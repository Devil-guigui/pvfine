<script setup lang="ts">
/**
 * PVF 归档双栏差异对比与实时合并组件
 * 基于 @codemirror/merge MergeView：
 * - 左侧为本地/目标归档，支持直接编辑、撤销重做与 Ctrl+S 保存；
 * - 右侧为对比/基准归档，只读浏览；
 * - 两栏中间提供段落快捷合入按钮（«）；
 * - 顶部工具栏支持差异段落跳转导航、单文件全量覆盖合入以及未保存状态提示。
 *
 * @author flobit
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { NButton, NIcon, NSpin, NTag, NTooltip, useMessage } from "naive-ui";
import {
  ArrowDown20Regular,
  ArrowUp20Regular,
  CheckmarkCircle20Regular,
  Copy20Regular,
  DocumentArrowLeft20Regular,
  Open20Regular,
  Save20Regular,
  TextWrap24Regular,
} from "@vicons/fluent";
import {
  EditorView,
  ViewUpdate,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
} from "@codemirror/view";
import { Compartment, EditorState } from "@codemirror/state";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { MergeView } from "@codemirror/merge";
import { ArchiveService } from "../../bindings/pvfine/services";
import type { ArchiveDiffFile } from "../../bindings/pvfine/services/models";
import { useArchiveStore } from "../stores/archive";
import { useArchiveDiffStore } from "../stores/archiveDiff";
import { useEditorStore } from "../stores/editor";
import { pvfHighlighting, pvfLanguage } from "../pvfLanguage";

const props = defineProps<{
  file: ArchiveDiffFile;
  reverse?: boolean;
}>();

const diffStore = useArchiveDiffStore();
const archiveStore = useArchiveStore();
const editorStore = useEditorStore();
const message = useMessage();

const mergeHostRef = ref<HTMLDivElement | null>(null);
let mergeViewInstance: MergeView | null = null;

const chunksCount = ref(0);
const activeChunkIndex = ref(0);
const isSaving = ref(false);

const wrappingCompA = new Compartment();
const wrappingCompB = new Compartment();

const leftScrollbarRef = ref<HTMLDivElement | null>(null);
const rightScrollbarRef = ref<HTMLDivElement | null>(null);
const leftScrollWidth = ref(0);
const rightScrollWidth = ref(0);
const leftHasScroll = ref(false);
const rightHasScroll = ref(false);

let isSyncingFromEditor = false;
let isSyncingFromScrollbar = false;
let resizeObserver: ResizeObserver | null = null;

function updateScrollbarMetrics(): void {
  if (!mergeViewInstance) return;
  const scrollerA = mergeViewInstance.a?.scrollDOM;
  const scrollerB = mergeViewInstance.b?.scrollDOM;

  if (scrollerA) {
    leftScrollWidth.value = scrollerA.scrollWidth;
    leftHasScroll.value = scrollerA.scrollWidth > scrollerA.clientWidth + 2;
    if (leftScrollbarRef.value) {
      leftScrollbarRef.value.scrollLeft = scrollerA.scrollLeft;
    }
  }
  if (scrollerB) {
    rightScrollWidth.value = scrollerB.scrollWidth;
    rightHasScroll.value = scrollerB.scrollWidth > scrollerB.clientWidth + 2;
    if (rightScrollbarRef.value) {
      rightScrollbarRef.value.scrollLeft = scrollerB.scrollLeft;
    }
  }
}

function syncScrollbarsFromEditor(): void {
  if (isSyncingFromScrollbar || !mergeViewInstance) return;
  isSyncingFromEditor = true;
  if (leftScrollbarRef.value && mergeViewInstance.a?.scrollDOM) {
    leftScrollbarRef.value.scrollLeft = mergeViewInstance.a.scrollDOM.scrollLeft;
  }
  if (rightScrollbarRef.value && mergeViewInstance.b?.scrollDOM) {
    rightScrollbarRef.value.scrollLeft = mergeViewInstance.b.scrollDOM.scrollLeft;
  }
  isSyncingFromEditor = false;
}

function onLeftScrollbarScroll(e: Event): void {
  if (isSyncingFromEditor || !mergeViewInstance) return;
  isSyncingFromScrollbar = true;
  const target = e.target as HTMLElement;
  if (mergeViewInstance.a?.scrollDOM) {
    mergeViewInstance.a.scrollDOM.scrollLeft = target.scrollLeft;
  }
  isSyncingFromScrollbar = false;
}

function onRightScrollbarScroll(e: Event): void {
  if (isSyncingFromEditor || !mergeViewInstance) return;
  isSyncingFromScrollbar = true;
  const target = e.target as HTMLElement;
  if (mergeViewInstance.b?.scrollDOM) {
    mergeViewInstance.b.scrollDOM.scrollLeft = target.scrollLeft;
  }
  isSyncingFromScrollbar = false;
}

function applyLineWrapping(enabled: boolean): void {
  diffStore.lineWrapping = enabled;
  if (!mergeViewInstance) return;
  mergeViewInstance.a.dispatch({
    effects: wrappingCompA.reconfigure(enabled ? EditorView.lineWrapping : []),
  });
  mergeViewInstance.b.dispatch({
    effects: wrappingCompB.reconfigure(enabled ? EditorView.lineWrapping : []),
  });
  void nextTick(() => {
    updateScrollbarMetrics();
  });
}

function toggleLineWrapping(): void {
  applyLineWrapping(!diffStore.lineWrapping);
}

function handleEditorWheel(view: EditorView, e: WheelEvent): void {
  if (diffStore.lineWrapping) return;
  let dx = e.deltaX;
  if (!dx && e.shiftKey && e.deltaY) {
    dx = e.deltaY;
  }
  if (dx !== 0 && view.scrollDOM) {
    const maxScroll = view.scrollDOM.scrollWidth - view.scrollDOM.clientWidth;
    if (maxScroll > 0) {
      const nextScroll = Math.max(0, Math.min(maxScroll, view.scrollDOM.scrollLeft + dx));
      if (nextScroll !== view.scrollDOM.scrollLeft) {
        view.scrollDOM.scrollLeft = nextScroll;
        syncScrollbarsFromEditor();
        e.preventDefault();
        e.stopPropagation();
      }
    }
  }
}

function normalizeText(text?: string | null): string {
  if (!text) return "";
  return text.replaceAll("\r\n", "\n").replaceAll("\r", "\n");
}

function formatSize(bytes: number): string {
  if (bytes <= 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

function copyPath(): void {
  if (!props.file?.path) return;
  void navigator.clipboard.writeText(props.file.path);
  message.success("已复制文件路径");
}

const localExists = computed(() => (props.reverse ? props.file?.beforeExists : props.file?.afterExists));
const otherExists = computed(() => (props.reverse ? props.file?.afterExists : props.file?.beforeExists));
const localSize = computed(() => (props.reverse ? props.file?.beforeSize ?? 0 : props.file?.afterSize ?? 0));
const otherSize = computed(() => (props.reverse ? props.file?.afterSize ?? 0 : props.file?.beforeSize ?? 0));
const localText = computed(() => (props.reverse ? props.file?.beforeText : props.file?.afterText));
const otherText = computed(() => (props.reverse ? props.file?.afterText : props.file?.beforeText));

const canOpenInEditor = computed(() => {
  return archiveStore.open && !!localExists.value;
});

async function openInEditor(): Promise<void> {
  if (!archiveStore.open || !props.file?.path) return;
  try {
    const nodes = await ArchiveService.ResolveFiles([props.file.path]);
    const target = nodes?.[0];
    if (!target || target.fileIndex < 0) {
      message.warning("当前归档中未找到该文件");
      return;
    }
    await editorStore.openFile(target.fileIndex);
    message.success("已在主编辑器中打开");
  } catch (err: any) {
    message.error(err?.message || "无法在主编辑器中打开");
  }
}

function renderRevertControlElement(): HTMLElement {
  const btn = document.createElement("button");
  btn.className = "cm-revert-action-btn";
  btn.title = "合入此段落到本地";
  btn.setAttribute("aria-label", "合入此段落到本地");
  btn.textContent = "«";
  return btn;
}

function createEditorTheme() {
  return EditorView.theme(
    {
      "&": {
        height: "100%",
        fontSize: "12px",
        color: "var(--pvf-text-primary)",
        backgroundColor: "var(--pvf-surface-code)",
      },
      ".cm-scroller": {
        fontFamily: "'SF Mono', Menlo, Consolas, 'Courier New', monospace",
        lineHeight: "1.55",
        userSelect: "text",
      },
      ".cm-gutters": {
        backgroundColor: "transparent",
        border: "none",
        color: "var(--pvf-editor-gutter-text, var(--pvf-text-muted))",
      },
      ".cm-content": { padding: "8px 0" },
      ".cm-activeLine": { backgroundColor: "var(--pvf-editor-active-line, rgba(255, 255, 255, 0.05))" },
      ".cm-activeLineGutter": { backgroundColor: "var(--pvf-editor-active-line, rgba(255, 255, 255, 0.05))" },
    },
    { dark: true }
  );
}

function handleEditorUpdate(update: ViewUpdate): void {
  if (!update.docChanged) return;
  const newText = update.state.doc.toString();
  diffStore.updateDraftText(newText);
  if (mergeViewInstance) {
    chunksCount.value = mergeViewInstance.chunks.length;
  }
}

function destroyMergeView(): void {
  if (mergeViewInstance) {
    mergeViewInstance.a.scrollDOM.removeEventListener("scroll", syncScrollbarsFromEditor);
    mergeViewInstance.b.scrollDOM.removeEventListener("scroll", syncScrollbarsFromEditor);
    mergeViewInstance.destroy();
    mergeViewInstance = null;
  }
}

function initMergeView(): void {
  destroyMergeView();
  if (!mergeHostRef.value || !props.file || !props.file.textAvailable) {
    return;
  }

  const baseDoc = normalizeText(diffStore.draftText || localText.value || "");
  const targetDoc = normalizeText(otherText.value || "");
  const themeExt = createEditorTheme();

  const commonExtensions = [
    lineNumbers(),
    highlightActiveLineGutter(),
    highlightActiveLine(),
    drawSelection(),
    history(),
    pvfLanguage.extension,
    pvfHighlighting,
    themeExt,
    keymap.of([...defaultKeymap, ...historyKeymap]),
  ];

  mergeViewInstance = new MergeView({
    a: {
      doc: baseDoc,
      extensions: [
        ...commonExtensions,
        wrappingCompA.of(diffStore.lineWrapping ? EditorView.lineWrapping : []),
        keymap.of([
          {
            key: "Mod-s",
            run: () => {
              void handleSave();
              return true;
            },
          },
        ]),
        EditorView.updateListener.of((u) => {
          handleEditorUpdate(u);
          if (u.docChanged || u.geometryChanged) {
            updateScrollbarMetrics();
          }
        }),
      ],
    },
    b: {
      doc: targetDoc,
      extensions: [
        ...commonExtensions,
        wrappingCompB.of(diffStore.lineWrapping ? EditorView.lineWrapping : []),
        EditorState.readOnly.of(true),
        EditorView.editable.of(false),
        EditorView.updateListener.of((u) => {
          if (u.geometryChanged) {
            updateScrollbarMetrics();
          }
        }),
      ],
    },
    parent: mergeHostRef.value,
    revertControls: "b-to-a",
    renderRevertControl: renderRevertControlElement,
    gutter: true,
    highlightChanges: true,
    orientation: "a-b",
  });

  mergeViewInstance.a.scrollDOM.addEventListener("scroll", syncScrollbarsFromEditor);
  mergeViewInstance.b.scrollDOM.addEventListener("scroll", syncScrollbarsFromEditor);
  mergeViewInstance.a.dom.addEventListener("wheel", (e) => {
    if (mergeViewInstance) handleEditorWheel(mergeViewInstance.a, e);
  }, { passive: false });
  mergeViewInstance.b.dom.addEventListener("wheel", (e) => {
    if (mergeViewInstance) handleEditorWheel(mergeViewInstance.b, e);
  }, { passive: false });

  chunksCount.value = mergeViewInstance.chunks.length;
  activeChunkIndex.value = 0;

  void nextTick(() => {
    updateScrollbarMetrics();
  });
}

async function handleSave(): Promise<void> {
  if (!props.file?.path || !diffStore.isDraftDirty) return;
  isSaving.value = true;
  try {
    const success = await diffStore.mergeText(props.file.path, diffStore.draftText);
    if (success) {
      message.success("修改已成功保存并同步到归档");
    } else {
      message.error("保存修改失败");
    }
  } catch (err: any) {
    message.error(`保存失败: ${err?.message ?? err}`);
  } finally {
    isSaving.value = false;
  }
}

async function handleMergeCurrentFile(): Promise<void> {
  if (!props.file?.path) return;
  isSaving.value = true;
  try {
    const success = await diffStore.mergeSingleFile(props.file.path);
    if (success) {
      message.success(`已成功合入 ${props.file.path}`);
      await nextTick();
      initMergeView();
    } else {
      message.error("合入文件失败");
    }
  } catch (err: any) {
    message.error(`合入失败: ${err?.message ?? err}`);
  } finally {
    isSaving.value = false;
  }
}

function scrollToChunk(index: number): void {
  if (!mergeViewInstance || mergeViewInstance.chunks.length === 0) return;
  const total = mergeViewInstance.chunks.length;
  const clamped = ((index % total) + total) % total;
  activeChunkIndex.value = clamped;
  const chunk = mergeViewInstance.chunks[clamped];
  if (!chunk) return;

  const scroller = mergeViewInstance.dom;
  const clientHeight = scroller.clientHeight || 500;
  const lineA = mergeViewInstance.a.state.doc.lineAt(
    Math.min(chunk.fromA, mergeViewInstance.a.state.doc.length)
  );
  const totalLines = mergeViewInstance.a.state.doc.lines || 1;
  const ratio = Math.max(0, Math.min(1, lineA.number / totalLines));
  const estimatedScrollTop = Math.max(0, ratio * scroller.scrollHeight - clientHeight / 2);
  scroller.scrollTo({ top: estimatedScrollTop, behavior: "smooth" });
}

function handleGlobalKeydown(e: KeyboardEvent): void {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
    if (diffStore.isDraftDirty) {
      e.preventDefault();
      void handleSave();
    }
  }
}

watch(
  () => [props.file?.path, props.file?.beforeText, props.file?.afterText, props.reverse],
  () => {
    void nextTick(() => {
      initMergeView();
    });
  },
  { immediate: true }
);

onMounted(() => {
  window.addEventListener("keydown", handleGlobalKeydown);
  if (mergeHostRef.value && typeof ResizeObserver !== "undefined") {
    resizeObserver = new ResizeObserver(() => {
      updateScrollbarMetrics();
    });
    resizeObserver.observe(mergeHostRef.value);
  }
});

onBeforeUnmount(() => {
  window.removeEventListener("keydown", handleGlobalKeydown);
  if (resizeObserver) {
    resizeObserver.disconnect();
    resizeObserver = null;
  }
  destroyMergeView();
});
</script>

<template>
  <div class="side-by-side-diff-container">
    <div class="side-diff-toolbar">
      <div class="toolbar-side toolbar-side--left">
        <span class="column-badge column-badge--local" title="当前归档 (可编辑侧)">当前归档</span>
        <span class="file-path-text" :title="props.file.path">{{ props.file.path }}</span>
        <span class="file-size-badge">{{ formatSize(localSize) }}</span>

        <div class="side-actions-group">
          <NTooltip v-if="canOpenInEditor" trigger="hover">
            <template #trigger>
              <button
                class="icon-tool-btn"
                aria-label="在主编辑器中打开此文件"
                @click="openInEditor"
              >
                <NIcon size="14"><Open20Regular /></NIcon>
              </button>
            </template>
            在主编辑器新 Tab 中打开
          </NTooltip>

          <NTooltip trigger="hover">
            <template #trigger>
              <button class="icon-tool-btn" aria-label="复制路径" @click="copyPath">
                <NIcon size="13"><Copy20Regular /></NIcon>
              </button>
            </template>
            复制文件路径
          </NTooltip>
        </div>

        <div v-if="diffStore.isDraftDirty" class="save-status-group">
          <NTooltip trigger="hover">
            <template #trigger>
              <button
                class="icon-tool-btn icon-tool-btn--save"
                :disabled="isSaving"
                aria-label="保存修改到归档 (Ctrl+S)"
                @click="handleSave"
              >
                <span class="dirty-dot">●</span>
                <NIcon size="13"><Save20Regular /></NIcon>
              </button>
            </template>
            草稿已修改，点击保存同步到归档 (Ctrl+S)
          </NTooltip>
        </div>
      </div>

      <div class="toolbar-center">
        <div v-if="chunksCount > 0" class="chunk-navigation-group">
          <NTooltip trigger="hover">
            <template #trigger>
              <button class="nav-arrow-btn" aria-label="上一个差异" @click="scrollToChunk(activeChunkIndex - 1)">
                <NIcon size="13"><ArrowUp20Regular /></NIcon>
              </button>
            </template>
            上一个差异
          </NTooltip>

          <NTooltip trigger="hover">
            <template #trigger>
              <span class="chunk-indicator">{{ activeChunkIndex + 1 }} / {{ chunksCount }}</span>
            </template>
            差异段落 {{ activeChunkIndex + 1 }} / {{ chunksCount }}
          </NTooltip>

          <NTooltip trigger="hover">
            <template #trigger>
              <button class="nav-arrow-btn" aria-label="下一个差异" @click="scrollToChunk(activeChunkIndex + 1)">
                <NIcon size="13"><ArrowDown20Regular /></NIcon>
              </button>
            </template>
            下一个差异
          </NTooltip>
        </div>
        <div v-else class="no-diff-badge" title="两份归档中该文件内容完全一致">
          <span>0 / 0</span>
        </div>

        <NTooltip trigger="hover">
          <template #trigger>
            <button
              class="icon-tool-btn icon-tool-btn--merge"
              :disabled="isSaving || diffStore.merging"
              aria-label="合入整文件"
              @click="handleMergeCurrentFile"
            >
              <NIcon size="14"><DocumentArrowLeft20Regular /></NIcon>
            </button>
          </template>
          以对比归档为准，将整个文件覆盖合并到本地
        </NTooltip>

        <NTooltip trigger="hover">
          <template #trigger>
            <button
              class="icon-tool-btn icon-tool-btn--wrap"
              :class="{ 'icon-tool-btn--active': diffStore.lineWrapping }"
              :aria-pressed="diffStore.lineWrapping"
              :aria-label="diffStore.lineWrapping ? '关闭自动换行' : '开启自动换行'"
              @click="toggleLineWrapping"
            >
              <NIcon size="14"><TextWrap24Regular /></NIcon>
            </button>
          </template>
          {{ diffStore.lineWrapping ? '关闭自动换行' : '开启自动换行（超长行可能导致卡顿）' }}
        </NTooltip>
      </div>

      <div class="toolbar-side toolbar-side--right">
        <span class="column-badge column-badge--target" title="对比归档 (只读侧)">对比归档</span>
        <span class="file-path-text" :title="props.file.path">{{ props.file.path }}</span>
        <span class="file-size-badge">{{ formatSize(otherSize) }}</span>

        <div class="side-actions-group">
          <NTooltip trigger="hover">
            <template #trigger>
              <button class="icon-tool-btn" aria-label="复制路径" @click="copyPath">
                <NIcon size="13"><Copy20Regular /></NIcon>
              </button>
            </template>
            复制文件路径
          </NTooltip>
        </div>
      </div>
    </div>

    <div ref="mergeHostRef" class="merge-editor-host" />

    <!-- 底部双栏视口水平滚动条（未换行且内容超宽时显示） -->
    <div
      v-show="!diffStore.lineWrapping && (leftHasScroll || rightHasScroll)"
      class="side-diff-bottom-scrollbars"
      role="region"
      aria-label="双栏水平滚动条"
    >
      <div
        ref="leftScrollbarRef"
        class="diff-h-scrollbar-track diff-h-scrollbar-track--left"
        :class="{ 'is-scrollable': leftHasScroll }"
        @scroll="onLeftScrollbarScroll"
      >
        <div class="diff-h-scrollbar-spacer" :style="{ width: `${leftScrollWidth}px` }" />
      </div>

      <div class="diff-h-scrollbar-divider" />

      <div
        ref="rightScrollbarRef"
        class="diff-h-scrollbar-track diff-h-scrollbar-track--right"
        :class="{ 'is-scrollable': rightHasScroll }"
        @scroll="onRightScrollbarScroll"
      >
        <div class="diff-h-scrollbar-spacer" :style="{ width: `${rightScrollWidth}px` }" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.side-by-side-diff-container {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: var(--pvf-surface-code);
  overflow: hidden;
}

.side-diff-toolbar {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  align-items: center;
  gap: 12px;
  padding: 0 12px;
  height: 36px;
  min-height: 36px;
  max-height: 36px;
  background: var(--pvf-surface-subtle);
  border-bottom: 1px solid var(--pvf-border-subtle);
  font-size: 12px;
  user-select: none;
  box-sizing: border-box;
  overflow: hidden;
}

.toolbar-side {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
  height: 100%;
}

.toolbar-side--right {
  justify-content: flex-end;
}

.toolbar-center {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  flex-shrink: 0;
  white-space: nowrap;
  height: 100%;
}

.column-badge {
  font-size: 11px;
  font-weight: 600;
  padding: 1px 6px;
  height: 20px;
  line-height: 18px;
  border-radius: 4px;
  flex-shrink: 0;
  white-space: nowrap;
  box-sizing: border-box;
}

.column-badge--local {
  background: rgba(46, 160, 67, 0.15);
  color: var(--pvf-success-hover, #3fb950);
  border: 1px solid rgba(46, 160, 67, 0.3);
}

.column-badge--target {
  background: rgba(31, 111, 235, 0.15);
  color: #58a6ff;
  border: 1px solid rgba(31, 111, 235, 0.3);
}

.file-path-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  color: var(--pvf-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 0 1 auto;
  min-width: 0;
}

.file-size-badge {
  font-size: 11px;
  color: var(--pvf-text-muted);
  flex-shrink: 0;
  white-space: nowrap;
}

.side-actions-group {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
  white-space: nowrap;
}

.save-status-group {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  white-space: nowrap;
}

.dirty-dot {
  color: #ffc53d;
  font-size: 11px;
  line-height: 1;
}

.dirty-badge {
  color: #e3b341;
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
  flex-shrink: 0;
}

.action-pill-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 0 7px;
  height: 22px;
  line-height: 20px;
  border-radius: 4px;
  border: 1px solid transparent;
  font-size: 11px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s ease;
  user-select: none;
  white-space: nowrap !important;
  flex-shrink: 0 !important;
  box-sizing: border-box;
}

.action-pill-btn span {
  white-space: nowrap !important;
}

.action-pill-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.action-pill-btn--save {
  background: #238636;
  color: #ffffff;
  border-color: rgba(255, 255, 255, 0.15);
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.2);
}

.action-pill-btn--save:hover:not(:disabled) {
  background: #2ea043;
}

.action-pill-btn--merge-file {
  background: var(--pvf-surface-card);
  color: var(--pvf-text-primary);
  border-color: var(--pvf-border-normal);
}

.action-pill-btn--merge-file:hover:not(:disabled) {
  background: var(--pvf-surface-hover);
  border-color: var(--pvf-border-strong);
}

@media (max-width: 900px) {
  .file-size-badge {
    display: none;
  }
}

.chunk-navigation-group {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  background: var(--pvf-surface-card);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 4px;
  padding: 1px 4px;
}

.nav-arrow-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: none;
  background: transparent;
  color: var(--pvf-text-secondary);
  border-radius: 3px;
  cursor: pointer;
}

.nav-arrow-btn:hover {
  background: var(--pvf-surface-hover);
  color: var(--pvf-text-primary);
}

.chunk-indicator {
  font-size: 11px;
  color: var(--pvf-text-secondary);
  padding: 0 4px;
  font-variant-numeric: tabular-nums;
}

.no-diff-badge {
  font-size: 11px;
  color: var(--pvf-text-muted);
  padding: 2px 6px;
}

.icon-tool-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  background: transparent;
  border: 1px solid transparent;
  color: var(--pvf-text-muted);
  cursor: pointer;
  border-radius: 3px;
  flex-shrink: 0;
  box-sizing: border-box;
  transition: all 0.15s ease;
}

.icon-tool-btn:hover {
  color: var(--pvf-text-primary);
  background: var(--pvf-surface-hover);
  border-color: var(--pvf-border-subtle);
}

.icon-tool-btn--save {
  background: rgba(46, 160, 67, 0.2);
  color: #3fb950;
  border-color: rgba(46, 160, 67, 0.4);
  gap: 2px;
  width: auto;
  padding: 0 4px;
}

.icon-tool-btn--save:hover:not(:disabled) {
  background: #238636;
  color: #ffffff;
  border-color: rgba(255, 255, 255, 0.2);
}

.icon-tool-btn--merge {
  background: var(--pvf-surface-card);
  color: var(--pvf-text-primary);
  border-color: var(--pvf-border-normal);
}

.icon-tool-btn--merge:hover:not(:disabled) {
  background: var(--pvf-surface-hover);
  border-color: var(--pvf-border-strong);
}

.merge-editor-host {
  flex: 1 1 0%;
  min-height: 0;
  min-width: 0;
  width: 100%;
  height: 100%;
  position: relative;
  overflow: hidden;
}

:deep(.cm-mergeView) {
  height: 100% !important;
  max-height: 100% !important;
  width: 100%;
  overflow-y: auto !important;
  overflow-x: auto !important;
  box-sizing: border-box;
}

:deep(.cm-mergeViewEditors) {
  min-height: 100%;
  display: flex;
  align-items: stretch;
}

:deep(.cm-mergeViewEditor) {
  flex: 1 1 0%;
  min-width: 0;
  overflow: hidden;
}

:deep(.cm-mergeViewEditor:first-child) {
  border-right: 1px solid var(--pvf-border-subtle);
}

:deep(.cm-editor) {
  box-sizing: border-box;
}

:deep(.cm-scroller) {
  font-family: 'SF Mono', Menlo, Consolas, 'Courier New', monospace;
  line-height: 1.55;
  user-select: text;
}

:deep(.cm-merge-revert) {
  width: 28px !important;
  min-width: 28px !important;
  position: relative !important;
  flex-grow: 0 !important;
  flex-shrink: 0 !important;
  background: var(--pvf-surface-subtle) !important;
  border-left: 1px solid var(--pvf-border-subtle) !important;
  border-right: 1px solid var(--pvf-border-subtle) !important;
  user-select: none;
}

:deep(.cm-merge-revert button),
:deep(.cm-revert-action-btn) {
  position: absolute;
  left: 3px;
  width: 22px;
  height: 20px;
  padding: 0;
  margin: 0;
  border: 1px solid rgba(46, 160, 67, 0.4);
  border-radius: 4px;
  background: rgba(46, 160, 67, 0.2);
  color: var(--pvf-success-hover, #3fb950);
  font-size: 14px;
  font-weight: bold;
  line-height: 18px;
  text-align: center;
  cursor: pointer;
  transition: all 0.15s ease;
  z-index: 10;
}

:deep(.cm-merge-revert button:hover),
:deep(.cm-revert-action-btn:hover) {
  background: var(--pvf-success-hover, #3fb950);
  color: #ffffff;
  border-color: var(--pvf-success-hover, #3fb950);
  transform: scale(1.08);
}

.icon-tool-btn--active {
  background: rgba(24, 160, 88, 0.18) !important;
  color: var(--pvf-primary, #18a058) !important;
  border-color: rgba(24, 160, 88, 0.4) !important;
}

.icon-tool-btn--active:hover {
  background: rgba(24, 160, 88, 0.28) !important;
}

.side-diff-bottom-scrollbars {
  display: flex;
  align-items: stretch;
  height: 12px;
  min-height: 12px;
  max-height: 12px;
  background: var(--pvf-surface-subtle);
  border-top: 1px solid var(--pvf-border-subtle);
  flex-shrink: 0;
  box-sizing: border-box;
  overflow: hidden;
  user-select: none;
  z-index: 5;
}

.diff-h-scrollbar-track {
  flex: 1 1 0%;
  min-width: 0;
  overflow-x: auto;
  overflow-y: hidden;
  height: 100%;
}

.diff-h-scrollbar-track:not(.is-scrollable) {
  overflow-x: hidden;
}

.diff-h-scrollbar-track::-webkit-scrollbar {
  height: 10px;
}

.diff-h-scrollbar-track::-webkit-scrollbar-track {
  background: transparent;
}

.diff-h-scrollbar-track::-webkit-scrollbar-thumb {
  background: var(--pvf-border-normal, rgba(255, 255, 255, 0.2));
  border-radius: 5px;
}

.diff-h-scrollbar-track::-webkit-scrollbar-thumb:hover {
  background: var(--pvf-border-focus, rgba(255, 255, 255, 0.4));
}

.diff-h-scrollbar-spacer {
  height: 1px;
}

.diff-h-scrollbar-divider {
  width: 28px;
  min-width: 28px;
  flex-shrink: 0;
  background: var(--pvf-surface-subtle);
  border-left: 1px solid var(--pvf-border-subtle);
  border-right: 1px solid var(--pvf-border-subtle);
}
</style>
