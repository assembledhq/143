import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AutomationActionsConfig, actionConfigError } from "./automation-actions-config";

describe("automation action configuration", () => {
  it.each([
    ["Slack only", { actions: ["slack_notification"], slack_channel_id: "C0123456789" }, true],
    ["Slack with a user group", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_user_group_id: "S0123456789" }, true],
    ["Slack with an empty user group", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_user_group_id: "" }, true],
    ["Slack with a channel as its user group", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_user_group_id: "C0123456789" }, false],
    ["Slack with a lowercase user group", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_user_group_id: "Sabcdefgh" }, false],
    ["Slack with a short user group", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_user_group_id: "S0123456" }, false],
    ["Slack with an oversized user group", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_user_group_id: `S${"0".repeat(32)}` }, false],
    ["Slack with user group markup", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_user_group_id: "<!subteam^S0123456789>" }, false],
    ["inactive Slack with retained user group markup", { actions: ["github_issue_comment"], repository: "owner/repo", slack_user_group_id: "<!subteam^S0123456789>" }, true],
    ["Slack with a PR author map", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config/team.json" }, true],
    ["Slack with an empty PR author map", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "" }, true],
    ["Slack with a hidden JSON map", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: ".json" }, true],
    ["Slack with an absolute PR author map", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "/config/team.json" }, false],
    ["Slack with parent traversal", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "../team.json" }, false],
    ["Slack with a dot segment", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config/./team.json" }, false],
    ["Slack with an empty segment", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config//team.json" }, false],
    ["Slack with a non-JSON map", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config/team.yaml" }, false],
    ["Slack with a map query", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config/team.json?raw=1" }, false],
    ["Slack with a map fragment", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config/team.json#team" }, false],
    ["Slack with encoded traversal", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config/%2e%2e/team.json" }, false],
    ["Slack with backslashes", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config\\team.json" }, false],
    ["Slack with a control character", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config/\nteam.json" }, false],
    ["Slack with a maximum-length map", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: `${"a".repeat(251)}.json` }, true],
    ["Slack with an oversized map", { actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: `${"a".repeat(252)}.json` }, false],
    ["inactive Slack with a retained map", { actions: ["github_issue_comment"], repository: "owner/repo", slack_pr_author_map_path: "../team.json" }, true],
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
    expect(save.mock.calls[0][0]).not.toHaveProperty("slack_user_group_id");
    expect(save.mock.calls[0][0]).not.toHaveProperty("slack_pr_author_map_path");
  });
  it("edits, reloads and clears a saved Slack user group", async () => {
    const user = userEvent.setup();
    const save = vi.fn();
    const { rerender } = render(<AutomationActionsConfig config={{ actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_user_group_id: "S0123456789" }} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    const group = screen.getByLabelText("Slack user group ID (optional)");
    expect(group).toHaveValue("S0123456789");
    await user.clear(group);
    await user.type(group, "S9876543210");
    await user.click(screen.getByRole("button", { name: "Save configuration" }));
    expect(save).toHaveBeenLastCalledWith(expect.objectContaining({ slack_user_group_id: "S9876543210" }));

    rerender(<AutomationActionsConfig config={save.mock.calls[0][0]} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    expect(screen.getByLabelText("Slack user group ID (optional)")).toHaveValue("S9876543210");
    await user.clear(screen.getByLabelText("Slack user group ID (optional)"));
    await user.click(screen.getByRole("button", { name: "Save configuration" }));
    expect(save.mock.calls[1][0]).not.toHaveProperty("slack_user_group_id");

    rerender(<AutomationActionsConfig config={save.mock.calls[1][0]} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    expect(screen.getByLabelText("Slack user group ID (optional)")).toHaveValue("");
  });
  it("retains the group while Slack notifications are turned off", async () => {
    const user = userEvent.setup();
    const save = vi.fn();
    render(<AutomationActionsConfig config={{ actions: ["github_issue_comment", "slack_notification"], repository: "owner/repo", slack_channel_id: "C0123456789", slack_user_group_id: "S0123456789" }} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    await user.click(screen.getByRole("checkbox", { name: "Send a Slack message" }));
    expect(screen.queryByLabelText("Slack user group ID (optional)")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save configuration" }));
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ actions: ["github_issue_comment"], slack_user_group_id: "S0123456789" }));
  });
  it("edits, reloads and clears a saved PR author map", async () => {
    const user = userEvent.setup();
    const save = vi.fn();
    const { rerender } = render(<AutomationActionsConfig config={{ actions: ["slack_notification"], slack_channel_id: "C0123456789", slack_pr_author_map_path: "config/team.json" }} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    const path = screen.getByLabelText("PR author mapping file (optional)");
    expect(path).toHaveValue("config/team.json");
    expect(path).toHaveAccessibleDescription("In continuous PR sessions, tags the PR author using github_name and slack_id values from this JSON file on the repository’s default branch.");
    await user.clear(path);
    await user.type(path, "config/people.json");
    await user.click(screen.getByRole("button", { name: "Save configuration" }));
    expect(save).toHaveBeenLastCalledWith(expect.objectContaining({ slack_pr_author_map_path: "config/people.json" }));

    rerender(<AutomationActionsConfig config={save.mock.calls[0][0]} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    expect(screen.getByLabelText("PR author mapping file (optional)")).toHaveValue("config/people.json");
    await user.clear(screen.getByLabelText("PR author mapping file (optional)"));
    await user.click(screen.getByRole("button", { name: "Save configuration" }));
    expect(save.mock.calls[1][0]).not.toHaveProperty("slack_pr_author_map_path");

    rerender(<AutomationActionsConfig config={save.mock.calls[1][0]} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    expect(screen.getByLabelText("PR author mapping file (optional)")).toHaveValue("");
  });
  it("retains the PR author map while Slack notifications are turned off", async () => {
    const user = userEvent.setup();
    const save = vi.fn();
    render(<AutomationActionsConfig config={{ actions: ["github_issue_comment", "slack_notification"], repository: "owner/repo", slack_channel_id: "C0123456789", slack_pr_author_map_path: "config/team.json" }} onSave={save} />);
    await user.click(screen.getByRole("button", { name: "Configure actions" }));
    await user.click(screen.getByRole("checkbox", { name: "Send a Slack message" }));
    expect(screen.queryByLabelText("PR author mapping file (optional)")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save configuration" }));
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ actions: ["github_issue_comment"], slack_pr_author_map_path: "config/team.json" }));
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
