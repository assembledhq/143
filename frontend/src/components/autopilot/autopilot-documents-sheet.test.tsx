import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AutopilotDocumentsSheet } from "./autopilot-documents-sheet";

describe("AutopilotDocumentsSheet", () => {
  it("shows an empty state when there are no documents", async () => {
    server.use(
      http.get("/api/v1/pm/documents", () => HttpResponse.json({ data: [], meta: {} }))
    );

    renderWithProviders(<AutopilotDocumentsSheet open onOpenChange={vi.fn()} />);

    expect(await screen.findByText("No documents yet.")).toBeInTheDocument();
  });

  it("creates and renders a new document", async () => {
    const user = userEvent.setup();
    let created = false;

    server.use(
      http.get("/api/v1/pm/documents", () => {
        const docs = created ? [{
          id: "doc-1",
          org_id: "org-1",
          title: "Roadmap",
          content: "Content",
          doc_type: "roadmap",
          sort_order: 0,
          source_type: "manual",
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        }] : [];
        return HttpResponse.json({ data: docs, meta: {} });
      }),
      http.post("/api/v1/pm/documents", async () => {
        created = true;
        return HttpResponse.json({
          data: {
            id: "doc-1",
            org_id: "org-1",
            title: "Roadmap",
            content: "Content",
            doc_type: "roadmap",
            sort_order: 0,
            source_type: "manual",
            created_at: "2026-03-20T00:00:00Z",
            updated_at: "2026-03-20T00:00:00Z",
          },
        });
      })
    );

    renderWithProviders(<AutopilotDocumentsSheet open onOpenChange={vi.fn()} />);

    await user.click(await screen.findByRole("button", { name: "Add document" }));
    await user.type(screen.getByLabelText("Title"), "Roadmap");
    await user.type(screen.getByLabelText("Content"), "Content");
    await user.click(screen.getByRole("button", { name: "Save document" }));

    expect((await screen.findAllByText("Roadmap")).length).toBeGreaterThan(0);
  });
});
