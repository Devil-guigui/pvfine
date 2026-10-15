import { computed, ref, watch } from "vue";
import { defineStore } from "pinia";
import { Events } from "@wailsio/runtime";
import type { TreeOption } from "naive-ui";
import { ArchiveDiffService } from "../../bindings/pvfine/services";
import type { ArchiveDiffStatus, ArchiveDiffFile, ArchiveDiffNode } from "../../bindings/pvfine/services/models";
import { useEditorStore } from "./editor";
import { orientArchiveDiff } from "../archiveDiff";

export type DiffTreeNode = TreeOption & { node: ArchiveDiffNode; children?: DiffTreeNode[] };
const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error);
function treeNodes(nodes: ArchiveDiffNode[]): DiffTreeNode[] {
  return nodes.map(node => ({
    key: node.path.toLowerCase(),
    label: node.name,
    isLeaf: !node.isDir,
    node,
    selectable: !node.isDir,
  }));
}

export const useArchiveDiffStore = defineStore("archiveDiff", () => {
  const visible = ref(false);
  const otherPath = ref("");
  const reverse = ref(false);
  const status = ref<ArchiveDiffStatus | null>(null);
  const tree = ref<DiffTreeNode[]>([]);
  const file = ref<ArchiveDiffFile | null>(null);
  const activeNode = ref<ArchiveDiffNode | null>(null);
  const selectedKeys = ref<Array<string | number>>([]);
  const expandedKeys = ref<Array<string | number>>([]);
  const starting = ref(false);
  const selecting = ref(false);
  const fileLoading = ref(false);
  const rootLoading = ref(false);
  const error = ref("");
  const fileError = ref("");
  let epoch = 0;
  let fileEpoch = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;

  // 视图模式与列表检索模式
  const viewMode = ref<"side-by-side" | "unified">("side-by-side");
  const listMode = ref<"tree" | "flat">("tree");
  const filterOperation = ref<string>("all");
  const searchQuery = ref<string>("");
  const flatFiles = ref<ArchiveDiffNode[]>([]);
  const flatLoading = ref(false);
  const lineWrapping = ref(false);

  // 多选勾选集合与合并状态
  const selectedPaths = ref<Set<string>>(new Set());
  const merging = ref(false);

  // 双栏编辑器内草稿与切换保存保护
  const draftPath = ref("");
  const draftText = ref("");
  const originalDraftText = ref("");
  const isDraftDirty = computed(() => !!draftPath.value && draftText.value !== originalDraftText.value);
  const pendingSwitchNode = ref<ArchiveDiffNode | null>(null);
  const pendingCloseTab = ref(false);
  const showDraftConfirmModal = ref(false);

  const busy = computed(() => starting.value || rootLoading.value || ["loading", "comparing", "classifying", "verifying", "sorting", "building", "sorting-tree"].includes(status.value?.state ?? ""));
  const displayedFile = computed(() => orientArchiveDiff(file.value, reverse.value));
  const counts = computed(() => ({
    added: (reverse.value ? status.value?.deleted : status.value?.added) ?? 0,
    modified: status.value?.modified ?? 0,
    deleted: (reverse.value ? status.value?.added : status.value?.deleted) ?? 0,
  }));
  const totalCount = computed(() => counts.value.added + counts.value.modified + counts.value.deleted);

  function getBackendOperation(op: string): string {
    if (op === "all" || !op) return "all";
    if (!reverse.value) return op;
    if (op === "add") return "delete";
    if (op === "delete") return "add";
    return op;
  }

  function setDraft(path: string, text: string, originalText: string) {
    draftPath.value = path;
    draftText.value = text;
    originalDraftText.value = originalText;
  }

  function updateDraftText(text: string) {
    draftText.value = text;
  }

  function discardDraft() {
    draftText.value = originalDraftText.value;
    draftPath.value = "";
    originalDraftText.value = "";
  }

  function isSelected(path: string): boolean {
    const lower = path.toLowerCase();
    if (selectedPaths.value.has(lower)) return true;
    let p = lower;
    while (true) {
      const idx = p.lastIndexOf("/");
      if (idx <= 0) break;
      p = p.substring(0, idx);
      if (selectedPaths.value.has(p)) return true;
    }
    return false;
  }

  function toggleSelect(path: string) {
    const lower = path.toLowerCase();
    const next = new Set(selectedPaths.value);
    if (isSelected(lower)) {
      next.delete(lower);
      let p = lower;
      while (true) {
        const idx = p.lastIndexOf("/");
        if (idx <= 0) break;
        p = p.substring(0, idx);
        next.delete(p);
      }
    } else {
      next.add(lower);
    }
    selectedPaths.value = next;
  }

  function toggleSelectDir(path: string) {
    toggleSelect(path);
  }

  function selectAll(paths: string[]) {
    const next = new Set(selectedPaths.value);
    for (const p of paths) next.add(p.toLowerCase());
    selectedPaths.value = next;
  }

  function deselectPaths(paths: string[]): void {
    const next = new Set(selectedPaths.value);
    for (const p of paths) {
      const lower = p.toLowerCase();
      next.delete(lower);
      let parent = lower;
      while (true) {
        const idx = parent.lastIndexOf("/");
        if (idx <= 0) break;
        parent = parent.substring(0, idx);
        next.delete(parent);
      }
    }
    selectedPaths.value = next;
  }

  function deselectAll(paths: string[]): void {
    deselectPaths(paths);
  }

  function clearSelection() {
    selectedPaths.value = new Set();
  }

  function buildTreeFromFiles(files: ArchiveDiffNode[]): { tree: DiffTreeNode[]; dirKeys: string[] } {
    const root: DiffTreeNode[] = [];
    const dirMap = new Map<string, DiffTreeNode>();
    const dirKeys: string[] = [];

    for (const file of files) {
      const parts = file.path.split("/");
      let currentPath = "";
      let parentChildren = root;

      for (let i = 0; i < parts.length - 1; i++) {
        const part = parts[i];
        currentPath = currentPath ? `${currentPath}/${part}` : part;
        const lowerPath = currentPath.toLowerCase();

        let dirNode = dirMap.get(lowerPath);
        if (!dirNode) {
          dirNode = {
            key: lowerPath,
            label: part,
            isLeaf: false,
            selectable: false,
            children: [],
            node: {
              name: part,
              path: currentPath,
              isDir: true,
              operation: "modify",
              count: 0,
            },
          };
          dirMap.set(lowerPath, dirNode);
          dirKeys.push(lowerPath);
          parentChildren.push(dirNode);
        }
        dirNode.node.count++;
        parentChildren = dirNode.children!;
      }

      const fileName = parts[parts.length - 1];
      parentChildren.push({
        key: file.path.toLowerCase(),
        label: fileName,
        isLeaf: true,
        selectable: true,
        node: file,
      });
    }
    return { tree: root, dirKeys };
  }

  const searchedTreeNodes = ref<DiffTreeNode[]>([]);
  const savedExpandedKeys = ref<Array<string | number> | null>(null);
  let searchTreeSeq = 0;

  async function searchTree(query: string) {
    const q = query.trim();
    if (!q) {
      clearSearch();
      return;
    }
    const id = status.value?.id;
    if (!id) return;

    if (savedExpandedKeys.value === null) {
      savedExpandedKeys.value = [...expandedKeys.value];
    }

    const currentSeq = ++searchTreeSeq;
    try {
      const op = getBackendOperation(filterOperation.value);
      const res = await ArchiveDiffService.FlatFiles(id, q, op);
      if (currentSeq !== searchTreeSeq) return;
      const files = (res ?? []).filter((f) => !f.isDir);
      const limited = files.slice(0, 1000);
      const { tree: resultTree, dirKeys } = buildTreeFromFiles(limited);
      searchedTreeNodes.value = resultTree;
      expandedKeys.value = dirKeys;
    } catch {
      if (currentSeq === searchTreeSeq) {
        searchedTreeNodes.value = [];
      }
    }
  }

  function clearSearch() {
    searchQuery.value = "";
    searchedTreeNodes.value = [];
    if (savedExpandedKeys.value !== null) {
      expandedKeys.value = savedExpandedKeys.value;
      savedExpandedKeys.value = null;
    }
    if (listMode.value === "flat") {
      void loadFlatFiles();
    }
  }

  function collapseAll() {
    expandedKeys.value = [];
    savedExpandedKeys.value = [];
  }

  const displayedTree = computed<TreeOption[]>(() => {
    if (searchQuery.value.trim()) {
      return searchedTreeNodes.value;
    }
    return tree.value;
  });

  function clearResult() {
    rootLoading.value = false;
    clearTimeout(timer);
    status.value = null;
    tree.value = [];
    searchedTreeNodes.value = [];
    savedExpandedKeys.value = null;
    file.value = null;
    activeNode.value = null;
    selectedKeys.value = [];
    expandedKeys.value = [];
    selectedPaths.value = new Set();
    flatFiles.value = [];
    discardDraft();
    showDraftConfirmModal.value = false;
    pendingSwitchNode.value = null;
    fileEpoch++;
    fileLoading.value = false;
    fileError.value = "";
  }

  function open() { visible.value = true; }
  function close() {
    epoch++;
    visible.value = false;
    starting.value = false;
    selecting.value = false;
    clearResult();
    otherPath.value = "";
    reverse.value = false;
    filterOperation.value = "all";
    error.value = "";
    void ArchiveDiffService.Release().catch(() => {});
  }

  async function selectArchive() {
    const token = epoch;
    selecting.value = true;
    try {
      const path = await ArchiveDiffService.SelectArchive();
      if (token !== epoch || !visible.value || !path) return;
      otherPath.value = path;
      selecting.value = false;
      await refresh();
    } catch (err) {
      if (token === epoch) error.value = errorMessage(err);
    } finally { if (token === epoch) selecting.value = false; }
  }

  async function refresh() {
    if (!otherPath.value || busy.value) return;
    let drafts;
    try { drafts = useEditorStore().archiveDiffDrafts(); }
    catch (err) { error.value = errorMessage(err); return; }
    const token = ++epoch;
    clearResult();
    starting.value = true;
    error.value = "";
    try {
      const result = await ArchiveDiffService.Start({ path: otherPath.value, drafts });
      if (token !== epoch) {
        if (result) await ArchiveDiffService.Cancel(result.id);
        return;
      }
      if (!result) throw new Error("未收到对比会话");
      status.value = result;
      starting.value = false;
      await poll(token, result.id);
    } catch (err) {
      if (token === epoch) { error.value = errorMessage(err); clearResult(); }
    } finally { if (token === epoch) starting.value = false; }
  }

  async function loadRootTree() {
    const id = status.value?.id;
    if (!id) return;
    const token = epoch;
    rootLoading.value = true;
    try {
      const op = getBackendOperation(filterOperation.value);
      const nodes = await ArchiveDiffService.FilteredChildren(id, "", op);
      if (token === epoch) tree.value = treeNodes(nodes ?? []);
    } catch (err) {
      if (token === epoch) error.value = errorMessage(err);
    } finally {
      if (token === epoch) rootLoading.value = false;
    }
  }

  async function poll(token: number, id: string) {
    try {
      const result = await ArchiveDiffService.Status(id);
      if (token !== epoch) return;
      if (!result) throw new Error("对比会话已关闭");
      status.value = result;
      if (result.state === "ready") {
        await loadRootTree();
        if (listMode.value === "flat") {
          void loadFlatFiles();
        } else if (searchQuery.value.trim()) {
          void searchTree(searchQuery.value);
        }
      } else if (result.state === "error") {
        error.value = result.error;
      } else if (result.state !== "cancelled") {
        timer = setTimeout(() => void poll(token, id), 250);
      }
    } catch (err) { if (token === epoch) { error.value = errorMessage(err); clearResult(); } }
  }

  async function loadChildren(option: TreeOption) {
    const target = option as DiffTreeNode;
    const token = epoch;
    const id = status.value?.id;
    if (!id || target.children) return;
    try {
      const op = getBackendOperation(filterOperation.value);
      const nodes = await ArchiveDiffService.FilteredChildren(id, target.node.path, op);
      if (token !== epoch) return;
      target.children = treeNodes(nodes ?? []);
      tree.value = [...tree.value];
    } catch (err) { if (token === epoch) error.value = errorMessage(err); }
  }

  async function refreshCurrentView(): Promise<void> {
    if (listMode.value === "flat") {
      await loadFlatFiles();
    } else if (searchQuery.value.trim()) {
      void searchTree(searchQuery.value);
    } else {
      await loadRootTree();
    }
  }

  async function setFilterOperation(op: string) {
    if (filterOperation.value === op) return;
    filterOperation.value = op;
    expandedKeys.value = [];
    await refreshCurrentView();
  }

  async function toggleReverse() {
    reverse.value = !reverse.value;
    expandedKeys.value = [];
    await refreshCurrentView();
  }

  async function loadFlatFiles() {
    const id = status.value?.id;
    if (!id) {
      flatFiles.value = [];
      return;
    }
    flatLoading.value = true;
    try {
      const op = getBackendOperation(filterOperation.value);
      const res = await ArchiveDiffService.FlatFiles(id, searchQuery.value, op);
      flatFiles.value = res ?? [];
    } catch (err) {
      error.value = errorMessage(err);
    } finally {
      flatLoading.value = false;
    }
  }

  async function openFile(node: ArchiveDiffNode, force = false): Promise<boolean> {
    if (node.isDir) return false;
    if (!force && isDraftDirty.value && draftPath.value !== node.path) {
      pendingSwitchNode.value = node;
      showDraftConfirmModal.value = true;
      return false;
    }
    selectedKeys.value = [node.path.toLowerCase()];
    await openFileDirect(node);
    return true;
  }

  async function openFileDirect(node: ArchiveDiffNode) {
    const id = status.value?.id;
    if (!id || node.isDir) return;
    activeNode.value = node;
    const token = ++fileEpoch;
    const sessionEpoch = epoch;
    fileLoading.value = true;
    file.value = null;
    fileError.value = "";
    try {
      const result = await ArchiveDiffService.FileDiff(id, node.path);
      if (token === fileEpoch && sessionEpoch === epoch) {
        file.value = result;
        orientArchiveDiff(result, reverse.value);
        // 可编辑底稿始终取本地工作区归档的文本（result.afterText），避免反向对比时取错对比侧覆盖本地内容
        const baseTxt = result?.afterText ?? "";
        setDraft(node.path, baseTxt, baseTxt);
      }
    } catch (err) {
      if (token === fileEpoch && sessionEpoch === epoch) fileError.value = errorMessage(err);
    } finally { if (token === fileEpoch && sessionEpoch === epoch) fileLoading.value = false; }
  }

  async function confirmDraftSaveAndSwitch(): Promise<void> {
    if (draftPath.value && isDraftDirty.value) {
      await mergeText(draftPath.value, draftText.value);
    }
    showDraftConfirmModal.value = false;
    if (pendingCloseTab.value) {
      pendingCloseTab.value = false;
      useEditorStore().closeTab(useEditorStore().activeTab?.index ?? -999);
      close();
      return;
    }
    const nextNode = pendingSwitchNode.value;
    pendingSwitchNode.value = null;
    if (nextNode) {
      await openFile(nextNode, true);
    }
  }

  function confirmDraftDiscardAndSwitch(): void {
    discardDraft();
    showDraftConfirmModal.value = false;
    if (pendingCloseTab.value) {
      pendingCloseTab.value = false;
      useEditorStore().closeTab(useEditorStore().activeTab?.index ?? -999);
      close();
      return;
    }
    const nextNode = pendingSwitchNode.value;
    pendingSwitchNode.value = null;
    if (nextNode) {
      void openFile(nextNode, true);
    }
  }

  function cancelDraftSwitch(): void {
    pendingSwitchNode.value = null;
    pendingCloseTab.value = false;
    showDraftConfirmModal.value = false;
  }

  async function mergeSelected() {
    const id = status.value?.id;
    const paths = Array.from(selectedPaths.value);
    if (!id || paths.length === 0) return null;
    merging.value = true;
    try {
      const res = await ArchiveDiffService.MergeFiles(id, paths);
      if (res && res.mergedPaths && res.mergedPaths.length > 0) {
        await useEditorStore().refreshMergedFiles(res.mergedPaths);
        deselectPaths(res.mergedPaths);
        if (activeNode.value && res.mergedPaths.includes(activeNode.value.path)) {
          await openFileDirect(activeNode.value);
        }
        await refreshCurrentView();
      }
      return res;
    } finally {
      merging.value = false;
    }
  }

  async function mergeSingleFile(path: string): Promise<boolean> {
    const id = status.value?.id;
    if (!id || !path) return false;
    merging.value = true;
    try {
      const res = await ArchiveDiffService.MergeFiles(id, [path]);
      if (res && res.successCount > 0) {
        await useEditorStore().refreshMergedFiles([path]);
        deselectPaths([path]);
        if (activeNode.value && activeNode.value.path === path) {
          await openFileDirect(activeNode.value);
        }
        await refreshCurrentView();
        return true;
      }
      return false;
    } finally {
      merging.value = false;
    }
  }

  async function mergeText(path: string, text: string): Promise<boolean> {
    const id = status.value?.id;
    if (!id || !path) return false;
    merging.value = true;
    try {
      await ArchiveDiffService.MergeText(id, path, text);
      await useEditorStore().refreshMergedFiles([path]);
      originalDraftText.value = text;
      draftText.value = text;
      if (activeNode.value && activeNode.value.path === path) {
        await openFileDirect(activeNode.value);
      }
      await refreshCurrentView();
      return true;
    } finally {
      merging.value = false;
    }
  }

  async function cancel() {
    rootLoading.value = false;
    const id = status.value?.id;
    epoch++;
    clearTimeout(timer);
    if (id) {
      if (status.value) status.value = { ...status.value, state: "cancelled" };
      try { await ArchiveDiffService.Cancel(id); }
      catch (err) { error.value = errorMessage(err); }
    }
  }

  Events.On("archive:opened", close);
  Events.On("archive:closed", close);
  Events.On("archive:reloaded", close);

  return {
    visible, otherPath, reverse, status, tree, file, activeNode, displayedFile, selectedKeys, expandedKeys,
    starting, selecting, busy, fileLoading, rootLoading, error, fileError, counts, totalCount,
    viewMode, listMode, filterOperation, searchQuery, flatFiles, flatLoading, displayedTree,
    lineWrapping,
    selectedPaths, merging, draftPath, draftText, originalDraftText, isDraftDirty,
    showDraftConfirmModal, pendingSwitchNode, pendingCloseTab,
    open, close, selectArchive, refresh, loadRootTree, loadChildren, openFile, openFileDirect, cancel,
    toggleSelect, selectAll, deselectAll, clearSelection, isSelected,
    toggleSelectDir, searchTree, clearSearch, collapseAll, setFilterOperation, toggleReverse,
    loadFlatFiles, refreshCurrentView, mergeSelected, mergeSingleFile, mergeText,
    setDraft, updateDraftText, discardDraft,
    confirmDraftSaveAndSwitch, confirmDraftDiscardAndSwitch, cancelDraftSwitch,
  };
});
