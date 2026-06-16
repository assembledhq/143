import { describe, expect, it } from "vitest";
import {
  automationProductTriggersToGitHubEvents,
  githubEventsToAutomationProductTriggers,
} from "./automation-triggers";

describe("automation trigger mapping", () => {
  it("expands new PR feedback to all underlying GitHub feedback events", () => {
    expect(
      automationProductTriggersToGitHubEvents(["github.pr.feedback"]),
    ).toEqual([
      "github.issue_comment.created",
      "github.pull_request_review.submitted",
      "github.pull_request_review_comment.created",
    ]);
  });

  it("expands all product-level pull request triggers to raw GitHub events", () => {
    expect(
      automationProductTriggersToGitHubEvents([
        "github.pr.opened",
        "github.pr.updated",
        "github.pr.feedback",
        "github.checks.completed",
        "github.pr.merged",
      ]),
    ).toEqual([
      "github.pull_request.opened",
      "github.pull_request.updated",
      "github.issue_comment.created",
      "github.pull_request_review.submitted",
      "github.pull_request_review_comment.created",
      "github.check_suite.completed",
      "github.pull_request.merged",
    ]);
  });

  it("deduplicates expanded GitHub events while preserving product trigger order", () => {
    expect(
      automationProductTriggersToGitHubEvents([
        "github.pr.feedback",
        "github.pr.opened",
        "github.pr.feedback",
      ]),
    ).toEqual([
      "github.issue_comment.created",
      "github.pull_request_review.submitted",
      "github.pull_request_review_comment.created",
      "github.pull_request.opened",
    ]);
  });

  it("coalesces raw feedback events back into the new PR feedback product trigger", () => {
    expect(
      githubEventsToAutomationProductTriggers([
        "github.pull_request_review_comment.created",
        "github.issue_comment.created",
        "github.pull_request.merged",
      ]),
    ).toEqual(["github.pr.feedback", "github.pr.merged"]);
  });

  it("coalesces either GitHub checks event into the checks product trigger", () => {
    expect(
      githubEventsToAutomationProductTriggers(["github.check_run.completed"]),
    ).toEqual(["github.checks.completed"]);
  });
});
