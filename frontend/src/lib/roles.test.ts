import { describe, expect, it } from "vitest";

import { roleLabel } from "./roles";

describe("roleLabel", () => {
  it("labels the persisted member role as Engineer", () => {
    expect(roleLabel("member")).toBe("Engineer");
  });

  it("keeps the other role labels human-readable", () => {
    expect(roleLabel("admin")).toBe("Admin");
    expect(roleLabel("builder")).toBe("Builder");
    expect(roleLabel("viewer")).toBe("Viewer");
  });
});
