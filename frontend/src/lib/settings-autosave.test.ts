import { describe, expect, it } from "vitest";
import { applyOrgSettingsPatch, coalesceSettingsPatch } from "./settings-autosave";
import type { Organization, SingleResponse } from "./types";

function orgResponse(settings: Organization["settings"]): SingleResponse<Organization> {
  return {
    data: {
      id: "org-1",
      name: "Test Org",
      settings,
      created_at: "2026-05-01T12:00:00Z",
      updated_at: "2026-05-01T12:00:00Z",
    },
  };
}

describe("settings autosave helpers", () => {
  it("deep-merges nested runtime settings when applying optimistic patches", () => {
    const previous = orgResponse({
      sandbox_lifecycle: {
        completed_session_retention_minutes: 120,
        idle_preview_ttl_minutes: 300,
        preview_holds_sandbox: false,
      },
      sandbox_resources: {
        agent_default_tier: "standard",
        preview_default_tier: "small",
        allow_repo_resource_requests: false,
        preview_max_tier: "large",
      },
    });

    const actual = applyOrgSettingsPatch(previous, {
      settings: {
        sandbox_lifecycle: { completed_session_retention_minutes: 90 },
        sandbox_resources: { agent_default_tier: "large" },
      },
    });

    expect(actual).toEqual(
      orgResponse({
        sandbox_lifecycle: {
          completed_session_retention_minutes: 90,
          idle_preview_ttl_minutes: 300,
          preview_holds_sandbox: false,
        },
        sandbox_resources: {
          agent_default_tier: "large",
          preview_default_tier: "small",
          allow_repo_resource_requests: false,
          preview_max_tier: "large",
        },
      }),
    );
  });

  it("coalesces nested runtime patches without dropping sibling fields", () => {
    const actual = coalesceSettingsPatch(
      { settings: { sandbox_lifecycle: { completed_session_retention_minutes: 90 } } },
      { settings: { sandbox_lifecycle: { preview_holds_sandbox: true } } },
    );

    expect(actual).toEqual({
      settings: {
        sandbox_lifecycle: {
          completed_session_retention_minutes: 90,
          preview_holds_sandbox: true,
        },
      },
    });
  });
});
