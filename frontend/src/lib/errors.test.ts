import { describe, expect, it, vi } from "vitest";
import { captureError, captureMessage } from "./errors";
import * as Sentry from "@sentry/nextjs";

vi.mock("@sentry/nextjs", () => ({
  captureException: vi.fn(),
  captureMessage: vi.fn(),
}));

describe("captureError", () => {
  it("forwards error and tags to Sentry.captureException", () => {
    const err = new Error("test");
    captureError(err, { feature: "test-feature" });

    expect(Sentry.captureException).toHaveBeenCalledWith(err, {
      tags: { feature: "test-feature" },
    });
  });

  it("works without tags", () => {
    const err = new Error("no tags");
    captureError(err);

    expect(Sentry.captureException).toHaveBeenCalledWith(err, {
      tags: undefined,
    });
  });
});

describe("captureMessage", () => {
  it("forwards message and tags to Sentry.captureMessage", () => {
    captureMessage("something unexpected", { endpoint: "/api/test" });

    expect(Sentry.captureMessage).toHaveBeenCalledWith("something unexpected", {
      tags: { endpoint: "/api/test" },
    });
  });
});
