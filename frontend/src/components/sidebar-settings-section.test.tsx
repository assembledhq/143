import { describe, expect, it } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { SidebarSettingsSection } from "./sidebar-settings-section";

describe("SidebarSettingsSection", () => {
  it("matches desktop primary nav icon sizing and row spacing", () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/sessions" userRole="admin" />,
    );

    expect(screen.getByTestId("sidebar-settings-divider")).toHaveClass("border-t");

    const trigger = screen.getByRole("button", { name: /Settings/ });
    expect(trigger).toHaveClass("gap-2.5", "py-[7px]", "text-xs");
    expect(trigger).not.toHaveClass("gap-2", "py-1.5");

    const settingsIcon = trigger.querySelector("svg");
    expect(settingsIcon).toHaveClass("h-4", "w-4");
  });

  it("uses the same touch target sizing as other mobile nav tabs", () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/sessions" userRole="admin" variant="mobile" />,
    );

    expect(screen.getByRole("button", { name: /Settings/ })).toHaveClass("px-2.5", "py-3", "text-sm");
  });

  it("uses mobile-sized text for nested settings links in the mobile nav drawer", () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings" userRole="admin" variant="mobile" />,
    );

    expect(screen.getByRole("link", { name: "Account" })).toHaveClass("py-2.5", "text-sm");
    expect(screen.getByRole("link", { name: "General" })).toHaveClass("py-2.5", "text-sm");
  });

  it("shows admin-only entries when role is admin", () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings" userRole="admin" />,
    );

    for (const label of [
      "Account",
      "Integrations",
      "Coding agents",
      "LLM",
      "Autopilot",
      "Runtime",
      "Preview",
      "Evals",
      "General",
      "Team",
      "Usage",
      "Audit log",
    ]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
  });

  it("shows view-only pages for members and hides admin-only pages", () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings/account" userRole="member" />,
    );

    for (const visible of [
      "Account",
      "Evals",
      "Team",
      "Integrations",
      "Coding agents",
    ]) {
      expect(screen.getByText(visible)).toBeInTheDocument();
    }

    for (const hidden of [
      "LLM",
      "Autopilot",
      "Runtime",
      "Preview",
      "General",
      "Usage",
      "Audit log",
    ]) {
      expect(screen.queryByText(hidden)).not.toBeInTheDocument();
    }
  });

  it("shows only Account for viewers", () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings/account" userRole="viewer" />,
    );

    expect(screen.getByText("Account")).toBeInTheDocument();

    for (const hidden of [
      "Integrations",
      "Coding agents",
      "LLM",
      "Autopilot",
      "Runtime",
      "Preview",
      "Evals",
      "General",
      "Team",
      "Usage",
      "Audit log",
    ]) {
      expect(screen.queryByText(hidden)).not.toBeInTheDocument();
    }
  });

  it("shows only builder-safe settings entries for builders", () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings/account" userRole="builder" />,
    );

    for (const visible of [
      "Account",
      "Coding agents",
    ]) {
      expect(screen.getByText(visible)).toBeInTheDocument();
    }

    for (const hidden of [
      "Integrations",
      "Preview",
      "Runtime",
      "Evals",
      "Team",
      "LLM",
      "Autopilot",
      "General",
      "Usage",
      "Audit log",
    ]) {
      expect(screen.queryByText(hidden)).not.toBeInTheDocument();
    }
  });

  it("labels the preview settings item as Preview", () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings/previews" userRole="admin" />,
    );

    expect(screen.getByRole("link", { name: /preview/i })).toHaveAttribute("href", "/settings/previews");
    expect(screen.queryByText("Preview API")).not.toBeInTheDocument();
  });
});
