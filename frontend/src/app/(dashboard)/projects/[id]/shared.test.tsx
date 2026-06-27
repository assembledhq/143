import { describe, it, expect, vi } from "vitest";
import { fireEvent } from "@testing-library/react";
import { renderWithProviders, screen } from "@/test/test-utils";
import { formatTimestamp, ProgressBar, CollapsibleSection, taskStatusConfig, specTypeConfig, attachmentCategoryConfig } from "./shared";
import { FileText } from "lucide-react";

// Mock lucide-react icons
vi.mock("lucide-react", () => {
  const icon = (name: string) => {
    const Component = (props: Record<string, unknown>) => (
      <span data-testid={`icon-${name}`} {...props} />
    );
    Component.displayName = name;
    return Component;
  };
  return {
    FileText: icon("FileText"),
    ChevronDown: icon("ChevronDown"),
    ChevronRight: icon("ChevronRight"),
    AlertCircle: icon("AlertCircle"),
    CheckCircle2: icon("CheckCircle2"),
    Circle: icon("Circle"),
    Loader2: icon("Loader2"),
    Ban: icon("Ban"),
    Pause: icon("Pause"),
    ArrowUpRight: icon("ArrowUpRight"),
  };
});

describe("formatTimestamp", () => {
  it("returns dash for undefined", () => {
    expect(formatTimestamp(undefined)).toBe("-");
  });
  it("returns dash for empty string", () => {
    expect(formatTimestamp("")).toBe("-");
  });
  it("formats a valid date string", () => {
    const result = formatTimestamp("2024-01-15T10:30:00");
    expect(result).toBe("Jan 15, 10:30 AM");
    expect(result).not.toContain(":00");
  });
});

describe("ProgressBar", () => {
  it("renders 0% when total is 0", () => {
    renderWithProviders(<ProgressBar completed={0} total={0} />);
    expect(screen.getByText("0/0 (0%)")).toBeInTheDocument();
  });
  it("renders correct percentage", () => {
    renderWithProviders(<ProgressBar completed={3} total={10} />);
    expect(screen.getByText("3/10 (30%)")).toBeInTheDocument();
  });
  it("renders 100% when all completed", () => {
    renderWithProviders(<ProgressBar completed={5} total={5} />);
    expect(screen.getByText("5/5 (100%)")).toBeInTheDocument();
  });
});

describe("CollapsibleSection", () => {
  it("renders title and children when defaultOpen", () => {
    renderWithProviders(
      <CollapsibleSection title="Test Section" icon={FileText} defaultOpen={true}>
        <p>Section content</p>
      </CollapsibleSection>
    );
    expect(screen.getByText("Test Section")).toBeInTheDocument();
    expect(screen.getByText("Section content")).toBeInTheDocument();
  });

  it("hides children when defaultOpen is false", () => {
    renderWithProviders(
      <CollapsibleSection title="Test Section" icon={FileText} defaultOpen={false}>
        <p>Hidden content</p>
      </CollapsibleSection>
    );
    expect(screen.getByText("Test Section")).toBeInTheDocument();
    expect(screen.queryByText("Hidden content")).not.toBeInTheDocument();
  });

  it("toggles content on click", () => {
    renderWithProviders(
      <CollapsibleSection title="Toggle Me" icon={FileText} defaultOpen={true}>
        <p>Toggleable content</p>
      </CollapsibleSection>
    );
    expect(screen.getByText("Toggleable content")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Toggle Me"));
    expect(screen.queryByText("Toggleable content")).not.toBeInTheDocument();
    fireEvent.click(screen.getByText("Toggle Me"));
    expect(screen.getByText("Toggleable content")).toBeInTheDocument();
  });

  it("renders count badge when count > 0", () => {
    renderWithProviders(
      <CollapsibleSection title="With Count" icon={FileText} count={5}>
        <p>Content</p>
      </CollapsibleSection>
    );
    expect(screen.getByText("5")).toBeInTheDocument();
  });

  it("does not render count badge when count is 0", () => {
    renderWithProviders(
      <CollapsibleSection title="No Count" icon={FileText} count={0}>
        <p>Content</p>
      </CollapsibleSection>
    );
    // The "0" badge should not appear
    const badges = screen.queryAllByText("0");
    // Filter for badges only (not other elements that might contain 0)
    expect(badges.length).toBe(0);
  });

  it("renders actions slot", () => {
    renderWithProviders(
      <CollapsibleSection title="Actions" icon={FileText} actions={<button>Action</button>}>
        <p>Content</p>
      </CollapsibleSection>
    );
    expect(screen.getByText("Action")).toBeInTheDocument();
  });
});

describe("config objects", () => {
  it("taskStatusConfig has expected keys", () => {
    expect(taskStatusConfig).toHaveProperty("pending");
    expect(taskStatusConfig).toHaveProperty("completed");
    expect(taskStatusConfig).toHaveProperty("failed");
    expect(taskStatusConfig).toHaveProperty("running");
    expect(taskStatusConfig.pending.label).toBe("Pending");
    expect(taskStatusConfig.completed.label).toBe("Completed");
  });

  it("specTypeConfig has expected keys", () => {
    expect(specTypeConfig).toHaveProperty("prd");
    expect(specTypeConfig).toHaveProperty("technical");
    expect(specTypeConfig.prd.label).toBe("PRD");
  });

  it("attachmentCategoryConfig has expected keys", () => {
    expect(attachmentCategoryConfig).toHaveProperty("screenshot");
    expect(attachmentCategoryConfig).toHaveProperty("mockup");
    expect(attachmentCategoryConfig.screenshot.label).toBe("Screenshot");
  });
});
