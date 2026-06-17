import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

describe("SessionDetail route loading shell", () => {
  it("does not define a route-level loading fallback for session-to-session navigation", () => {
    const routeDir = dirname(fileURLToPath(import.meta.url));

    expect(
      existsSync(join(routeDir, "loading.tsx")),
      "the [id]/loading.tsx fallback replaces the current session with a skeleton during /sessions/:id -> /sessions/:id navigation",
    ).toBe(false);
  });
});
