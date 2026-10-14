import { defineStore } from "pinia";
import { ref } from "vue";
import { Events } from "@wailsio/runtime";
import { ImageService } from "../../bindings/pvfine/services";
import { createImageLoader } from "../utils/image-loader";
import type { ImageIndexStatus } from "../../bindings/pvfine/services/models";

const idleStatus: ImageIndexStatus = {
  state: "idle",
  stage: "idle",
  directory: "",
  done: 0,
  total: 0,
  npkFiles: 0,
  imgFiles: 0,
  imageCount: 0,
  skipped: 0,
  duplicates: 0,
  error: "",
  generation: 0,
  buildDurationMs: 0,
};

export const useImageStore = defineStore("images", () => {
  const status = ref<ImageIndexStatus>({ ...idleStatus });
  const revision = ref(0);
  const loader = createImageLoader(
    () => status.value.generation,
    async (references, generation) => (await ImageService.GetImages(references, generation)) ?? [],
    (error) => console.warn("load PVF images failed", error),
  );
  let initialized = false;

  function eventStatus(event: any): ImageIndexStatus | null {
    const value = event?.data ?? event;
    if (!value || typeof value !== "object" || typeof value.state !== "string") return null;
    return value as ImageIndexStatus;
  }

  function acceptStatus(next: ImageIndexStatus | null): void {
    if (!next) return;
    if (next.generation !== status.value.generation) {
      loader.reset();
      revision.value++;
    }
    status.value = next;
    if (next.state === "ready" || next.state === "error") {
      revision.value++;
      loader.reset();
    }
  }

  async function initialize(): Promise<ImageIndexStatus> {
    if (initialized) return status.value;
    initialized = true;
    try {
      acceptStatus(await ImageService.Initialize());
    } catch (error) {
      initialized = false;
      console.error("initialize image index failed", error);
    }
    return status.value;
  }

  async function selectDirectory(): Promise<ImageIndexStatus> {
    const next = await ImageService.SelectNPKDirectory();
    acceptStatus(next);
    return status.value;
  }

  async function rebuild(): Promise<ImageIndexStatus> {
    const next = await ImageService.RebuildIndex();
    acceptStatus(next);
    return status.value;
  }

  const loadImage = loader.load;

  Events.On("image:index-progress", (event: any) => acceptStatus(eventStatus(event)));
  Events.On("image:index-ready", (event: any) => acceptStatus(eventStatus(event)));
  Events.On("image:index-error", (event: any) => acceptStatus(eventStatus(event)));

  return {
    status,
    revision,
    initialize,
    selectDirectory,
    rebuild,
    loadImage,
  };
});
