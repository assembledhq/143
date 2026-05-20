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

  it("keeps hot-path frontend tests fast and reserves coverage for scheduled/manual runs", () => {
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

    const affectedTestsStep = frontendTestJob.match(
      /      - name: Run affected tests \(PR\)\n[\s\S]*?(?=\n      - name:|\n  [a-z0-9-]+:|\n$)/
    )?.[0];
    const fullMainStep = frontendTestJob.match(
      /      - name: Run full tests \(main\)\n[\s\S]*?(?=\n      - name:|\n  [a-z0-9-]+:|\n$)/
    )?.[0];
    const fullCoverageStep = frontendTestJob.match(
      /      - name: Run full tests with coverage \(scheduled\/manual\)\n[\s\S]*?(?=\n      - name:|\n  [a-z0-9-]+:|\n$)/
    )?.[0];

    expect(affectedTestsStep).toBeDefined();
    expect(affectedTestsStep).toContain("--changed=pr-base");
    expect(affectedTestsStep).not.toContain("--coverage");
    expect(frontendTestJob).not.toContain("diff-cover");

    expect(fullMainStep).toBeDefined();
    expect(fullMainStep).toContain("if: github.event_name == 'push'");
    expect(fullMainStep).not.toContain("--coverage");

    expect(fullCoverageStep).toBeDefined();
    expect(fullCoverageStep).toContain("github.event_name == 'schedule'");
    expect(fullCoverageStep).toContain("github.event_name == 'workflow_dispatch'");
    expect(fullCoverageStep).toContain("--coverage");
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
});
