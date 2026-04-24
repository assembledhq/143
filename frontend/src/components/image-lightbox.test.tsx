import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { ImageLightbox } from "./image-lightbox";

describe("ImageLightbox", () => {
  it("renders a full-screen centered stage with a viewport-anchored close button", () => {
    const onOpenChange = vi.fn();

    renderWithProviders(
      <ImageLightbox
        open
        src="/api/v1/uploads/files/org-1/2026-04/screenshot.png"
        alt="screenshot.png"
        onOpenChange={onOpenChange}
      />,
    );

    const dialog = screen.getByRole("dialog", { name: "Image preview" });
    expect(dialog).toHaveClass("inset-0");
    expect(dialog).toHaveClass("flex");
    expect(dialog).toHaveClass("items-center");
    expect(dialog).toHaveClass("justify-center");
    expect(dialog).toHaveClass("h-screen");
    expect(dialog).toHaveClass("w-screen");
    expect(dialog).toHaveClass("border-none");
    expect(dialog).toHaveClass("bg-transparent");

    const image = screen.getByRole("img", { name: "screenshot.png" });
    expect(image).toHaveClass("max-h-[88vh]");
    expect(image).toHaveClass("max-w-[92vw]");

    const closeButton = screen.getByRole("button", { name: "Close image preview" });
    expect(closeButton).toHaveClass("absolute");
    expect(closeButton).toHaveClass("right-4");
    expect(closeButton).toHaveClass("top-4");
    expect(dialog).toContainElement(closeButton);
  });

  it("closes when the backdrop is clicked", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();

    renderWithProviders(
      <ImageLightbox
        open
        src="/api/v1/uploads/files/org-1/2026-04/screenshot.png"
        alt="screenshot.png"
        onOpenChange={onOpenChange}
      />,
    );

    const backdrop = document.querySelector("[data-slot='dialog-overlay']");
    expect(backdrop).not.toBeNull();

    await user.click(backdrop as Element);

    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
  });
});
