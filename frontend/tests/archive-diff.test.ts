import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { createPinia, disposePinia, setActivePinia, type Pinia } from "pinia";
import type { ArchiveDiffFile } from "../bindings/pvfine/services/models";
import { diffOperation, orientArchiveDiff } from "../src/archiveDiff";
const mocks = vi.hoisted(() => ({
  Start: vi.fn(), Release: vi.fn(), Status: vi.fn(), Children: vi.fn(), FileDiff: vi.fn(), Cancel: vi.fn(), SelectArchive: vi.fn(),
  archiveDiffDrafts: vi.fn(), events: new Map<string, () => void>(),
}));
vi.mock("../bindings/pvfine/services", () => ({ ArchiveDiffService: mocks }));
vi.mock("../src/stores/editor", () => ({ useEditorStore: () => ({ archiveDiffDrafts: mocks.archiveDiffDrafts }) }));
vi.mock("@wailsio/runtime", () => ({ Events: { On: (name: string, callback: () => void) => mocks.events.set(name, callback) } }));
import { useArchiveDiffStore } from "../src/stores/archiveDiff";
const ready = { id: "1", state: "ready", currentPath: "current.pvf", otherPath: "other.pvf", processed: 10, total: 10, added: 2, modified: 1, deleted: 3, error: "" };
const file: ArchiveDiffFile = { path: "a.equ", operation: "add", beforeHash: "", afterHash: "hash", beforeType: 0, afterType: 1, beforeSize: 0, afterSize: 10, beforeExists: false, afterExists: true, beforeText: "", afterText: "new", textAvailable: true, message: "" };
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes; }); return { promise, resolve }; }
let pinia: Pinia;
beforeEach(() => {
  pinia = createPinia(); setActivePinia(pinia); vi.resetAllMocks(); mocks.events.clear();
  mocks.Start.mockResolvedValue(ready); mocks.Status.mockResolvedValue(ready); mocks.Children.mockResolvedValue([{ path: "equipment", name: "equipment", isDir: true, count: 6, operation: "" }]);
  mocks.Release.mockResolvedValue(undefined); mocks.Cancel.mockResolvedValue(undefined);
  mocks.FileDiff.mockResolvedValue(file); mocks.archiveDiffDrafts.mockReturnValue([{ fileIndex: 0, path: "a.equ", text: "draft" }]);
});
afterEach(() => { useArchiveDiffStore().close(); disposePinia(pinia); });
test("默认另一份归档为基准，反向交换新增删除和所有详情侧且不重新比较", async () => {
  const store = useArchiveDiffStore(); store.open(); store.otherPath = "other.pvf"; await store.refresh();
  expect(store.reverse).toBe(false); expect(store.counts).toEqual({ added: 2, modified: 1, deleted: 3 });
  await store.openFile({ path: "a.equ", name: "a.equ", isDir: false, operation: "add", count: 1 });
  store.reverse = true;
  expect(store.counts).toEqual({ added: 3, modified: 1, deleted: 2 });
  expect(store.displayedFile).toMatchObject({ operation: "delete", beforeText: "new", afterText: "", beforeExists: true, afterExists: false });
  expect(mocks.Start).toHaveBeenCalledTimes(1);
  expect(diffOperation("modify", true)).toBe("modify");
  expect(orientArchiveDiff(orientArchiveDiff(file, true), true)).toEqual(file);
});
test("选择归档后生成差异树，草稿传递且选择状态结束", async () => {
  const store = useArchiveDiffStore(); store.open(); mocks.SelectArchive.mockResolvedValue("other.pvf");
  await store.selectArchive();
  expect(store.selecting).toBe(false);
  expect(mocks.Start).toHaveBeenCalledWith({ path: "other.pvf", drafts: [{ fileIndex: 0, path: "a.equ", text: "draft" }] });
  expect(store.tree[0]).toMatchObject({ key: "equipment", isLeaf: false, node: { count: 6 } });
  mocks.Children.mockResolvedValue([{ path: "equipment/a.equ", name: "a.equ", isDir: false, operation: "add", count: 1 }]);
  await store.loadChildren(store.tree[0]!);
  expect(store.tree[0]?.children?.[0]).toMatchObject({ key: "equipment/a.equ", isLeaf: true });
});
test("取消选档不启动比较，未应用 GUI 修改保留原结果", async () => {
  const store = useArchiveDiffStore(); store.open(); mocks.SelectArchive.mockResolvedValue(""); await store.selectArchive();
  expect(mocks.Start).not.toHaveBeenCalled();
  store.otherPath = "other.pvf"; await store.refresh();
  mocks.archiveDiffDrafts.mockImplementation(() => { throw new Error("请先应用修改"); });
  await store.refresh();
  expect(store.error).toBe("请先应用修改"); expect(store.status?.id).toBe("1"); expect(mocks.Start).toHaveBeenCalledTimes(1);
});
test("关闭期间返回的 Start 被取消且不恢复已关闭弹窗", async () => {
  const store = useArchiveDiffStore(); store.open(); store.otherPath = "other.pvf";
  const pending = deferred<typeof ready>(); mocks.Start.mockReturnValue(pending.promise);
  const promise = store.refresh(); store.close(); pending.resolve(ready); await promise;
  expect(store.status).toBeNull(); expect(store.visible).toBe(false); expect(mocks.Cancel).toHaveBeenCalledWith("1");
});
test("切换工作区释放会话，过期详情不覆盖新选择", async () => {
  const store = useArchiveDiffStore(); store.open(); store.otherPath = "other.pvf"; await store.refresh();
  const pending = deferred<ArchiveDiffFile>(); mocks.FileDiff.mockReturnValueOnce(pending.promise);
  const first = store.openFile({ path: "a.equ", name: "a", isDir: false, operation: "add", count: 1 });
  mocks.FileDiff.mockResolvedValue({ ...file, path: "b.equ" });
  await store.openFile({ path: "b.equ", name: "b", isDir: false, operation: "add", count: 1 });
  pending.resolve(file); await first; expect(store.file?.path).toBe("b.equ");
  mocks.events.get("archive:opened")?.(); expect(store.visible).toBe(false); expect(store.tree).toEqual([]); expect(mocks.Release).toHaveBeenCalled();
});
test("文本复核和建树期间仍处于忙碌状态", () => {
  const store = useArchiveDiffStore();
  for (const state of ["classifying", "verifying", "sorting", "building", "sorting-tree"]) {
    store.status = { ...ready, state };
    expect(store.busy).toBe(true);
  }
  store.status = ready;
  expect(store.busy).toBe(false);
});
test("根节点返回前保持加载状态，取消后忽略旧树", async () => {
  const store = useArchiveDiffStore(); store.open(); store.otherPath = "other.pvf";
  const pending = deferred<[]>(); mocks.Children.mockReturnValue(pending.promise);
  const refresh = store.refresh();
  await vi.waitFor(() => expect(store.rootLoading).toBe(true));
  expect(store.busy).toBe(true);
  await store.cancel();
  expect(store.busy).toBe(false);
  pending.resolve([]); await refresh;
  expect(store.tree).toEqual([]);
  expect(store.status?.state).toBe("cancelled");
});
