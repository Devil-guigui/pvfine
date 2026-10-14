export interface TextDiffInput {
  operation: string;
  textAvailable: boolean;
  beforeText?: string;
  afterText?: string;
}

export interface DisplayDiffLine {
  kind: "add" | "remove" | "context" | "hunk";
  text: string;
  oldLine?: number;
  newLine?: number;
}

export function computeDiffLines(diff: TextDiffInput | null): {
  lines: DisplayDiffLine[];
  truncated: boolean;
  addCount: number;
  removeCount: number;
} {
  if (!diff || !diff.textAvailable) {
    return { lines: [], truncated: false, addCount: 0, removeCount: 0 };
  }

  const splitLines = (text?: string): string[] => {
    if (text === undefined || text === null || text === "") return [];
    return text.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
  };

  const beforeLines = splitLines(diff.beforeText);
  const afterLines = splitLines(diff.afterText);

  if (beforeLines.join("\n") === afterLines.join("\n")) {
    return { lines: [], truncated: false, addCount: 0, removeCount: 0 };
  }

  // Case 1: Pure add
  if (diff.operation === "add" || beforeLines.length === 0) {
    const isTruncated = afterLines.length > 1999;
    const linesToUse = isTruncated ? afterLines.slice(0, 1999) : afterLines;
    const lines: DisplayDiffLine[] = linesToUse.map((text, idx) => ({
      kind: "add",
      text,
      newLine: idx + 1,
    }));
    lines.unshift({ kind: "hunk", text: `@@ -0,0 +1,${afterLines.length} @@` });
    return {
      lines,
      truncated: isTruncated,
      addCount: afterLines.length,
      removeCount: 0,
    };
  }

  // Case 2: Pure delete
  if (diff.operation === "delete" || afterLines.length === 0) {
    const isTruncated = beforeLines.length > 1999;
    const linesToUse = isTruncated ? beforeLines.slice(0, 1999) : beforeLines;
    const lines: DisplayDiffLine[] = linesToUse.map((text, idx) => ({
      kind: "remove",
      text,
      oldLine: idx + 1,
    }));
    lines.unshift({ kind: "hunk", text: `@@ -1,${beforeLines.length} +0,0 @@` });
    return {
      lines,
      truncated: isTruncated,
      addCount: 0,
      removeCount: beforeLines.length,
    };
  }

  // Case 3: Modify
  let start = 0;
  while (
    start < beforeLines.length &&
    start < afterLines.length &&
    beforeLines[start] === afterLines[start]
  ) {
    start++;
  }

  let oldEnd = beforeLines.length - 1;
  let newEnd = afterLines.length - 1;
  while (
    oldEnd >= start &&
    newEnd >= start &&
    beforeLines[oldEnd] === afterLines[newEnd]
  ) {
    oldEnd--;
    newEnd--;
  }

  type RawOp = { kind: "=" | "-" | "+"; text: string };
  const allOps: RawOp[] = [];

  // Common prefix
  for (let idx = 0; idx < start; idx++) {
    allOps.push({ kind: "=", text: beforeLines[idx] });
  }

  const oldMid = beforeLines.slice(start, oldEnd + 1);
  const newMid = afterLines.slice(start, newEnd + 1);

  if (oldMid.length === 0) {
    for (const line of newMid) allOps.push({ kind: "+", text: line });
  } else if (newMid.length === 0) {
    for (const line of oldMid) allOps.push({ kind: "-", text: line });
  } else if (
    oldMid.length > 1000 ||
    newMid.length > 1000 ||
    oldMid.length * newMid.length > 400000
  ) {
    for (const line of oldMid) allOps.push({ kind: "-", text: line });
    for (const line of newMid) allOps.push({ kind: "+", text: line });
  } else {
    // Dynamic programming LCS
    const rows = oldMid.length;
    const cols = newMid.length;
    const dp: number[][] = Array.from({ length: rows + 1 }, () =>
      new Array(cols + 1).fill(0)
    );
    for (let r = rows - 1; r >= 0; r--) {
      for (let c = cols - 1; c >= 0; c--) {
        if (oldMid[r] === newMid[c]) {
          dp[r][c] = dp[r + 1][c + 1] + 1;
        } else {
          dp[r][c] = Math.max(dp[r + 1][c], dp[r][c + 1]);
        }
      }
    }
    let r = 0,
      c = 0;
    while (r < rows && c < cols) {
      if (oldMid[r] === newMid[c]) {
        allOps.push({ kind: "=", text: oldMid[r] });
        r++;
        c++;
      } else if (dp[r + 1][c] >= dp[r][c + 1]) {
        allOps.push({ kind: "-", text: oldMid[r] });
        r++;
      } else {
        allOps.push({ kind: "+", text: newMid[c] });
        c++;
      }
    }
    while (r < rows) {
      allOps.push({ kind: "-", text: oldMid[r] });
      r++;
    }
    while (c < cols) {
      allOps.push({ kind: "+", text: newMid[c] });
      c++;
    }
  }

  // Common suffix
  for (let idx = oldEnd + 1; idx < beforeLines.length; idx++) {
    allOps.push({ kind: "=", text: beforeLines[idx] });
  }

  let oldLineNum = 1;
  let newLineNum = 1;
  let addCount = 0;
  let removeCount = 0;

  const opsWithLines = allOps.map((op) => {
    const item = {
      kind: op.kind,
      text: op.text,
      oldLine: op.kind !== "+" ? oldLineNum : undefined,
      newLine: op.kind !== "-" ? newLineNum : undefined,
    };
    if (op.kind === "+") {
      addCount++;
      newLineNum++;
    } else if (op.kind === "-") {
      removeCount++;
      oldLineNum++;
    } else {
      oldLineNum++;
      newLineNum++;
    }
    return item;
  });

  // Context filtering with 3 lines of context
  const CONTEXT = 3;
  const visible = new Array(opsWithLines.length).fill(false);
  for (let i = 0; i < opsWithLines.length; i++) {
    if (opsWithLines[i].kind !== "=") {
      const from = Math.max(0, i - CONTEXT);
      const to = Math.min(opsWithLines.length - 1, i + CONTEXT);
      for (let k = from; k <= to; k++) {
        visible[k] = true;
      }
    }
  }

  const oldStarts: number[] = [], newStarts: number[] = [];
  let oldStartCounter = 1, newStartCounter = 1;
  for (const op of opsWithLines) {
    oldStarts.push(oldStartCounter); newStarts.push(newStartCounter);
    if (op.kind !== "+") oldStartCounter++;
    if (op.kind !== "-") newStartCounter++;
  }
  const result: DisplayDiffLine[] = [];
  let lastVisible = -1;

  for (let i = 0; i < opsWithLines.length; i++) {
    if (!visible[i]) continue;
    if (lastVisible === -1 || i > lastVisible + 1) {
      let end = i;
      while (end < opsWithLines.length && visible[end]) end++;
      const block = opsWithLines.slice(i, end);
      const oldStart = oldStarts[i];
      const newStart = newStarts[i];
      const oldCount = block.filter(op => op.kind !== "+").length;
      const newCount = block.filter(op => op.kind !== "-").length;
      result.push({ kind: "hunk", text: `@@ -${oldCount ? oldStart : oldStart - 1},${oldCount} +${newCount ? newStart : newStart - 1},${newCount} @@` });
    }
    const op = opsWithLines[i];
    result.push({
      kind: op.kind === "+" ? "add" : op.kind === "-" ? "remove" : "context",
      text: op.text,
      oldLine: op.oldLine,
      newLine: op.newLine,
    });
    lastVisible = i;
  }

  const isTruncated = result.length > 2000;
  const lines = isTruncated ? result.slice(0, 2000) : result;
  return { lines, truncated: isTruncated, addCount, removeCount };
}

