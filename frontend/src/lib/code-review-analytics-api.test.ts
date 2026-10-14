import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";
import { setActiveOrgId } from "./active-org";
import { api } from "./api";

describe("Analytics trend requests", () => {
  it.each([
    { params: {}, expected: {} },
    { params: { include_trend: false }, expected: { include_trend: "false" } },
    {
      params: { include_trend: true, created_after: "2026-10-02T18:00:00Z", trend_span_seconds: 604_800 },
      expected: { include_trend: "true", created_after: "2026-10-02T18:00:00Z", trend_span_seconds: "604800" },
    },
    {
      params: {
        include_trend: true,
        created_after: "2026-10-01T04:00:00Z",
        created_before: "2026-10-10T03:59:59.999Z",
        trend_current_start: "2026-10-01T04:00:00Z",
        trend_current_end: "2026-11-01T04:00:00Z",
        trend_previous_start: "2026-09-01T04:00:00Z",
        trend_previous_end: "2026-10-01T04:00:00Z",
      },
      expected: {
        include_trend: "true",
        created_after: "2026-10-01T04:00:00Z",
        created_before: "2026-10-10T03:59:59.999Z",
        trend_current_start: "2026-10-01T04:00:00Z",
        trend_current_end: "2026-11-01T04:00:00Z",
        trend_previous_start: "2026-09-01T04:00:00Z",
        trend_previous_end: "2026-10-01T04:00:00Z",
      },
    },
  ])("encodes only requested options: $expected", async ({ params, expected }) => {
    let captured: Request | undefined;
    setActiveOrgId("org-chart-test");
    server.use(http.get("/api/v1/code-reviews/analytics", ({ request }) => {
      captured = request;
      return HttpResponse.json({ data: { marker: "report" } });
    }));
    try {
      expect(await api.codeReviews.analytics(params)).toEqual({ data: { marker: "report" } });
      expect(Object.fromEntries(new URL(captured!.url).searchParams)).toEqual(expected);
      expect(captured!.headers.get("X-Active-Org-ID")).toBe("org-chart-test");
    } finally {
      setActiveOrgId(null);
    }
  });
});
