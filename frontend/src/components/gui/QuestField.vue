<script setup lang="ts">
import { computed } from 'vue';
import { NButton, NInput, NInputNumber, NSelect } from 'naive-ui';
import type { EditorAnnotation } from '../../../bindings/pvfine/services/models';
import { questAnnotationText, questReferenceAnnotation, questField, questValues, type QuestDocument, type QuestChange } from '../../gui/quest';
const props = withDefaults(defineProps<{ document: QuestDocument; name: string; label: string; kind: 'string' | 'integer'; disabled: boolean; multiline?: boolean; options?: { label: string; value: string }[]; width?: number; annotations?: EditorAnnotation[] }>(), { annotations: () => [] });
const emit = defineEmits<{ (e: 'change', change: QuestChange): void; (e: 'edit-reference', name: string, label: string): void }>();
const values = computed(() => questValues(props.document, props.name));
const reference = computed(() => questField(props.document, props.name)?.tokens[0]?.reference);
const annotation = computed(() => reference.value && questReferenceAnnotation(reference.value, props.annotations));
const issue = computed(() => {
  const field = questField(props.document, props.name);
  return field?.issue || (field?.tokens.some((t) => t.kind !== props.kind) ? '数据类型不匹配，请在 DSL 中编辑' : '') || (values.value.length > (props.width ?? 1) ? '参数数量无法识别，请在 DSL 中编辑' : '');
});
function update(index: number, value: string | number | null) {
  const next = [...values.value];
  if (value === null) return;
  while (next.length < (props.width ?? 1)) next.push('-1');
  next[index] = String(value);
  emit('change', { name: props.name, kind: props.kind, values: next });
}
</script>
<template>
  <div class="quest-field">
    <label>{{ label }}</label>
    <template v-if="!issue && reference && kind === 'string'">
      <NInput :value="annotation ? questAnnotationText(annotation) : ''" readonly :type="multiline ? 'textarea' : 'text'" :autosize="multiline ? { minRows: 3, maxRows: 8 } : false" :input-props="{ 'aria-label': label }" :placeholder="annotation?.placeholder?.missing ? '字符串表中尚无译文' : '请先保存任务以加载引用文本'" />
      <div class="reference-actions"><span>&lt;{{ reference.tableIndex }}::{{ reference.key }}&gt;</span><NButton size="small" :disabled="disabled || !annotation" @click="emit('edit-reference', name, label)">{{ annotation?.placeholder?.missing ? '创建译文' : '修改译文' }}</NButton></div>
      <small v-if="annotation?.placeholder?.fallback">当前显示语言覆盖层的译文</small>
    </template>
    <div v-else-if="!issue" class="field-inputs">
      <NSelect v-if="options" :value="values[0] ?? null" :options="options" filterable :disabled="disabled" :aria-label="label" :input-props="{ 'aria-label': label }" placeholder="未配置" @update:value="update(0, $event)" />
      <NInput v-else-if="kind === 'string'" :value="values[0] ?? ''" :type="multiline ? 'textarea' : 'text'" :autosize="multiline ? { minRows: 3, maxRows: 8 } : false" :disabled="disabled" :aria-label="label" :input-props="{ 'aria-label': label }" placeholder="未配置" @update:value="update(0, $event)" />
      <NInputNumber v-else v-for="i in (width ?? 1)" :key="i" :value="values[i - 1] === undefined ? null : Number(values[i - 1])" :disabled="disabled" :validator="(v: number) => Number.isInteger(v)" :min="-2147483648" :max="2147483647" :show-button="false" :update-value-on-input="true" :input-props="{ 'aria-label': `${label}${width ? i === 1 ? '下限' : '上限' : ''}` }" placeholder="未配置" @update:value="update(i - 1, $event)" />
    </div>
    <template v-else><div class="field-raw">{{ values.join(' ') }}</div><small>{{ issue }}</small></template>
  </div>
</template>
<style scoped>
.quest-field { display: grid; gap: 6px; min-width: 0; }
label { font-size: 12px; color: var(--pvf-text-secondary); }
.field-inputs { display: flex; gap: 8px; } .field-inputs > * { flex: 1; min-width: 0; }
small { color: var(--pvf-warning); } .field-raw { white-space: pre-wrap; overflow-wrap: anywhere; }
.reference-actions { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.reference-actions > span { color: var(--pvf-text-muted); font-size: 11px; overflow-wrap: anywhere; min-width: 0; }
</style>
