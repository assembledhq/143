import { useEffect, useState, type DragEvent } from "react";

type UseFileDropzoneOptions = {
  enabled?: boolean;
  onFilesDropped: (files: File[]) => Promise<void> | void;
  onAfterDrop?: () => void;
  getDragMessage?: (files: File[]) => string;
};

function isFileDrag(event: DragEvent<HTMLElement>) {
  const types = Array.from(event.dataTransfer.types ?? []);
  return types.includes("Files");
}

export function useFileDropzone({
  enabled = true,
  onFilesDropped,
  onAfterDrop,
  getDragMessage,
}: UseFileDropzoneOptions) {
  const [isDragActive, setIsDragActive] = useState(false);
  const [dragMessage, setDragMessage] = useState<string | null>(null);

  function resetDragState() {
    setIsDragActive(false);
    setDragMessage(null);
  }

  useEffect(() => {
    if (enabled) {
      return;
    }
    // eslint-disable-next-line react-hooks/set-state-in-effect -- disabled state invalidates any in-progress browser drag gesture.
    resetDragState();
  }, [enabled]);

  function getFiles(event: DragEvent<HTMLElement>) {
    return Array.from(event.dataTransfer.files ?? []);
  }

  function updateDragMessage(event: DragEvent<HTMLElement>) {
    if (!getDragMessage) {
      return;
    }
    setDragMessage(getDragMessage(getFiles(event)));
  }

  function handleDragEnter(event: DragEvent<HTMLElement>) {
    if (!enabled || !isFileDrag(event)) {
      return;
    }
    event.preventDefault();
    setIsDragActive(true);
    updateDragMessage(event);
  }

  function handleDragOver(event: DragEvent<HTMLElement>) {
    if (!enabled || !isFileDrag(event)) {
      return;
    }
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
    setIsDragActive(true);
    updateDragMessage(event);
  }

  function handleDragLeave(event: DragEvent<HTMLElement>) {
    if (!enabled || !isFileDrag(event)) {
      return;
    }
    event.preventDefault();
    if (event.target !== event.currentTarget) {
      return;
    }
    const nextTarget = event.relatedTarget ?? event.nativeEvent.relatedTarget;
    if (nextTarget instanceof Node && event.currentTarget.contains(nextTarget)) {
      return;
    }
    resetDragState();
  }

  async function handleDrop(event: DragEvent<HTMLElement>) {
    if (!enabled || !isFileDrag(event)) {
      return;
    }
    event.preventDefault();
    const files = getFiles(event);
    resetDragState();
    if (files.length === 0) {
      return;
    }
    await onFilesDropped(files);
    onAfterDrop?.();
  }

  return {
    isDragActive,
    dragMessage,
    dropzoneProps: {
      "data-drag-active": isDragActive ? "true" : "false",
      onDragEnter: handleDragEnter,
      onDragOver: handleDragOver,
      onDragLeave: handleDragLeave,
      onDrop: handleDrop,
    },
  };
}
