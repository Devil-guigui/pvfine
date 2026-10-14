import type { ArchiveDiffFile } from "../bindings/pvfine/services/models";

export function diffOperation(operation: string, reverse: boolean): string {
  if (!reverse) return operation;
  return operation === "add" ? "delete" : operation === "delete" ? "add" : operation;
}
export function orientArchiveDiff(file: ArchiveDiffFile | null, reverse: boolean): ArchiveDiffFile | null {
  if (!file || !reverse) return file;
  return {
    ...file,
    operation: diffOperation(file.operation, true),
    beforeHash: file.afterHash, afterHash: file.beforeHash,
    beforeType: file.afterType, afterType: file.beforeType,
    beforeSize: file.afterSize, afterSize: file.beforeSize,
    beforeExists: file.afterExists, afterExists: file.beforeExists,
    beforeText: file.afterText, afterText: file.beforeText,
  };
}
export function archiveName(path: string): string {
  return path.split(/[\\/]/).pop() || "当前工作区";
}
