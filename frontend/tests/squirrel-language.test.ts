import { expect, test } from "vitest";
import { EditorState } from "@codemirror/state";
import { ensureSyntaxTree } from "@codemirror/language";
import { highlightTree, tags, tagHighlighter } from "@lezer/highlight";
import { archiveEditorLanguage } from "../src/luaLanguage";
import { squirrelLanguage } from "../src/squirrelLanguage";

test("归档中的 nut 后缀启用 Squirrel，兼容大小写并保留其他语言识别", () => {
  expect(archiveEditorLanguage("scripts/example.nut")).toBe("squirrel");
  expect(archiveEditorLanguage("scripts/example.NUT")).toBe("squirrel");
  expect(archiveEditorLanguage("scripts/example.nut.bak")).toBe("pvf");
  expect(archiveEditorLanguage("scripts/example.lua")).toBe("lua");
  expect(archiveEditorLanguage("equipment/example.equ")).toBe("pvf");
});

test("Squirrel 高亮识别特有关键字、数字、转义字符串和跨行注释", () => {
  const doc = [
    "local count = 42;",
    'local text = "hello\\\"world";',
    "// single line comment",
    "/* block",
    "comment */",
    "foreach (item in items) { yield clone item; }",
    "return null;",
  ].join("\n");
  const state = EditorState.create({ doc, extensions: squirrelLanguage.extension });
  const tree = ensureSyntaxTree(state, doc.length, 1000);
  expect(tree).not.toBeNull();
  const highlighted: Record<string, string[]> = {};
  highlightTree(tree!, tagHighlighter([
    { tag: tags.keyword, class: "keyword" },
    { tag: tags.number, class: "number" },
    { tag: tags.string, class: "string" },
    { tag: tags.comment, class: "comment" },
    { tag: tags.atom, class: "atom" },
  ]), (from, to, style) => {
    (highlighted[style] ??= []).push(doc.slice(from, to));
  });
  expect(highlighted.keyword).toEqual(expect.arrayContaining([
    "local", "foreach", "in", "yield", "clone", "return",
  ]));
  expect(highlighted.number).toEqual(["42"]);
  expect(highlighted.string).toContain('"hello\\\"world"');
  expect(highlighted.comment).toContain("// single line comment");
  expect(highlighted.comment?.join("\n")).toContain("/* block\ncomment */");
  expect(highlighted.atom).toContain("null");
});
