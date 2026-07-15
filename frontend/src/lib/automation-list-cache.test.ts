import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import {
  removeAutomationFromListCaches,
  upsertAutomationInListCaches,
} from "./automation-list-cache";
import { queryKeys } from "./query-keys";
import type { Automation, ListResponse } from "./types";

function makeAutomation(overrides: Partial<Automation> = {}): Automation {
  return {
    id: "automation-1",
    org_id: "org-1",
    repository_id: "repo-1",
    name: "Automation",
    goal: "Do recurring work.",
    icon_type: "emoji",
    icon_value: "⚙️",
    execution_mode: "sequential",
    max_concurrent: 1,
    base_branch: "main",
    identity_scope: "org",
    publish_policy: "pull_request",
    pre_pr_review_loops: 1,
    schedule_type: "interval",
    interval_value: 1,
    interval_unit: "days",
    interval_run_at: "09:00",
    timezone: "UTC",
    enabled: true,
    priority: 50,
    github_event_triggers: [],
    created_at: "2026-03-04T12:00:00Z",
    updated_at: "2026-03-04T12:00:00Z",
    ...overrides,
  };
}

describe("upsertAutomationInListCaches", () => {
  it("updates existing automations in cached list responses", () => {
    const queryClient = new QueryClient();
    const existing = makeAutomation();
    const other = makeAutomation({ id: "automation-2", name: "Other" });
    const updated = makeAutomation({
      name: "Updated automation",
      enabled: false,
      updated_at: "2026-03-05T12:00:00Z",
    });

    queryClient.setQueryData<ListResponse<Automation>>(
      queryKeys.automations.all,
      { data: [existing, other], meta: {} },
    );

    upsertAutomationInListCaches(queryClient, updated);

    expect(
      queryClient.getQueryData<ListResponse<Automation>>(
        queryKeys.automations.all,
      )?.data,
    ).toEqual([updated, other]);
  });

  it("does not touch caches that merely share the automations key prefix", () => {
    const queryClient = new QueryClient();
    const created = makeAutomation({ id: "automation-created" });
    const eventTriggersKey = queryKeys.automations.eventTriggers("automation-1");
    const eventTriggers = { data: [{ id: "trigger-1" }], meta: {} };

    queryClient.setQueryData(eventTriggersKey, eventTriggers);

    upsertAutomationInListCaches(queryClient, created, {
      prependIfMissing: true,
    });

    expect(queryClient.getQueryData(eventTriggersKey)).toEqual(eventTriggers);
  });

  it("prepends missing automations only when requested", () => {
    const queryClient = new QueryClient();
    const existing = makeAutomation({ id: "automation-existing" });
    const created = makeAutomation({ id: "automation-created" });

    queryClient.setQueryData<ListResponse<Automation>>(
      queryKeys.automations.all,
      { data: [existing], meta: {} },
    );

    upsertAutomationInListCaches(queryClient, created);
    expect(
      queryClient.getQueryData<ListResponse<Automation>>(
        queryKeys.automations.all,
      )?.data,
    ).toEqual([existing]);

    upsertAutomationInListCaches(queryClient, created, {
      prependIfMissing: true,
    });
    expect(
      queryClient.getQueryData<ListResponse<Automation>>(
        queryKeys.automations.all,
      )?.data,
    ).toEqual([created, existing]);
  });
});

describe("removeAutomationFromListCaches", () => {
  it("removes deleted automations from cached list responses", () => {
    const queryClient = new QueryClient();
    const deleted = makeAutomation({ id: "automation-deleted" });
    const other = makeAutomation({ id: "automation-2", name: "Other" });

    queryClient.setQueryData<ListResponse<Automation>>(
      queryKeys.automations.all,
      { data: [deleted, other], meta: {} },
    );
    queryClient.setQueryData(queryKeys.automations.detail(deleted.id), {
      data: deleted,
    });

    removeAutomationFromListCaches(queryClient, deleted.id);

    expect(
      queryClient.getQueryData<ListResponse<Automation>>(
        queryKeys.automations.all,
      )?.data,
    ).toEqual([other]);
    expect(
      queryClient.getQueryData(queryKeys.automations.detail(deleted.id)),
    ).toEqual({ data: deleted });
  });
});
