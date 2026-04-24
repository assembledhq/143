import { afterEach, describe, expect, it } from "vitest";
import { clearDraft, loadDraft, saveDraft, type SessionDraft } from "./session-draft";

const STORAGE_KEY = "143:new-session-draft";

const emptyDraft: SessionDraft = {
  message: "",
  attachments: [],
  references: [],
  selectedModel: "",
  userSelectedRepoId: null,
  branchByRepoId: {},
  showImageInput: false,
  imageURL: "",
};

afterEach(() => {
  window.sessionStorage.clear();
});

describe("session-draft storage", () => {
  it("returns null when no draft is stored", () => {
    expect(loadDraft()).toBeNull();
  });

  it("round-trips a populated draft", () => {
    const draft: SessionDraft = {
      message: "Refactor the auth middleware",
      attachments: ["https://example.com/a.png", "https://example.com/b.png"],
      references: [
        { kind: "file", display: "auth.go", path: "internal/auth/auth.go", token: "@auth.go" },
      ],
      selectedModel: "claude-sonnet-4-6",
      userSelectedRepoId: "repo-abc",
      branchByRepoId: { "repo-abc": "feature/auth" },
      showImageInput: true,
      imageURL: "https://example.com/c.png",
    };
    saveDraft(draft);
    expect(loadDraft()).toEqual(draft);
  });

  it("does not persist an empty draft and clears existing storage", () => {
    saveDraft({ ...emptyDraft, message: "seed" });
    expect(window.sessionStorage.getItem(STORAGE_KEY)).not.toBeNull();

    saveDraft(emptyDraft);
    expect(window.sessionStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("clearDraft removes the stored value", () => {
    saveDraft({ ...emptyDraft, message: "hi" });
    clearDraft();
    expect(loadDraft()).toBeNull();
  });

  it("discards drafts with a mismatched schema version", () => {
    window.sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ __v: 999, message: "legacy", attachments: [] }),
    );
    expect(loadDraft()).toBeNull();
  });

  it("discards drafts whose JSON is malformed", () => {
    window.sessionStorage.setItem(STORAGE_KEY, "{not valid json");
    expect(loadDraft()).toBeNull();
  });

  it("sanitizes structurally broken fields rather than throwing", () => {
    window.sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        __v: 1,
        message: 42,
        attachments: ["ok", 7, null],
        references: [
          { kind: "file", display: "ok.go" },
          { nope: true },
        ],
        selectedModel: null,
        userSelectedRepoId: 0,
        branchByRepoId: ["not", "a", "map"],
        showImageInput: "yes",
        imageURL: undefined,
      }),
    );
    const draft = loadDraft();
    expect(draft).toEqual({
      message: "",
      attachments: ["ok"],
      references: [{ kind: "file", display: "ok.go" }],
      selectedModel: "",
      userSelectedRepoId: null,
      branchByRepoId: {},
      showImageInput: false,
      imageURL: "",
    });
  });
});
