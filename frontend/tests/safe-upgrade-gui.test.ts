import { describe, expect, it } from "vitest";
import { editSafeUpgradeBatch, safeUpgradeRows, upgradeRange } from "../src/gui/upgrade";
import type { SafeUpgradeRow } from "../bindings/pvfine/services/models";

function fixture() {
  const lines = ["0 7 0 0 9 10327281 40 124448", "1 7 20000 5000 9 10327281 41 124448", "2 7 94000 1000 9 10327281 1529 4626036"];
  const prefix = "# 😀\n[table]\n[table type]\n`normal` 99\n[/table]\n[safe upgrade]\n";
  const suffix = "\n[/safe upgrade]\n[other]\n88";
  const normalized = prefix + lines.join("\n") + suffix;
  let offset = prefix.length;
  const raw: SafeUpgradeRow[] = lines.map(line => {
    const fields = [...line.matchAll(/\d+/g)].map(match => ({ value: Number(match[0]), start: offset + match.index!, end: offset + match.index! + match[0].length, tokenType: 0 }));
    offset += line.length + 1;
    return { fields, costName: "炉岩核" } as SafeUpgradeRow;
  });
  return { text: normalized.replaceAll("\n", "\r\n"), lines, prefix, suffix, rows: safeUpgradeRows(raw) };
}

describe("安全强化编辑", () => {
  it("允许单条修改失败率，按 100000 分母转换，保留隐藏字段与原始等级", () => {
    const { text, rows } = fixture();
    expect(rows.map(row => row.level)).toEqual([1, 2, 3]);
    expect(rows[0]?.costName).toBe("炉岩核");
    expect(editSafeUpgradeBatch(text, [rows[2]!], [{ column: 0, value: 87.501 }])).toBe(text.replace("2 7 94000 1000 9", "2 7 87501 1000 9"));
    expect(editSafeUpgradeBatch(text, [rows[2]!], [{ column: 0, value: 100 }])).toContain("2 7 100000 1000 9");
    expect(editSafeUpgradeBatch(text, [rows[2]!], [{ column: 0, value: 0 }])).toContain("2 7 0 1000 9");
  });
  it("显示 +1 对应原始等级 0，批量修改不偏移记录且保留原始等级、隐藏字段", () => {
    const { text, rows, lines, prefix, suffix } = fixture();
    const targets = upgradeRange(rows, [1, 2]);
    expect(targets).toHaveLength(2);
    const result = editSafeUpgradeBatch(text, targets, [{ column: 0, value: 50 }, { column: 1, value: 2000 }, { column: 2, value: 100 }, { column: 3, value: 55 }, { column: 4, value: 99 }]);
    expect(result).toBe((prefix + ["0 7 50000 2000 9 55 99 100", "1 7 50000 2000 9 55 99 100", lines[2]].join("\n") + suffix).replaceAll("\n", "\r\n"));
  });
  it("拒绝过期数据、越界、重复定位与小数材料数量", () => {
    const { text, rows } = fixture();
    expect(() => editSafeUpgradeBatch(text, rows, [{ column: 0, value: 101 }])).toThrow();
    expect(() => editSafeUpgradeBatch(text, rows, [{ column: 4, value: 1.2 }])).toThrow();
    expect(() => editSafeUpgradeBatch(text, rows, [{ column: 2, value: 2147483648 }])).toThrow();
    expect(() => editSafeUpgradeBatch(text.replace("94000", "93000"), [rows[2]!], [{ column: 0, value: 20 }])).toThrow("草稿已变化");
    expect(() => editSafeUpgradeBatch(text, rows, [{ column: 1, value: 10 }, { column: 1, value: 20 }])).toThrow("重叠");
  });
});
