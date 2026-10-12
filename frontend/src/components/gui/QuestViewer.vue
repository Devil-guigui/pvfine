<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue';
import {
  NAlert,
  NButton,
  NCollapse,
  NCollapseItem,
  NIcon,
  NInput,
  NModal,
  NSelect,
  NTabPane,
  NTabs,
  NTag,
  useMessage,
} from 'naive-ui';
import {
  ChatMultiple24Regular,
  Code24Regular,
  DocumentText24Regular,
  Gift24Regular,
  Info24Regular,
  Save24Regular,
  Settings24Regular,
  Target24Regular,
} from '@vicons/fluent';
import type { GUIFile } from '../../gui/types';
import {
  editQuest,
  parseQuest,
  questConditions,
  questRewards,
  questGrades,
  questField,
  questDisplayValue,
  questReferenceAnnotation,
  questAnnotationText,
  type QuestChange,
} from '../../gui/quest';
import { skillJobNames } from '../../gui/skill';
import { useEditorStore } from '../../stores/editor';
import { useFileGUIStore } from '../../stores/fileGUI';
import QuestField from './QuestField.vue';
import QuestParameters from './QuestParameters.vue';

const props = defineProps<{ file: GUIFile; active: boolean; paneId?: string }>();
const emit = defineEmits<{ (e: 'close'): void }>();
const editor = useEditorStore();
const gui = useFileGUIStore();
const message = useMessage();

const referenceEdit = reactive({
  show: false,
  saving: false,
  name: '',
  label: '',
  text: '',
  source: '',
  tableIndex: 0,
  key: '',
  fallback: false,
  missing: false,
  index: -1,
  path: '',
  epoch: 0,
});
const unregisterReferenceDraft = editor.registerGUIDirtyChecker(props.file.index, () => referenceEdit.show);
onBeforeUnmount(unregisterReferenceDraft);

const parsed = computed(() => {
  try {
    return { document: parseQuest(props.file.text), error: '' };
  } catch (e) {
    return { document: { sections: [] }, error: String(e) };
  }
});
const document = computed(() => parsed.value.document);
const disabled = computed(
  () =>
    !props.active ||
    !props.file.editable ||
    editor.saving ||
    editor.guiApplying ||
    referenceEdit.saving ||
    !!parsed.value.error,
);
const dirty = computed(() => {
  const tab = editor.getFileDraft(props.file.index);
  return !!tab && tab.path === props.file.path && editor.isTextDirty(tab);
});
const annotations = computed(() => editor.getFileDraft(props.file.index)?.annotations ?? []);
const value = (name: string) => questDisplayValue(document.value, name, annotations.value);

const referenceOptions = computed(() =>
  basicFields.find((f) => f.name === referenceEdit.name)?.options ??
  (referenceEdit.name === 'reward type'
    ? questRewards
    : referenceEdit.name === 'type'
    ? [
        ...new Map(
          questConditions.map((c) => [
            c.type,
            { value: c.type, label: c.type === '[condition under clear]' ? '通关条件' : c.label },
          ]),
        ).values(),
      ]
    : undefined),
);

const condition = computed(() =>
  questConditions.find((c) => c.type === value('type') && String(c.subType) === value('sub type')),
);
const reward = computed(() => questRewards.find((r) => r.value === value('reward type')));

const jobs = [
  { label: '不限职业', value: '[all]' },
  ...Object.entries(skillJobNames)
    .filter(([key]) => key !== 'common')
    .map(([key, label]) => ({ value: `[${key}]`, label })),
];
const difficulties = ['S', 'A', 'B', 'C', 'M', 'Z', 'Y', 'X', 'W', 'O', 'E', 'F', 'G', 'D', 'U'].map((v) => ({
  label: v,
  value: v,
}));

const basicFields = [
  { name: 'name', label: '任务名称', kind: 'string' as const },
  { name: 'grade', label: '任务类型', kind: 'string' as const, options: questGrades },
  { name: 'difficulty', label: '任务难度', kind: 'string' as const, options: difficulties },
  { name: 'npc index', label: '发布 NPC ID', kind: 'integer' as const },
  { name: 'complete npc index', label: '完成 NPC ID', kind: 'integer' as const },
  { name: 'job', label: '职业限制', kind: 'string' as const, options: jobs },
  { name: 'grow type', label: '转职限制（-1 为不限）', kind: 'integer' as const },
  { name: 'level', label: '等级限制（下限 / 上限）', kind: 'integer' as const, width: 2 },
];

