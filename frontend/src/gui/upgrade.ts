import type { UpgradeRow } from "../../bindings/pvfine/services/models";
import { editSkillNumbers } from "./skill";

export const upgradeGroupLabels = { T1: "武器", T2: "防具" } as const;

/** T1 is weapons; T2 is armor. Materials are shared by both groups. */
export function upgradeRows(rows: UpgradeRow[], group: "T1" | "T2"): UpgradeRow[] {
  if (group === "T1") return rows;
  return rows.map(row => {
    if (row.fieldsT2?.length !== 3 || row.fields?.length !== 5) throw new Error("缺少防具数据");
    return { ...row, fields: [...row.fieldsT2, ...row.fields.slice(3)] };
  });
}

export function upgradeSuccessRate(row: UpgradeRow): number {
  const failure = row.fields?.[0];
  if (!failure) throw new Error("缺少失败概率数据");
  return Number((100 - failure.value / 1000).toFixed(3));
}

/** Edit only the selected columns, preserving every other table parameter. */
export function editUpgrade(text: string, rows: UpgradeRow[], column: number, value: number): string {
  return editUpgradeBatch(text, rows, [{ column, value }]);
}

export interface UpgradeChange { column: number; value: number }

export function upgradeRange(rows: UpgradeRow[], range: [number, number]): UpgradeRow[] {
  if (!range.every(Number.isInteger) || range[0] < 1 || range[1] < range[0]) throw new Error("等级范围无效");
  return rows.filter(row => row.level >= range[0] && row.level <= range[1]);
}

/** Validate the complete batch before publishing one source patch. */
export function editUpgradeBatch(text: string, rows: UpgradeRow[], changes: UpgradeChange[]): string {
  if (!changes.length) throw new Error("请至少选择一项修改");
  const patches = changes.flatMap(({ column, value }) => {
    if (!rows.length || !Number.isFinite(value) || column < 0 || column > 4 || !Number.isInteger(column)) throw new Error("请输入有效数值");
    const maximum = [100, 3, rows.every(row => row.fieldsT2?.length === 3) ? 2147483647 : 31, 2147483647, 2147483647][column]!;
    if (value < (column === 1 ? 1 : 0) || value > maximum || (column !== 0 && !Number.isInteger(value))) throw new Error("数值超出允许范围");
    const operand = column === 0 ? Math.round((100 - value) * 1000) : value;
    // Switching away from downgrade also clears its parameter, as in pvftools.
    return rows.flatMap((row) => {
      const field = row.fields?.[column];
      if (!field) throw new Error("数据已失效，请重新加载");
      const edits = [{ token: field, value: operand }];
      if (column === 1 && value !== 2) {
        const parameter = row.fields?.[2];
        if (!parameter) throw new Error("数据已失效，请重新加载");
        edits.push({ token: parameter, value: 0 });
      }
      return edits;
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
