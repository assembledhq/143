import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { ListResponse, SessionDetail, SessionListItem } from "./types";
import { applyCreatedSessionToSessionListCaches, applySessionDetailToSessionListCaches } from "./session-list-cache";

function makeSession(overrides: Partial<SessionListItem> = {}): SessionListItem {
  return {
    id: "session-1",
    org_id: "org-1",
    agent_type: "codex",
    status: "completed",
    autonomy_level: "semi",
    token_mode: "standard",
    current_turn: 1,
    last_activity_at: "2026-01-01T00:00:00.000Z",
    sandbox_state: "stopped",
    created_at: "2026-01-01T00:00:00.000Z",
    ...overrides,
  };
}

function makeSessionDetail(overrides: Partial<SessionDetail> = {}): SessionDetail {
  return {
    ...makeSession(overrides),
    threads: [],
    changesets: [],
    ...overrides,
  };
}

describe("applySessionDetailToSessionListCaches", () => {
  it("removes archived sessions from cached non-archived list responses", () => {
    const queryClient = new QueryClient();
    const archived = makeSessionDetail({
      archived_at: "2026-01-01T00:10:00.000Z",
    });
    const other = makeSession({ id: "session-2" });

    queryClient.setQueryData<ListResponse<SessionListItem>>(
      ["sessions", null, "filtered", "all", "mine"],
      { data: [makeSession(), other], meta: {} },
    );
    queryClient.setQueryData<ListResponse<SessionListItem>>(
      ["sessions", null, "filtered", "active", "mine"],
      { data: [makeSession(), other], meta: {} },
    );
    queryClient.setQueryData(["sessions", "counts", null, "mine"], {
      data: { all: 2, active: 0, archived: 0, cap: 100 },
    });

    applySessionDetailToSessionListCaches(queryClient, archived);

    expect(queryClient.getQueryData<ListResponse<SessionListItem>>(["sessions", null, "filtered", "all", "mine"])?.data).toEqual([other]);
    expect(queryClient.getQueryData<ListResponse<SessionListItem>>(["sessions", null, "filtered", "active", "mine"])?.data).toEqual([other]);
    expect(queryClient.getQueryData(["sessions", "counts", null, "mine"])).toEqual({
      data: { all: 2, active: 0, archived: 0, cap: 100 },
    });
  });

  it("removes archived sessions from non-archived search caches whose query text is archived", () => {
    const queryClient = new QueryClient();
    const archived = makeSessionDetail({
      archived_at: "2026-01-01T00:10:00.000Z",
    });
    const other = makeSession({ id: "session-2" });

    queryClient.setQueryData<ListResponse<SessionListItem>>(
      ["sessions", null, "filtered", "all", "mine", "archived"],
      { data: [makeSession(), other], meta: {} },
    );
    queryClient.setQueryData<ListResponse<SessionListItem>>(
      ["sessions", null, "search", "archived"],
      { data: [makeSession(), other], meta: {} },
    );
    queryClient.setQueryData<ListResponse<SessionListItem>>(
      ["sessions", null, "filtered", "archived", "mine", ""],
      { data: [makeSession(), other], meta: {} },
    );

    applySessionDetailToSessionListCaches(queryClient, archived);

    expect(queryClient.getQueryData<ListResponse<SessionListItem>>(["sessions", null, "filtered", "all", "mine", "archived"])?.data).toEqual([other]);
    expect(queryClient.getQueryData<ListResponse<SessionListItem>>(["sessions", null, "search", "archived"])?.data).toEqual([other]);
    expect(queryClient.getQueryData<ListResponse<SessionListItem>>(["sessions", null, "filtered", "archived", "mine", ""])?.data).toEqual([
      { ...makeSession(), archived_at: "2026-01-01T00:10:00.000Z", threads: undefined },
      other,
    ]);
  });
});

describe("applyCreatedSessionToSessionListCaches", () => {
  it("prepends newly-created sessions to cached non-archived list responses", () => {
    const queryClient = new QueryClient();
    const created = makeSession({ id: "session-created", status: "pending" });
    const existing = makeSession({ id: "session-existing" });

    queryClient.setQueryData<ListResponse<SessionListItem>>(
      ["sessions", null, "filtered", "all", "mine"],
      { data: [existing], meta: {} },
    );
    queryClient.setQueryData<ListResponse<SessionListItem>>(
      ["sessions", null, "filtered", "active", "mine"],
      { data: [existing], meta: {} },
    );
    queryClient.setQueryData<ListResponse<SessionListItem>>(
      ["sessions", null, "filtered", "archived", "mine"],
      { data: [makeSession({ id: "archived-session", archived_at: "2026-01-01T00:10:00.000Z" })], meta: {} },
    );

    applyCreatedSessionToSessionListCaches(queryClient, created);

    expect(queryClient.getQueryData<ListResponse<SessionListItem>>(["sessions", null, "filtered", "all", "mine"])?.data).toEqual([
      created,
      existing,
    ]);
    expect(queryClient.getQueryData<ListResponse<SessionListItem>>(["sessions", null, "filtered", "active", "mine"])?.data).toEqual([
      created,
      existing,
    ]);
    expect(queryClient.getQueryData<ListResponse<SessionListItem>>(["sessions", null, "filtered", "archived", "mine"])?.data).toEqual([
      makeSession({ id: "archived-session", archived_at: "2026-01-01T00:10:00.000Z" }),
    ]);
  });

  it("does not duplicate a session already present in cached list responses", () => {
    const queryClient = new QueryClient();
    const created = makeSession({ id: "session-created", status: "pending" });

    queryClient.setQueryData<ListResponse<SessionListItem>>(
      ["sessions", null, "filtered", "all", "mine"],
      { data: [created], meta: {} },
    );

    applyCreatedSessionToSessionListCaches(queryClient, created);

    expect(queryClient.getQueryData<ListResponse<SessionListItem>>(["sessions", null, "filtered", "all", "mine"])?.data).toEqual([
      created,
    ]);
  });
});
