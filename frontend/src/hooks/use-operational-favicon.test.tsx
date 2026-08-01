import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { sessionFaviconState, useOperationalFavicon } from "./use-operational-favicon";
import type { SessionStatus } from "@/lib/types";

function Harness({ status }: { status: SessionStatus }) {
  useOperationalFavicon(status);
  return null;
}

describe("sessionFaviconState", () => {
  it.each([
    ["running", "working"],
    ["pending", "working"],
    ["awaiting_input", "waiting"],
    ["needs_human_guidance", "waiting"],
    ["failed", "failed"],
    ["completed", null],
    ["idle", null],
  ] as const)("maps %s to %s", (status, expected) => {
    expect(sessionFaviconState(status), `${status} should map to browser awareness`).toBe(expected);
  });
});

describe("useOperationalFavicon", () => {
  it("replaces and removes the temporary operational favicon as state settles", () => {
    const { rerender, unmount } = render(<Harness status="running" />);
    expect(document.querySelector('[data-operational-favicon="working"]'), "working should add a blue-badged icon").not.toBeNull();

    rerender(<Harness status="awaiting_input" />);
    expect(document.querySelector('[data-operational-favicon="working"]'), "the previous state icon should be removed").toBeNull();
    expect(document.querySelector('[data-operational-favicon="waiting"]'), "waiting should add an amber-badged icon").not.toBeNull();

    unmount();
    expect(document.querySelector("[data-operational-favicon]"), "unmount should restore normal metadata icons").toBeNull();
  });
});
