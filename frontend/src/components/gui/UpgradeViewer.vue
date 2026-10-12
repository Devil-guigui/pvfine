<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";
import { NButton, NButtonGroup, NCheckbox, NEmpty, NInputNumber, NModal, NSelect, NSlider, NSpin, useMessage } from "naive-ui";
import { FileGUIService } from "../../../bindings/pvfine/services";
import type { UpgradeDocument, UpgradeRow } from "../../../bindings/pvfine/services/models";
import type { GUIFile } from "../../gui/types";
import { editUpgrade, editUpgradeBatch, upgradeRange, type UpgradeChange, upgradeGroupLabels, upgradeRows, upgradeSuccessRate } from "../../gui/upgrade";
import { useEditorStore } from "../../stores/editor";
import { useFileGUIStore } from "../../stores/fileGUI";
import ItemPicker from "../ItemPicker.vue";

const props = defineProps<{ file: GUIFile; active: boolean }>();
const editor = useEditorStore();
const gui = useFileGUIStore();
const message = useMessage();
const document = ref<UpgradeDocument | null>(null);
const loading = ref(false);
const error = ref("");
const sourceText = ref("");
const group = ref<"T1" | "T2">("T1");
const hasT2 = computed(() => !!document.value?.rows?.[0]?.fieldsT2?.length);
const rows = computed(() => upgradeRows(document.value?.rows ?? [], hasT2.value ? group.value : "T1"));
let generation = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
const title = computed(() => props.file.path.toLowerCase().includes("amplifyupgrade") ? "增幅概率" : "强化概率");
const ready = computed(() => !loading.value && !error.value && sourceText.value === props.file.text);
const canEdit = computed(() => ready.value && props.file.editable && !editor.saving && !editor.guiApplying);
const dirty = computed(() => {
  const tab = editor.getFileDraft(props.file.index);
  return !!tab && tab.path === props.file.path && editor.isTextDirty(tab);
});
const operations = [{ label: "不处理", value: 1 }, { label: "降级", value: 2 }, { label: "损坏", value: 3 }];
const labels = ["成功率（%）", "失败操作", "降级等级", "消耗道具", "消耗数量"];
const edit = reactive({ show: false, column: 0, value: null as number | null, itemID: "", name: "", rows: [] as UpgradeRow[], text: "", index: -1, epoch: 0 });
const editTitle = computed(() => `${edit.rows.length > 1 ? '全部等级' : `+${edit.rows[0]?.level ?? 1}`} · ${hasT2.value && edit.column < 3 ? `${upgradeGroupLabels[group.value]} · ` : ''}${labels[edit.column]}`);

const batch = reactive({ show: false, range: [1, 1] as [number, number], rows: [] as UpgradeRow[], text: "", index: -1, epoch: 0,
  rateEnabled: false, rate: null as number | null, failureEnabled: false, failure: 1, downgrade: 0 as number | null,
  itemEnabled: false, itemID: "", countEnabled: false, count: null as number | null });
const batchTargets = computed(() => upgradeRange(batch.rows, batch.range));
const batchMax = computed(() => batch.rows.at(-1)?.level ?? 1);
const batchSelected = computed(() => batch.rateEnabled || batch.failureEnabled || batch.itemEnabled || batch.countEnabled);
function openBatch() {
  if (!canEdit.value || !rows.value.length) return;
  const first = rows.value[0]!;
  Object.assign(batch, { show: true, range: [1, rows.value.at(-1)!.level], rows: rows.value, text: props.file.text, index: props.file.index, epoch: gui.epoch,
    rateEnabled: false, rate: upgradeSuccessRate(first), failureEnabled: false, failure: first.fields?.[1]?.value ?? 1, downgrade: first.fields?.[2]?.value ?? 0,
    itemEnabled: false, itemID: String(first.fields?.[3]?.value ?? 0), countEnabled: false, count: first.fields?.[4]?.value ?? 0 });
}
function applyBatch() {
  if (!canEdit.value || !batchSelected.value) return;
  try {
    if (batch.text !== props.file.text || batch.index !== props.file.index || batch.epoch !== gui.epoch) throw new Error("文件已变化，请重新打开批量设置");
    const changes: UpgradeChange[] = [];
    const add = (column: number, value: number | null) => { if (value === null) throw new Error("请输入有效数值"); changes.push({ column, value }); };
    if (batch.rateEnabled) add(0, batch.rate);
    if (batch.failureEnabled) { add(1, batch.failure); if (batch.failure === 2) add(2, batch.downgrade); }
    if (batch.itemEnabled) add(3, batch.itemID.trim() ? Number(batch.itemID) : NaN);
    if (batch.countEnabled) add(4, batch.count);
    const text = editUpgradeBatch(batch.text, batchTargets.value, changes);
    editor.updateContent(props.file.index, text);
    batch.show = false;
    message.success(`已更新 ${batchTargets.value.length} 个等级的草稿`);
  } catch (cause) { message.error(String(cause)); }
}

