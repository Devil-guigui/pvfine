import { describe, expect, it } from "vitest";
import { editUpgrade, editUpgradeBatch, upgradeRange, upgradeRows, upgradeSuccessRate } from "../src/gui/upgrade";
import { getGUIProvider } from "../src/gui/registry";
import type { UpgradeRow } from "../bindings/pvfine/services/models";

function fixture(crlf = false) {
  const normalized = "# 😀\n[table]\n2 0.018 0.1 0.033 0.3 0.6 12500 2 3 3037 10 1 0.018 0.1 0.033 0.17 0.6\n[/table]\n[other]\n99";
  const start = normalized.indexOf("12500");
  let offset = start;
  const fields = [12500, 2, 3, 3037, 10].map((value) => {
    const token = { value, start: offset, end: offset + String(value).length, tokenType: 0 };
    offset = token.end + 1;
    return token;
  });
  return { text: crlf ? normalized.replaceAll("\n", "\r\n") : normalized, row: { level: 1, fields, costName: "材料" } as UpgradeRow };
}

describe("强化/增幅 GUI", () => {
  it("按等级范围同时批量设置概率、失败操作、降级参数和材料消耗", () => {
    const { text, row } = fixture();
    const rows = [1, 2, 3].map((level) => ({ ...row, level, fields: row.fields!.map(field => ({ ...field, start: field.start + (level - 1) * (text.length + 1), end: field.end + (level - 1) * (text.length + 1) })) }));
    const source = [text, text, text].join("\n");
    const targets = upgradeRange(rows, [2, 3]);
    expect(targets.map(row => row.level)).toEqual([2, 3]);
    const changed = text.replace("12500 2 3 3037 10", "50000 2 1 55 99");
    expect(editUpgradeBatch(source, targets, [{ column: 0, value: 50 }, { column: 1, value: 2 }, { column: 2, value: 1 }, { column: 3, value: 55 }, { column: 4, value: 99 }])).toBe([text, changed, changed].join("\n"));
    expect(editUpgradeBatch(source, targets, [{ column: 1, value: 3 }])).toBe([text, text.replace("12500 2 3", "12500 3 0"), text.replace("12500 2 3", "12500 3 0")].join("\n"));
    expect(() => upgradeRange(rows, [3, 2])).toThrow("等级范围");
    expect(() => editUpgradeBatch(source, targets, [{ column: 0, value: 50 }, { column: 4, value: -1 }])).toThrow();
    expect(() => editUpgradeBatch(source, targets, [])).toThrow("至少选择");
  });
  it("新格式 T1/T2 独立编辑，仅改变 normal 表的对应字段，保留其它表与 CRLF", () => {
    const rowText = "74.3 0 0 0 0 63 0 90.5625 90000 88000 2 3 3 0 3037 260 8.2 0.62 10.3 3.501 17.17 23.94 0 27.531";
    const prefix = "# 😀\n[table]\n[table type]\n`event` 90000 88000\n[/table]\n[table]\n[table type]\n`normal` ";
    const suffix = "\n[/table]\n[table]\n[table type]\n`amplify` " + rowText + "\n[/table]";
    const text = (prefix + rowText + suffix).replaceAll("\n", "\r\n");
    const tokens = [...rowText.matchAll(/\d+(?:\.\d+)?/g)].map(match => ({ value: Number(match[0]), start: prefix.length + match.index!, end: prefix.length + match.index! + match[0].length, tokenType: match[0].includes('.') ? 2 : 0 }));
    const row = { level: 18, fields: [8, 10, 11, 14, 15].map(i => tokens[i]!), fieldsT2: [9, 12, 13].map(i => tokens[i]!), costName: "材料" } as UpgradeRow;
    const t1 = upgradeRows([row], "T1"), t2 = upgradeRows([row], "T2");
    expect(upgradeSuccessRate(t1[0]!)).toBe(10);
    expect(upgradeSuccessRate(t2[0]!)).toBe(12);
    const expected = (normalRow: string) => (prefix + normalRow + suffix).replaceAll("\n", "\r\n");
    expect(editUpgrade(text, t1, 0, 20)).toBe(expected(rowText.replace("90000 88000", "80000 88000")));
    expect(editUpgrade(text, t2, 0, 25)).toBe(expected(rowText.replace("90000 88000", "90000 75000")));
    expect(editUpgrade(text, t1, 1, 1)).toBe(expected(rowText.replace("2 3 3 0", "1 0 3 0")));
    expect(editUpgrade(text, t2, 1, 2)).toBe(expected(rowText.replace("2 3 3 0", "2 3 2 0")));
    expect(editUpgrade(text, t2, 3, 55)).toBe(expected(rowText.replace("3037", "55")));
    expect(() => upgradeRows([fixture().row], "T2")).toThrow("缺少防具");
  });
  it("只匹配两个概率文件，兼容路径大小写与分隔符", () => {
    const file = { index: 1, path: "ETC\\UPGRADE.ETC", text: "", editable: true };
    expect(getGUIProvider(file)?.id).toBe("upgrade");
    expect(getGUIProvider({ ...file, path: "etc/amplifyupgrade.etc" })?.id).toBe("upgrade");
    expect(getGUIProvider({ ...file, path: "etc/upgrade_separate.etc" })?.id).toBe("forge");
  });
  it("概率换算保留千分之一百分比，覆盖 0% 和 100%", () => {
    const { text, row } = fixture();
    expect(upgradeSuccessRate(row)).toBe(87.5);
    expect(editUpgrade(text, [row], 0, 100)).toBe(text.replace("12500", "0"));
    expect(editUpgrade(text, [row], 0, 0)).toBe(text.replace("12500", "100000"));
    expect(editUpgrade(text, [row], 0, 87.501)).toBe(text.replace("12500", "12499"));
  });
  it("失败操作同步清零降级参数，保留其它字段和 CRLF", () => {
    const { text, row } = fixture(true);
    expect(editUpgrade(text, [row], 1, 3)).toBe(text.replace("12500 2 3", "12500 3 0"));
    expect(editUpgrade(text, [row], 1, 1)).toBe(text.replace("12500 2 3", "12500 1 0"));
    expect(editUpgrade(text, [row], 1, 2)).toBe(text);
    expect(editUpgrade(text, [row], 3, 999)).toBe(text.replace("3037", "999"));
  });
  it("批量替换材料，只更改目标列", () => {
    const { text, row } = fixture();
    const shift = text.length + 1;
    const second = { ...row, level: 2, fields: row.fields!.map(field => ({ ...field, start: field.start + shift, end: field.end + shift })) };
    const source = text + "\n" + text;
    expect(editUpgrade(source, [row, second], 3, 55)).toBe(source.replaceAll("3037", "55"));
  });
  it("拒绝无效参数、过期草稿及重叠定位", () => {
    const { text, row } = fixture();
    for (const [column, value] of [[0, 101], [1, 0], [2, 32], [3, -1], [4, 1.5], [4, 2147483648]]) {
      expect(() => editUpgrade(text, [row], column!, value!)).toThrow();
    }
    expect(() => editUpgrade(text.replace("3037", "1234"), [row], 3, 55)).toThrow("草稿已变化");
    expect(() => editUpgrade(text, [row, row], 3, 55)).toThrow("重叠");
  });
});
