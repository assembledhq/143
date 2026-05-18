import { describe, expect, it } from "vitest";
import { NextRequest } from "next/server";
import { config, middleware } from "./middleware";

function requestFor(path: string, cookie?: string) {
  return new NextRequest(`https://143.dev${path}`, {
    headers: cookie ? { cookie } : undefined,
  });
}

describe("middleware", () => {
  it("runs on the website homepage so authenticated sessions land in the app", () => {
    expect(config.matcher).toContain("/");
  });

  it("redirects authenticated homepage requests to sessions", () => {
    const response = middleware(requestFor("/", "session_token=token-123"));

    expect(response.status).toBe(307);
    expect(response.headers.get("location")).toBe("https://143.dev/sessions");
  });

  it("allows unauthenticated homepage requests through", () => {
    const response = middleware(requestFor("/"));

    expect(response.headers.get("location")).toBeNull();
  });
});
