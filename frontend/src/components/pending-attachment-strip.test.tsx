import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { PendingAttachmentStrip } from "./pending-attachment-strip";

describe("PendingAttachmentStrip", () => {
  it("shows a larger preview on hover for image attachments", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <PendingAttachmentStrip
        attachments={["/api/v1/uploads/files/org-1/2026-04/screenshot.png"]}
        onRemove={vi.fn()}
      />,
    );

    const thumbnail = screen.getByRole("button", { name: "Preview screenshot.png" });
    await user.hover(thumbnail);

    await waitFor(() => {
      expect(screen.getByAltText("Preview of screenshot.png")).toBeInTheDocument();
    });

    expect(screen.getByAltText("Preview of screenshot.png")).toHaveClass("max-h-[70vh]");
    expect(screen.getByAltText("Preview of screenshot.png")).toHaveClass("max-w-[min(70vw,56rem)]");
  });

  it("opens a lightbox when clicking an image attachment", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <PendingAttachmentStrip
        attachments={["/api/v1/uploads/files/org-1/2026-04/screenshot.png"]}
        onRemove={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Preview screenshot.png" }));

    expect(screen.getByRole("dialog", { name: "Image preview" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "screenshot.png" })).toBeInTheDocument();
  });

  it("removes attachments from the dedicated remove control", async () => {
    const user = userEvent.setup();
    const onRemove = vi.fn();

    renderWithProviders(
      <PendingAttachmentStrip
        attachments={["/api/v1/uploads/files/org-1/2026-04/screenshot.png"]}
        onRemove={onRemove}
      />,
    );

    const removeButton = screen.getByRole("button", { name: "Remove screenshot.png" });
    expect(removeButton).toHaveClass("top-0", "right-0");

    await user.click(removeButton);

    expect(onRemove).toHaveBeenCalledWith("/api/v1/uploads/files/org-1/2026-04/screenshot.png");
  });

  it("renders non-image attachments without preview triggers", () => {
    renderWithProviders(
      <PendingAttachmentStrip
        attachments={["/api/v1/uploads/files/org-1/2026-04/debug.txt"]}
        onRemove={vi.fn()}
      />,
    );

    expect(screen.getByText("debug.txt")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Preview debug.txt" })).not.toBeInTheDocument();
  });
});
