import { describe, expect, it } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { Button } from "@/components/ui/button";
import { PageHeader } from "./page-header";

describe("PageHeader", () => {
  it("gives header actions a full-width mobile wrapper", () => {
    renderWithProviders(
      <PageHeader
        title="Settings"
        description="Manage the org."
        action={<Button>Invite</Button>}
      />,
    );

    const actionButton = screen.getByRole("button", { name: "Invite" });
    const actionWrapper = actionButton.parentElement;

    expect(actionWrapper).toHaveClass("w-full");
    expect(actionWrapper).toHaveClass("sm:w-auto");
    expect(actionWrapper).toHaveClass("[&>*]:w-full");
    expect(actionWrapper).toHaveClass("sm:[&>*]:w-auto");
  });
});
