import { describe, it, expect } from "vitest";
import { clearCachedViewerScope, readCachedViewerScope, writeCachedViewerScope } from "./viewer-scope-cache";

function memoryStorage(): Pick<Storage, "getItem" | "setItem" | "removeItem"> & { store: Map<string, string> } {
  const store = new Map<string, string>();
  return {
    store,
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
    removeItem: (key: string) => {
      store.delete(key);
    },
  };
}

describe("viewer-scope-cache", () => {
  it("round-trips a viewer scope", () => {
    const storage = memoryStorage();
    writeCachedViewerScope(storage, { userId: "user-1", orgId: "org-1" });
    expect(readCachedViewerScope(storage)).toEqual({ userId: "user-1", orgId: "org-1" });
  });

  it("normalizes a missing org to null", () => {
    const storage = memoryStorage();
    writeCachedViewerScope(storage, { userId: "user-1" });
    expect(readCachedViewerScope(storage)).toEqual({ userId: "user-1", orgId: null });
  });

  it("returns null when nothing is cached", () => {
    expect(readCachedViewerScope(memoryStorage())).toBeNull();
  });

  it("rejects malformed payloads", () => {
    const storage = memoryStorage();
    storage.store.set("143:last-viewer-scope", "not-json{");
    expect(readCachedViewerScope(storage)).toBeNull();

    storage.store.set("143:last-viewer-scope", JSON.stringify({ version: 2, userId: "user-1" }));
    expect(readCachedViewerScope(storage)).toBeNull();

    storage.store.set("143:last-viewer-scope", JSON.stringify({ version: 1, userId: "" }));
    expect(readCachedViewerScope(storage)).toBeNull();
  });

  it("does not write an empty user id", () => {
    const storage = memoryStorage();
    writeCachedViewerScope(storage, { userId: "", orgId: "org-1" });
    expect(storage.store.size).toBe(0);
  });

  it("clears a cached scope", () => {
    const storage = memoryStorage();
    writeCachedViewerScope(storage, { userId: "user-1", orgId: "org-1" });
    clearCachedViewerScope(storage);
    expect(readCachedViewerScope(storage)).toBeNull();
  });
});
