import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { ExternalIdentitiesCard } from "./external-identities-card";

describe("ExternalIdentitiesCard", () => {
  it("shows connected identities and their trust source", async () => {
    server.use(http.get("/api/v1/users/me/external-identities", () => HttpResponse.json({ data: [{ id: "link-1", org_id: "org-1", provider: "slack", provider_workspace_id: "T1", provider_user_id: "U1", user_id: "user-1", source: "self_linked", status: "active", confidence: 100, external_display_name: "Alice", created_at: new Date().toISOString() }], meta: {} })));
    renderWithProviders(<ExternalIdentitiesCard />);
    expect(await screen.findByText("Alice")).toBeInTheDocument();
    expect(screen.getByText("Connected by user")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Disconnect/ })).toBeEnabled();
  });

  it("shows admin suggestions and unmapped actors", async () => {
    server.use(
      http.get("/api/v1/integrations/external-user-link-suggestions", () => HttpResponse.json({ data: [{ id: "suggestion-1", org_id: "org-1", provider: "linear", provider_workspace_id: "W1", provider_user_id: "L1", suggested_user_id: "user-1", reason: "profile_hint", confidence: 40, external_display_name: "Alice L", last_seen_at: new Date().toISOString() }], meta: {} })),
      http.get("/api/v1/integrations/external-unmapped-users", () => HttpResponse.json({ data: [{ id: "actor-1", org_id: "org-1", provider: "slack", provider_workspace_id: "T1", provider_user_id: "U2", external_display_name: "Bob", last_seen_at: new Date().toISOString() }], meta: {} })),
    );
    renderWithProviders(<ExternalIdentitiesCard admin members={[{ id: "user-1", org_id: "org-1", email: "alice@example.com", name: "Alice", role: "member", created_at: new Date().toISOString() }]} />);
    expect(await screen.findByText(/Alice L/)).toBeInTheDocument();
    expect(await screen.findByText("Bob")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
  });
});