async function load() {
  const request = ++generation;
  const epoch = gui.epoch;
  const text = props.file.text;
  const index = props.file.index;
  loading.value = true;
  error.value = "";
  try {
    const result = await FileGUIService.ReadUpgrade(index, text);
    if (request !== generation || epoch !== gui.epoch || index !== props.file.index || text !== props.file.text) return;
    document.value = result;
    sourceText.value = text;
  } catch (cause) {
    if (request === generation && epoch === gui.epoch) { error.value = String(cause); document.value = null; }
  } finally { if (request === generation) loading.value = false; }
}
watch(() => [props.file.index, props.file.text, gui.epoch, gui.revision, props.active], () => {
  generation++;
  clearTimeout(timer);
  edit.show = false;
  batch.show = false;
  if (props.active) timer = setTimeout(load, 80);
}, { immediate: true });
watch(() => gui.epoch, () => { document.value = null; });
watch(group, () => { edit.show = false; batch.show = false; });
onBeforeUnmount(() => { generation++; clearTimeout(timer); });

function openEdit(column: number, row?: UpgradeRow) {
  if (!canEdit.value) return;
  const targets = row ? [row] : rows.value;
  if (!targets.length) return;
  const first = targets[0]!;
  Object.assign(edit, { show: true, column, rows: targets, value: column === 0 ? upgradeSuccessRate(first) : first.fields?.[column]!.value,
    itemID: String(first.fields?.[3]!.value), name: first.costName, text: props.file.text, index: props.file.index, epoch: gui.epoch });
}
function apply() {
  if (!canEdit.value) return;
  try {
    if (edit.text !== props.file.text || edit.index !== props.file.index || edit.epoch !== gui.epoch) throw new Error("文件已变化，请重新打开表单");
    const value = edit.column === 3 ? (edit.itemID.trim() ? Number(edit.itemID) : NaN) : edit.value;
    if (value === null) throw new Error("请输入有效数值");
    const text = editUpgrade(edit.text, edit.rows, edit.column, value);
    editor.updateContent(props.file.index, text);
    edit.show = false;
    message.success("已更新草稿，保存后写入工作区");
  } catch (cause) { message.error(String(cause)); }
}
async function save() {
  if (!canEdit.value || !dirty.value || edit.show || batch.show) return;
  const epoch = gui.epoch;
  try {
    const saved = await editor.saveFileTab(props.file.index);
    if (saved && epoch === gui.epoch) message.success("已保存到工作区；保存 PVF 后写入归档文件");
  } catch (cause) { if (epoch === gui.epoch) message.error(String(cause)); }
}
</script>

