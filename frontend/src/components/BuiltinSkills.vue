<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { NAlert, NButton, NCheckbox, NInput, NSelect, useMessage } from "naive-ui";
import { SkillService } from "../../bindings/pvfine/services";
import type { BuiltinSkill, SkillInstallResult } from "../../bindings/pvfine/services/models";

const message = useMessage();
const skills = ref<BuiltinSkill[]>([]);
const selected = ref<string[]>([]);
const target = ref("global");
const directory = ref("");
const resolved = ref("");
const pathError = ref("");
const loading = ref(false);
const ready = ref(false);
const overwrite = ref(false);
const results = ref<SkillInstallResult[]>([]);
const needsDirectory = computed(() => target.value === "project" || target.value === "custom");
const options = [
  { label: "通用 skill（~/.agents/skills）", value: "global" },
  { label: "项目级 skill（项目/.agents/skills）", value: "project" },
  { label: "Claude Code（~/.claude/skills）", value: "claude" },
  { label: "自定义 skill 根目录", value: "custom" },
];
let resolveSequence = 0;
async function resolveDirectory() {
  const sequence = ++resolveSequence;
  resolved.value = "";
  pathError.value = "";
  results.value = [];
  try {
    const path = await SkillService.ResolveDirectory(target.value, directory.value);
    if (sequence === resolveSequence) resolved.value = path;
  } catch (error: any) {
    if (sequence === resolveSequence) pathError.value = String(error?.message ?? error);
  }
}
watch([target, directory], resolveDirectory);
watch(overwrite, () => { results.value = []; });
async function chooseDirectory() {
  try {
    const path = await SkillService.ChooseDirectory();
    if (path) directory.value = path;
  } catch (error: any) { message.error(String(error?.message ?? error)); }
}
function toggle(id: string, checked: boolean) {
  selected.value = checked ? [...selected.value, id] : selected.value.filter(value => value !== id);
  results.value = [];
}
async function install() {
  loading.value = true;
  try {
    results.value = (await SkillService.Install(selected.value, target.value, directory.value, overwrite.value)) ?? [];
    const installed = results.value.filter(result => result.status === "installed").length;
    const updated = results.value.filter(result => result.status === "updated").length;
    const skipped = results.value.filter(result => result.status === "skipped").length;
    const failed = results.value.filter(result => result.status === "failed").length;
    if (failed) message.error(`已安装 ${installed} 个，已更新 ${updated} 个，${failed} 个安装失败，请查看结果`);
    else message.success(`已安装 ${installed} 个，已更新 ${updated} 个，跳过 ${skipped} 个已有 skill`);
  } catch (error: any) { message.error(String(error?.message ?? error)); }
  finally { loading.value = false; }
}
onMounted(async () => {
  try {
    skills.value = (await SkillService.List()) ?? [];
    selected.value = skills.value.map(skill => skill.id);
    ready.value = true;
    await resolveDirectory();
  } catch (error: any) { message.error(String(error?.message ?? error)); }
});
</script>

<template>
  <section class="builtin-skills" aria-label="内置 skill 安装">
    <h3>内置 skill</h3>
    <p>为外部 AI agent 安装 PVF 编辑指南，配合上方 MCP 服务读取和修改归档。可单独选择需要的指南。</p>
    <div class="skill-list">
      <label v-for="skill in skills" :key="skill.id" class="skill-item">
        <NCheckbox :checked="selected.includes(skill.id)" :disabled="loading" @update:checked="toggle(skill.id, $event)" />
        <span><strong>{{ skill.title }}</strong><small>{{ skill.description }}</small></span>
      </label>
    </div>
    <label class="field-label" for="skill-target">安装位置</label>
    <NSelect input-id="skill-target" v-model:value="target" :options="options" :disabled="loading" />
    <div v-if="needsDirectory" class="directory-row">
      <NInput v-model:value="directory" :disabled="loading" :placeholder="target === 'project' ? '选择项目根目录' : '输入 skill 根目录的绝对路径'" aria-label="skill 安装目录" />
      <NButton :disabled="loading" @click="chooseDirectory">选择目录</NButton>
    </div>
    <p v-if="resolved" class="destination">将安装至：{{ resolved }}<br>每个 skill 使用独立子目录。</p>
    <p v-else-if="pathError" role="status">{{ pathError }}</p>
    <div class="overwrite-option"><NCheckbox v-model:checked="overwrite" :disabled="loading">覆盖已有 skill（更新内置文件，保留额外文件）</NCheckbox></div>
    <NButton type="primary" secondary :loading="loading" :disabled="!ready || !resolved || !selected.length || loading" @click="install">安装 skill（{{ selected.length }}）</NButton>
    <p>默认跳过已有 skill。勾选覆盖后，内置文件的本地修改将被替换；额外添加的文件保留。符号链接或文件类型冲突会报错。安装后请让 AI 工具重新加载 skill；MCP 连接需另行配置。</p>
    <div class="install-results" aria-live="polite">
      <NAlert v-for="result in results" :key="result.id" :type="result.status === 'failed' ? 'error' : (result.status === 'installed' || result.status === 'updated') ? 'success' : 'info'" :title="`${result.id} · ${result.message}`">
        <span class="destination">{{ result.path }}</span>
      </NAlert>
    </div>
  </section>
</template>

<style scoped>
.builtin-skills { margin-top: 28px; padding-top: 24px; border-top: 1px solid var(--pvf-border); }
h3 { margin: 0 0 16px; font-size: 20px; }
p, small { color: var(--pvf-text-secondary); line-height: 1.7; }
.skill-list { display: grid; gap: 16px; margin: 20px 0; }
.skill-item { display: flex; align-items: flex-start; gap: 10px; cursor: pointer; }
.skill-item small { display: block; margin-top: 4px; }
.field-label { display: block; margin-bottom: 8px; }
.directory-row { display: flex; gap: 8px; margin-top: 12px; }
.destination { overflow-wrap: anywhere; user-select: text; }
.overwrite-option { margin: 16px 0; }
.install-results { display: grid; gap: 10px; }
</style>
