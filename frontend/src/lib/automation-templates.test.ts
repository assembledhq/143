import { describe, expect, it } from "vitest";
import {
  automationTemplateCategories,
  automationTemplates,
  featuredAutomationTemplateIDs,
  getAutomationTemplate,
} from "./automation-templates";

describe("automation template catalog", () => {
  it("defines a broad library rather than only a handful of starter prompts", () => {
    expect(automationTemplates.length).toBeGreaterThanOrEqual(10);
    expect(featuredAutomationTemplateIDs.length).toBeGreaterThanOrEqual(5);
    expect(automationTemplateCategories.length).toBeGreaterThanOrEqual(4);
  });

  it("stores richer prompt content for each template", () => {
    for (const template of automationTemplates) {
      expect(template.summary.length).toBeGreaterThan(40);
      expect(template.goal.length).toBeGreaterThan(240);
      expect(template.outcomes.length).toBeGreaterThanOrEqual(3);
      expect(template.tags.length).toBeGreaterThanOrEqual(2);
      expect(template.goal).toContain("What to do");
      expect(template.goal).toContain("Output requirements");
      expect(template.goal).toContain("Verification");
    }
  });

  it("can look up templates by id", () => {
    expect(getAutomationTemplate("security-sweep")?.name).toBe("Security sweep");
    expect(getAutomationTemplate("missing-template")).toBeUndefined();
  });

  it("guides flaky-test automations to use CI evidence before editing tests", () => {
    const template = getAutomationTemplate("flaky-tests");

    expect(template?.goal).toContain("CI/CD evidence");
    expect(template?.goal).toContain("Current GitHub PR tools");
    expect(template?.goal).toContain("do not expose flaky-test signals or check-run logs directly");
    expect(template?.goal).toContain("CircleCI");
    expect(template?.goal).toContain("same commit");
    expect(template?.goal).toContain("14-day window");
    expect(template?.goal).toContain("Do not classify a test as flaky from one failed run alone");
  });

  it("guides missing-index automations toward evidence-backed database changes", () => {
    const template = getAutomationTemplate("missing-indexes");

    expect(template?.name).toBe("Check for missing indexes");
    expect(template?.goal).toContain("recently added or substantially changed database queries");
    expect(template?.goal).toContain("last 7 days of commits");
    expect(template?.goal).toContain("open a focused index migration when the evidence is strong");
    expect(template?.goal).toContain("query text, call path, tables involved, filters, joins, ordering, limits, and expected cardinality");
    expect(template?.goal).toContain("missing tenant or ownership scoping");
    expect(template?.goal).toContain("EXPLAIN");
    expect(template?.goal).toContain("measured with representative data");
    expect(template?.goal).toContain("schema-only inference");
    expect(template?.goal).toContain("migration");
    expect(template?.goal).toContain("CREATE INDEX CONCURRENTLY");
    expect(template?.goal).toContain("Do not add indexes for tiny tables");
    expect(template?.goal).toContain("low-cardinality booleans");
    expect(template?.goal).toContain("query rewrite, pagination, predicate order, or existing-index alignment");
    expect(template?.goal).toContain("read-heavy, write-heavy, or mixed");
    expect(template?.outcomes).toContain("Index recommendations tied to specific recent queries");
    expect(template?.tags).toContain("database");
    expect(template?.defaultUnit).toBe("weeks");
  });
});
