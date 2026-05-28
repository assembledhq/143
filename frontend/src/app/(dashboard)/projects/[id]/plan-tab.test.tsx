import { describe, it, expect, vi } from "vitest";
import { fireEvent } from "@testing-library/react";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { http, HttpResponse } from "msw";
import type { Project, ProjectSpec, ProjectAttachment } from "@/lib/types";
import { SpecsSection, DesignsSection, AnalysisSection, PlanTab } from "./plan-tab";

// Mock next/link to render a plain anchor
vi.mock("next/link", () => ({
  default: ({
    children,
    href,
    ...props
  }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
}));

// Mock lucide-react icons to simple spans so JSDOM can render them
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
    ExternalLink: icon("ExternalLink"),
    Image: icon("Image"),
    FileText: icon("FileText"),
    Sparkles: icon("Sparkles"),
    Trash2: icon("Trash2"),
    Pencil: icon("Pencil"),
    Save: icon("Save"),
    X: icon("X"),
    Loader2: icon("Loader2"),
    CheckIcon: icon("CheckIcon"),
    ChevronDown: icon("ChevronDown"),
    ChevronDownIcon: icon("ChevronDownIcon"),
    ChevronUpIcon: icon("ChevronUpIcon"),
    ChevronRight: icon("ChevronRight"),
    AlertCircle: icon("AlertCircle"),
    CheckCircle2: icon("CheckCircle2"),
    Circle: icon("Circle"),
    Ban: icon("Ban"),
    Pause: icon("Pause"),
    ArrowUpRight: icon("ArrowUpRight"),
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
  total_tasks: 0,
  completed_tasks: 0,
  failed_tasks: 0,
  proposed_by_pm: false,
  source_issue_ids: [],
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

const mockSpec: ProjectSpec = {
  id: "spec-1",
  project_id: "proj-1",
  org_id: "org-1",
  title: "Test PRD",
  content: "# Overview\n\nThis is a test PRD",
  spec_type: "prd",
  sort_order: 0,
  version: 1,
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

const mockTechnicalSpec: ProjectSpec = {
  id: "spec-2",
  project_id: "proj-1",
  org_id: "org-1",
  title: "API Design",
  content: "# Technical Spec\n\nAPI endpoints",
  spec_type: "technical",
  sort_order: 1,
  version: 1,
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

const mockAttachment: ProjectAttachment = {
  id: "attach-1",
  project_id: "proj-1",
  org_id: "org-1",
  file_name: "mockup.png",
  file_url: "https://example.com/mockup.png",
  file_type: "image",
  category: "screenshot",
  sort_order: 0,
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

const mockWireframe: ProjectAttachment = {
  id: "attach-2",
  project_id: "proj-1",
  org_id: "org-1",
  file_name: "wireframe.svg",
  file_url: "https://example.com/wireframe.svg",
  file_type: "image",
  category: "wireframe",
  sort_order: 1,
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

describe("SpecsSection", () => {
  it("renders empty state when no specs", () => {
    renderWithProviders(
      <SpecsSection project={mockProject} specs={[]} />,
    );

    expect(screen.getByText("Specs & requirements")).toBeInTheDocument();
    expect(
      screen.getByText(
        /No specs yet\. Add product requirements, technical specs, or user stories/,
      ),
    ).toBeInTheDocument();
  });

  it("renders spec cards with type badges", () => {
    renderWithProviders(
      <SpecsSection project={mockProject} specs={[mockSpec, mockTechnicalSpec]} />,
    );

    // Spec titles
    expect(screen.getByText("Test PRD")).toBeInTheDocument();
    expect(screen.getByText("API Design")).toBeInTheDocument();

    // Type badges
    expect(screen.getByText("PRD")).toBeInTheDocument();
    expect(screen.getByText("Technical")).toBeInTheDocument();

    // Version labels
    expect(screen.getAllByText("v1")).toHaveLength(2);

    // Spec content rendered inside <pre> (use substring match for multiline content)
    expect(screen.getByText(/# Overview/)).toBeInTheDocument();
    expect(screen.getByText(/This is a test PRD/)).toBeInTheDocument();
  });

  it("shows delete confirmation on trash click", () => {
    renderWithProviders(
      <SpecsSection project={mockProject} specs={[mockSpec]} />,
    );

    // Initially no Confirm/Cancel buttons
    expect(screen.queryByText("Confirm")).not.toBeInTheDocument();

    // Click the trash icon button
    const trashButtons = screen.getAllByRole("button").filter(
      (btn) => btn.querySelector('[data-testid="icon-Trash2"]'),
    );
    expect(trashButtons.length).toBeGreaterThan(0);
    fireEvent.click(trashButtons[0]);

    // Now Confirm and Cancel buttons should appear
    expect(screen.getByText("Confirm")).toBeInTheDocument();
    expect(screen.getByText("Cancel")).toBeInTheDocument();
  });

  it("toggles add form visibility", () => {
    renderWithProviders(
      <SpecsSection project={mockProject} specs={[]} />,
    );

    // Click the Add button to show the form
    fireEvent.click(screen.getByText("Add"));

    // Form elements should appear
    expect(screen.getByText("Title")).toBeInTheDocument();
    expect(screen.getByText("Create spec")).toBeInTheDocument();

    // Click Cancel to hide the form
    fireEvent.click(screen.getByText("Cancel"));

    // Form should be gone
    expect(screen.queryByText("Create spec")).not.toBeInTheDocument();
  });

  it("creates a spec via the form", async () => {
    const user = userEvent.setup();

    server.use(
      http.post("*/api/v1/projects/:id/specs", () => {
        return HttpResponse.json(
          {
            data: {
              id: "spec-new",
              project_id: "proj-1",
              org_id: "org-1",
              title: "New Spec",
              content: "# New content",
              spec_type: "prd",
              sort_order: 0,
              version: 1,
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          },
          { status: 201 },
        );
      }),
    );

    renderWithProviders(
      <SpecsSection project={mockProject} specs={[]} />,
    );

    // Open the add form
    await user.click(screen.getByText("Add"));

    // Fill in the form
    await user.type(
      screen.getByPlaceholderText("Product Requirements Document"),
      "New Spec",
    );
    await user.type(
      screen.getByPlaceholderText(/# Overview/),
      "# New content",
    );

    // Submit the form
    await user.click(screen.getByText("Create spec"));

    // Wait for the form to disappear after successful creation
    await waitFor(() => {
      expect(screen.queryByText("Create spec")).not.toBeInTheDocument();
    });
  });

  it("enters and cancels edit mode", () => {
    renderWithProviders(
      <SpecsSection project={mockProject} specs={[mockSpec]} />,
    );

    // Click the pencil icon button to enter edit mode
    const pencilButton = screen.getAllByRole("button").find(
      (btn) => btn.querySelector('[data-testid="icon-Pencil"]'),
    )!;
    fireEvent.click(pencilButton);

    // The title should now be in an input
    expect(screen.getByDisplayValue("Test PRD")).toBeInTheDocument();

    // Click the X icon button to cancel editing
    const xButton = screen.getAllByRole("button").find(
      (btn) => btn.querySelector('[data-testid="icon-X"]'),
    )!;
    fireEvent.click(xButton);

    // The title should be back as plain text
    expect(screen.getByText("Test PRD")).toBeInTheDocument();
  });

  it("cancels delete", () => {
    renderWithProviders(
      <SpecsSection project={mockProject} specs={[mockSpec]} />,
    );

    // Click the trash icon
    const trashButton = screen.getAllByRole("button").find(
      (btn) => btn.querySelector('[data-testid="icon-Trash2"]'),
    )!;
    fireEvent.click(trashButton);

    // Confirm should appear
    expect(screen.getByText("Confirm")).toBeInTheDocument();

    // Click Cancel
    fireEvent.click(screen.getByText("Cancel"));

    // Confirm should be gone
    expect(screen.queryByText("Confirm")).not.toBeInTheDocument();
  });
});

describe("DesignsSection", () => {
  it("renders empty state when no attachments", () => {
    renderWithProviders(
      <DesignsSection project={mockProject} attachments={[]} />,
    );

    expect(screen.getByText("Designs & screenshots")).toBeInTheDocument();
    expect(
      screen.getByText(
        /No designs yet\. Add screenshots, mockups, or wireframes/,
      ),
    ).toBeInTheDocument();
  });

  it("renders attachment cards with category badges", () => {
    renderWithProviders(
      <DesignsSection
        project={mockProject}
        attachments={[mockAttachment, mockWireframe]}
      />,
    );

    // File names
    expect(screen.getByText("mockup.png")).toBeInTheDocument();
    expect(screen.getByText("wireframe.svg")).toBeInTheDocument();

    // Category badges
    expect(screen.getByText("Screenshot")).toBeInTheDocument();
    expect(screen.getByText("Wireframe")).toBeInTheDocument();
  });

  it("shows delete confirmation on trash click", () => {
    renderWithProviders(
      <DesignsSection project={mockProject} attachments={[mockAttachment]} />,
    );

    // Initially no Confirm/Cancel buttons
    expect(screen.queryByText("Confirm")).not.toBeInTheDocument();

    // Click the trash icon button
    const trashButtons = screen.getAllByRole("button").filter(
      (btn) => btn.querySelector('[data-testid="icon-Trash2"]'),
    );
    expect(trashButtons.length).toBeGreaterThan(0);
    fireEvent.click(trashButtons[0]);

    // Confirm and Cancel buttons should now appear
    expect(screen.getByText("Confirm")).toBeInTheDocument();
    expect(screen.getByText("Cancel")).toBeInTheDocument();
  });

  it("toggles add form and creates attachment", async () => {
    const user = userEvent.setup();

    server.use(
      http.post("*/api/v1/projects/:id/attachments", () => {
        return HttpResponse.json(
          {
            data: {
              id: "attach-new",
              project_id: "proj-1",
              org_id: "org-1",
              file_name: "new-mockup.png",
              file_url: "https://example.com/new-mockup.png",
              file_type: "image",
              category: "screenshot",
              sort_order: 0,
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          },
          { status: 201 },
        );
      }),
    );

    renderWithProviders(
      <DesignsSection project={mockProject} attachments={[]} />,
    );

    // Click Add to show the form
    await user.click(screen.getByText("Add"));

    // Form should appear
    expect(screen.getByText("File name")).toBeInTheDocument();

    // Fill in the form
    await user.type(
      screen.getByPlaceholderText("homepage-mockup.png"),
      "new-mockup.png",
    );
    await user.type(
      screen.getByPlaceholderText("https://..."),
      "https://example.com/new-mockup.png",
    );

    // Click the submit Add button (inside the form, not the header one)
    const addButtons = screen.getAllByRole("button").filter(
      (btn) => btn.textContent === "Add",
    );
    // The last "Add" button is the submit button inside the form
    await user.click(addButtons[addButtons.length - 1]);

    // Wait for the form to disappear after successful creation
    await waitFor(() => {
      expect(screen.queryByText("File name")).not.toBeInTheDocument();
    });
  });

  it("confirms delete of attachment", async () => {
    server.use(
      http.delete("*/api/v1/projects/:id/attachments/:attachId", () => {
        return HttpResponse.json(undefined, { status: 204 });
      }),
    );

    renderWithProviders(
      <DesignsSection project={mockProject} attachments={[mockAttachment]} />,
    );

    // Click trash icon
    const trashButton = screen.getAllByRole("button").find(
      (btn) => btn.querySelector('[data-testid="icon-Trash2"]'),
    )!;
    fireEvent.click(trashButton);

    // Click Confirm
    fireEvent.click(screen.getByText("Confirm"));
  });
});

describe("AnalysisSection", () => {
  it("renders with analyze button", () => {
    renderWithProviders(<AnalysisSection project={mockProject} />);

    // Section header should be visible
    expect(screen.getByText("Project analysis")).toBeInTheDocument();

    // AnalysisSection defaults to collapsed (defaultOpen={false}), so click to open
    fireEvent.click(screen.getByText("Project analysis"));

    // Now the Analyze button and target select should be visible
    expect(screen.getByText("Analyze")).toBeInTheDocument();

    // The target select should use the shared combobox primitive.
    const select = screen.getByRole("combobox", { name: "Analysis target" });
    expect(select).toBeInTheDocument();
    expect(select).toHaveClass("max-sm:text-base");
  });

  it("shows suggestions after analyze", async () => {
    const user = userEvent.setup();

    server.use(
      http.post("*/api/v1/projects/:id/ai/improve", () => {
        return HttpResponse.json({
          data: {
            suggestions: [
              {
                type: "addition",
                title: "Add error handling",
                description: "Consider adding error handling for edge cases",
                priority: "high",
              },
            ],
            summary: "Analysis complete",
          },
        });
      }),
    );

    renderWithProviders(<AnalysisSection project={mockProject} />);

    // Open the collapsed section
    await user.click(screen.getByText("Project analysis"));

    // Click Analyze
    await user.click(screen.getByText("Analyze"));

    // Wait for the suggestion to appear
    await waitFor(() => {
      expect(screen.getByText("Add error handling")).toBeInTheDocument();
    });

    // Verify priority badge
    expect(screen.getByText("high")).toBeInTheDocument();
  });

  it("shows error on failed analysis", async () => {
    const user = userEvent.setup();

    server.use(
      http.post("*/api/v1/projects/:id/ai/improve", () => {
        return HttpResponse.json(
          { error: { code: "INTERNAL_ERROR", message: "Something went wrong" } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<AnalysisSection project={mockProject} />);

    // Open the collapsed section
    await user.click(screen.getByText("Project analysis"));

    // Click Analyze
    await user.click(screen.getByText("Analyze"));

    // Wait for the error message to appear
    await waitFor(() => {
      expect(screen.getByText("Failed to get suggestions.")).toBeInTheDocument();
    });
  });
});

describe("PlanTab", () => {
  it("renders all three sections", () => {
    renderWithProviders(
      <PlanTab
        project={mockProject}
        specs={[mockSpec]}
        attachments={[mockAttachment]}
      />,
    );

    // SpecsSection header
    expect(screen.getByText("Specs & requirements")).toBeInTheDocument();

    // DesignsSection header
    expect(screen.getByText("Designs & screenshots")).toBeInTheDocument();

    // AnalysisSection header
    expect(screen.getByText("Project analysis")).toBeInTheDocument();

    // Content from SpecsSection
    expect(screen.getByText("Test PRD")).toBeInTheDocument();

    // Content from DesignsSection
    expect(screen.getByText("mockup.png")).toBeInTheDocument();
  });
});
