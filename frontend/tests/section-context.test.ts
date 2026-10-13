import { describe, expect, it } from "vitest";
import { sectionAt } from "../src/sectionContext";
describe("section context", () => {
  it("uses the same name for opening and closing tags", () => {
    const text = "[name]\n`value`\n[/name]";
    expect(sectionAt(text, 2)).toBe("name");
    expect(sectionAt(text, text.indexOf("[/") + 2)).toBe("name");
    expect(sectionAt(text, text.indexOf("value"))).toBeNull();
  });
  it("excludes comments, multiline strings and escaped backticks", () => {
    const text = "# [fake]\n`start\n[fake] `` [also fake]\nend`\n[real]";
    expect(sectionAt(text, text.indexOf("[fake]") + 1)).toBeNull();
    expect(sectionAt(text, text.lastIndexOf("[fake]") + 1)).toBeNull();
    expect(sectionAt(text, text.indexOf("[also") + 1)).toBeNull();
    expect(sectionAt(text, text.indexOf("[real]") + 1)).toBe("real");
  });
  it("ignores incomplete tags and normalizes whitespace", () => {
    expect(sectionAt("[", 0)).toBeNull();
    expect(sectionAt("[/ name ]", 3)).toBe("name");
    expect(sectionAt("[ ]", 1)).toBeNull();
  });
});
