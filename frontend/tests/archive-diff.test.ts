import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { createPinia, disposePinia, setActivePinia, type Pinia } from "pinia";
import type { ArchiveDiffFile } from "../bindings/pvfine/services/models";
import { diffOperation, orientArchiveDiff } from "../src/archiveDiff";
const mocks = vi.hoisted(() => ({
  Start: vi.fn(), Release: vi.fn(), Status: vi.fn(), Children: vi.fn(), FilteredChildren: vi.fn(), FileDiff: vi.fn(), Cancel: vi.fn(), SelectArchive: vi.fn(),
  FlatFiles: vi.fn(), archiveDiffDrafts: vi.fn(), events: new Map<string, () => void>(),
}));
vi.mock("../bindings/pvfine/services", () => ({ ArchiveDiffService: mocks }));
vi.mock("../src/stores/editor", () => ({
  useEditorStore: () => ({
    archiveDiffDrafts: mocks.archiveDiffDrafts,
    refreshMergedFiles: vi.fn().mockResolvedValue(undefined),
  }),
}));
vi.mock("@wailsio/runtime", () => ({ Events: { On: (name: string, callback: () => void) => mocks.events.set(name, callback) } }));
import { useArchiveDiffStore, preserveTreeExpansion, type DiffTreeNode } from "../src/stores/archiveDiff";
const ready = { id: "1", state: "ready", currentPath: "current.pvf", otherPath: "other.pvf", processed: 10, total: 10, added: 2, modified: 1, deleted: 3, error: "" };
const file: ArchiveDiffFile = { path: "a.equ", operation: "add", beforeHash: "", afterHash: "hash", beforeType: 0, afterType: 1, beforeSize: 0, afterSize: 10, beforeExists: false, afterExists: true, beforeText: "", afterText: "new", textAvailable: true, message: "" };
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes; }); return { promise, resolve }; }
let pinia: Pinia;
beforeEach(() => {
  pinia = createPinia(); setActivePinia(pinia); vi.resetAllMocks(); mocks.events.clear();
  mocks.Start.mockResolvedValue(ready); mocks.Status.mockResolvedValue(ready);
  mocks.Children.mockResolvedValue([{ path: "equipment", name: "equipment", isDir: true, count: 6, operation: "" }]);
  mocks.FilteredChildren.mockImplementation((id: string, parent: string) => mocks.Children(id, parent));
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
  // 反向对比下重新打开文件，可编辑草稿仍取本地工作区内容（afterText），不被对比侧覆盖
  await store.openFile({ path: "a.equ", name: "a.equ", isDir: false, operation: "add", count: 1 });
  expect(store.draftText).toBe("new");
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
test("筛选后勾选目录仅选择匹配项，不包含隐藏文件", async () => {
  const store = useArchiveDiffStore(); store.open(); store.otherPath = "other.pvf"; await store.refresh();
  mocks.FlatFiles.mockResolvedValue([
    { path: "equipment/weapon/sword.equ", name: "sword.equ", isDir: false, operation: "add", count: 1 },
    { path: "equipment/weapon/axe.equ", name: "axe.equ", isDir: false, operation: "add", count: 1 },
  ]);
  store.filterOperation = "add";
  await store.toggleSelectDir("equipment/weapon", 2);
  expect(store.selectedPaths).toEqual(new Set(["equipment/weapon/sword.equ", "equipment/weapon/axe.equ"]));
  expect(store.isDirSelected("equipment/weapon", 2)).toBe(true);
  expect(store.isDirIndeterminate("equipment", 5)).toBe(true);
});
test("归档重载后若已配置目标则自动刷新对比，未配置时保留页面选档能力", async () => {
  const store = useArchiveDiffStore();
  store.open();
  store.otherPath = "other.pvf";
  await store.refresh();
  expect(mocks.Start).toHaveBeenCalledTimes(1);

  // 触发归档重载事件：若已配置对比目标，自动刷新比较
  mocks.events.get("archive:reloaded")?.();
  await vi.waitFor(() => expect(mocks.Start).toHaveBeenCalledTimes(2));
  expect(store.otherPath).toBe("other.pvf");
  expect(store.status?.state).toBe("ready");

  // 若关闭后在残留页面重新选档，选档不受中断影响且正常恢复比较
  store.close();
  expect(store.visible).toBe(false);
  mocks.SelectArchive.mockResolvedValue("new_target.pvf");
  await store.selectArchive();
  expect(store.visible).toBe(true);
  expect(store.otherPath).toBe("new_target.pvf");
  expect(mocks.Start).toHaveBeenCalledTimes(3);
});

test("根据关键字过滤后勾选目录仅选择匹配文件，绝不包含被隐藏的文件", async () => {
  const store = useArchiveDiffStore();
  store.open();
  store.otherPath = "other.pvf";
  await store.refresh();

  // 模拟搜索 sword：后端仅返回匹配项，被关键字隐藏的 axe.equ 不应被选中
  mocks.FlatFiles.mockResolvedValue([
    { path: "equipment/weapon/sword.equ", name: "sword.equ", isDir: false, operation: "modify", count: 1 },
  ]);

  store.searchQuery = "sword";
  await store.searchTree("sword");

  // 确认搜索树已构建
  expect(store.displayedTree.length).toBe(1);
  expect(store.displayedTree[0].node.path).toBe("equipment");

  // 勾选顶层 equipment 目录
  await store.toggleSelectDir("equipment");

  // 断言：只有匹配的 sword.equ 被选中，绝不包含被隐藏的文件
  expect(store.selectedPaths).toEqual(new Set(["equipment/weapon/sword.equ"]));
  expect(store.isDirSelected("equipment", 1)).toBe(true);
  expect(store.isDirSelected("equipment/weapon", 1)).toBe(true);

  // 再次勾选目录执行反选，完全清空已选项
  await store.toggleSelectDir("equipment");
  expect(store.selectedPaths.size).toBe(0);
  expect(store.isDirSelected("equipment", 1)).toBe(false);
});

test("方案 B（物理侧锚定）：反向视角下 store.file 保持原始物理归属，可编辑草稿严格锚定本地当前内容", async () => {
  const store = useArchiveDiffStore();
  store.open();
  store.otherPath = "other.pvf";
  await store.refresh();

  // 原始差异数据：本地（after）为 local_content，外部对比目标（before）为 target_content
  const diffFile: ArchiveDiffFile = {
    path: "test.equ",
    operation: "modify",
    beforeHash: "h1",
    afterHash: "h2",
    beforeType: 1,
    afterType: 1,
    beforeSize: 20,
    afterSize: 30,
    beforeExists: true,
    afterExists: true,
    beforeText: "target_content",
    afterText: "local_content",
    textAvailable: true,
    message: "",
  };
  mocks.FileDiff.mockResolvedValue(diffFile);

  await store.openFile({ path: "test.equ", name: "test.equ", isDir: false, operation: "modify", count: 1 });

  // 1. 正向视角下：
  expect(store.reverse).toBe(false);
  expect(store.file?.afterText).toBe("local_content");
  expect(store.file?.beforeText).toBe("target_content");
  expect(store.draftText).toBe("local_content");

  // 用户修改了草稿
  store.updateDraftText("local_content_modified");
  expect(store.isDraftDirty).toBe(true);

  // 2. 切换为反向视角（方案 B）：
  store.toggleReverse();
  expect(store.reverse).toBe(true);

  // 物理侧原始数据 store.file 绝对不被原地翻转修改（双栏编辑器消费此对象）
  expect(store.file?.afterText).toBe("local_content");
  expect(store.file?.beforeText).toBe("target_content");

  // 草稿依然保留用户修改，绝不被目标归档覆盖
  expect(store.draftText).toBe("local_content_modified");
  expect(store.isDraftDirty).toBe(true);

  // 仅供单栏差异和树形列表消费的 displayedFile 执行逻辑翻转
  expect(store.displayedFile?.beforeText).toBe("local_content");
  expect(store.displayedFile?.afterText).toBe("target_content");
});

test("openFileDirect 在静默模式下不置空 file 且不设置 fileLoading 为 true", async () => {
  const store = useArchiveDiffStore();
  store.open();
  store.otherPath = "other.pvf";
  await store.refresh();

  // 第一次正常打开文件（非静默）
  await store.openFile({ path: "a.equ", name: "a.equ", isDir: false, operation: "add", count: 1 });
  expect(store.file?.path).toBe("a.equ");
  expect(store.fileLoading).toBe(false);

  // 构造异步挂起模拟静默拉取过程
  const pending = deferred<ArchiveDiffFile>();
  mocks.FileDiff.mockReturnValueOnce(pending.promise);

  const silentPromise = store.openFileDirect({ path: "a.equ", name: "a.equ", isDir: false, operation: "add", count: 1 }, true);
  // 静默模式下：fileLoading 应保持 false，file 应继续保留原先的引用（不被置为 null 导致外部组件卸载闪烁）
  expect(store.fileLoading).toBe(false);
  expect(store.file).not.toBeNull();
  expect(store.file?.path).toBe("a.equ");

  pending.resolve({ ...file, afterText: "updated_after_save" });
  await silentPromise;
  expect(store.file?.afterText).toBe("updated_after_save");
  expect(store.fileLoading).toBe(false);
});

test("preserveTreeExpansion 能够将旧树已展开的 children 平滑嫁接给新树", () => {
  const oldTree: DiffTreeNode[] = [
    {
      key: "equipment",
      label: "equipment",
      isLeaf: false,
      node: { path: "equipment", name: "equipment", isDir: true, count: 2, operation: "" },
      children: [
        {
          key: "equipment/weapon",
          label: "weapon",
          isLeaf: false,
          node: { path: "equipment/weapon", name: "weapon", isDir: true, count: 1, operation: "" },
          children: [
            {
              key: "equipment/weapon/sword.equ",
              label: "sword.equ",
              isLeaf: true,
              node: { path: "equipment/weapon/sword.equ", name: "sword.equ", isDir: false, count: 1, operation: "add" },
            },
          ],
        },
      ],
    },
  ];

  // 新拉取的树只有 1 级节点（未展开，children 为 undefined）
  const newTree: DiffTreeNode[] = [
    {
      key: "equipment",
      label: "equipment",
      isLeaf: false,
      node: { path: "equipment", name: "equipment", isDir: true, count: 2, operation: "" },
    },
  ];

  preserveTreeExpansion(newTree, oldTree);

  // 断言：newTree 的 equipment 节点成功保留了已展开的 children
  expect(newTree[0].children).toBeDefined();
  expect(newTree[0].children?.length).toBe(1);
  expect(newTree[0].children?.[0].key).toBe("equipment/weapon");
  expect(newTree[0].children?.[0].children?.length).toBe(1);
});

test("refreshTreeForSavedFile 针对已展开的父目录进行局部刷新，不破坏整树状态", async () => {
  const store = useArchiveDiffStore();
  store.open();
  store.otherPath = "other.pvf";
  await store.refresh();

  // 展开 equipment 目录
  const equipmentNode = store.tree[0]!;
  mocks.FilteredChildren.mockResolvedValueOnce([
    { path: "equipment/a.equ", name: "a.equ", isDir: false, operation: "add", count: 1 },
  ]);
  await store.loadChildren(equipmentNode);
  expect(equipmentNode.children?.length).toBe(1);

  // 模拟保存 equipment/a.equ 后的局部刷新
  mocks.FilteredChildren.mockImplementation(async (_id: string, parent: string) => {
    if (parent === "equipment") {
      // 局部刷新 equipment 目录下仅返回最新数据
      return [{ path: "equipment/a.equ", name: "a.equ", isDir: false, operation: "modify", count: 1 }];
    }
    if (parent === "") {
      // 根节点最新计数值
      return [{ path: "equipment", name: "equipment", isDir: true, count: 1, operation: "" }];
    }
    return [];
  });

  await store.refreshCurrentView("equipment/a.equ");

  // 断言：equipment 节点的 children 得到局部精准更新，且整树结构未被坍塌
  expect(store.tree.length).toBe(1);
  expect(store.tree[0].node.path).toBe("equipment");
  expect(store.tree[0].children?.length).toBe(1);
  expect(store.tree[0].children?.[0].node.operation).toBe("modify");
});
