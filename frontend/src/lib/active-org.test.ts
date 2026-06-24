import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ACTIVE_ORG_CHANGED_EVENT,
  getActiveOrgId,
  setActiveOrgId,
} from "./active-org";

const originalSessionStorage = Object.getOwnPropertyDescriptor(window, "sessionStorage");

function restoreSessionStorage(): void {
  if (originalSessionStorage) {
    Object.defineProperty(window, "sessionStorage", originalSessionStorage);
  }
}

describe("active org storage", () => {
  afterEach(() => {
    restoreSessionStorage();
    window.sessionStorage.clear();
  });

  it("stores and reads the active org from tab-local session storage", () => {
    setActiveOrgId("org-1");

    expect(getActiveOrgId()).toBe("org-1");
  });

  it("removes the active org when set to null", () => {
    setActiveOrgId("org-1");
    setActiveOrgId(null);

    expect(getActiveOrgId()).toBeNull();
  });

  it("dispatches an active-org-changed event after storage updates", () => {
    const listener = vi.fn();
    window.addEventListener(ACTIVE_ORG_CHANGED_EVENT, listener);

    setActiveOrgId("org-2");

    expect(listener).toHaveBeenCalledTimes(1);
    expect(listener.mock.calls[0]?.[0]).toMatchObject({
      detail: { id: "org-2" },
    });

    window.removeEventListener(ACTIVE_ORG_CHANGED_EVENT, listener);
  });

  it("returns null when session storage cannot be read", () => {
    Object.defineProperty(window, "sessionStorage", {
      configurable: true,
      value: {
        getItem: vi.fn(() => {
          throw new Error("blocked");
        }),
      },
    });

    expect(getActiveOrgId()).toBeNull();
  });

  it("does not dispatch when session storage cannot be written", () => {
    const listener = vi.fn();
    window.addEventListener(ACTIVE_ORG_CHANGED_EVENT, listener);
    Object.defineProperty(window, "sessionStorage", {
      configurable: true,
      value: {
        setItem: vi.fn(() => {
          throw new Error("blocked");
        }),
      },
    });

    setActiveOrgId("org-3");

    expect(listener).not.toHaveBeenCalled();
    window.removeEventListener(ACTIVE_ORG_CHANGED_EVENT, listener);
  });
});
