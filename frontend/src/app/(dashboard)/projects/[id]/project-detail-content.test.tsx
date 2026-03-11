import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, waitFor, userEvent } from "@/test/test-utils";
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
    ChevronDownIcon: icon("ChevronDownIcon"),
    ChevronUpIcon: icon("ChevronUpIcon"),
    CheckIcon: icon("CheckIcon"),
    ChevronRight: icon("ChevronRight"),
    AlertCircle: icon("AlertCircle"),
    CheckCircle2: icon("CheckCircle2"),
    Circle: icon("Circle"),
    Ban: icon("Ban"),
    Pause: icon("Pause"),
    ArrowUpRight: icon("ArrowUpRight"),
    RotateCcw: icon("RotateCcw"),
    CircleIcon: icon("CircleIcon"),
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

  it("shows Start Project and Cancel Project buttons for draft project", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return HttpResponse.json({
          data: {
            project: {
              id: "proj-1", org_id: "org-1", repository_id: "repo-1",
              title: "Draft Project", goal: "Some goal",
              status: "draft", priority: 50, execution_mode: "sequential",
              max_concurrent: 1, auto_merge: false, base_branch: "main",
              total_tasks: 0, completed_tasks: 0, failed_tasks: 0,
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
      expect(screen.getByText("Draft Project")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: /Settings/ }));

    expect(screen.getByRole("button", { name: "Start Project" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel Project" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Resume" })).not.toBeInTheDocument();
  });

  it("shows Pause and Cancel Project buttons for active project", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return HttpResponse.json({
          data: {
            project: {
              id: "proj-1", org_id: "org-1", repository_id: "repo-1",
              title: "Active Settings Project", goal: "Active goal",
              status: "active", priority: 50, execution_mode: "sequential",
              max_concurrent: 1, auto_merge: false, base_branch: "main",
              total_tasks: 2, completed_tasks: 1, failed_tasks: 0,
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
      expect(screen.getByText("Active Settings Project")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: /Settings/ }));

    expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel Project" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start Project" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Resume" })).not.toBeInTheDocument();
  });

  it("shows Resume and Cancel Project buttons for paused project", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return HttpResponse.json({
          data: {
            project: {
              id: "proj-1", org_id: "org-1", repository_id: "repo-1",
              title: "Paused Settings Project", goal: "Paused goal",
              status: "paused", priority: 50, execution_mode: "sequential",
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
      expect(screen.getByText("Paused Settings Project")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: /Settings/ }));

    expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel Project" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start Project" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
  });

  it("renders configuration fields in settings tab", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return HttpResponse.json({
          data: {
            project: {
              id: "proj-1", org_id: "org-1", repository_id: "repo-1",
              title: "Config Project", goal: "Build great things",
              scope: "Frontend only", completion_criteria: "All tests pass",
              status: "draft", priority: 50, execution_mode: "sequential",
              max_concurrent: 1, auto_merge: false, base_branch: "main",
              total_tasks: 0, completed_tasks: 0, failed_tasks: 0,
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
      expect(screen.getByText("Config Project")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: /Settings/ }));

    expect(screen.getByLabelText("Goal")).toBeInTheDocument();
    expect(screen.getByLabelText("Scope")).toBeInTheDocument();
    expect(screen.getByLabelText("Completion Criteria")).toBeInTheDocument();
    expect(screen.getByText("Execution Mode")).toBeInTheDocument();
    expect(screen.getByLabelText("Sequential")).toBeInTheDocument();
    expect(screen.getByLabelText("Parallel")).toBeInTheDocument();
    expect(screen.getByText("Priority")).toBeInTheDocument();
    expect(screen.getByLabelText("Base Branch")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save Changes" })).toBeInTheDocument();
  });

  it("shows Max Concurrent field when Parallel execution mode is selected", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return HttpResponse.json({
          data: {
            project: {
              id: "proj-1", org_id: "org-1", repository_id: "repo-1",
              title: "Parallel Project", goal: "Parallel goal",
              status: "active", priority: 50, execution_mode: "parallel",
              max_concurrent: 3, auto_merge: false, base_branch: "main",
              total_tasks: 4, completed_tasks: 1, failed_tasks: 0,
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
      expect(screen.getByText("Parallel Project")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("tab", { name: /Settings/ }));

    expect(screen.getByLabelText("Max Concurrent")).toBeInTheDocument();
  });

  it("shows stats for project with running tasks and PRs", async () => {
    server.use(
      http.get("*/api/v1/projects/:id", () => {
        return HttpResponse.json({
          data: {
            project: {
              id: "proj-1", org_id: "org-1", repository_id: "repo-1",
              title: "Stats Project", goal: "Stats goal",
              status: "active", priority: 50, execution_mode: "sequential",
              max_concurrent: 1, auto_merge: false, base_branch: "main",
              total_tasks: 4, completed_tasks: 1, failed_tasks: 0,
              proposed_by_pm: false, source_issue_ids: [],
              current_phase: "implementation",
              created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
            },
            tasks: [
              {
                id: "task-1", project_id: "proj-1", org_id: "org-1",
                title: "Task 1", sort_order: 1, batch_number: 1,
                status: "running", retry_count: 0, max_retries: 3,
                created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
              },
              {
                id: "task-2", project_id: "proj-1", org_id: "org-1",
                title: "Task 2", sort_order: 2, batch_number: 1,
                status: "completed", pr_url: "https://github.com/org/repo/pull/1",
                retry_count: 0, max_retries: 3,
                created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
              },
              {
                id: "task-3", project_id: "proj-1", org_id: "org-1",
                title: "Task 3", sort_order: 3, batch_number: 2,
                status: "pending", retry_count: 0, max_retries: 3,
                created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
              },
            ],
            recent_cycles: [],
            attachments: [
              {
                id: "att-1", project_id: "proj-1", org_id: "org-1",
                file_name: "mockup.png", file_url: "https://example.com/mockup.png",
                file_type: "image/png", category: "mockup", sort_order: 1,
                created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
              },
            ],
            specs: [
              {
                id: "spec-1", project_id: "proj-1", org_id: "org-1",
                title: "PRD", content: "Some content", spec_type: "prd",
                sort_order: 1, version: 1,
                created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
              },
            ],
          },
        });
      }),
    );

    renderWithProviders(<ProjectDetailContent id="proj-1" />);
    await waitFor(() => {
      expect(screen.getByText("Stats Project")).toBeInTheDocument();
    });

    expect(screen.getByText("1 running")).toBeInTheDocument();
    expect(screen.getByText("1 PRs")).toBeInTheDocument();
    expect(screen.getByText("1 specs")).toBeInTheDocument();
    expect(screen.getByText("1 designs")).toBeInTheDocument();
    expect(screen.getByText("Phase: implementation")).toBeInTheDocument();
  });
});
