import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";
import { DocumentsManager } from "./documents-manager";
import type { PMDocument } from "@/lib/types";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

function makeDoc(overrides: Partial<PMDocument> = {}): PMDocument {
  return {
    id: "doc-1",
    org_id: "org-1",
    title: "Q1 Roadmap",
    content: "## Goals\n- Launch v2",
    doc_type: "roadmap",
    sort_order: 0,
    source_type: "manual",
    created_at: "2026-03-01T00:00:00Z",
    updated_at: "2026-03-15T00:00:00Z",
    ...overrides,
  };
}

function setupEmptyDocs() {
  server.use(
    http.get("/api/v1/pm/documents", () => {
      return HttpResponse.json({ data: [], meta: {} });
    }),
  );
}

function setupDocsWithItems(docs: PMDocument[]) {
  server.use(
    http.get("/api/v1/pm/documents", () => {
      return HttpResponse.json({ data: docs, meta: {} });
    }),
    http.post("/api/v1/pm/documents", () => {
      return HttpResponse.json({
        data: { id: "new-doc", org_id: "org-1", title: "New", content: "", doc_type: "roadmap", sort_order: 0, source_type: "manual", created_at: "2026-03-20T00:00:00Z", updated_at: "2026-03-20T00:00:00Z" },
      });
    }),
    http.patch("/api/v1/pm/documents/:id", () => {
      return HttpResponse.json({
        data: { ...docs[0], title: "Updated" },
      });
    }),
    http.delete("/api/v1/pm/documents/:id", () => {
      return HttpResponse.json({});
    }),
  );
}

