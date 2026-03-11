import { describe, it, expect, vi } from "vitest";

import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import type { Project, ProjectTask, ProjectCycle } from "@/lib/types";
import { WorkTab } from "./work-tab";

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
    Plus: icon("Plus"),
    RotateCcw: icon("RotateCcw"),
    ExternalLink: icon("ExternalLink"),
    GitPullRequest: icon("GitPullRequest"),
    ArrowUpRight: icon("ArrowUpRight"),
    FileText: icon("FileText"),
    ChevronDown: icon("ChevronDown"),
    ChevronRight: icon("ChevronRight"),
    AlertCircle: icon("AlertCircle"),
    CheckCircle2: icon("CheckCircle2"),
    Circle: icon("Circle"),
    Loader2: icon("Loader2"),
    Ban: icon("Ban"),
    Pause: icon("Pause"),
  };
});

const mockProject: Project = {
  id: "proj-1",
  org_id: "org-1",
  repository_id: "repo-1",
  title: "Test Project",
  goal: "Test Goal",
  status: "active",
  priority: 50,
  execution_mode: "sequential",
  max_concurrent: 1,
  auto_merge: false,
  base_branch: "main",
  total_tasks: 4,
  completed_tasks: 1,
  failed_tasks: 1,
  proposed_by_pm: false,
  source_issue_ids: [],
  schedule_enabled: false,
  schedule_interval: 1,
  schedule_unit: "days",
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

const mockTasks: ProjectTask[] = [
  {
    id: "task-1", project_id: "proj-1", org_id: "org-1",
    title: "Pending Task", status: "pending",
    sort_order: 1, batch_number: 1, retry_count: 0, max_retries: 2,
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
  },
  {
    id: "task-2", project_id: "proj-1", org_id: "org-1",
    title: "Running Task", status: "running", description: "Doing work",
    sort_order: 2, batch_number: 1, retry_count: 0, max_retries: 2,
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
  },
  {
    id: "task-3", project_id: "proj-1", org_id: "org-1",
    title: "Completed Task", status: "completed",
    pr_url: "https://github.com/org/repo/pull/1", branch_name: "feat/task-3",
    sort_order: 3, batch_number: 1, retry_count: 0, max_retries: 2,
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
  },
  {
    id: "task-4", project_id: "proj-1", org_id: "org-1",
    title: "Failed Task", status: "failed",
    sort_order: 4, batch_number: 1, retry_count: 1, max_retries: 2,
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
  },
];

const mockCycles: ProjectCycle[] = [
  {
    id: "cycle-1", project_id: "proj-1", org_id: "org-1",
    cycle_number: 1, analysis: "First planning cycle analysis",
    decisions: {}, progress_pct: 25,
    tasks_completed_this_cycle: 1, tasks_failed_this_cycle: 0, tasks_created_this_cycle: 4,
    created_at: new Date().toISOString(),
  },
];

describe("WorkTab", () => {
  it("renders task board with columns", () => {
    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={mockCycles} />,
    );
    expect(screen.getByText("Task Board")).toBeInTheDocument();
    expect(screen.getByText("To Do")).toBeInTheDocument();
    expect(screen.getByText("In Progress")).toBeInTheDocument();
    expect(screen.getByText("Done")).toBeInTheDocument();
    expect(screen.getByText("Needs Attention")).toBeInTheDocument();
  });

  it("renders task titles in correct columns", () => {
    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={mockCycles} />,
    );
    expect(screen.getByText("Pending Task")).toBeInTheDocument();
    expect(screen.getByText("Running Task")).toBeInTheDocument();
    // "Completed Task" appears in both board and PR sections
    expect(screen.getAllByText("Completed Task").length).toBeGreaterThan(0);
    expect(screen.getByText("Failed Task")).toBeInTheDocument();
  });

  it("shows retry button for failed tasks", () => {
    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={mockCycles} />,
    );
    expect(screen.getByText("Retry")).toBeInTheDocument();
  });

  it("shows PR links for tasks with PR URLs", () => {
    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={mockCycles} />,
    );
    const prLinks = screen.getAllByText("PR");
    expect(prLinks.length).toBeGreaterThan(0);
  });

  it("renders empty state when no tasks", () => {
    renderWithProviders(
      <WorkTab project={mockProject} tasks={[]} cycles={[]} />,
    );
    expect(screen.getByText(/No tasks yet/)).toBeInTheDocument();
  });

  it("shows task description when present", () => {
    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={mockCycles} />,
    );
    expect(screen.getByText("Doing work")).toBeInTheDocument();
  });

  it("renders Pull Requests section for tasks with PRs", () => {
    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={mockCycles} />,
    );
    expect(screen.getByText("Pull Requests")).toBeInTheDocument();
  });

  it("renders planning cycles timeline", () => {
    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={mockCycles} />,
    );
    expect(screen.getByText("Planning Cycles")).toBeInTheDocument();
  });

  it("does not render PR section when no tasks have PRs", () => {
    const tasksWithoutPRs = mockTasks.map((t) => ({ ...t, pr_url: undefined }));
    renderWithProviders(
      <WorkTab project={mockProject} tasks={tasksWithoutPRs} cycles={[]} />,
    );
    expect(screen.queryByText("Pull Requests")).not.toBeInTheDocument();
  });

  it("does not render cycles section when no cycles", () => {
    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={[]} />,
    );
    expect(screen.queryByText("Planning Cycles")).not.toBeInTheDocument();
  });

  it("toggles add task form visibility", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={[]} />,
    );

    // Click "Add Task" to show form
    await user.click(screen.getByText("Add Task"));
    expect(screen.getByPlaceholderText("Task title")).toBeInTheDocument();

    // Click "Cancel" to hide form
    await user.click(screen.getByText("Cancel"));
    expect(screen.queryByPlaceholderText("Task title")).not.toBeInTheDocument();
  });

  it("creates a task via the form", async () => {
    const user = userEvent.setup();

    server.use(
      http.post("*/api/v1/projects/:id/tasks", () => {
        return HttpResponse.json(
          {
            data: {
              id: "task-new",
              project_id: "proj-1",
              org_id: "org-1",
              title: "New Task",
              status: "pending",
              sort_order: 5,
              batch_number: 1,
              retry_count: 0,
              max_retries: 2,
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          },
          { status: 201 },
        );
      }),
    );

    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={[]} />,
    );

    await user.click(screen.getByText("Add Task"));
    await user.type(screen.getByPlaceholderText("Task title"), "New Task");
    await user.click(screen.getByRole("button", { name: "Add" }));

    await waitFor(() => {
      expect(screen.queryByPlaceholderText("Task title")).not.toBeInTheDocument();
    });
  });

  it("disables Add button when title is empty", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={[]} />,
    );

    await user.click(screen.getByText("Add Task"));

    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
  });

  it("renders task with complexity badge", () => {
    const tasksWithComplexity: ProjectTask[] = [
      {
        id: "task-cx",
        project_id: "proj-1",
        org_id: "org-1",
        title: "Complex Task",
        status: "pending",
        complexity: "hard",
        sort_order: 1,
        batch_number: 1,
        retry_count: 0,
        max_retries: 2,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    ];

    renderWithProviders(
      <WorkTab project={mockProject} tasks={tasksWithComplexity} cycles={[]} />,
    );

    expect(screen.getByText("hard")).toBeInTheDocument();
  });

  it("renders task with session_id as Run link", () => {
    const tasksWithRun: ProjectTask[] = [
      {
        id: "task-run",
        project_id: "proj-1",
        org_id: "org-1",
        title: "Task with Run",
        status: "running",
        session_id: "run-123",
        sort_order: 1,
        batch_number: 1,
        retry_count: 0,
        max_retries: 2,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    ];

    renderWithProviders(
      <WorkTab project={mockProject} tasks={tasksWithRun} cycles={[]} />,
    );

    const runLink = screen.getByText("Session").closest("a");
    expect(runLink).toHaveAttribute("href", "/sessions/run-123");
  });

  it("renders planning cycle details when expanded", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <WorkTab project={mockProject} tasks={mockTasks} cycles={mockCycles} />,
    );

    // Planning Cycles section is collapsed by default (defaultOpen={false})
    await user.click(screen.getByText("Planning Cycles"));

    expect(screen.getByText("Cycle #1")).toBeInTheDocument();
    expect(screen.getByText("First planning cycle analysis")).toBeInTheDocument();
    expect(screen.getByText("25% done")).toBeInTheDocument();
    expect(screen.getByText("1 completed")).toBeInTheDocument();
    expect(screen.getByText("4 created")).toBeInTheDocument();
  });
});