const dialogues = [
  { name: 'depend message', label: '接受对话' },
  { name: 'condition message', label: '任务提示' },
  { name: 'solve message', label: '完成对话' },
];

const known = new Set([
  ...basicFields.map((f) => f.name),
  ...dialogues.map((f) => f.name),
  'type',
  'sub type',
  'int data',
  'reward type',
  'reward int data',
  'reward selection int data',
  'quest point',
]);

const otherSections = computed(() => document.value.sections.filter((s) => !known.has(s.name)));

// UI 增强状态
const activeDialogueTab = ref('depend message');
const dialogueLayout = ref<'tabs' | 'list'>('tabs');

const gradeLabel = computed(() => {
  const g = value('grade');
  return questGrades.find((item) => item.value === g)?.label || g || '';
});

const difficultyLabel = computed(() => value('difficulty'));

function apply(...changes: QuestChange[]) {
  if (disabled.value) return;
  try {
    const draft = editor.getFileDraft(props.file.index);
    if (!draft || draft.path !== props.file.path || draft.text !== props.file.text)
      throw new Error('任务草稿已变化，请重新编辑');
    editor.updateContent(props.file.index, editQuest(draft.text, changes));
  } catch (e) {
    message.error(String(e));
  }
}

function openReference(name: string, label: string) {
  if (disabled.value) return;
  const token = questField(document.value, name)?.tokens[0];
  const ref = token?.reference;
  const annotation = ref && questReferenceAnnotation(ref, annotations.value);
  if (!token || !ref || !annotation) return;
  Object.assign(referenceEdit, {
    show: true,
    name,
    label,
    text: questAnnotationText(annotation),
    source: token.value,
    ...ref,
    fallback: !!annotation.placeholder?.fallback,
    missing: !!annotation.placeholder?.missing,
    index: props.file.index,
    path: props.file.path,
    epoch: gui.epoch,
  });
}

async function saveReference() {
  if (disabled.value) return;
  if (!referenceEdit.text.trim()) {
    message.warning('译文不能为空');
    return;
  }
  const epoch = referenceEdit.epoch;
  if (
    epoch !== gui.epoch ||
    props.file.index !== referenceEdit.index ||
    props.file.path !== referenceEdit.path ||
    questField(document.value, referenceEdit.name)?.tokens[0]?.value !== referenceEdit.source
  ) {
    message.warning('任务引用已变化，请重新打开译文编辑');
    return;
  }
  referenceEdit.saving = true;
  try {
    await editor.setPlaceholderText(
      referenceEdit.index,
      referenceEdit.tableIndex,
      referenceEdit.key,
      referenceEdit.text,
    );
    if (epoch !== gui.epoch) return;
    referenceEdit.show = false;
    message.success('已更新字符串表，保存 PVF 后落盘');
  } catch (e) {
    if (epoch === gui.epoch) message.error(`修改译文失败：${String(e)}`);
  } finally {
    referenceEdit.saving = false;
  }
}

watch(
  () => [props.file.index, props.file.path, props.active, gui.epoch],
  () => {
    referenceEdit.show = false;
  },
);

function setCondition(key: string) {
  const selected = questConditions.find((c) => c.value === key);
  if (!selected) return;
  apply(
    { name: 'type', values: [selected.type], kind: 'string' },
    { name: 'sub type', values: [String(selected.subType)], kind: 'integer' },
  );
}

const conditionDisabled = computed(
  () =>
    disabled.value ||
    ['type', 'sub type'].some((name) => {
      const field = questField(document.value, name);
      return (
        !!field?.issue ||
        (field?.tokens.length ?? 0) > 1 ||
        field?.tokens.some((t) => t.kind !== (name === 'type' ? 'string' : 'integer'))
      );
    }),
);

async function save() {
  if (disabled.value || !dirty.value || referenceEdit.show) return;
  try {
    if (await editor.saveFileTab(props.file.index)) message.success('已保存任务到工作区');
  } catch (e) {
    message.error(`保存任务失败：${String(e)}`);
  }
}
</script>

