import { describe, expect, it } from "vitest";

import type { CodingCredentialSummary, OpenCodeModelInfo } from "@/lib/types";
import { openCodeAvailabilityById, runnableOpenCodeBackings } from "./use-opencode-models";

function cred(partial: Partial<CodingCredentialSummary>): CodingCredentialSummary {
  return {
    id: "id",
    org_id: "org",
    scope: "org",
    priority: 0,
    agent: "opencode",
    auth_type: "api_key",
    provider: "openrouter",
    label: "key",
    status: "healthy",
    is_default: false,
    created_at: "",
    updated_at: "",
    ...partial,
  } as CodingCredentialSummary;
}

const GLM: OpenCodeModelInfo = {
  id: "glm-5.2",
  display_name: "GLM 5.2",
  routes: [
    { backing: "openrouter", transport_label: "OpenRouter", physical_model_id: "openrouter/z-ai/glm-5.2" },
    { backing: "opencode", transport_label: "OpenCode native", physical_model_id: "opencode/glm-5.2" },
  ],
};

describe("runnableOpenCodeBackings", () => {
  it("collects healthy/rate-limited opencode backings only", () => {
    const backings = runnableOpenCodeBackings([
      cred({ provider: "openrouter", status: "healthy" }),
      cred({ provider: "opencode", status: "rate_limited" }),
      cred({ provider: "openai", status: "needs_reauth" }), // excluded
      cred({ provider: "anthropic", agent: "claude_code" }), // wrong agent
    ]);
    expect(backings).toEqual(new Set(["openrouter", "opencode"]));
  });
});

describe("openCodeAvailabilityById", () => {
  it("prefers the first available route for the transport badge", () => {
    const map = openCodeAvailabilityById([GLM], new Set(["openrouter", "opencode"]), false);
    expect(map.get("glm-5.2")).toEqual({ hasRunnableRoute: true, transportLabel: "OpenRouter" });
    // physical ids resolve the same way (pinned selections)
    expect(map.get("opencode/glm-5.2")?.transportLabel).toBe("OpenRouter");
  });

  it("falls back to native when only a native key exists", () => {
    const map = openCodeAvailabilityById([GLM], new Set(["opencode"]), false);
    expect(map.get("glm-5.2")).toEqual({ hasRunnableRoute: true, transportLabel: "OpenCode native" });
  });

  it("disables when no route has a runnable credential", () => {
    const map = openCodeAvailabilityById([GLM], new Set(["openai"]), false);
    expect(map.get("glm-5.2")).toEqual({ hasRunnableRoute: false, transportLabel: null });
  });

  it("excludes the native route when RequireOpenRouter is set", () => {
    const map = openCodeAvailabilityById([GLM], new Set(["opencode"]), true);
    expect(map.get("glm-5.2")).toEqual({ hasRunnableRoute: false, transportLabel: null });
  });
});
