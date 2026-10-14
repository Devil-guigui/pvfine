import { computed, ref } from "vue";
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
  return nodes.map(node => ({ key: node.path.toLowerCase(), label: node.name, isLeaf: !node.isDir, node }));
}

export const useArchiveDiffStore = defineStore("archiveDiff", () => {
  const visible = ref(false);
  const otherPath = ref("");
  const reverse = ref(false);
  const status = ref<ArchiveDiffStatus | null>(null);
  const tree = ref<DiffTreeNode[]>([]);
  const file = ref<ArchiveDiffFile | null>(null);
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
  const busy = computed(() => starting.value || rootLoading.value || ["loading", "comparing", "classifying", "verifying", "sorting", "building", "sorting-tree"].includes(status.value?.state ?? ""));
  const displayedFile = computed(() => orientArchiveDiff(file.value, reverse.value));
  const counts = computed(() => ({
    added: (reverse.value ? status.value?.deleted : status.value?.added) ?? 0,
    modified: status.value?.modified ?? 0,
    deleted: (reverse.value ? status.value?.added : status.value?.deleted) ?? 0,
  }));
  function clearResult() {
    rootLoading.value = false;
    clearTimeout(timer);
    status.value = null;
    tree.value = [];
    file.value = null;
    selectedKeys.value = [];
    expandedKeys.value = [];
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
  async function poll(token: number, id: string) {
    try {
      const result = await ArchiveDiffService.Status(id);
      if (token !== epoch) return;
      if (!result) throw new Error("对比会话已关闭");
      status.value = result;
      if (result.state === "ready") {
        rootLoading.value = true;
        try {
          const nodes = await ArchiveDiffService.Children(id, "");
          if (token === epoch) tree.value = treeNodes(nodes ?? []);
        } finally { if (token === epoch) rootLoading.value = false; }
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
      const nodes = await ArchiveDiffService.Children(id, target.node.path);
      if (token !== epoch) return;
      target.children = treeNodes(nodes ?? []);
      tree.value = [...tree.value];
    } catch (err) { if (token === epoch) error.value = errorMessage(err); }
  }
  async function openFile(node: ArchiveDiffNode) {
    const id = status.value?.id;
    if (!id || node.isDir) return;
    const token = ++fileEpoch;
    const sessionEpoch = epoch;
    fileLoading.value = true;
    file.value = null;
    fileError.value = "";
    try {
      const result = await ArchiveDiffService.FileDiff(id, node.path);
      if (token === fileEpoch && sessionEpoch === epoch) file.value = result;
    } catch (err) {
      if (token === fileEpoch && sessionEpoch === epoch) fileError.value = errorMessage(err);
    } finally { if (token === fileEpoch && sessionEpoch === epoch) fileLoading.value = false; }
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
  return { visible, otherPath, reverse, status, tree, file, displayedFile, selectedKeys, expandedKeys,
    starting, selecting, busy, fileLoading, rootLoading, error, fileError, counts, open, close, selectArchive, refresh, loadChildren, openFile, cancel };
});