<template>
  <section class="quest-viewer" aria-label="任务编辑器">
    <!-- 顶部工具栏 -->
    <header class="quest-header">
      <div class="header-main">
        <div class="header-title-row">
          <div class="quest-title-icon" aria-hidden="true">
            <NIcon :size="20">
              <DocumentText24Regular />
            </NIcon>
          </div>
          <h2 class="quest-title" :title="value('name') || '任务编辑器'">
            {{ value('name') || '任务编辑器' }}
          </h2>
          <div class="header-tags">
            <NTag v-if="gradeLabel" size="small" :bordered="false" class="meta-tag tag-grade">
              {{ gradeLabel }}
            </NTag>
            <NTag v-if="difficultyLabel" size="small" :bordered="false" class="meta-tag tag-diff">
              难度 {{ difficultyLabel }}
            </NTag>
            <NTag v-if="dirty" size="small" type="warning" :bordered="false" class="meta-tag">
              有未保存修改
            </NTag>
            <NTag v-else-if="!file.editable" size="small" :bordered="false" class="meta-tag tag-readonly">
              只读
            </NTag>
          </div>
        </div>
        <div class="header-subtitle">
          <span class="file-path" :title="file.path">{{ file.path }}</span>
          <span class="subtitle-sep" aria-hidden="true">·</span>
          <span class="status-tip">
            {{ file.editable ? (dirty ? '当前任务草稿包含未保存的修改' : '编辑任务基础属性、完成条件、奖励与剧情对话') : '当前归档处于只读状态' }}
          </span>
        </div>
      </div>
      <div class="header-actions">
        <NButton secondary size="medium" @click="emit('close')">
          <template #icon>
            <NIcon><Code24Regular /></NIcon>
          </template>
          查看 DSL
        </NButton>
        <NButton
          type="primary"
          size="medium"
          :loading="editor.saving"
          :disabled="disabled || !dirty || referenceEdit.show"
          @click="save"
        >
          <template #icon>
            <NIcon><Save24Regular /></NIcon>
          </template>
          保存任务
        </NButton>
      </div>
    </header>

    <!-- 解析错误警告 -->
    <NAlert v-if="parsed.error" type="error" title="任务解析错误" class="quest-alert" role="alert">
      {{ parsed.error }}
    </NAlert>

    <!-- 主工作区 -->
    <div v-else class="quest-workspace">
      <!-- 左列：基本信息 + 剧情对话 -->
      <div class="workspace-column">
        <!-- 任务基本信息卡片 -->
        <section class="quest-card">
          <div class="card-header">
            <div class="card-header-left">
              <span class="card-header-icon" aria-hidden="true">
                <NIcon :size="18"><Info24Regular /></NIcon>
              </span>
              <div>
                <h3 class="card-title">基本信息</h3>
                <span class="card-subtitle">名称、NPC、职业与等级限制</span>
              </div>
            </div>
          </div>
          <div class="card-body">
            <div class="basic-fields">
              <QuestField
                v-for="field in basicFields"
                :key="field.name"
                :annotations="annotations"
                @edit-reference="openReference"
                v-bind="field"
                :document="document"
                :disabled="disabled"
                @change="apply($event)"
              />
            </div>
          </div>
        </section>

        <!-- 任务剧情对话卡片 -->
        <section class="quest-card">
          <div class="card-header">
            <div class="card-header-left">
              <span class="card-header-icon" aria-hidden="true">
                <NIcon :size="18"><ChatMultiple24Regular /></NIcon>
              </span>
              <div>
                <h3 class="card-title">剧情与对话</h3>
                <span class="card-subtitle">接取、进行与完成时的 NPC 剧情文本</span>
              </div>
            </div>
            <div class="card-header-right">
              <div class="view-mode-toggle" role="radiogroup" aria-label="对话展示方式">
                <button
                  type="button"
                  class="toggle-btn"
                  :class="{ active: dialogueLayout === 'tabs' }"
                  title="分页模式"
                  @click="dialogueLayout = 'tabs'"
                >
                  分页
                </button>
                <button
                  type="button"
                  class="toggle-btn"
                  :class="{ active: dialogueLayout === 'list' }"
                  title="平铺全部"
                  @click="dialogueLayout = 'list'"
                >
                  平铺
                </button>
              </div>
            </div>
          </div>
          <div class="card-body">
            <!-- 分页模式 -->
            <div v-if="dialogueLayout === 'tabs'" class="dialogue-tabs-container">
              <NTabs v-model:value="activeDialogueTab" type="segment" size="small">
                <NTabPane
                  v-for="field in dialogues"
                  :key="field.name"
                  :name="field.name"
                  :tab="field.label"
                >
                  <div class="dialogue-pane-content">
                    <QuestField
                      :annotations="annotations"
                      @edit-reference="openReference"
                      v-bind="field"
                      kind="string"
                      multiline
                      :document="document"
                      :disabled="disabled"
                      @change="apply($event)"
                    />
                  </div>
                </NTabPane>
              </NTabs>
            </div>
            <!-- 平铺模式 -->
            <div v-else class="dialogue-list-container">
              <div v-for="field in dialogues" :key="field.name" class="dialogue-list-item">
                <QuestField
                  :annotations="annotations"
                  @edit-reference="openReference"
                  v-bind="field"
                  kind="string"
                  multiline
                  :document="document"
                  :disabled="disabled"
                  @change="apply($event)"
                />
              </div>
            </div>
          </div>
        </section>
      </div>

      <!-- 右列：完成条件 + 任务奖励 -->
      <div class="workspace-column">
        <!-- 完成条件卡片 -->
        <section class="quest-card">
          <div class="card-header">
            <div class="card-header-left">
              <span class="card-header-icon" aria-hidden="true">
                <NIcon :size="18"><Target24Regular /></NIcon>
              </span>
              <div>
                <h3 class="card-title">完成条件</h3>
                <span class="card-subtitle">{{ condition ? condition.label : (value('type') || '配置任务目标与要求') }}</span>
              </div>
            </div>
            <div class="card-header-right">
              <NTag v-if="condition" size="small" :bordered="false" type="info">
                {{ condition.label }}
              </NTag>
            </div>
          </div>
          <div class="card-body">
            <template v-if="questField(document, 'type')?.tokens[0]?.reference">
              <div class="condition-reference-grid">
                <QuestField
                  :annotations="annotations"
                  @edit-reference="openReference"
                  :document="document"
                  name="type"
                  label="条件类型"
                  kind="string"
                  :disabled="disabled"
                />
                <QuestField
                  :annotations="annotations"
                  @edit-reference="openReference"
                  :document="document"
                  name="sub type"
                  label="条件子类型"
                  kind="integer"
                  :disabled="disabled"
                  @change="apply($event)"
                />
              </div>
            </template>
            <div v-else class="type-field-group">
              <label class="field-label">
                <span>条件类型</span>
                <span v-if="condition" class="field-label-desc">{{ condition.type }} (sub: {{ condition.subType }})</span>
              </label>
              <NSelect
                :value="condition?.value ?? null"
                :options="questConditions.map(({ value, label }) => ({ value, label }))"
                :disabled="conditionDisabled"
                :placeholder="value('type') || '选择完成条件'"
                @update:value="setCondition"
              />
            </div>

            <!-- 提示与状态信息 -->
            <div class="callout callout-info">
              <span class="callout-icon" aria-hidden="true">💡</span>
              <div class="callout-body">
                <div>切换类型会保留现有参数，请按新条件核对。</div>
                <div class="callout-sub">副本难度：-1 不限 · 0 普通 · 1 冒险 · 2 王者 · 3 地狱</div>
              </div>
            </div>
            <div v-if="!condition" class="callout callout-warning">
              <span class="callout-icon" aria-hidden="true">⚠️</span>
              <div class="callout-body">
                {{ value('type') ? `未识别的条件：${value('type')}（子类型：${value('sub type')}）` : '未配置条件' }}，可直接编辑下方原始整数参数。
              </div>
            </div>
            <div v-if="conditionDisabled && !disabled" class="callout callout-error">
              <span class="callout-icon" aria-hidden="true">⛔</span>
              <div class="callout-body">
                条件类型或子类型包含特殊数据，请在 DSL 视图中修改类型。
              </div>
            </div>

            <!-- 参数列表 -->
            <div class="params-section">
              <div class="params-header">
                <span class="params-title">条件参数明细</span>
                <span class="params-meta">{{ (condition?.fields ?? ['原始参数']).join(' / ') }}</span>
              </div>
              <QuestParameters
                :document="document"
                name="int data"
                :labels="condition?.fields ?? ['原始参数']"
                :repeat="condition?.repeat ?? !condition"
                :items="condition?.value === 'seeking'"
                :queries="condition?.queries"
                :disabled="disabled"
                @change="apply($event)"
              />
            </div>
          </div>
        </section>

        <!-- 任务奖励卡片 -->
        <section class="quest-card">
          <div class="card-header">
            <div class="card-header-left">
              <span class="card-header-icon" aria-hidden="true">
                <NIcon :size="18"><Gift24Regular /></NIcon>
              </span>
              <div>
                <h3 class="card-title">任务奖励</h3>
                <span class="card-subtitle">{{ reward ? reward.label : (value('reward type') || '固定与自选奖励') }}</span>
              </div>
            </div>
            <div class="card-header-right">
              <NTag v-if="reward" size="small" :bordered="false" type="success">
                {{ reward.label }}
              </NTag>
            </div>
          </div>
          <div class="card-body">
            <div class="reward-type-group">
              <QuestField
                :annotations="annotations"
                @edit-reference="openReference"
                :document="document"
                name="reward type"
                label="奖励类型"
                kind="string"
                :options="questRewards"
                :disabled="disabled"
                @change="apply($event)"
              />
            </div>

            <div class="callout callout-info">
              <span class="callout-icon" aria-hidden="true">💡</span>
              <div class="callout-body">
                切换奖励类型会保留现有参数，请核对 ID 和数量。
              </div>
            </div>

            <!-- 奖励参数 -->
            <div class="params-section">
              <div class="params-header">
                <span class="params-title">奖励内容</span>
                <span class="params-meta">{{ (reward?.fields ?? ['原始参数']).join(' / ') }}</span>
              </div>
              <QuestParameters
                :document="document"
                name="reward int data"
                :labels="reward?.fields ?? ['原始参数']"
                :repeat="reward?.repeat ?? !reward"
                :items="reward?.value === '[item]'"
                :disabled="disabled"
                @change="apply($event)"
              />
            </div>

            <!-- QP 点数奖励 -->
            <div class="quest-points-box">
              <QuestField
                :annotations="annotations"
                @edit-reference="openReference"
                :document="document"
                name="quest point"
                label="QP 奖励 (quest point)"
                kind="integer"
                :disabled="disabled"
                @change="apply($event)"
              />
            </div>

            <!-- 可选奖励 -->
            <div
              v-if="reward?.value === '[item]' || questField(document, 'reward selection int data')"
              class="optional-reward-box"
            >
              <div class="optional-reward-title">
                <span class="optional-title-text">🎁 可选奖励（自选其一）</span>
                <span class="optional-title-desc">玩家可在完成任务时自选其中一项</span>
              </div>
              <QuestParameters
                :document="document"
                name="reward selection int data"
                :labels="['物品 ID', '数量']"
                repeat
                items
                :disabled="disabled"
                @change="apply($event)"
              />
            </div>
          </div>
        </section>
      </div>
    </div>

    <!-- 底部其他配置（全宽） -->
    <section v-if="otherSections.length" class="quest-card other-card">
      <NCollapse>
        <NCollapseItem name="other-sections">
          <template #header>
            <div class="other-collapse-header">
              <NIcon class="other-icon"><Settings24Regular /></NIcon>
              <span class="other-title">其他扩展配置</span>
              <NTag size="small" round :bordered="false" class="other-count-tag">
                {{ otherSections.length }} 个字段
              </NTag>
            </div>
          </template>
          <div class="other-content">
            <div class="callout callout-info other-callout">
              <span class="callout-icon" aria-hidden="true">ℹ️</span>
              <div class="callout-body">
                包含未在标准结构中的自定义字段。整数和文本可在此直接编辑，复杂嵌套或特殊类型请在 DSL 视图中编辑。
              </div>
            </div>
            <div class="other-fields-grid">
              <div
                v-for="section in otherSections"
                :key="section.name + section.start"
                class="other-section-item"
              >
                <div class="other-item-header">
                  <span class="other-section-tag">[{{ section.name }}]</span>
                  <span v-if="section.issue" class="other-issue-badge">解析异常</span>
                </div>
                <div class="other-item-body">
                  <template v-if="!section.issue && section.tokens.every((t) => t.kind === 'integer')">
                    <QuestParameters
                      :document="document"
                      :name="section.name"
                      :labels="['原始参数']"
                      repeat
                      :disabled="disabled"
                      @change="apply($event)"
                    />
                  </template>
                  <QuestField
                    :annotations="annotations"
                    @edit-reference="openReference"
                    v-else-if="!section.issue && section.tokens.length === 1 && section.tokens[0]?.kind === 'string'"
                    :document="document"
                    :name="section.name"
                    :label="`[${section.name}]`"
                    kind="string"
                    multiline
                    :disabled="disabled"
                    @change="apply($event)"
                  />
                  <template v-else>
                    <div class="raw-box">
                      <pre>{{ file.text.slice(section.body, section.end).trim() }}</pre>
                      <p v-if="section.issue" class="raw-error">{{ section.issue }}</p>
                    </div>
                  </template>
                </div>
              </div>
            </div>
          </div>
        </NCollapseItem>
      </NCollapse>
    </section>
  </section>

  <!-- 字符串表译文编辑弹窗 -->
  <NModal
    v-model:show="referenceEdit.show"
    preset="card"
    :title="`修改${referenceEdit.label}`"
    style="width: min(520px, 95vw)"
    :mask-closable="false"
    :closable="!referenceEdit.saving"
    :close-on-esc="!referenceEdit.saving"
  >
    <div class="reference-edit">
      <div class="ref-badge-card">
        <span class="ref-badge-label">引用标识</span>
        <code class="ref-badge-code">&lt;{{ referenceEdit.tableIndex }}::{{ referenceEdit.key }}&gt;</code>
      </div>
      <p class="ref-notice">修改将直接写入字符串表，使用该引用的所有任务与位置都会同步更新。</p>
      <NAlert v-if="referenceEdit.fallback" type="warning" size="small" :bordered="false" class="ref-alert">
        当前译文来自语言覆盖层，修改后将写入该覆盖层。
      </NAlert>
      <NAlert v-if="referenceEdit.missing" type="info" size="small" :bordered="false" class="ref-alert">
        该键在字符串表中尚无译文，保存后将自动创建对应条目。
      </NAlert>
      <div class="ref-form-item">
        <label class="ref-field-label">译文文本</label>
        <NSelect
          v-if="referenceOptions"
          v-model:value="referenceEdit.text"
          :options="referenceOptions"
          :disabled="referenceEdit.saving"
          :input-props="{ 'aria-label': referenceEdit.label }"
        />
        <NInput
          v-else
          v-model:value="referenceEdit.text"
          type="textarea"
          placeholder="输入译文内容..."
          :autosize="{ minRows: 4, maxRows: 12 }"
          :disabled="referenceEdit.saving"
          :input-props="{ 'aria-label': referenceEdit.label }"
        />
      </div>
      <div class="ref-modal-actions">
        <NButton :disabled="referenceEdit.saving" @click="referenceEdit.show = false">
          取消
        </NButton>
        <NButton
          type="primary"
          :loading="referenceEdit.saving"
          :disabled="disabled || !referenceEdit.text.trim()"
          @click="saveReference"
        >
          保存译文
        </NButton>
      </div>
    </div>
  </NModal>
