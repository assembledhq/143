import { useCallback, useSyncExternalStore } from "react";

const STORAGE_KEY = "143:command-palette:recents";
const MAX_ENTRIES = 10;
const DISPLAY_COUNT = 5;

export interface RecentItem {
  type: "session" | "project" | "navigation";
  id: string;
  label: string;
  href: string;
  timestamp: number;
}

function getStoredItems(): RecentItem[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as RecentItem[]) : [];
  } catch {
    return [];
  }
}

function setStoredItems(items: RecentItem[]) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(items));
  // Dispatch a storage event so other tabs pick it up. The native storage
  // event only fires on *other* tabs, so we also fire a custom event for
  // the current tab.
  window.dispatchEvent(new Event("palette-recents-changed"));
}

// Thin external-store wrapper so React re-renders when recents change.
// Initialized lazily to avoid calling localStorage during SSR module import.
let snapshot: RecentItem[] | null = null;

function subscribe(callback: () => void) {
  const onUpdate = () => {
    snapshot = getStoredItems();
    callback();
  };

  // Cross-tab sync via native storage event.
  window.addEventListener("storage", onUpdate);
  // Same-tab sync via custom event.
  window.addEventListener("palette-recents-changed", onUpdate);

  return () => {
    window.removeEventListener("storage", onUpdate);
    window.removeEventListener("palette-recents-changed", onUpdate);
  };
}

function getSnapshot() {
  if (snapshot === null) snapshot = getStoredItems();
  return snapshot;
}

function getServerSnapshot() {
  return [] as RecentItem[];
}

export function useRecentPaletteItems() {
  const items = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);

  const addRecent = useCallback((item: Omit<RecentItem, "timestamp">) => {
    const current = getStoredItems();
    const dedupeKey = `${item.type}:${item.id}`;
    const filtered = current.filter((i) => `${i.type}:${i.id}` !== dedupeKey);
    const updated: RecentItem[] = [
      { ...item, timestamp: Date.now() },
      ...filtered,
    ].slice(0, MAX_ENTRIES);
    setStoredItems(updated);
  }, []);

  const removeRecent = useCallback((type: string, id: string) => {
    const current = getStoredItems();
    const updated = current.filter((i) => !(i.type === type && i.id === id));
    setStoredItems(updated);
  }, []);

  return {
    /** Most recent 5 items for display. */
    displayItems: items.slice(0, DISPLAY_COUNT),
    addRecent,
    removeRecent,
  };
}
