// Persists in-progress /sessions/new form state across navigation within a
// tab. Uses sessionStorage so drafts are scoped to the tab and auto-expire on
// close — matches the "I stepped away mid-compose" use case without leaving
// stale prompts haunting the user next week.
//
// To upgrade to cross-tab-close persistence (localStorage), swap `getStorage()`
// to return `window.localStorage` and add a TTL/discard affordance at the call
// site. The serialization format here is forward-compatible: a schema-version
// mismatch silently discards the old draft rather than hydrating junk.

import { toCodingAgentReasoningEffort, type CodingAgentReasoningEffort } from "@/lib/coding-agent-reasoning";
import type { SessionInputReference } from "@/lib/types";

const STORAGE_KEY = "143:new-session-draft";
const SCHEMA_VERSION = 1;

export type SessionDraft = {
  message: string;
  attachments: string[];
  references: SessionInputReference[];
  selectedModel: string;
  reasoningOverride: CodingAgentReasoningEffort;
  userSelectedRepoId: string | null;
  branchByRepoId: Record<string, string>;
  showImageInput: boolean;
  imageURL: string;
};

type StoredDraft = SessionDraft & { __v: number };

function getStorage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

export function loadDraft(): SessionDraft | null {
  const storage = getStorage();
  if (!storage) return null;
  let raw: string | null;
  try {
    raw = storage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
  if (!raw) return null;

  let parsed: Partial<StoredDraft>;
  try {
    parsed = JSON.parse(raw) as Partial<StoredDraft>;
  } catch {
    return null;
  }
  if (parsed.__v !== SCHEMA_VERSION) return null;

  return {
    message: typeof parsed.message === "string" ? parsed.message : "",
    attachments: Array.isArray(parsed.attachments)
      ? parsed.attachments.filter((a): a is string => typeof a === "string")
      : [],
    references: Array.isArray(parsed.references)
      ? parsed.references.filter(isValidReference)
      : [],
    selectedModel: typeof parsed.selectedModel === "string" ? parsed.selectedModel : "",
    // Sanitize via the canonical coercer: unknown/invalid values collapse to "".
    reasoningOverride: toCodingAgentReasoningEffort(
      typeof parsed.reasoningOverride === "string" ? parsed.reasoningOverride : "",
    ),
    userSelectedRepoId: typeof parsed.userSelectedRepoId === "string" ? parsed.userSelectedRepoId : null,
    branchByRepoId: isStringRecord(parsed.branchByRepoId) ? parsed.branchByRepoId : {},
    showImageInput: parsed.showImageInput === true,
    imageURL: typeof parsed.imageURL === "string" ? parsed.imageURL : "",
  };
}

export function saveDraft(draft: SessionDraft): void {
  const storage = getStorage();
  if (!storage) return;

  if (isEmptyDraft(draft)) {
    try { storage.removeItem(STORAGE_KEY); } catch {}
    return;
  }

  const stored: StoredDraft = { __v: SCHEMA_VERSION, ...draft };
  try {
    storage.setItem(STORAGE_KEY, JSON.stringify(stored));
  } catch {
    // Quota exceeded, storage access denied, or serialization failure — a
    // best-effort save is acceptable here; losing a draft is strictly better
    // than blowing up the composer.
  }
}

export function clearDraft(): void {
  const storage = getStorage();
  if (!storage) return;
  try { storage.removeItem(STORAGE_KEY); } catch {}
}

function isEmptyDraft(draft: SessionDraft): boolean {
  return (
    draft.message.length === 0
    && draft.attachments.length === 0
    && draft.references.length === 0
    && draft.selectedModel === ""
    && draft.reasoningOverride === ""
    && draft.userSelectedRepoId === null
    && Object.keys(draft.branchByRepoId).length === 0
    && !draft.showImageInput
    && draft.imageURL === ""
  );
}

function isValidReference(value: unknown): value is SessionInputReference {
  if (!value || typeof value !== "object") return false;
  const ref = value as Record<string, unknown>;
  return (
    typeof ref.kind === "string"
    && typeof ref.display === "string"
    && (ref.token === undefined || typeof ref.token === "string")
    && (ref.path === undefined || typeof ref.path === "string")
    && (ref.id === undefined || typeof ref.id === "string")
  );
}

function isStringRecord(value: unknown): value is Record<string, string> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  return Object.values(value as Record<string, unknown>).every((v) => typeof v === "string");
}
