import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { createImageLoader } from "../src/utils/image-loader";
import type { ImageBatchResult } from "../bindings/pvfine/services/models";

const image = { dataUrl: "data:image/png;base64,test", width: 1, height: 1 };
const reference = { path: "Item/test.img", index: 0 };
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

test("coalesces visible requests, deduplicates pending work and uses completed cache", async () => {
  const batch = vi.fn(async (refs) => refs.map(() => ({ data: image, error: "" })));
  const loader = createImageLoader(() => 3, batch);
  const first = loader.load(reference);
  expect(loader.load({ path: "item\\test.img", index: 0 })).toBe(first);
  const second = loader.load({ path: "item/other.img", index: 2 });
  expect(batch).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(8);
  expect(batch).toHaveBeenCalledExactlyOnceWith([
    { path: "item/test.img", index: 0 }, { path: "item/other.img", index: 2 },
  ], 3);
  expect(await first).toEqual(image);
  expect(await second).toEqual(image);
  expect(await loader.load(reference)).toEqual(image);
  expect(batch).toHaveBeenCalledTimes(1);
});

test("bounds batch size and simultaneous bridge calls", async () => {
  const requests = Array.from({ length: 3 }, () => deferred<ImageBatchResult[]>());
  const batch = vi.fn().mockImplementationOnce(() => requests[0]!.promise)
    .mockImplementationOnce(() => requests[1]!.promise).mockImplementationOnce(() => requests[2]!.promise);
  const loader = createImageLoader(() => 1, batch);
  const promises = Array.from({ length: 300 }, (_, index) => loader.load({ path: "item/test.img", index }));
  await vi.advanceTimersByTimeAsync(8);
  expect(batch).toHaveBeenCalledTimes(2);
  expect(batch.mock.calls[0]![0]).toHaveLength(128);
  expect(batch.mock.calls[1]![0]).toHaveLength(128);
  requests[0]!.resolve(Array.from({ length: 128 }, () => ({ data: image, error: "" })));
  await vi.advanceTimersByTimeAsync(0);
  expect(batch).toHaveBeenCalledTimes(3);
  expect(batch.mock.calls[2]![0]).toHaveLength(44);
  requests[1]!.resolve([]);
  requests[2]!.resolve([]);
  expect((await Promise.all(promises)).filter(Boolean)).toHaveLength(128);
});

test("reset settles queued and active requests and rejects late generation results", async () => {
  let generation = 1;
  const old = deferred<ImageBatchResult[]>();
  const batch = vi.fn().mockImplementationOnce(() => old.promise)
    .mockResolvedValue([{ data: image, error: "" }]);
  const loader = createImageLoader(() => generation, batch);
  const active = loader.load(reference);
  await vi.advanceTimersByTimeAsync(8);
  const queued = loader.load({ path: "queued.img", index: 0 });
  generation = 2;
  loader.reset();
  expect(await active).toBeNull();
  expect(await queued).toBeNull();
  const current = loader.load(reference);
  await vi.advanceTimersByTimeAsync(8);
  expect(await current).toEqual(image);
  old.resolve([{ data: { ...image, width: 99 }, error: "" }]);
  await vi.advanceTimersByTimeAsync(0);
  expect(await loader.load(reference)).toEqual(image);
  expect(batch.mock.calls[1]![1]).toBe(2);
});

test("same-generation reset prevents old completion from removing a replacement pending request", async () => {
  const old = deferred<ImageBatchResult[]>();
  const current = deferred<ImageBatchResult[]>();
  const batch = vi.fn().mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
  const loader = createImageLoader(() => 1, batch);
  const first = loader.load(reference);
  await vi.advanceTimersByTimeAsync(8);
  loader.reset();
  expect(await first).toBeNull();
  const replacement = loader.load(reference);
  await vi.advanceTimersByTimeAsync(8);
  old.resolve([{ data: image, error: "" }]);
  await vi.advanceTimersByTimeAsync(0);
  expect(loader.load(reference)).toBe(replacement);
  current.resolve([{ data: image, error: "" }]);
  expect(await replacement).toEqual(image);
});

test("isolates per-frame errors and retries failures without caching them", async () => {
  const onError = vi.fn();
  const batch = vi.fn().mockResolvedValueOnce([{ data: null, error: "broken frame" }, { data: image, error: "" }])
    .mockRejectedValueOnce(new Error("bridge failure")).mockResolvedValueOnce([{ data: image, error: "" }]);
  const loader = createImageLoader(() => 1, batch, onError);
  const bad = loader.load(reference);
  const good = loader.load({ path: "other.img", index: 0 });
  await vi.advanceTimersByTimeAsync(8);
  expect(await bad).toBeNull();
  expect(await good).toEqual(image);
  const failed = loader.load(reference);
  await vi.advanceTimersByTimeAsync(8);
  expect(await failed).toBeNull();
  const retry = loader.load(reference);
  await vi.advanceTimersByTimeAsync(8);
  expect(await retry).toEqual(image);
  expect(onError).toHaveBeenCalledTimes(2);
});

test("invalid inputs create no bridge work", async () => {
  const batch = vi.fn();
  const loader = createImageLoader(() => 1, batch);
  for (const input of [null, { path: "", index: 0 }, { path: "///", index: 0 }, { path: "x", index: -1 }, { path: "x", index: 0.5 }]) {
    expect(await loader.load(input)).toBeNull();
  }
  await vi.advanceTimersByTimeAsync(8);
  expect(batch).not.toHaveBeenCalled();
});
