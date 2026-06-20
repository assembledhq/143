import { describe, it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { VerifiedDomainsSection } from "./verified-domains-section";

interface DomainFixture {
  id: string;
  domain: string;
  status: "pending" | "verified";
  auto_join_enabled?: boolean;
  failed_checks?: number;
}

function mockDomains(domains: DomainFixture[]) {
  const enriched = domains.map((d) => ({
    org_id: "org-1",
    auto_join_enabled: true,
    created_at: new Date().toISOString(),
    verification_token: "tok123",
    failed_checks: 0,
    dns_record_name: `_143-verify.${d.domain}`,
    dns_record_value: "143-domain-verify=tok123",
    ...d,
  }));
  server.use(
    http.get("/api/v1/team/domains", () => HttpResponse.json({ data: enriched, meta: {} })),
    http.get("/api/v1/team/github-orgs", () => HttpResponse.json({ github_orgs: [] })),
    http.get("/api/v1/team/github/status", () => HttpResponse.json({ data: { connected: false } })),
  );
}

describe("VerifiedDomainsSection", () => {
  it("renders the empty state when no domains are claimed", async () => {
    mockDomains([]);
    renderWithProviders(<VerifiedDomainsSection />);

    expect(await screen.findByText(/People who match these rules join as members automatically/)).toBeInTheDocument();
    expect(await screen.findByText(/No domains yet/)).toBeInTheDocument();
  });

  it("keeps the add-domain controls the same height", async () => {
    mockDomains([]);
    renderWithProviders(<VerifiedDomainsSection />);

    const input = await screen.findByLabelText("Domain to verify");
    const button = screen.getByRole("button", { name: "Add domain" });

    expect(input).toHaveClass("h-9");
    expect(button).toHaveClass("h-9");
  });

  it("does not ask connected GitHub users to reconnect when no organization installation is available", async () => {
    mockDomains([]);
    server.use(
      http.get("/api/v1/team/github/status", () => HttpResponse.json({ data: { connected: true } })),
    );

    renderWithProviders(<VerifiedDomainsSection />);

    expect(await screen.findByText(/GitHub is connected, but the app isn't installed on a GitHub organization/)).toBeInTheDocument();
    expect(screen.queryByText(/Connect GitHub to enable organization-based auto-join/)).not.toBeInTheDocument();
  });

  it("shows the DNS record instructions for a pending domain", async () => {
    mockDomains([{ id: "d1", domain: "assembledhq.com", status: "pending" }]);
    renderWithProviders(<VerifiedDomainsSection />);

    expect(await screen.findByText("assembledhq.com")).toBeInTheDocument();
    expect(screen.getByText("Pending verification")).toBeInTheDocument();
    expect(screen.getByText("_143-verify.assembledhq.com")).toBeInTheDocument();
    expect(screen.getByText("143-domain-verify=tok123")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Verify" })).toBeInTheDocument();
  });

  it("shows the auto-join toggle for a verified domain instead of DNS instructions", async () => {
    mockDomains([{ id: "d1", domain: "assembledhq.com", status: "verified" }]);
    renderWithProviders(<VerifiedDomainsSection />);

    expect(await screen.findByText("Verified")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Auto-join for assembledhq.com" })).toBeChecked();
    expect(screen.queryByText("143-domain-verify=tok123")).not.toBeInTheDocument();
  });

  it("renders GitHub organization auto-join rows and approval guidance", async () => {
    mockDomains([]);
    let patched: { auto_join_enabled: boolean } | null = null;
    server.use(
      http.get("/api/v1/team/github-orgs", () =>
        HttpResponse.json({
          github_orgs: [
            {
              installation_id: 123,
              account_login: "assembledhq",
              account_type: "Organization",
              auto_join_enabled: true,
              members_permission: "granted",
              captured_by_other_org: false,
            },
            {
              installation_id: 456,
              account_login: "needs-approval",
              account_type: "Organization",
              auto_join_enabled: false,
              members_permission: "missing",
              captured_by_other_org: false,
              settings_url: "https://github.com/organizations/needs-approval/settings/installations/456",
            },
          ],
        }),
      ),
      http.patch("/api/v1/team/github-orgs/123", async ({ request }) => {
        patched = (await request.json()) as { auto_join_enabled: boolean };
        return HttpResponse.json({ data: {} });
      }),
    );

    renderWithProviders(<VerifiedDomainsSection />);

    expect(await screen.findByText("GitHub organization assembledhq")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Auto-join for GitHub organization assembledhq" })).toBeChecked();
    expect(screen.getByText(/owner of needs-approval needs to approve/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("switch", { name: "Auto-join for GitHub organization assembledhq" }));
    await userEvent.click(await screen.findByRole("button", { name: "Turn off" }));

    await waitFor(() => expect(patched).toEqual({ auto_join_enabled: false }));
  });

  it("adds a domain and refetches the list", async () => {
    mockDomains([]);
    let posted = "";
    server.use(
      http.post("/api/v1/team/domains", async ({ request }) => {
        const body = (await request.json()) as { domain: string };
        posted = body.domain;
        return HttpResponse.json(
          {
            data: {
              id: "d-new",
              org_id: "org-1",
              domain: body.domain,
              status: "pending",
              auto_join_enabled: true,
              verification_token: "tok-new",
              failed_checks: 0,
              created_at: new Date().toISOString(),
              dns_record_name: `_143-verify.${body.domain}`,
              dns_record_value: "143-domain-verify=tok-new",
            },
          },
          { status: 201 },
        );
      }),
    );

    renderWithProviders(<VerifiedDomainsSection />);
    await userEvent.type(await screen.findByLabelText("Domain to verify"), "assembledhq.com");
    await userEvent.click(screen.getByRole("button", { name: "Add domain" }));

    await waitFor(() => {
      expect(posted).toBe("assembledhq.com");
    });
  });

  it("surfaces the server's error message when adding a blocked domain", async () => {
    mockDomains([]);
    server.use(
      http.post("/api/v1/team/domains", () =>
        HttpResponse.json(
          { error: { code: "INVALID_DOMAIN", message: '"gmail.com" is a public email provider and cannot be claimed by an organization' } },
          { status: 400 },
        ),
      ),
    );

    renderWithProviders(<VerifiedDomainsSection />);
    await userEvent.type(await screen.findByLabelText("Domain to verify"), "gmail.com");
    await userEvent.click(screen.getByRole("button", { name: "Add domain" }));

    expect(await screen.findByText(/public email provider/)).toBeInTheDocument();
  });

  it("warns when the daily DNS re-check is failing", async () => {
    mockDomains([{ id: "d1", domain: "assembledhq.com", status: "verified", failed_checks: 2 }]);
    renderWithProviders(<VerifiedDomainsSection />);

    expect(await screen.findByTestId("domain-recheck-warning-d1")).toHaveTextContent(
      "missing for 2 daily checks",
    );
  });

  it("explains an automatic auto-join disable after repeated failures", async () => {
    mockDomains([
      { id: "d1", domain: "assembledhq.com", status: "verified", auto_join_enabled: false, failed_checks: 3 },
    ]);
    renderWithProviders(<VerifiedDomainsSection />);

    expect(await screen.findByTestId("domain-recheck-disabled-d1")).toHaveTextContent(
      "turned off automatically",
    );
    expect(screen.getByRole("switch", { name: "Auto-join for assembledhq.com" })).not.toBeChecked();
  });

  it("verify failure shows the server's DNS guidance", async () => {
    mockDomains([{ id: "d1", domain: "assembledhq.com", status: "pending" }]);
    server.use(
      http.post("/api/v1/team/domains/d1/verify", () =>
        HttpResponse.json(
          { error: { code: "DOMAIN_NOT_VERIFIED", message: "TXT record not found. Publish it and try again." } },
          { status: 422 },
        ),
      ),
    );

    renderWithProviders(<VerifiedDomainsSection />);
    await userEvent.click(await screen.findByRole("button", { name: "Verify" }));

    expect(await screen.findByText(/TXT record not found/)).toBeInTheDocument();
  });
});
