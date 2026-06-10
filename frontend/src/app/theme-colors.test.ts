import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import { describe, expect, it } from "vitest";

const cssPath = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "globals.css");

function tokenValue(css: string, selector: string, token: string): string {
  const selectorStart = css.indexOf(`${selector} {`);
  expect(selectorStart, `${selector} should define theme tokens`).toBeGreaterThanOrEqual(0);

  const blockStart = css.indexOf("{", selectorStart);
  const blockEnd = css.indexOf("\n}", blockStart);
  expect(blockEnd, `${selector} token block should be closed`).toBeGreaterThan(blockStart);

  const block = css.slice(blockStart + 1, blockEnd);
  const match = block.match(new RegExp(`\\s${token}:\\s*([^;]+);`));
  expect(match, `${token} should be defined in ${selector}`).not.toBeNull();
  return match![1].trim();
}

describe("theme color tokens", () => {
  it("uses GitHub's open pull request green for the light success token", () => {
    const css = readFileSync(cssPath, "utf8");

    expect(tokenValue(css, ":root", "--success"), "light success should match GitHub's #1f883d open PR emphasis color in OKLCH").toBe("oklch(0.552 0.145 148.215)");
    expect(tokenValue(css, "@theme inline", "--color-success"), "Tailwind success should resolve through the semantic palette token").toBe("var(--success)");
  });
});
