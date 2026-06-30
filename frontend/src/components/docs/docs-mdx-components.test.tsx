import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  AgentNote,
  BoundaryDiagram,
  ConfigField,
  FlowDiagram,
  Screenshot,
  getDocsMDXComponents,
} from "./docs-mdx-components";

describe("docs MDX components", () => {
  it("exposes 143-specific MDX components alongside Fumadocs defaults", () => {
    const components = getDocsMDXComponents();

    expect(components.AgentNote).toBe(AgentNote);
    expect(components.BoundaryDiagram).toBe(BoundaryDiagram);
    expect(components.ConfigField).toBe(ConfigField);
    expect(components.FlowDiagram).toBe(FlowDiagram);
    expect(components.Screenshot).toBe(Screenshot);
    expect(components.Callout).toBeDefined();
    expect(components.Tabs).toBeDefined();
    expect(components.Steps).toBeDefined();
  });

  it("renders config fields with type, required state, default, and description", () => {
    render(
      <ConfigField
        name="preview.primary"
        type="string"
        required
        defaultValue="app"
        description="Service key that receives browser traffic."
      />
    );

    expect(screen.getByText("preview.primary")).toBeInTheDocument();
    expect(screen.getByText("string")).toBeInTheDocument();
    expect(screen.getByText("required")).toBeInTheDocument();
    expect(screen.getByText("default: app")).toBeInTheDocument();
    expect(
      screen.getByText("Service key that receives browser traffic.")
    ).toBeInTheDocument();
  });

  it("renders agent notes as public docs callouts", () => {
    render(<AgentNote>Use raw Markdown when ingesting these docs.</AgentNote>);

    expect(screen.getByText("For agents")).toBeInTheDocument();
    expect(screen.getByText("Use raw Markdown when ingesting these docs.")).toBeInTheDocument();
  });

  it("renders flow diagrams with ordered steps and captions", () => {
    render(
      <FlowDiagram
        items={["Issue", "Session", "Preview", "PR"]}
        caption="A 143 workflow keeps code changes attached to review."
      />
    );

    expect(screen.getByText("Issue")).toBeInTheDocument();
    expect(screen.getByText("Session")).toBeInTheDocument();
    expect(screen.getByText("Preview")).toBeInTheDocument();
    expect(screen.getByText("PR")).toBeInTheDocument();
    expect(
      screen.getByText("A 143 workflow keeps code changes attached to review.")
    ).toBeInTheDocument();
  });

  it("renders boundary diagrams with separate item groups and captions", () => {
    render(
      <BoundaryDiagram
        leftTitle="Keep in settings"
        leftItems={["Provider credentials", "Model defaults"]}
        rightTitle="Keep out of prompts"
        rightItems={["API keys", "Database URLs"]}
        caption="Secrets belong in runtime settings, not task context."
      />
    );

    expect(screen.getByText("Keep in settings")).toBeInTheDocument();
    expect(screen.getByText("Provider credentials")).toBeInTheDocument();
    expect(screen.getByText("Model defaults")).toBeInTheDocument();
    expect(screen.getByText("Keep out of prompts")).toBeInTheDocument();
    expect(screen.getByText("API keys")).toBeInTheDocument();
    expect(screen.getByText("Database URLs")).toBeInTheDocument();
    expect(
      screen.getByText("Secrets belong in runtime settings, not task context.")
    ).toBeInTheDocument();
  });
});
