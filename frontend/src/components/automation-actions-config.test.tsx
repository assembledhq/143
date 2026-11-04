import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AutomationActionsConfig, actionConfigError } from "./automation-actions-config";

describe("automation action configuration", () => {
  it.each([
    ["Slack only", { actions: ["slack_notification"], slack_channel_id: "C0123456789" }, true],
    ["GitHub comments only", { actions: ["github_issue_comment"], repository: "owner/repo" }, true],
    ["GitHub comment updates only", { actions: ["github_issue_comment_update"], repository: "owner/repo" }, true],
    ["GitHub comment updates without a repository", { actions: ["github_issue_comment_update"] }, false],
    ["GitHub inline reviews only", { actions: ["github_pull_request_review"], repository: "owner/repo" }, true],
    ["GitHub inline reviews without a repository", { actions: ["github_pull_request_review"] }, false],
    ["all actions", {
      actions: ["github_label", "github_team_review", "github_issue_comment", "github_issue_comment_update", "github_pull_request_review", "notion_tracking_row", "slack_notification"],
      repository: "owner/repo",
      label: "needs-review",
      team: "reviewers",
      notion_data_source_id: "379d57062bc08021a0e6000b04d8902d",
      notion_properties: { Report: "title" },
      slack_channel_id: "C0123456789",
    }, true],
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
  it("saves comment updates separately from comment creation", async () => {
    const user = userEvent.setup();
    const save = vi.fn();
    render(<AutomationActionsConfig config={{}} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    await user.click(screen.getByRole("checkbox", { name: "Update this automation’s GitHub PR comments" }));
    expect(screen.getByRole("checkbox", { name: "Comment on a GitHub PR" })).not.toBeChecked();
    await user.type(screen.getByLabelText("Repository"), "owner/repo");
    await user.click(screen.getByRole("button", { name: "Save configuration" }));
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ actions: ["github_issue_comment_update"], repository: "owner/repo" }));
  });
  it("grants inline reviews without granting issue comments or team review requests", async () => {
    const user = userEvent.setup();
    const save = vi.fn();
    render(<AutomationActionsConfig config={{}} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    await user.click(screen.getByRole("checkbox", { name: "Post an advisory GitHub PR review with inline comments" }));
    expect(screen.getByRole("checkbox", { name: "Comment on a GitHub PR" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Request a GitHub team review" })).not.toBeChecked();
    await user.type(screen.getByLabelText("Repository"), "owner/repo");
    await user.click(screen.getByRole("button", { name: "Save configuration" }));
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ actions: ["github_pull_request_review"], repository: "owner/repo" }));
  });
  it("keeps configuration unavailable without admin permission", () => {
    render(<AutomationActionsConfig config={{}} disabled onSave={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Configure actions" })).toBeDisabled();
  });
});
