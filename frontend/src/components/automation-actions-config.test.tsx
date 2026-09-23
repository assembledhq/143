import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AutomationActionsConfig, actionConfigError } from "./automation-actions-config";

describe("automation action configuration", () => {
  it.each([
    ["Slack only", { actions: ["slack_notification"], slack_channel_id: "C0123456789" }, true],
    ["GitHub comments only", { actions: ["github_issue_comment"], repository: "owner/repo" }, true],
    ["no actions", {}, false],
    ["unknown action", { actions: ["run_shell"] }, false],
    ["missing channel", { actions: ["slack_notification"] }, false],
    ["Notion undashed UUID", { actions: ["notion_tracking_row"], notion_data_source_id: "379d57062bc08021a0e6000b04d8902d", notion_properties: { Report: "title", Notes: "rich_text" } }, true],
    ["missing Notion title", { actions: ["notion_tracking_row"], notion_data_source_id: "379d57062bc08021a0e6000b04d8902d", notion_properties: { Notes: "rich_text" } }, false],
  ])("validates %s", (_name, config, valid) => {
    expect(actionConfigError(config) === null).toBe(valid);
  });
  it("saves a Slack-only configuration without unrelated destinations", async () => {
    const user = userEvent.setup(); const save = vi.fn();
    render(<AutomationActionsConfig config={{}} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    await user.click(screen.getByLabelText("Send a Slack message"));
    await user.type(screen.getByLabelText("Slack channel ID"), "C0123456789");
    expect(screen.queryByLabelText("Repository")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Notion data source ID")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save configuration" }));
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ actions: ["slack_notification"], slack_channel_id: "C0123456789" }));
  });
  it("keeps configuration unavailable without admin permission", () => {
    render(<AutomationActionsConfig config={{}} disabled onSave={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Configure actions" })).toBeDisabled();
  });
  it("stacks Notion property controls on narrow screens", async () => {
    const user = userEvent.setup();
    render(<AutomationActionsConfig config={{}} onSave={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    await user.click(screen.getByLabelText("Create a Notion page"));
    await user.click(screen.getByRole("button", { name: "Add property" }));
    expect(screen.getByTestId("notion-property-row")).toHaveClass("flex-col", "sm:flex-row");
    expect(screen.getByLabelText("Property 1 type")).toHaveClass("w-full", "sm:w-40");
  });
});
