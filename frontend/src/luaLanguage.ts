import { StreamLanguage } from "@codemirror/language";
import { lua } from "@codemirror/legacy-modes/mode/lua";

export const luaLanguage = StreamLanguage.define(lua);

export function archiveEditorLanguage(path: string): "pvf" | "lua" | "squirrel" {
  if (/\.lua$/i.test(path)) return "lua";
  if (/\.nut$/i.test(path)) return "squirrel";
  return "pvf";
}