</template>

<style scoped>
.quest-viewer {
  width: 100%;
  max-width: 1240px;
  min-width: 0;
  margin: 0 auto;
  /* 保持表单自然高度，由 FileGUIHost 滚动完整内容。 */
  align-self: flex-start;
  container-type: inline-size;
  container-name: quest-editor;
  color: var(--pvf-text-primary);
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 8px 12px 32px;
  box-sizing: border-box;
}

.quest-viewer > * {
  flex-shrink: 0;
}

/* 顶部工具栏 */
.quest-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  padding: 12px 18px;
  background: var(--pvf-surface-card);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 8px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.05);
}

.header-main {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.header-title-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.quest-title-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--pvf-primary);
  flex-shrink: 0;
}

.quest-title {
  margin: 0;
  font-size: 17px;
  font-weight: 600;
  line-height: 1.3;
  color: var(--pvf-text-primary);
}

.header-tags {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.meta-tag {
  font-size: 11px;
}

.tag-grade {
  background: var(--pvf-primary-soft);
  color: var(--pvf-primary);
  font-weight: 500;
}

.tag-diff {
  background: var(--pvf-surface-inset);
  color: var(--pvf-text-secondary);
}

.tag-readonly {
  background: var(--pvf-surface-inset);
  color: var(--pvf-text-muted);
}

.header-subtitle {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--pvf-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.file-path {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 11px;
  color: var(--pvf-text-muted);
}

.subtitle-sep {
  color: var(--pvf-text-muted);
}

.status-tip {
  color: var(--pvf-text-secondary);
  font-size: 12px;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}

.quest-alert {
  border-radius: 8px;
}

/* 工作区网格 */
.quest-workspace {
  display: grid;
  grid-template-columns: minmax(0, 1.02fr) minmax(0, 0.98fr);
  gap: 16px;
  align-items: start;
}

.workspace-column {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}

/* 通用卡片容器 */
.quest-card {
  background: var(--pvf-surface-card);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 8px;
  overflow: hidden;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04);
  transition: border-color 0.15s ease;
}

.quest-card:hover {
  border-color: var(--pvf-border-normal);
}

.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 11px 16px;
  background: var(--pvf-surface-subtle);
  border-bottom: 1px solid var(--pvf-border-subtle);
}

