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
});
