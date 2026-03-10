import { describe, it, expect, vi } from "vitest";
import { fireEvent } from "@testing-library/react";
import { renderWithProviders, screen } from "@/test/test-utils";
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
    ChevronDown: icon("ChevronDown"),
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

    expect(screen.getByText("Specs & Requirements")).toBeInTheDocument();
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
});

describe("DesignsSection", () => {
  it("renders empty state when no attachments", () => {
    renderWithProviders(
      <DesignsSection project={mockProject} attachments={[]} />,
    );

    expect(screen.getByText("Designs & Screenshots")).toBeInTheDocument();
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
});

describe("AnalysisSection", () => {
  it("renders with analyze button", () => {
    renderWithProviders(<AnalysisSection project={mockProject} />);

    // Section header should be visible
    expect(screen.getByText("Project Analysis")).toBeInTheDocument();

    // AnalysisSection defaults to collapsed (defaultOpen={false}), so click to open
    fireEvent.click(screen.getByText("Project Analysis"));

    // Now the Analyze button and target select should be visible
    expect(screen.getByText("Analyze")).toBeInTheDocument();

    // The target select should have the default options
    const select = screen.getByDisplayValue("Specs");
    expect(select).toBeInTheDocument();
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
    expect(screen.getByText("Specs & Requirements")).toBeInTheDocument();

    // DesignsSection header
    expect(screen.getByText("Designs & Screenshots")).toBeInTheDocument();

    // AnalysisSection header
    expect(screen.getByText("Project Analysis")).toBeInTheDocument();

    // Content from SpecsSection
    expect(screen.getByText("Test PRD")).toBeInTheDocument();

    // Content from DesignsSection
    expect(screen.getByText("mockup.png")).toBeInTheDocument();
  });
});
