import { expect, test } from "vitest";
import { computeDiffLines } from "../src/textDiff";
const diff = (beforeText: string, afterText: string, operation = "modify") => computeDiffLines({ beforeText, afterText, operation, textAvailable: true });
test("统一 diff 包含首块头、正确的旧新行号和三行上下文", () => {
  const before = Array.from({ length: 30 }, (_, i) => `line ${i + 1}`);
  const after = [...before]; after[4] = "changed first"; after[24] = "changed second";
  const result = diff(before.join("\n"), after.join("\n"));
  expect(result.addCount).toBe(2); expect(result.removeCount).toBe(2);
  expect(result.lines.filter(line => line.kind === "hunk").map(line => line.text)).toEqual(["@@ -2,7 +2,7 @@", "@@ -22,7 +22,7 @@"]);
  expect(result.lines.find(line => line.text === "changed second")).toMatchObject({ kind: "add", newLine: 25, oldLine: undefined });
});
test("新增、删除及空文件的行号和块头", () => {
  expect(diff("", "one\ntwo", "add").lines[0].text).toBe("@@ -0,0 +1,2 @@");
  expect(diff("one", "", "delete").lines[0].text).toBe("@@ -1,1 +0,0 @@");
  expect(diff("", "", "add").lines).toEqual([]);
  expect(diff("one", "one").lines).toEqual([]);
});
test("纯插入和删除使用正确的零长度旧新侧块头", () => {
  expect(diff("a\nb", "a\nx\nb").lines[0].text).toBe("@@ -1,2 +1,3 @@");
  expect(diff("a\nx\nb", "a\nb").lines[0].text).toBe("@@ -1,3 +1,2 @@");
});
test("大文件少量修改保持紧凑，输出最多 2000 行并明确截断", () => {
  const before = Array.from({ length: 5000 }, (_, i) => `${i}`);
  const after = [...before]; after[4000] = "edit";
  expect(diff(before.join("\n"), after.join("\n")).lines.length).toBe(9);
  const result = diff("", before.join("\n"), "add");
  expect(result.truncated).toBe(true); expect(result.lines.length).toBe(2000); expect(result.addCount).toBe(5000);
});
test("换行风格相同化和不可用文本返回空 diff", () => {
  expect(diff("a\r\nb\r", "a\nb\n").lines).toEqual([]);
  expect(computeDiffLines(null).lines).toEqual([]);
});