.card-header-left {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.card-header-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--pvf-primary);
  flex-shrink: 0;
}

.card-title {
  margin: 0;
  font-size: 13.5px;
  font-weight: 600;
  line-height: 1.3;
  color: var(--pvf-text-primary);
}

.card-subtitle {
  display: block;
  font-size: 11px;
  color: var(--pvf-text-muted);
  line-height: 1.3;
}

.card-header-right {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.card-body {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

/* 基础信息网格 */
.basic-fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 14px;
}

.basic-fields > :first-child,
.basic-fields > :last-child {
  grid-column: 1 / -1;
}

/* 剧情对话模式切换 */
.view-mode-toggle {
  display: inline-flex;
  padding: 2px;
  background: var(--pvf-surface-inset);
  border-radius: 4px;
  border: 1px solid var(--pvf-border-faint);
  gap: 2px;
}

.toggle-btn {
  background: transparent;
  border: none;
  font-size: 11px;
  color: var(--pvf-text-secondary);
  padding: 2px 8px;
  border-radius: 3px;
  cursor: pointer;
  transition: all 0.15s ease;
}

.toggle-btn:hover {
  color: var(--pvf-text-primary);
}

.toggle-btn.active {
  background: var(--pvf-surface-card);
  color: var(--pvf-primary);
  font-weight: 500;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.08);
}

