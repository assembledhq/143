import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useFileDropzone } from "./use-file-dropzone";

function fileDragPayload(file: File) {
  return {
    dataTransfer: {
      files: [file],
      items: [{ kind: "file", type: file.type, getAsFile: () => file }],
      types: ["Files"],
    },
  };
}

function DropzoneHarness({
  enabled = true,
  onFilesDropped,
  onAfterDrop,
}: {
  enabled?: boolean;
  onFilesDropped: (files: File[]) => Promise<void> | void;
  onAfterDrop?: () => void;
}) {
  const dropzone = useFileDropzone({
    enabled,
    onFilesDropped,
    onAfterDrop,
    getDragMessage: (files) => files.length === 1 ? `Drop ${files[0].name}` : "Drop files",
  });

  return (
    <div data-testid="dropzone" {...dropzone.dropzoneProps}>
      <span>{dropzone.dragMessage ?? "No drag"}</span>
      <span>{dropzone.isDragActive ? "Active" : "Inactive"}</span>
    </div>
  );
}

describe("useFileDropzone", () => {
  it("sets active drag state and message for file drags", () => {
    const onFilesDropped = vi.fn();
    const file = new File(["image-bytes"], "shot.png", { type: "image/png" });

    render(<DropzoneHarness onFilesDropped={onFilesDropped} />);

    fireEvent.dragEnter(screen.getByTestId("dropzone"), fileDragPayload(file));

    expect(screen.getByTestId("dropzone")).toHaveAttribute("data-drag-active", "true");
    expect(screen.getByText("Drop shot.png")).toBeInTheDocument();
  });

  it("uploads dropped files, resets drag state, and runs the after-drop callback", async () => {
    const onFilesDropped = vi.fn().mockResolvedValue(undefined);
    const onAfterDrop = vi.fn();
    const file = new File(["image-bytes"], "drop.png", { type: "image/png" });

    render(<DropzoneHarness onFilesDropped={onFilesDropped} onAfterDrop={onAfterDrop} />);
    const dropzone = screen.getByTestId("dropzone");

    fireEvent.dragEnter(dropzone, fileDragPayload(file));
    fireEvent.drop(dropzone, fileDragPayload(file));

    await waitFor(() => {
      expect(onFilesDropped).toHaveBeenCalledWith([file]);
    });
    expect(dropzone).toHaveAttribute("data-drag-active", "false");
    expect(onAfterDrop).toHaveBeenCalledTimes(1);
  });

  it("ignores file drops when disabled", () => {
    const onFilesDropped = vi.fn();
    const file = new File(["image-bytes"], "disabled.png", { type: "image/png" });

    render(<DropzoneHarness enabled={false} onFilesDropped={onFilesDropped} />);
    const dropzone = screen.getByTestId("dropzone");

    fireEvent.dragEnter(dropzone, fileDragPayload(file));
    fireEvent.drop(dropzone, fileDragPayload(file));

    expect(dropzone).toHaveAttribute("data-drag-active", "false");
    expect(onFilesDropped).not.toHaveBeenCalled();
  });

  it("clears active drag state when the dropzone becomes disabled mid-drag", async () => {
    const onFilesDropped = vi.fn();
    const file = new File(["image-bytes"], "stale.png", { type: "image/png" });

    const { rerender } = render(<DropzoneHarness enabled onFilesDropped={onFilesDropped} />);
    const dropzone = screen.getByTestId("dropzone");

    fireEvent.dragEnter(dropzone, fileDragPayload(file));
    expect(dropzone).toHaveAttribute("data-drag-active", "true");

    rerender(<DropzoneHarness enabled={false} onFilesDropped={onFilesDropped} />);

    await waitFor(() => {
      expect(dropzone).toHaveAttribute("data-drag-active", "false");
    });
  });
});