<template>
  <div class="upgrade-viewer">
    <header class="upgrade-header">
      <div class="upgrade-heading"><h2>{{ title }}</h2><span class="upgrade-count">{{ rows.length }} 个等级</span></div>
      <div class="upgrade-actions">
        <NButton size="small" :disabled="!canEdit || edit.show || batch.show" @click="openBatch">批量设置</NButton>
        <NButton size="small" type="primary" :loading="editor.saving" :disabled="!canEdit || !dirty || edit.show || batch.show" @click="save">保存{{ title.startsWith('增幅') ? '增幅' : '强化' }}</NButton>
      </div>
    </header>
    <div v-if="hasT2" class="upgrade-group-bar">
      <NButtonGroup size="small" aria-label="装备类型">
        <NButton v-for="g in (['T1', 'T2'] as const)" :key="g" :type="group === g ? 'primary' : 'default'" :aria-pressed="group === g" :disabled="loading || edit.show || batch.show" @click="group = g">{{ upgradeGroupLabels[g] }}</NButton>
      </NButtonGroup>
      <span class="upgrade-hint">武器与防具的概率、失败处理独立设置，材料消耗共用</span>
    </div>
    <div v-if="error" class="error" role="alert">{{ error }} <NButton size="small" @click="load">重新加载</NButton></div>
    <NSpin :show="loading">
      <div v-if="document?.rows?.length" class="table-scroll">
        <table class="upgrade-table">
          <colgroup><col class="level-column" /><col class="rate-column" /><col class="operation-column" /><col class="downgrade-column" /><col /><col class="count-column" /></colgroup>
          <thead><tr><th>等级</th><th v-for="label in labels" :key="label">{{ label }}</th></tr></thead>
          <tbody><tr v-for="row in rows" :key="row.level">
            <th>+{{ row.level }}</th>
            <td><button class="upgrade-cell" :disabled="!canEdit" @click="openEdit(0, row)">{{ upgradeSuccessRate(row) }}%</button></td>
            <td><button class="upgrade-cell" :disabled="!canEdit" @click="openEdit(1, row)">{{ operations.find(option => option.value === row.fields?.[1]?.value)?.label }}</button></td>
            <td><button class="upgrade-cell" :disabled="!canEdit || row.fields?.[1]?.value !== 2" @click="openEdit(2, row)">{{ row.fields?.[1]?.value === 2 ? row.fields?.[2]?.value : '—' }}</button></td>
            <td><button class="upgrade-cell upgrade-material" :disabled="!canEdit" @click="openEdit(3, row)"><span>{{ row.costName || '物品' }}</span><small>#{{ row.fields?.[3]?.value }}</small></button></td>
            <td><button class="upgrade-cell" :disabled="!canEdit" @click="openEdit(4, row)">{{ row.fields?.[4]?.value }}</button></td>
          </tr></tbody>
        </table>
      </div>
      <NEmpty v-else-if="!loading && !error" description="没有可编辑的强化/增幅数据" />
    </NSpin>
    <NModal v-model:show="batch.show" preset="card" :title="`批量设置 · ${title} · ${upgradeGroupLabels[group]}`" style="width: 580px; max-width: 95vw" :mask-closable="false">
      <div class="upgrade-batch-form">
        <section class="upgrade-range">
          <div class="upgrade-range-heading"><strong>等级范围</strong><span>+{{ batch.range[0] }} ～ +{{ batch.range[1] }} · {{ batchTargets.length }} 个等级</span></div>
          <NSlider v-model:value="batch.range" range :min="1" :max="batchMax" :step="1" :disabled="!canEdit || batchMax === 1" />
          <div class="upgrade-range-inputs">
            <NInputNumber :value="batch.range[0]" :min="1" :max="batch.range[1]" :precision="0" :disabled="!canEdit" aria-label="起始等级" @update:value="batch.range[0] = $event ?? 1" />
            <span>至</span>
            <NInputNumber :value="batch.range[1]" :min="batch.range[0]" :max="batchMax" :precision="0" :disabled="!canEdit" aria-label="结束等级" @update:value="batch.range[1] = $event ?? batchMax" />
          </div>
        </section>
        <div class="upgrade-batch-row">
          <NCheckbox v-model:checked="batch.rateEnabled" :disabled="!canEdit">成功率</NCheckbox>
          <NInputNumber v-model:value="batch.rate" :min="0" :max="100" :precision="3" :disabled="!canEdit || !batch.rateEnabled"><template #suffix>%</template></NInputNumber>
        </div>
        <div class="upgrade-batch-row">
          <NCheckbox v-model:checked="batch.failureEnabled" :disabled="!canEdit">失败操作</NCheckbox>
          <div class="upgrade-batch-failure"><NSelect v-model:value="batch.failure" :options="operations" :disabled="!canEdit || !batch.failureEnabled" /><label>降级等级<NInputNumber v-model:value="batch.downgrade" :min="0" :max="hasT2 ? 2147483647 : 31" :precision="0" :disabled="!canEdit || !batch.failureEnabled || batch.failure !== 2" /></label></div>
        </div>
        <div class="upgrade-batch-row">
          <NCheckbox v-model:checked="batch.itemEnabled" :disabled="!canEdit">消耗道具</NCheckbox>
          <div class="upgrade-batch-item"><NInputNumber :value="batch.itemID.trim() ? Number(batch.itemID) : null" :min="0" :max="2147483647" :precision="0" :disabled="!canEdit || !batch.itemEnabled" placeholder="道具 ID" @update:value="batch.itemID = $event === null ? '' : String($event)" /><ItemPicker v-model="batch.itemID" :disabled="!canEdit || !batch.itemEnabled" /></div>
        </div>
        <div class="upgrade-batch-row">
          <NCheckbox v-model:checked="batch.countEnabled" :disabled="!canEdit">消耗数量</NCheckbox>
          <NInputNumber v-model:value="batch.count" :min="0" :max="2147483647" :precision="0" :disabled="!canEdit || !batch.countEnabled" />
        </div>
        <p class="upgrade-hint">只修改勾选项。概率与失败处理作用于当前{{ upgradeGroupLabels[group] }}；道具与数量由武器、防具共用。</p>
        <div class="upgrade-dialog-actions"><NButton @click="batch.show = false">取消</NButton><NButton type="primary" :disabled="!canEdit || !batchSelected || !batchTargets.length" @click="applyBatch">应用到 {{ batchTargets.length }} 个等级</NButton></div>
      </div>
    </NModal>
    <NModal v-model:show="edit.show" preset="card" :title="editTitle" style="width: 470px; max-width: 95vw" :mask-closable="false">
      <div class="edit-form">
        <template v-if="edit.column === 3">
          <label>道具 ID<NInputNumber :value="edit.itemID.trim() ? Number(edit.itemID) : null" :min="0" :max="2147483647" :precision="0" :disabled="!canEdit" @update:value="edit.itemID = $event === null ? '' : String($event); edit.name = ''" /></label>
          <ItemPicker v-model="edit.itemID" :label="edit.name" :disabled="!canEdit" @select="edit.name = $event?.name ?? ''" />
          <p v-if="edit.rows.length > 1">将替换 {{ edit.rows.length }} 个等级的消耗道具，数量保持不变。</p>
        </template>
        <NSelect v-else-if="edit.column === 1" v-model:value="edit.value" :options="operations" :disabled="!canEdit" />
        <NInputNumber v-else v-model:value="edit.value" :min="0" :max="edit.column === 0 ? 100 : edit.column === 2 && !hasT2 ? 31 : 2147483647" :precision="edit.column === 0 ? 3 : 0" :disabled="!canEdit" />
        <p v-if="edit.column === 1">选择不处理或损坏时，降级参数自动清零。</p>
        <div class="upgrade-dialog-actions"><NButton @click="edit.show = false">取消</NButton><NButton type="primary" :disabled="!canEdit" @click="apply">应用到草稿</NButton></div>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.upgrade-viewer { min-width: 0; width: 100%; color: var(--pvf-text-primary); font-size: 13px; }
