import { beforeEach, expect, test, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({ Save: vi.fn(), SaveAsDialog: vi.fn(), SetText: vi.fn(), GetAnnotations: vi.fn(), refreshInfo: vi.fn(), refreshTreeTags: vi.fn(), handlers: new Map<string, (event: any) => void>() }));
vi.mock("@wailsio/runtime", () => ({ Events: { On: (name: string, handler: (event: any) => void) => { api.handlers.set(name, handler); } } }));
vi.mock("../bindings/pvfine/services", () => ({ EditorService: api, ArchiveService: {}, FileGUIService: {} }));
vi.mock("../src/stores/archive", () => ({ useArchiveStore: () => ({ refreshInfo: api.refreshInfo }) }));
vi.mock("../src/stores/script", () => ({ useScriptStore: () => ({}) }));
vi.mock("../src/stores/explorer", () => ({ useExplorerStore: () => api }));
import { useEditorStore } from "../src/stores/editor";

beforeEach(() => {
  setActivePinia(createPinia());
  vi.resetAllMocks();
  api.handlers.clear();
});

test("保存从准备到写盘持续反馈，拦截重复保存，完成后释放状态", async () => {
  let finish!: (value: any) => void;
  api.Save.mockReturnValue(new Promise(resolve => { finish = resolve; }));
  const store = useEditorStore();
  const pending = store.save();
  expect(store.archiveSaveStage).toBe("preparing");
  expect(store.saving).toBe(true);
  await vi.waitFor(() => expect(store.archiveSaveStage).toBe("saving"));
  await expect(store.save()).rejects.toThrow("正在保存");
  await expect(store.saveAs()).rejects.toThrow("正在保存");
  expect(api.Save).toHaveBeenCalledTimes(1);
  expect(api.SaveAsDialog).not.toHaveBeenCalled();
  api.handlers.get("archive:save-progress")!({ data: { phase: "packing", done: 5, total: 10, unit: "chunks" } });
  expect(store.archiveSaveProgress).toEqual({ phase: "packing", done: 5, total: 10, unit: "chunks" });
  api.handlers.get("archive:save-progress")!({ data: { phase: "syncing", done: 0, total: 0, unit: "" } });
  expect(store.archiveSaveProgress?.phase).toBe("syncing");
  finish({ fileCount: 1 });
  await pending;
  expect(store.archiveSaveStage).toBeNull();
  expect(store.saving).toBe(false);
  expect(store.archiveSaveProgress).toBeNull();
  api.handlers.get("archive:save-progress")!({ data: { phase: "done", done: 1, total: 1, unit: "" } });
  expect(store.archiveSaveProgress).toBeNull();
});

test("写盘失败后收起反馈并允许重试", async () => {
  api.Save.mockRejectedValueOnce(new Error("磁盘写入失败")).mockResolvedValueOnce({});
  const store = useEditorStore();
  await expect(store.save()).rejects.toThrow("磁盘写入失败");
  expect(store.archiveSaveStage).toBeNull();
  expect(store.saving).toBe(false);
  await store.save();
  expect(api.Save).toHaveBeenCalledTimes(2);
});

test("另存为取消或失败后清理反馈；成功时等待工作区刷新", async () => {
  const store = useEditorStore();
  api.SaveAsDialog.mockResolvedValueOnce("").mockRejectedValueOnce(new Error("保存失败"));
  expect(await store.saveAs()).toBeNull();
  expect(store.archiveSaveStage).toBeNull();
  expect(api.refreshInfo).not.toHaveBeenCalled();
  await expect(store.saveAs()).rejects.toThrow("保存失败");
  expect(store.archiveSaveStage).toBeNull();
  expect(store.saving).toBe(false);

  let finish!: () => void;
  api.SaveAsDialog.mockImplementation(async () => {
    expect(store.archiveSaveStage).toBe("saveAs");
    return "saved.pvf";
  });
  api.refreshInfo.mockReturnValue(new Promise<void>(resolve => { finish = resolve; }));
  const pending = store.saveAs();
  await vi.waitFor(() => expect(store.archiveSaveStage).toBe("finishing"));
  expect(store.saving).toBe(true);
  finish();
  expect(await pending).toBe("saved.pvf");
  expect(store.archiveSaveStage).toBeNull();
  expect(store.saving).toBe(false);
});

test("单文件保存不触发归档遮罩", async () => {
  const store = useEditorStore();
  store.tabs.push({ index: 1, path: "test.stk", title: "test.stk", dataType: 1,
    size: 1, tags: [], editable: true, original: "before", text: "after", modified: false,
    annotations: [], annotationsHidden: false, lineWrapping: true, icon: null,
    fieldImage: null, loading: false, loadError: null });
  let finish!: () => void;
  api.SetText.mockReturnValue(new Promise<void>(resolve => { finish = resolve; }));
  const pending = store.saveFileTab(1);
  expect(store.saving).toBe(true);
  expect(store.archiveSaveStage).toBeNull();
  finish();
  expect(await pending).toBe(true);
  expect(api.SetText).toHaveBeenCalledWith(1, "after");
  expect(store.saving).toBe(false);
  expect(store.archiveSaveStage).toBeNull();
});
