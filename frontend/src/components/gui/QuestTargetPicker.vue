<script setup lang="ts">
import { computed, h, onBeforeUnmount, ref, watch } from 'vue';
import { NButton, NSelect, type SelectOption } from 'naive-ui';
import { ArchiveService } from '../../../bindings/pvfine/services';
import type { SearchHit } from '../../../bindings/pvfine/services/models';
import type { QuestTargetKind } from '../../gui/quest';
import { useFileGUIStore } from '../../stores/fileGUI';

const props = defineProps<{ modelValue: string; kind: QuestTargetKind; disabled: boolean }>();
const emit = defineEmits<{ (e: 'update:modelValue', id: string): void }>();
const gui = useFileGUIStore();
const names: Record<QuestTargetKind, string> = { dungeon: '副本', monster: '怪物', npc: 'NPC' };
const targetName = computed(() => names[props.kind]);
const hits = ref<SearchHit[]>([]);
const selected = ref<SearchHit | null>(null);
const query = ref('');
const loading = ref(false);
const error = ref('');
const cursor = ref(-1);
let generation = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
const options = computed<SelectOption[]>(() => hits.value.map((hit) => ({ value: hit.id, label: `${hit.name || '未命名'} · ${hit.id}`, path: hit.path })));
function fallback(id: string | number): SelectOption {
  return { value: id, label: selected.value?.id === String(id) ? `${selected.value.name || '未命名'} · ${id}` : `${targetName.value} #${id}`, path: selected.value?.id === String(id) ? selected.value.path : undefined };
}
function renderLabel(option: SelectOption) { return h('span', { title: option.path as string | undefined }, String(option.label)); }
async function search(more = false) {
  const request = ++generation;
  const kind = props.kind;
  const searchQuery = query.value.trim();
  loading.value = true;
  error.value = '';
  if (!more) { hits.value = []; cursor.value = -1; }
  try {
    const result = await ArchiveService.SearchQuestTargets(kind, searchQuery, more ? cursor.value : 0, 30);
    if (request !== generation) return;
    const byID = new Map((more ? hits.value : []).map((hit) => [hit.id, hit]));
    for (const hit of result?.hits ?? []) if (hit && !byID.has(hit.id)) byID.set(hit.id, hit);
    hits.value = [...byID.values()];
    cursor.value = result?.nextCursor ?? -1;
  } catch (e) { if (request === generation) error.value = String(e); }
  finally { if (request === generation) loading.value = false; }
}
function onSearch(value: string) {
  query.value = value;
  generation++;
  clearTimeout(timer);
  hits.value = []; cursor.value = -1; error.value = '';
  loading.value = !!value.trim();
  if (value.trim()) timer = setTimeout(() => void search(), 180);
}
function select(id: string | null) {
  if (props.disabled || id === null) return;
  selected.value = hits.value.find((hit) => hit.id === id) ?? null;
  emit('update:modelValue', id);
}
watch(() => [props.kind, gui.epoch], () => {
  generation++; clearTimeout(timer);
  hits.value = []; selected.value = null; query.value = ''; loading.value = false; error.value = ''; cursor.value = -1;
});
watch(() => props.modelValue, (id) => { if (selected.value?.id !== id) selected.value = null; });
onBeforeUnmount(() => { generation++; clearTimeout(timer); });
</script>

<template>
  <NSelect :value="modelValue || null" :disabled="disabled" filterable remote
    :options="options" :loading="loading" :fallback-option="fallback" :render-label="renderLabel"
    :placeholder="`输入${targetName} ID、名称或路径检索`" :input-props="{ 'aria-label': `${targetName}查询` }"
    @search="onSearch" @update:value="select">
    <template #empty>{{ error || (loading ? '正在搜索…' : query ? `没有匹配的${targetName}` : '输入 ID、名称或路径开始搜索') }}</template>
    <template #action>
      <NButton v-if="error" text @mousedown.prevent @click="search()">重试搜索</NButton>
      <NButton v-else-if="cursor >= 0" text :loading="loading" @mousedown.prevent @click="search(true)">加载更多</NButton>
    </template>
  </NSelect>
</template>
