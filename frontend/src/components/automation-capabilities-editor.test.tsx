import { describe, expect, it } from "vitest";

import type { AgentCapabilityDefinition, AgentCapabilityID } from "@/lib/types";

import {
  RECOMMENDED_DEFAULT_CAPABILITY_IDS,
  normalizeCapabilityGrants,
  recommendedDefaultGrants,
} from "./automation-capabilities-editor";

function def(
  id: AgentCapabilityID,
  overrides: Partial<AgentCapabilityDefinition> = {},
): AgentCapabilityDefinition {
  return {
    id,
    display_name: id,
    description: `${id} description`,
    category: "Context",
    max_access_level: "read",
    risk: "low",
    scope: "repository",
    ...overrides,
  };
}

// A trimmed catalog covering one of each access level plus a non-default entry,
// so the test stays meaningful without hardcoding the full production catalog.
const CATALOG: AgentCapabilityDefinition[] = [
  def("repo_context"),
  def("publishing", { max_access_level: "publish", risk: "high", category: "Actions" }),
  def("issue_sources", { risk: "medium", scope: "integration" }),
  def("production_diagnostics", { risk: "high", category: "Diagnostics" }),
];

describe("recommendedDefaultGrants", () => {
  it("enables exactly the recommended default capability ids", () => {
    const grants = recommendedDefaultGrants(CATALOG);
    const enabled = grants.filter((grant) => grant.enabled).map((grant) => grant.capability_id);

    const expected = CATALOG
      .map((definition) => definition.id)
      .filter((id) => RECOMMENDED_DEFAULT_CAPABILITY_IDS.includes(id));
    expect(enabled).toEqual(expected);
  });

  it("returns a grant for every catalog entry and disables non-default ones", () => {
    const grants = recommendedDefaultGrants(CATALOG);

    expect(grants).toHaveLength(CATALOG.length);
    const byID = new Map(grants.map((grant) => [grant.capability_id, grant]));
    expect(byID.get("repo_context")?.enabled).toBe(true);
    expect(byID.get("publishing")?.enabled).toBe(true);
    // issue_sources is intentionally scoped to triggered sessions, so it must
    // not be on by default in the org-level policy.
    expect(byID.get("issue_sources")?.enabled).toBe(false);
    expect(byID.get("production_diagnostics")?.enabled).toBe(false);
  });

  it("uses each capability's max access level for its grant", () => {
    const grants = recommendedDefaultGrants(CATALOG);
    const byID = new Map(grants.map((grant) => [grant.capability_id, grant]));

    expect(byID.get("repo_context")?.access_level).toBe("read");
    expect(byID.get("publishing")?.access_level).toBe("publish");
  });
});

describe("normalizeCapabilityGrants", () => {
  it("defaults every capability to disabled when no policy is stored", () => {
    const grants = normalizeCapabilityGrants(CATALOG, []);

    expect(grants).toHaveLength(CATALOG.length);
    expect(grants.every((grant) => grant.enabled === false)).toBe(true);
  });

  it("honors stored grants and leaves capabilities absent from the policy off", () => {
    const grants = normalizeCapabilityGrants(CATALOG, [
      { capability_id: "publishing", access_level: "publish", enabled: true, config: {} },
    ]);
    const byID = new Map(grants.map((grant) => [grant.capability_id, grant]));

    expect(byID.get("publishing")?.enabled).toBe(true);
    expect(byID.get("repo_context")?.enabled).toBe(false);
  });
});
