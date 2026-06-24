import { describe, expect, it } from "vitest";

import { formatAuditDetailValue } from "./audit-details";

describe("formatAuditDetailValue", () => {
  it("formats membership role details with product labels", () => {
    expect(formatAuditDetailValue("role", "member")).toBe("Engineer");
    expect(formatAuditDetailValue("previous_role", "viewer")).toBe("Viewer");
  });

  it("leaves non-role strings unchanged", () => {
    expect(formatAuditDetailValue("status", "member")).toBe("member");
    expect(formatAuditDetailValue("role", "owner")).toBe("owner");
  });

  it("serializes object values for display", () => {
    expect(formatAuditDetailValue("metadata", { repo: "app", count: 2 })).toBe(
      JSON.stringify({ repo: "app", count: 2 }),
    );
  });

  it("stringifies primitive values", () => {
    expect(formatAuditDetailValue("enabled", true)).toBe("true");
    expect(formatAuditDetailValue("attempts", 3)).toBe("3");
    expect(formatAuditDetailValue("deleted_at", null)).toBe("null");
  });
});