.dialogue-pane-content {
  padding-top: 8px;
}

.dialogue-list-container {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.dialogue-list-item {
  padding-bottom: 12px;
  border-bottom: 1px dashed var(--pvf-border-subtle);
}

.dialogue-list-item:last-child {
  padding-bottom: 0;
  border-bottom: none;
}

/* 条件与奖励表单项 */
.type-field-group,
.reward-type-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.field-label {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 12px;
  color: var(--pvf-text-secondary);
  font-weight: 500;
}

.field-label-desc {
  font-size: 11px;
  color: var(--pvf-text-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}

.condition-reference-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

/* 提示通知盒子 */
.callout {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 8px 12px;
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
}

.callout-icon {
  font-size: 13px;
  flex-shrink: 0;
  line-height: 1.5;
}

.callout-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.callout-sub {
  font-size: 11px;
  opacity: 0.85;
}

.callout-info {
  background: var(--pvf-primary-soft);
  color: var(--pvf-text-primary);
  border: 1px solid var(--pvf-primary-selected);
}

.callout-warning {
  background: var(--pvf-surface-warning);
  color: var(--pvf-warning);
  border: 1px solid var(--pvf-border-subtle);
}

.callout-error {
  background: var(--pvf-surface-error);
  color: var(--pvf-error);
  border: 1px solid var(--pvf-border-subtle);
}

/* 参数容器块 */
.params-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.params-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.params-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--pvf-text-secondary);
}

