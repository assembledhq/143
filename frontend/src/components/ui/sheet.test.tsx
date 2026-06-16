import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "./sheet";

describe("SheetContent", () => {
  it("provides vertical scrolling by default for side sheets", async () => {
    render(
      <Sheet open>
        <SheetContent>
          <SheetTitle>Details</SheetTitle>
          <SheetDescription>Scrollable details</SheetDescription>
          <div style={{ height: "200vh" }}>Tall content</div>
        </SheetContent>
      </Sheet>,
    );

    const dialog = await screen.findByRole("dialog");
    expect(dialog.className).toContain("overflow-y-auto");
  });

  it("uses most of the mobile viewport for side sheets while leaving modal context visible", async () => {
    render(
      <Sheet open>
        <SheetContent>
          <SheetTitle>Details</SheetTitle>
          <SheetDescription>Wide mobile details</SheetDescription>
        </SheetContent>
      </Sheet>,
    );

    const dialog = await screen.findByRole("dialog");
    expect(dialog.className).toContain("w-[calc(100vw-2rem)]");
  });

  it("allows consumers to override overflow behavior when needed", async () => {
    render(
      <Sheet open>
        <SheetContent className="overflow-hidden">
          <SheetTitle>Managed layout</SheetTitle>
          <SheetDescription>Managed overflow</SheetDescription>
          <div>Tall content</div>
        </SheetContent>
      </Sheet>,
    );

    const dialog = await screen.findByRole("dialog");
    expect(dialog.className).toContain("overflow-hidden");
  });

  it("bounds bottom sheets to the viewport so overflow can scroll internally", async () => {
    render(
      <Sheet open>
        <SheetContent side="bottom">
          <SheetTitle>Bottom sheet</SheetTitle>
          <SheetDescription>Viewport bounded</SheetDescription>
          <div style={{ height: "200vh" }}>Tall content</div>
        </SheetContent>
      </Sheet>,
    );

    const dialog = await screen.findByRole("dialog");
    expect(dialog.className).toContain("max-h-[100svh]");
  });
});
