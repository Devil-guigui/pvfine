<script setup lang="ts">
import { computed } from 'vue';
import { NButton, NInputNumber, NSelect } from 'naive-ui';
import ItemPicker from '../ItemPicker.vue';
import QuestTargetPicker from './QuestTargetPicker.vue';
import { questField, questValues, type QuestDocument, type QuestChange, type QuestTargetKind } from '../../gui/quest';
const props = defineProps<{ document: QuestDocument; name: string; labels: string[]; repeat: boolean; disabled: boolean; items?: boolean; queries?: Record<number, QuestTargetKind> }>();
const emit = defineEmits<{ (e: 'change', change: QuestChange): void }>();
const values = computed(() => questValues(props.document, props.name));
const width = computed(() => Math.max(1, props.labels.length));
const difficulties = [{ label: '不限', value: '-1' }, { label: '普通', value: '0' }, { label: '冒险', value: '1' }, { label: '王者', value: '2' }, { label: '地狱', value: '3' }];
const rows = computed(() => Math.ceil(values.value.length / width.value));
const issue = computed(() => questField(props.document, props.name)?.issue || (questField(props.document, props.name)?.tokens.some((t) => t.kind !== 'integer') ? '数据类型不匹配，请在 DSL 中编辑' : ''));
function change(next: string[]) { emit('change', { name: props.name, values: next, kind: 'integer', paired: true }); }
function update(index: number, value: number | string | null) {
  if (value === null || value === '') return;
  const next = [...values.value];
  while (next.length <= index) next.push('-1');
  next[index] = String(value);
  change(next);
}
function add() { change([...values.value, ...Array(width.value).fill('-1')]); }
function remove(row: number) { change(values.value.filter((_, i) => i < row * width.value || i >= (row + 1) * width.value)); }
</script>
<template>
  <div class="quest-parameters">
    <template v-if="issue"><p class="warning">{{ issue }}</p><pre>{{ values.join(' ') }}</pre></template>
    <template v-else>
      <p v-if="values.length % width" class="warning">最后一组参数不完整，请补齐；原始数据已保留。</p>
      <div v-for="row in rows" :key="row" class="parameter-row">
        <div class="parameter-fields">
          <label v-for="(label, column) in labels" :key="column">
            <span>{{ label }}</span>
            <NSelect v-if="label === '难度'" :value="values[(row - 1) * width + column] ?? null" :options="difficulties" :disabled="disabled" :input-props="{ 'aria-label': `第 ${row} 组${label}` }" placeholder="缺少参数" @update:value="update((row - 1) * width + column, $event)" />
            <template v-else-if="(items && column === 0) || queries?.[column]">
              <NInputNumber :value="values[(row - 1) * width + column] === undefined ? null : Number(values[(row - 1) * width + column])" :disabled="disabled" :show-button="false" :validator="(v: number) => Number.isInteger(v)" :min="-2147483648" :max="2147483647" :update-value-on-input="true" :input-props="{ 'aria-label': `第 ${row} 组${label}` }" @update:value="update((row - 1) * width + column, $event)" />
              <ItemPicker v-if="items && column === 0" :model-value="values[(row - 1) * width + column] ?? ''" :disabled="disabled" @update:model-value="update((row - 1) * width + column, $event)" />
              <QuestTargetPicker v-else :kind="queries![column]!" :model-value="values[(row - 1) * width + column] ?? ''" :disabled="disabled" @update:model-value="update((row - 1) * width + column, $event)" />
            </template>
            <NInputNumber v-else :value="values[(row - 1) * width + column] === undefined ? null : Number(values[(row - 1) * width + column])" :disabled="disabled" :show-button="false" :validator="(v: number) => Number.isInteger(v)" :min="-2147483648" :max="2147483647" :update-value-on-input="true" :input-props="{ 'aria-label': `第 ${row} 组${label}` }" placeholder="缺少参数" @update:value="update((row - 1) * width + column, $event)" />
          </label>
        </div>
        <NButton size="small" :disabled="disabled" :aria-label="`删除第 ${row} 组`" @click="remove(row - 1)">删除</NButton>
      </div>
      <p v-if="!rows" class="empty">尚未配置参数</p>
      <NButton size="small" :disabled="disabled || (!repeat && rows > 0) || !!(values.length % width)" @click="add">添加一组</NButton>
    </template>
  </div>
</template>
<style scoped>
.quest-parameters { display: grid; gap: 10px; } .parameter-row { display: flex; align-items: start; gap: 10px; padding: 12px 0; border-bottom: 1px solid var(--pvf-border-subtle); }
.parameter-fields { display: grid; align-items: start; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; flex: 1; min-width: 0; }
label { display: grid; gap: 6px; min-width: 0; } label > span { color: var(--pvf-text-secondary); font-size: 12px; }
.parameter-row > .n-button { margin-top: 23px; } .quest-parameters > .n-button { justify-self: start; }
p { margin: 0; font-size: 12px; } .empty { color: var(--pvf-text-muted); } .warning { color: var(--pvf-warning); } pre { white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