.params-meta {
  font-size: 11px;
  color: var(--pvf-text-muted);
}

.quest-points-box {
  padding-top: 4px;
}

/* 可选奖励盒子 */
.optional-reward-box {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  background: var(--pvf-surface-inset);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 6px;
}

.optional-reward-title {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.optional-title-text {
  font-size: 12px;
  font-weight: 600;
  color: var(--pvf-text-primary);
}

.optional-title-desc {
  font-size: 11px;
  color: var(--pvf-text-muted);
}

/* 其他配置折叠 */
.other-card {
  padding: 4px 12px;
}

.other-collapse-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  color: var(--pvf-text-primary);
}

.other-icon {
  color: var(--pvf-primary);
}

.other-title {
  font-size: 13px;
}

.other-count-tag {
  font-size: 11px;
  background: var(--pvf-surface-inset);
}

.other-content {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 8px 4px 14px;
}

.other-callout {
  margin: 0;
}

.other-fields-grid {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.other-section-item {
  padding: 10px 12px;
  background: var(--pvf-surface-subtle);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 6px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.other-item-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.other-section-tag {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-weight: 600;
  font-size: 12px;
  color: var(--pvf-primary);
}

.other-issue-badge {
  font-size: 11px;
  color: var(--pvf-warning);
}

.raw-box pre {
  margin: 0;
  padding: 8px;
  background: var(--pvf-surface-inset);
  border-radius: 4px;
  font-size: 11px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}

.raw-error {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--pvf-warning);
}

/* 引用修改弹窗 */
.reference-edit {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.ref-badge-card {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  background: var(--pvf-surface-inset);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 5px;
}

.ref-badge-label {
  font-size: 11px;
  color: var(--pvf-text-muted);
}

.ref-badge-code {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  color: var(--pvf-primary);
  font-weight: 600;
}

.ref-notice {
  margin: 0;
  font-size: 12px;
  color: var(--pvf-text-secondary);
  line-height: 1.5;
}

.ref-alert {
  margin: 0;
}

.ref-form-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.ref-field-label {
  font-size: 12px;
  font-weight: 500;
  color: var(--pvf-text-secondary);
}

.ref-modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 4px;
}

/* 深入样式优化子组件 */
:deep(.quest-field label) {
  font-size: 12px;
  font-weight: 500;
  color: var(--pvf-text-secondary);
}

:deep(.reference-actions) {
  margin-top: 4px;
  padding: 4px 8px;
  background: var(--pvf-surface-inset);
  border: 1px solid var(--pvf-border-faint);
  border-radius: 4px;
}

:deep(.quest-parameters) {
  background: var(--pvf-surface-inset);
  border: 1px solid var(--pvf-border-subtle);
  border-radius: 6px;
  padding: 10px 12px;
  gap: 8px;
}

:deep(.parameter-row) {
  padding: 8px 0;
  border-bottom: 1px solid var(--pvf-border-faint);
  align-items: center;
  gap: 12px;
}

:deep(.parameter-row:last-of-type) {
  border-bottom: none;
}

:deep(.parameter-row > .n-button) {
  margin-top: 0;
  align-self: center;
}

:deep(.parameter-fields label > span) {
  font-weight: 500;
  font-size: 11.5px;
}

:deep(.quest-parameters .empty) {
  padding: 10px 0;
  text-align: center;
  color: var(--pvf-text-muted);
}

/* 按编辑窗格宽度调整布局，兼容分屏。 */
@container quest-editor (max-width: 860px) {
  .quest-workspace {
    grid-template-columns: 1fr;
  }
  .quest-header {
    flex-direction: column;
    align-items: stretch;
    gap: 12px;
  }
  .header-actions {
    justify-content: flex-end;
  }
}

@container quest-editor (max-width: 520px) {
  .basic-fields {
    grid-template-columns: 1fr;
  }
  .condition-reference-grid {
    grid-template-columns: 1fr;
  }
  .card-header {
    flex-wrap: wrap;
  }
  .optional-reward-title {
    flex-wrap: wrap;
  }
  :deep(.parameter-row) {
    flex-direction: column;
    align-items: stretch;
  }
  :deep(.parameter-fields) {
    grid-template-columns: 1fr;
  }
  :deep(.parameter-row > .n-button) {
    align-self: flex-end;
  }
}
</style>