.upgrade-header { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; padding: 4px 0 14px; border-bottom: 1px solid var(--pvf-border-subtle); }
.upgrade-heading { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
.upgrade-heading h2 { margin: 0; font-size: 16px; line-height: 1.5; font-weight: 600; white-space: nowrap; }
.upgrade-count, .upgrade-hint { color: var(--pvf-text-secondary); font-size: 12px; }
.upgrade-count { white-space: nowrap; }
.upgrade-actions, .upgrade-dialog-actions { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
.upgrade-actions { margin-left: auto; }
.upgrade-dialog-actions { justify-content: flex-end; }
.upgrade-group-bar { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 12px 0; }
.table-scroll { overflow: auto; border: 1px solid var(--pvf-border-subtle); border-radius: 4px; max-height: 70vh; margin-top: 12px; }
.upgrade-group-bar + .error + .n-spin .table-scroll, .upgrade-group-bar + .n-spin .table-scroll { margin-top: 0; }
.upgrade-table { width: 100%; min-width: 650px; table-layout: fixed; border-collapse: separate; border-spacing: 0; font-size: 13px; font-variant-numeric: tabular-nums; }
.level-column { width: 65px; } .rate-column { width: 120px; } .operation-column { width: 110px; } .downgrade-column { width: 100px; } .count-column { width: 100px; }
.upgrade-table th, .upgrade-table td { border-bottom: 1px solid var(--pvf-border-subtle); text-align: left; }
.upgrade-table thead th { position: sticky; top: 0; z-index: 1; background: var(--pvf-surface-elevated); padding: 10px 12px; font-size: 12px; font-weight: 500; white-space: nowrap; color: var(--pvf-text-secondary); }
.upgrade-table tbody th { padding: 8px 12px; font-weight: 500; }
.upgrade-table tbody tr:last-child th, .upgrade-table tbody tr:last-child td { border-bottom: none; }
.upgrade-table tbody tr:hover { background: var(--pvf-surface-hover); }
.upgrade-cell { display: block; border: none; background: transparent; color: inherit; font: inherit; line-height: 20px; padding: 7px 12px; width: 100%; text-align: left; cursor: pointer; }
.upgrade-cell:hover:enabled { color: var(--pvf-primary); background: var(--pvf-primary-soft); }
.upgrade-cell:disabled { cursor: default; color: var(--pvf-text-secondary); }
.upgrade-cell:focus-visible { outline: 2px solid var(--pvf-primary); outline-offset: -2px; }
.upgrade-material { display: flex; align-items: baseline; gap: 8px; }
.upgrade-material span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.upgrade-material small { color: var(--pvf-text-secondary); font-size: 12px; white-space: nowrap; }
.error { padding: 10px; margin: 12px 0; background: var(--pvf-surface-warning); font-size: 13px; }
.edit-form { display: grid; gap: 14px; }
.edit-form label { display: grid; gap: 6px; }
.edit-form p { margin: 0; color: var(--pvf-text-secondary); font-size: 12px; line-height: 1.6; }
.upgrade-batch-form { display: grid; gap: 16px; }
.upgrade-range { padding: 12px 14px; background: var(--pvf-surface-inset); border-radius: 4px; }
.upgrade-range-heading { display: flex; justify-content: space-between; gap: 12px; margin-bottom: 14px; font-size: 13px; }
.upgrade-range-heading span { color: var(--pvf-text-secondary); font-size: 12px; }
.upgrade-range-inputs { display: flex; align-items: center; gap: 10px; margin-top: 12px; }
.upgrade-range-inputs .n-input-number { flex: 1; min-width: 0; }
.upgrade-batch-row { display: grid; grid-template-columns: 100px minmax(0, 1fr); gap: 12px; align-items: start; }
.upgrade-batch-row > .n-checkbox { margin-top: 6px; }
.upgrade-batch-failure, .upgrade-batch-item { display: grid; gap: 8px; }
.upgrade-batch-failure label { display: grid; grid-template-columns: 70px minmax(0, 1fr); gap: 8px; align-items: center; font-size: 12px; color: var(--pvf-text-secondary); }
.upgrade-batch-form p { margin: 0; line-height: 1.6; }
@media (max-width: 550px) { .upgrade-header { align-items: flex-start; } .upgrade-actions { width: 100%; margin-left: 0; justify-content: flex-end; } }
</style>
