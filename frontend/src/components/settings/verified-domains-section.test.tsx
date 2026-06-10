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
  );
}

describe("VerifiedDomainsSection", () => {
  it("renders the empty state when no domains are claimed", async () => {
    mockDomains([]);
    renderWithProviders(<VerifiedDomainsSection />);

    expect(await screen.findByText(/No domains yet/)).toBeInTheDocument();
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
