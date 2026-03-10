import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { http, HttpResponse } from "msw";
import { ProjectDetailContent } from "./project-detail-content";

vi.mock("next/link", () => ({
  default: ({ children, href, ...props }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock("lucide-react", () => {
  const icon = (name: string) => {
    const Component = (props: Record<string, unknown>) => (
      <span data-testid={`icon-${name}`} {...props} />
    );
    Component.displayName = name;
    return Component;
  };
  return {
    ArrowLeft: icon("ArrowLeft"),
    FileText: icon("FileText"),
    GitPullRequest: icon("GitPullRequest"),
    Settings: icon("Settings"),
    Plus: icon("Plus"),
    ExternalLink: icon("ExternalLink"),
    Image: icon("Image"),
    Sparkles: icon("Sparkles"),
    Trash2: icon("Trash2"),
    Pencil: icon("Pencil"),
    Save: icon("Save"),
    X: icon("X"),
    Loader2: icon("Loader2"),
    ChevronDown: icon("ChevronDown"),
    ChevronRight: icon("ChevronRight"),
    AlertCircle: icon("AlertCircle"),
    CheckCircle2: icon("CheckCircle2"),
    Circle: icon("Circle"),
    Ban: icon("Ban"),
    Pause: icon("Pause"),
    ArrowUpRight: icon("ArrowUpRight"),
    RotateCcw: icon("RotateCcw"),
  };
});

describe("ProjectDetailContent", () => {
  it("shows loading state initially", () => {
    // Use a handler that never responds to keep loading state
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return new Promise(() => {}); // Never resolves
      }),
    );

    renderWithProviders(<ProjectDetailContent id="proj-1" />);
    expect(screen.getByText("Loading project...")).toBeInTheDocument();
    expect(screen.getByText("Back to projects")).toBeInTheDocument();
  });

  it("shows error state on failure", async () => {
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return HttpResponse.json({ error: { code: "NOT_FOUND", message: "not found" } }, { status: 404 });
      }),
    );

    renderWithProviders(<ProjectDetailContent id="proj-1" />);
    await waitFor(() => {
      expect(screen.getByText("Failed to load project details.")).toBeInTheDocument();
    });
  });

  it("renders project details on success", async () => {
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return HttpResponse.json({
          data: {
            project: {
              id: "proj-1", org_id: "org-1", repository_id: "repo-1",
              title: "My Test Project", goal: "Build something great",
              status: "draft", priority: 50, execution_mode: "sequential",
              max_concurrent: 1, auto_merge: false, base_branch: "main",
              total_tasks: 3, completed_tasks: 1, failed_tasks: 0,
              proposed_by_pm: false, source_issue_ids: [],
              created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
            },
            tasks: [],
            recent_cycles: [],
            attachments: [],
            specs: [],
          },
        });
      }),
    );

    renderWithProviders(<ProjectDetailContent id="proj-1" />);
    await waitFor(() => {
      expect(screen.getByText("My Test Project")).toBeInTheDocument();
    });
    expect(screen.getByText("Draft")).toBeInTheDocument();
    expect(screen.getByText("1/3 (33%)")).toBeInTheDocument();
    expect(screen.getByText("Plan")).toBeInTheDocument();
    expect(screen.getByText("Work")).toBeInTheDocument();
    expect(screen.getByText("Settings")).toBeInTheDocument();
  });

  it("shows active indicator for active projects", async () => {
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return HttpResponse.json({
          data: {
            project: {
              id: "proj-1", org_id: "org-1", repository_id: "repo-1",
              title: "Active Project", goal: "In progress",
              status: "active", priority: 50, execution_mode: "parallel",
              max_concurrent: 3, auto_merge: false, base_branch: "main",
              total_tasks: 5, completed_tasks: 2, failed_tasks: 0,
              proposed_by_pm: false, source_issue_ids: [],
              created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
            },
            tasks: [],
            recent_cycles: [],
            attachments: [],
            specs: [],
          },
        });
      }),
    );

    renderWithProviders(<ProjectDetailContent id="proj-1" />);
    await waitFor(() => {
      expect(screen.getByText("Active Project")).toBeInTheDocument();
    });
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByText("parallel")).toBeInTheDocument();
  });
});
