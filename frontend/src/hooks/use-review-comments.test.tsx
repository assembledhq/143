import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ListResponse, SessionReviewComment, SingleResponse } from "@/lib/types";
import { api } from "@/lib/api";
import { useReviewComments } from "./use-review-comments";

vi.mock("@/lib/api", () => ({
  api: {
    sessions: {
      listReviewComments: vi.fn(),
      createReviewComment: vi.fn(),
      updateReviewComment: vi.fn(),
      deleteReviewComment: vi.fn(),
    },
  },
}));

function makeWrapper(client: QueryClient) {
  const Wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  Wrapper.displayName = "TestQueryClientProvider";
  return Wrapper;
}

function makeComment(overrides: Partial<SessionReviewComment> = {}): SessionReviewComment {
  return {
    id: "comment-1",
    session_id: "session-1",
    org_id: "org-1",
    user_id: "user-1",
    file_path: "src/app.ts",
    line_number: 12,
    diff_side: "new",
    body: "Saved comment",
    resolved: false,
    pass_number: 1,
    created_at: "2026-06-11T00:00:00.000Z",
    updated_at: "2026-06-11T00:00:00.000Z",
    ...overrides,
  };
}

describe("useReviewComments", () => {
  let queryClient: QueryClient;
  const mockedAPI = vi.mocked(api.sessions);

  beforeEach(() => {
    queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    });
    vi.clearAllMocks();
  });

  it("shows a submitted comment before the create request finishes", async () => {
    let resolveCreate: (value: SingleResponse<SessionReviewComment>) => void = () => {};
    let serverComments: SessionReviewComment[] = [];
    mockedAPI.listReviewComments.mockImplementation(async () => ({
      data: serverComments,
      meta: {},
    } satisfies ListResponse<SessionReviewComment>));
    mockedAPI.createReviewComment.mockReturnValue(
      new Promise((resolve) => {
        resolveCreate = resolve;
      }),
    );

    const { result } = renderHook(() => useReviewComments("session-1"), {
      wrapper: makeWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.comments).toEqual([]));

    act(() => {
      result.current.createComment({
        file_path: "src/app.ts",
        line_number: 12,
        side: "new",
        body: "Saved comment",
      });
    });

    await waitFor(() =>
      expect(result.current.comments).toEqual([
        expect.objectContaining({
          file_path: "src/app.ts",
          line_number: 12,
          diff_side: "new",
          body: "Saved comment",
          resolved: false,
        }),
      ]),
    );
    expect(result.current.comments[0]?.id).toMatch(/^pending-/);

    act(() => {
      serverComments = [makeComment()];
      resolveCreate({ data: serverComments[0] });
    });

    await waitFor(() => expect(result.current.comments[0]?.id).toBe("comment-1"));
    expect(mockedAPI.createReviewComment).toHaveBeenCalledTimes(1);
  });

  it("removes the optimistic comment when the create request fails", async () => {
    let rejectCreate!: (reason: Error) => void;
    mockedAPI.listReviewComments.mockResolvedValue({ data: [], meta: {} } satisfies ListResponse<SessionReviewComment>);
    mockedAPI.createReviewComment.mockReturnValue(
      new Promise((_resolve, reject) => {
        rejectCreate = reject;
      }),
    );

    const { result } = renderHook(() => useReviewComments("session-1"), {
      wrapper: makeWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.comments).toEqual([]));

    act(() => {
      result.current.createComment({
        file_path: "src/app.ts",
        line_number: 12,
        side: "new",
        body: "Optimistic comment",
      });
    });

    await waitFor(() => expect(result.current.comments).toHaveLength(1));
    expect(result.current.comments[0]?.id).toMatch(/^pending-/);

    act(() => {
      rejectCreate(new Error("server error"));
    });

    await waitFor(() => expect(result.current.comments).toHaveLength(0));
  });
});
