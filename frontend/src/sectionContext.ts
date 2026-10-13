/** Find actual PVF tags, excluding comments and multiline backtick strings. */
export function sectionAt(text: string, position: number): string | null {
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (quoted) {
      if (ch === "`") {
        if (text[i + 1] === "`") i++;
        else quoted = false;
      }
      continue;
    }
    if (ch === "`") {
      quoted = true;
      continue;
    }
    if (ch === "#") {
      const end = text.indexOf("\n", i);
      if (end < 0) return null;
      i = end;
      continue;
    }
    if (ch !== "[") continue;
    const end = text.indexOf("]", i + 1);
    if (end < 0) return null;
    const name = text.slice(i + 1, end);
    if (!name.includes("\n") && position >= i && position <= end + 1)
      return name.replace(/^\//, "").trim() || null;
    i = end;
  }
  return null;
}
