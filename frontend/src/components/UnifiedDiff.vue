<script setup lang="ts">
import { computed } from "vue";
import { computeDiffLines, type TextDiffInput } from "../textDiff";
const props = defineProps<{ diff: TextDiffInput | null }>();
const parsed = computed(() => computeDiffLines(props.diff));
</script>
<template>
  <div class="diff-lines-wrapper">
    <div class="diff-lines">
      <div v-for="(line, idx) in parsed.lines" :key="idx" :class="['diff-line', `diff-line--${line.kind}`]">
        <span v-if="line.kind === 'hunk'">{{ line.text }}</span>
        <template v-else>
          <span class="diff-line-number">{{ line.oldLine ?? '' }}</span>
          <span class="diff-line-number">{{ line.newLine ?? '' }}</span>
          <span class="diff-marker">{{ line.kind === 'add' ? '+' : line.kind === 'remove' ? '-' : ' ' }}</span>
          <span class="diff-text">{{ line.text }}</span>
        </template>
      </div>
    </div>
    <div v-if="parsed.truncated" class="diff-truncated-notice">Diff 已截断，仅展示前 2000 行变更。</div>
  </div>
</template>
<style scoped>
.diff-lines-wrapper {
  flex: 1;
  min-height: 0;
  overflow: auto;
  background: var(--pvf-surface-code);
}
.diff-lines {
  display: flex;
  flex-direction: column;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  line-height: 1.55;
  white-space: pre;
  min-width: 100%;
  width: max-content;
}
.diff-line {
  display: grid;
  grid-template-columns: 44px 44px 20px minmax(0, 1fr);
  min-height: 20px;
  padding: 0 8px;
}
.diff-line--add {
  color: var(--pvf-success-hover);
  background: var(--pvf-surface-success);
}
.diff-line--remove {
  color: var(--pvf-error-hover);
  background: var(--pvf-surface-error);
}
.diff-line--context {
  color: var(--pvf-text-primary);
}
.diff-line--hunk {
  display: block;
  padding: 3px 12px;
  background: var(--pvf-surface-subtle);
  color: var(--pvf-text-muted);
  font-style: italic;
  user-select: none;
}
.diff-line-number {
  padding-right: 8px;
  color: var(--pvf-text-faint);
  text-align: right;
  user-select: none;
}
.diff-marker {
  color: var(--pvf-text-faint);
  text-align: center;
  user-select: none;
}
.diff-text {
  min-width: 0;
}
.diff-truncated-notice {
  padding: 6px 12px;
  font-size: 11px;
  color: var(--pvf-text-muted);
  background: var(--pvf-surface-subtle);
  border-top: 1px solid var(--pvf-border-subtle);
}
</style>
