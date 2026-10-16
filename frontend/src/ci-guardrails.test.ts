import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const frontendDir = path.resolve(import.meta.dirname, "..");
const repoRoot = path.resolve(frontendDir, "..");

describe("frontend CI guardrails", () => {
  it("fails lint on warnings without duplicating lint in the CI test script", () => {
    const packageJson = JSON.parse(
      fs.readFileSync(path.join(frontendDir, "package.json"), "utf8")
    ) as {
      scripts?: Record<string, string>;
    };

    expect(packageJson.scripts?.lint).toContain("--max-warnings=0");
    expect(packageJson.scripts?.test).toBe("vitest");
    expect(packageJson.scripts?.["test:ci"]).toBe("vitest run --reporter=dot");
  });

  it("enforces shared control density through ESLint", () => {
    const eslintConfig = fs.readFileSync(
      path.join(frontendDir, "eslint.config.mjs"),
      "utf8"
    );

    expect(eslintConfig).toContain('"custom/no-ad-hoc-control-sizing": "error"');
    expect(eslintConfig).toContain("no-ad-hoc-control-sizing.mjs");
  });

  it("does not run duplicate lint in the frontend test workflow", () => {
    const workflow = fs.readFileSync(
      path.join(repoRoot, ".github", "workflows", "ci.yml"),
      "utf8"
    );
    const frontendTestJob = workflow.match(
      /  frontend-test:\n[\s\S]*?(?=\n  [a-z0-9-]+:|\n$)/
    )?.[0];

    expect(frontendTestJob).toBeDefined();
    if (!frontendTestJob) {
      throw new Error("frontend-test job should exist in CI workflow");
    }
    expect(frontendTestJob).not.toContain("- run: npm run lint");
    expect(frontendTestJob).toContain("npx vitest run");
  });

  it("requires full coverage for shared inputs while retaining affected leaf tests", () => {
    const workflow = fs.readFileSync(
      path.join(repoRoot, ".github", "workflows", "ci.yml"),
      "utf8"
    );
    const frontendTestJob = workflow.match(
      /  frontend-test:\n[\s\S]*?(?=\n  [a-z0-9-]+:|\n$)/
    )?.[0];
    expect(frontendTestJob).toBeDefined();
    if (!frontendTestJob) {
      throw new Error("frontend-test job should exist in CI workflow");
    }
    const step = (name: string) => {
      const content = frontendTestJob.split(`      - name: ${name}\n`)[1]?.split("\n      - name:")[0];
      expect(content).toBeDefined();
      return content ?? "";
    };

    const scopeStep = step("Select frontend test scope");
    expect(scopeStep).toContain("id: scope");
    expect(scopeStep).toContain("python3 -B .github/scripts/select_frontend_tests.py");
    expect(scopeStep).toContain("github.event.pull_request.base.sha");
    expect(scopeStep).toContain("github.event.before");

    const affectedStep = step("Run affected tests");
    expect(affectedStep).toContain("if: steps.scope.outputs.full == 'false'");
    expect(affectedStep).toContain("steps.scope.outputs.base");
    expect(affectedStep).toContain('--changed="$FRONTEND_TEST_BASE"');
    expect(affectedStep).not.toContain("--coverage");

    const fullCoverageStep = step("Run full tests with coverage");
    expect(fullCoverageStep).toContain("if: steps.scope.outputs.full == 'true'");
    expect(fullCoverageStep).toContain("--coverage");
    expect(fullCoverageStep).not.toContain("--passWithNoTests");
    expect(fullCoverageStep).not.toContain("--changed");
    expect(step("Check frontend coverage floor")).toContain("if: steps.scope.outputs.full == 'true'");
    expect(step("Check frontend coverage floor")).toContain("FRONTEND_COVERAGE_MIN");
    expect(step("Upload coverage report")).toContain("steps.scope.outputs.full == 'true'");
    expect(frontendTestJob).not.toContain("diff-cover");

    // Keep the twice-daily backstop even when a leaf's import graph misses a
    // dependency. The selector's behavior is exercised by Python Git fixtures.
    const cronLines = workflow.match(/- cron: "[^"]+"/g) ?? [];
    expect(cronLines.length).toBeGreaterThan(0);
    for (const line of cronLines) {
      const hourField = line.match(/- cron: "\S+ (\S+) /)?.[1] ?? "";
      expect(hourField.split(",").length).toBeGreaterThanOrEqual(2);
    }
  });

  it("splits Vitest tests into node and jsdom projects", () => {
    const config = fs.readFileSync(
      path.join(frontendDir, "vitest.config.ts"),
      "utf8"
    );

    expect(config).toContain("name: 'node'");
    expect(config).toContain("environment: 'node'");
    expect(config).toContain("name: 'jsdom'");
    expect(config).toContain("environment: 'jsdom'");
  });

  it("imports precompiled Tailwind CSS before app Tailwind utilities", () => {
    const globals = fs.readFileSync(
      path.join(frontendDir, "src", "app", "globals.css"),
      "utf8"
    );

    const fumadocsStyleImport = globals.indexOf('@import "fumadocs-ui/style.css";');
    const tailwindImport = globals.indexOf('@import "tailwindcss";');

    expect(fumadocsStyleImport).toBeGreaterThanOrEqual(0);
    expect(tailwindImport).toBeGreaterThanOrEqual(0);
    expect(fumadocsStyleImport).toBeLessThan(tailwindImport);
  });
});
