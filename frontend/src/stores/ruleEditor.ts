import { defineStore } from "pinia";
import { ref } from "vue";
export interface RuleEditorTarget {
  category: "annotations" | "rendering";
  path: string;
  section: string;
  directory: boolean;
}
export const useRuleEditorStore = defineStore("ruleEditor", () => {
  const target = ref<RuleEditorTarget | null>(null);
  const visible = ref(false);
  function open(value: RuleEditorTarget) {
    target.value = value;
    visible.value = true;
  }
  return { target, visible, open };
});
