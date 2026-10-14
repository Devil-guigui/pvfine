import type { ImageBatchResult, ImageData, ImageReference } from "../../bindings/pvfine/services/models";

const batchSize = 128;
const maxConcurrentBatches = 2;
const batchDelayMs = 8;
const cacheEntries = 512;

type ImageJob = {
  key: string;
  generation: number;
  epoch: number;
  reference: ImageReference;
  promise: Promise<ImageData | null>;
  resolve: (data: ImageData | null) => void;
};

// Coalesce only requested images, including visible tree rows and previews.
// Reset settles old callers immediately and prevents old responses filling the cache.
export function createImageLoader(
  getGeneration: () => number,
  loadBatch: (references: ImageReference[], generation: number) => Promise<ImageBatchResult[]>,
  onError: (error: unknown) => void = () => {},
) {
  const pending = new Map<string, ImageJob>();
  const resolved = new Map<string, ImageData | null>();
  let queue: ImageJob[] = [];
  let timer: ReturnType<typeof setTimeout> | null = null;
  let active = 0;
  let epoch = 0;

  function schedule() {
    if (timer !== null || queue.length === 0 || active >= maxConcurrentBatches) return;
    timer = setTimeout(() => {
      timer = null;
      pump();
    }, batchDelayMs);
  }

  function pump() {
    while (queue.length > 0 && active < maxConcurrentBatches) {
      const jobs = queue.splice(0, batchSize);
      active++;
      void run(jobs);
    }
  }

  async function run(jobs: ImageJob[]) {
    try {
      const results = await loadBatch(jobs.map((job) => job.reference), jobs[0]!.generation);
      for (let index = 0; index < jobs.length; index++) {
        const job = jobs[index]!;
        if (job.epoch !== epoch || job.generation !== getGeneration()) continue;
        const result = results[index];
        const data = result?.data ?? null;
        if (result?.error) {
          onError(result.error);
        } else if (result) {
          resolved.delete(job.key);
          resolved.set(job.key, data);
          while (resolved.size > cacheEntries) resolved.delete(resolved.keys().next().value!);
        }
        job.resolve(data);
      }
    } catch (error) {
      if (jobs[0]!.epoch === epoch) onError(error);
    } finally {
      for (const job of jobs) {
        job.resolve(null); // also settles errors or stale/incomplete responses
        if (pending.get(job.key) === job) pending.delete(job.key);
      }
      active--;
      pump();
    }
  }

  function load(reference: ImageReference | null | undefined): Promise<ImageData | null> {
    if (!reference?.path || !Number.isInteger(reference.index) || reference.index < 0) return Promise.resolve(null);
    const path = reference.path.replaceAll("\\", "/").replace(/^\/+|\/+$/g, "").toLowerCase();
    if (!path) return Promise.resolve(null);
    const generation = getGeneration();
    const key = `${generation}\u0000${path}\u0000${reference.index}`;
    if (resolved.has(key)) return Promise.resolve(resolved.get(key) ?? null);
    const existing = pending.get(key);
    if (existing) return existing.promise;
    let resolve!: ImageJob["resolve"];
    const promise = new Promise<ImageData | null>((done) => { resolve = done; });
    const job: ImageJob = { key, generation, epoch, reference: { path, index: reference.index }, promise, resolve };
    pending.set(key, job);
    queue.push(job);
    schedule();
    return promise;
  }

  function reset() {
    epoch++;
    if (timer !== null) clearTimeout(timer);
    timer = null;
    queue = [];
    for (const job of pending.values()) job.resolve(null);
    pending.clear();
    resolved.clear();
  }

  return { load, reset };
}