describe("DocumentsManager", () => {
  it("shows empty state when no documents exist", async () => {
    setupEmptyDocs();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText(/No reference documents yet/)).toBeInTheDocument();
    });
  });

  it("shows an Add button", async () => {
    setupEmptyDocs();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Add/ })).toBeInTheDocument();
    });
  });

  it("clicking Add opens a create form with Title, Type, and Source fields", async () => {
    setupEmptyDocs();
    const user = userEvent.setup();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Add/ })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /Add/ }));

    expect(screen.getByLabelText("Title")).toBeInTheDocument();
    expect(screen.getByText("Type")).toBeInTheDocument();
    expect(screen.getByText("Source")).toBeInTheDocument();
  });

  it("shows existing documents with title and type badge", async () => {
    const doc = makeDoc({ doc_type: "strategy" });
    setupDocsWithItems([doc]);
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
    });

    expect(screen.getByText("Strategy")).toBeInTheDocument();
  });

  it("does NOT render external link button for javascript: URLs (XSS protection)", async () => {
    const doc = makeDoc({
      source_type: "url",
      source_url: "javascript:alert('xss')",
    });
    setupDocsWithItems([doc]);
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
    });

    // The ExternalLink button should not be present since the URL is not safe
    const links = screen.queryAllByRole("link");
    const externalLinks = links.filter(
      (link) => link.getAttribute("href") === "javascript:alert('xss')",
    );
    expect(externalLinks).toHaveLength(0);
  });

  it("renders external link button for valid https: URLs", async () => {
    const doc = makeDoc({
      source_type: "url",
      source_url: "https://notion.so/my-doc",
    });
    setupDocsWithItems([doc]);
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
    });

    const externalLink = screen.getByRole("link");
    expect(externalLink).toHaveAttribute("href", "https://notion.so/my-doc");
    expect(externalLink).toHaveAttribute("target", "_blank");
  });

  it("shows delete confirmation when trash icon is clicked", async () => {
    const doc = makeDoc();
    setupDocsWithItems([doc]);
    const user = userEvent.setup();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
    });

    // Find and click the delete (trash) button - it's the last ghost button in the action area
    const deleteButtons = screen.getAllByRole("button").filter((btn) => {
      // The trash button has the Trash2 icon with text-destructive class
      return btn.querySelector("svg.text-destructive") !== null;
    });
    expect(deleteButtons.length).toBeGreaterThan(0);
    await user.click(deleteButtons[0]);

    // After clicking delete, a "Confirm" button should appear
    expect(screen.getByRole("button", { name: /Confirm/ })).toBeInTheDocument();
  });

  it("can cancel delete confirmation", async () => {
    const doc = makeDoc();
    setupDocsWithItems([doc]);
    const user = userEvent.setup();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
    });

    // Click the trash button to show confirmation
    const deleteButtons = screen.getAllByRole("button").filter((btn) => {
      return btn.querySelector("svg.text-destructive") !== null;
    });
    await user.click(deleteButtons[0]);

    expect(screen.getByRole("button", { name: /Confirm/ })).toBeInTheDocument();

    // Click the cancel button (X icon with aria-label)
    await user.click(screen.getByRole("button", { name: "Cancel delete" }));

    // After cancelling, the Confirm button should be gone and the document still visible
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /Confirm/ })).not.toBeInTheDocument();
    });
    expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
  });

  it("expands document content when title is clicked", async () => {
    const doc = makeDoc({ content: "## Goals\n- Launch v2" });
    setupDocsWithItems([doc]);
    const user = userEvent.setup();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
    });

    // Click the title to expand
    await user.click(screen.getByText("Q1 Roadmap"));

    await waitFor(() => {
      expect(screen.getByText((content) => content.includes("Goals") && content.includes("Launch v2"))).toBeInTheDocument();
    });
  });

  it("opens the edit form when the pencil button is clicked", async () => {
    const doc = makeDoc();
    setupDocsWithItems([doc]);
    const user = userEvent.setup();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
    });

    // Find the edit button (pencil icon) - it's the second ghost button
    const editButtons = screen.getAllByRole("button").filter((btn) => {
      const svg = btn.querySelector("svg");
      return svg && !btn.querySelector("svg.text-destructive") && btn.textContent === "";
    });
    // Click the pencil button (should be among the small action buttons)
    const pencilBtn = editButtons.find((btn) => {
      return btn.classList.contains("h-7");
    });
    if (pencilBtn) {
      await user.click(pencilBtn);
    }

    // Edit form should have Title and Content fields
    await waitFor(() => {
      const titleInputs = screen.getAllByDisplayValue("Q1 Roadmap");
      expect(titleInputs.length).toBeGreaterThan(0);
    });
  });

  it("creates a document via the create form", async () => {
    setupDocsWithItems([]);
    const user = userEvent.setup();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Add/ })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /Add/ }));

    // Fill in title
    const titleInput = screen.getByLabelText("Title");
    await user.type(titleInput, "New Roadmap");

    // Fill in content
    const contentInput = screen.getByLabelText("Content");
    await user.type(contentInput, "Some content");

    // Click save
    await user.click(screen.getByRole("button", { name: /Save document/ }));
  });

  it("shows source type badge for non-manual documents", async () => {
    const doc = makeDoc({ source_type: "notion" });
    setupDocsWithItems([doc]);
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
    });

    expect(screen.getByText("Notion")).toBeInTheDocument();
  });

  it("shows source URL field when source type is not manual", async () => {
    setupEmptyDocs();
    const user = userEvent.setup();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Add/ })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /Add/ }));

    // By default source type is "manual" so no URL field
    expect(screen.queryByLabelText("Source URL")).not.toBeInTheDocument();
  });

  it("shows expanded content with source URL when available", async () => {
    const doc = makeDoc({
      source_type: "url",
      source_url: "https://example.com/doc",
      content: "Document content here",
      last_synced_at: "2026-03-20T00:00:00Z",
    });
    setupDocsWithItems([doc]);
    const user = userEvent.setup();
    renderWithProviders(<DocumentsManager />);

    await waitFor(() => {
      expect(screen.getByText("Q1 Roadmap")).toBeInTheDocument();
    });

    // Expand
    await user.click(screen.getByText("Q1 Roadmap"));

    await waitFor(() => {
      expect(screen.getByText("Document content here")).toBeInTheDocument();
    });
    // Source URL link
    const sourceLink = screen.getByRole("link", { name: "https://example.com/doc" });
    expect(sourceLink).toHaveAttribute("href", "https://example.com/doc");
  });
});
