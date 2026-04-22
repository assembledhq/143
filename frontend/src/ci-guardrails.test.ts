import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const frontendDir = path.resolve(import.meta.dirname, "..");
const repoRoot = path.resolve(frontendDir, "..");

describe("frontend CI guardrails", () => {
  it("fails lint on warnings and runs lint in the CI test script", () => {
    const packageJson = JSON.parse(
      fs.readFileSync(path.join(frontendDir, "package.json"), "utf8")
    ) as {
      scripts?: Record<string, string>;
    };

    expect(packageJson.scripts?.lint).toContain("--max-warnings=0");
    expect(packageJson.scripts?.test).toMatch(/^npm run lint && /);
    expect(packageJson.scripts?.["test:ci"]).toMatch(/^npm run lint && /);
  });

  it("runs lint in the frontend test workflow before Vitest", () => {
    const workflow = fs.readFileSync(
      path.join(repoRoot, ".github", "workflows", "ci.yml"),
      "utf8"
    );

    expect(workflow).toContain("frontend-test:");
    expect(workflow).toContain("- run: npm run lint");
  });
});
