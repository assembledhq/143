import { describe, expect, it } from "vitest";

import type { SessionInputCommand, SessionInputReference } from "@/lib/types";
import {
  COMPOSER_TRIGGER_SPECS,
  findActiveMention,
  findActiveTrigger,
  insertCommandAtCaret,
  insertMentionAtCaret,
  removeCommandReference,
  removeMentionReference,
  syncCommandsWithMessage,
  syncReferencesWithMessage,
} from "./session-composer-mentions";

const reviewCommand: SessionInputCommand = {
  kind: "command",
  agent_type: "claude_code",
  name: "review",
  token: "/review",
  display: "/review",
};

const clearCommand: SessionInputCommand = {
  kind: "command",
  agent_type: "claude_code",
  name: "clear",
  token: "/clear",
  display: "/clear",
};

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

  it("keeps the same references array when no references are removed", () => {
    const references = [fileReference];

    expect(syncReferencesWithMessage("Inspect @internal/api/handlers/sessions.go now", references)).toBe(references);
  });
});

describe("session composer slash command triggers", () => {
  it("matches a slash trigger at the start of the message", () => {
    const text = "/rev";
    expect(findActiveTrigger(text, text.length, COMPOSER_TRIGGER_SPECS)).toEqual({
      start: 0,
      end: 4,
      query: "rev",
      trigger: "/",
    });
  });

  it("matches a slash trigger at the start of a new line", () => {
    const text = "context line\n/rev";
    expect(findActiveTrigger(text, text.length, COMPOSER_TRIGGER_SPECS)).toEqual({
      start: 13,
      end: 17,
      query: "rev",
      trigger: "/",
    });
  });

  it("does not fire on slashes inside paths", () => {
    const text = "look at dir/foo";
    expect(findActiveTrigger(text, text.length, COMPOSER_TRIGGER_SPECS)).toBeNull();
  });

  it("does not fire on slashes inside URLs", () => {
    const text = "see https://example.com/path";
    expect(findActiveTrigger(text, text.length, COMPOSER_TRIGGER_SPECS)).toBeNull();
  });

  it("falls back to the @ trigger when both could match", () => {
    const text = "/review @sess";
    const match = findActiveTrigger(text, text.length, COMPOSER_TRIGGER_SPECS);
    expect(match?.trigger).toBe("@");
  });

  it("inserts a slash command at the active range and adds trailing space", () => {
    const text = "/rev";
    const match = findActiveTrigger(text, text.length, COMPOSER_TRIGGER_SPECS)!;
    const inserted = insertCommandAtCaret(text, match, reviewCommand);
    expect(inserted.text).toBe("/review ");
    expect(inserted.caret).toBe("/review ".length);
  });

  it("syncs commands with the message text", () => {
    expect(syncCommandsWithMessage("/review focus on auth", [reviewCommand, clearCommand])).toEqual([reviewCommand]);
  });

  it("keeps the same commands array when no commands are removed", () => {
    const commands = [reviewCommand];

    expect(syncCommandsWithMessage("/review focus on auth", commands)).toBe(commands);
  });

  it("drops commands that only appear mid-line", () => {
    expect(syncCommandsWithMessage("Please /review focus on auth", [reviewCommand])).toEqual([]);
  });

  it("removes a command from the message when its chip is deleted", () => {
    expect(removeCommandReference("/review focus on auth", reviewCommand)).toBe("focus on auth");
  });

  it("backwards-compatible findActiveMention still detects @ triggers", () => {
    const text = "Inspect @sess";
    expect(findActiveMention(text, text.length)).toEqual({
      start: 8,
      end: 13,
      query: "sess",
    });
  });

  it("findActiveMention does not detect slash triggers", () => {
    expect(findActiveMention("/rev", 4)).toBeNull();
  });
});
