import { describe, expect, it } from "vitest";

import type { SessionInputReference } from "@/lib/types";
import { findActiveMention, insertMentionAtCaret, removeMentionReference, syncReferencesWithMessage } from "./session-composer-mentions";

const fileReference: SessionInputReference = {
  kind: "file",
  token: "@internal/api/handlers/sessions.go",
  path: "internal/api/handlers/sessions.go",
  display: "internal/api/handlers/sessions.go",
};

const directoryReference: SessionInputReference = {
  kind: "directory",
  token: "@internal/api",
  path: "internal/api",
  display: "internal/api",
};

describe("session composer mentions", () => {
  it("finds the active mention at the caret", () => {
    const text = "Investigate @sess";
    expect(findActiveMention(text, text.length)).toEqual({
      start: 12,
      end: 17,
      query: "sess",
    });
  });

  it("ignores @ inside words", () => {
    const text = "email@test.com";
    expect(findActiveMention(text, text.length)).toBeNull();
  });

  it("stops the mention when the user types a space after @", () => {
    expect(findActiveMention("Investigate @ ", "Investigate @ ".length)).toBeNull();
  });

  it("stops the mention when the user deletes back past the @ token", () => {
    expect(findActiveMention("Investigate ", "Investigate ".length)).toBeNull();
  });

  it("inserts a selected mention token at the active range", () => {
    const text = "Inspect @sess next";
    const mention = findActiveMention(text, 13);
    expect(mention).not.toBeNull();

    const inserted = insertMentionAtCaret(text, mention!, fileReference);
    expect(inserted.text).toBe("Inspect @internal/api/handlers/sessions.go next");
    expect(inserted.caret).toBe("Inspect @internal/api/handlers/sessions.go".length);
  });

  it("drops references whose token disappeared from the message", () => {
    expect(syncReferencesWithMessage("Inspect only text", [fileReference])).toEqual([]);
  });

  it("drops overlapping references when only a longer token remains", () => {
    expect(
      syncReferencesWithMessage(
        "Inspect @internal/api/handlers/sessions.go now",
        [directoryReference, fileReference],
      ),
    ).toEqual([fileReference]);
  });

  it("removes a mention token from the message when a chip is deleted", () => {
    expect(removeMentionReference("Inspect @internal/api/handlers/sessions.go now", fileReference)).toBe("Inspect now");
  });

  it("removes every repeated mention token for the same reference", () => {
    expect(
      removeMentionReference(
        "Inspect @internal/api/handlers/sessions.go and @internal/api/handlers/sessions.go now",
        fileReference,
      ),
    ).toBe("Inspect and now");
  });

  it("keeps references when the token is followed by punctuation", () => {
    expect(syncReferencesWithMessage("Inspect @internal/api/handlers/sessions.go,", [fileReference])).toEqual([fileReference]);
  });
});
