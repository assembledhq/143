import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AgentNote, ConfigField, Screenshot, getDocsMDXComponents } from "./docs-mdx-components";

describe("docs MDX components", () => {
  it("exposes 143-specific MDX components alongside Fumadocs defaults", () => {
    const components = getDocsMDXComponents();

    expect(components.AgentNote).toBe(AgentNote);
    expect(components.ConfigField).toBe(ConfigField);
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
});
