import type { UpgradeRow } from "../../bindings/pvfine/services/models";
import { editSkillNumbers } from "./skill";
export type ForgeScope = "table" | "grades" | "maximum";
export function editForge(text: string, rows: UpgradeRow[], scope: ForgeScope, changes: { column: number; value: number }[], maximumLevel: number): string {
  if (!rows.length || !changes.length) throw new Error("请选择等级和修改项");
  const patches = changes.flatMap(({ column, value }) => {
    const valid = scope === "table" ? [2, 3] : scope === "grades" ? [0, 1] : [0];
    const limit = scope === "maximum" ? maximumLevel : scope === "table" && column === 2 ? 100 : 2147483647;
    if (!valid.includes(column) || !Number.isFinite(value) || value < (scope === "maximum" ? 1 : 0) || value > limit || (scope !== "table" && !Number.isInteger(value))) throw new Error("数值超出允许范围");
    return rows.map(row => {
      const token = row.fields?.[column];
      if (!token) throw new Error("数据已失效");
      const operand = scope === "table" && column === 2 ? Math.round(value * 100) : value;
      // Preserve the original numeric type; integer multipliers cannot represent fractions.
      if (token.tokenType === 0 && !Number.isInteger(operand)) throw new Error("该字段在源文件中为整数，不能写入小数");
      return { token, value: operand };
    });
  }).sort((a, b) => b.token.start - a.token.start);
  let result = text;
  let previous = Infinity;
  for (const patch of patches) {
    if (patch.token.end > previous) throw new Error("数据位置重叠");
    result = editSkillNumbers(result, [patch.token], "set", patch.value);
    previous = patch.token.start;
  }
  return result;
}
