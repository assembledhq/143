"use client";

import {
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { useMutation } from "@tanstack/react-query";
import {
  MousePointer2,
  Send,
  X,
  Pencil,
  AlertTriangle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type {
  ElementInfo,
  DesignModeFeedback,
} from "@/lib/preview-types";
import { VisualEditingPanel } from "./visual-editing-panel";

interface DesignModeOverlayProps {
  sessionId: string;
}

interface SelectedElement {
  info: ElementInfo;
  selector: string;
}

function buildSelector(info: ElementInfo): string {
  const id = info.attributes?.id;
  if (id) return `#${CSS.escape(id)}`;
  const classList = info.attributes?.class?.split(/\s+/).filter(Boolean) ?? [];
  if (classList.length > 0) {
    return `${info.tag_name}.${classList.map((c: string) => CSS.escape(c)).join(".")}`;
  }
  return info.tag_name;
}

export function DesignModeOverlay({
  sessionId,
}: DesignModeOverlayProps) {
  const overlayRef = useRef<HTMLDivElement>(null);

  const [selectedElements, setSelectedElements] = useState<SelectedElement[]>(
    []
  );
  const [instruction, setInstruction] = useState("");
  const [showEditPanel, setShowEditPanel] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Inspect element at coordinates
  const inspectMutation = useMutation({
    mutationFn: ({ x, y }: { x: number; y: number }) =>
      api.sessions.preview.inspect(sessionId, x, y),
    onError: (err) => {
      setError(`Failed to inspect element: ${err.message}`);
    },
  });

  const inspectMutateRef = useRef(inspectMutation.mutate);
  useEffect(() => {
    inspectMutateRef.current = inspectMutation.mutate;
  }, [inspectMutation.mutate]);

  // Send design feedback to agent
  const feedbackMutation = useMutation({
    mutationFn: (feedback: DesignModeFeedback) =>
      api.sessions.preview.designFeedback(sessionId, feedback),
    onSuccess: () => {
      setError(null);
      setSelectedElements([]);
      setInstruction("");
    },
    onError: (err) => {
      setError(`Failed to send feedback: ${err.message}`);
    },
  });

  // Convert overlay coords to iframe-relative coords
  const toIframeCoords = useCallback(
    (clientX: number, clientY: number) => {
      const overlay = overlayRef.current;
      if (!overlay) return { x: 0, y: 0 };
      const rect = overlay.getBoundingClientRect();
      return {
        x: clientX - rect.left,
        y: clientY - rect.top,
      };
    },
    []
  );

  const handleClick = useCallback(
    (e: React.MouseEvent) => {
      const { x, y } = toIframeCoords(e.clientX, e.clientY);

      inspectMutateRef.current(
        { x, y },
        {
          onSuccess: (info) => {
            const selector = buildSelector(info);
            const newElement: SelectedElement = { info, selector };

            if (e.shiftKey) {
              setSelectedElements((prev) => {
                const exists = prev.find((el) => el.selector === selector);
                if (exists) {
                  return prev.filter((el) => el.selector !== selector);
                }
                return [...prev, newElement];
              });
            } else {
              setSelectedElements([newElement]);
            }
          },
        }
      );
    },
    [toIframeCoords]
  );

  const handleSendFeedback = () => {
    if (!instruction.trim() && selectedElements.length === 0) return;

    feedbackMutation.mutate({
      type: "design_mode_feedback",
      instruction: instruction.trim(),
      elements: selectedElements.map((el) => el.info),
    });
  };

  const primarySelected =
    selectedElements.length > 0 ? selectedElements[0] : null;

  return (
    <div className="absolute inset-0 z-10">
      {/* Transparent overlay for capturing clicks */}
      <div
        ref={overlayRef}
        className="absolute inset-0 cursor-crosshair"
        onClick={handleClick}
      >
        {/* SVG layer for element highlights */}
        <svg
          className="absolute inset-0 w-full h-full pointer-events-none"
        >
          {selectedElements.map((el) => (
            <rect
              key={el.selector}
              x={el.info.bounding_box.x}
              y={el.info.bounding_box.y}
              width={el.info.bounding_box.width}
              height={el.info.bounding_box.height}
              fill="rgba(99, 102, 241, 0.1)"
              stroke="rgba(99, 102, 241, 0.8)"
              strokeWidth={2}
            />
          ))}
        </svg>
      </div>

      {/* Toolbar (top-left) */}
      <div className="absolute top-2 left-2 flex items-center gap-1 rounded-md border bg-surface-raised/95 backdrop-blur p-1 shadow-sm pointer-events-auto">
        <Button
          size="icon-xs"
          title="Select element"
          aria-label="Select element"
          className="rounded p-1.5"
        >
          <MousePointer2 className="size-3.5" />
        </Button>
      </div>

      {/* Error banner */}
      {error && (
        <div className="absolute top-2 right-2 flex items-center gap-2 max-w-xs rounded-md border border-destructive/20 bg-destructive/5 backdrop-blur p-2 text-xs text-destructive pointer-events-auto shadow-sm">
          <AlertTriangle className="size-3.5 shrink-0" />
          <span className="flex-1">{error}</span>
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => setError(null)}
            className="rounded p-0.5 hover:bg-destructive/10"
          >
            <X className="size-3" />
          </Button>
        </div>
      )}

      {/* Element info panel (bottom-left) */}
      {primarySelected && (
        <div className="absolute bottom-2 left-2 w-72 rounded-lg border bg-surface-raised/95 backdrop-blur shadow-lg pointer-events-auto">
          <div className="flex items-center justify-between p-2 border-b">
            <div className="flex items-center gap-1.5">
              <Badge variant="secondary" className="font-mono text-xs">
                {"<"}
                {primarySelected.info.tag_name}
                {">"}
              </Badge>
              {primarySelected.info.component_name && (
                <Badge variant="outline" className="text-xs">
                  {primarySelected.info.component_name}
                </Badge>
              )}
            </div>
            <div className="flex items-center gap-0.5">
              <Button
                size="icon-xs"
                variant="ghost"
                onClick={() => setShowEditPanel(!showEditPanel)}
                title="Visual editor"
                aria-label="Visual editor"
              >
                <Pencil className="size-3" />
              </Button>
              <Button
                size="icon-xs"
                variant="ghost"
                onClick={() => setSelectedElements([])}
              >
                <X className="size-3" />
              </Button>
            </div>
          </div>

          {/* Element details */}
          <div className="p-2 space-y-2 text-xs">
            {primarySelected.info.attributes?.id && (
              <div className="flex items-center gap-1">
                <span className="text-muted-foreground">id:</span>
                <code className="font-mono text-xs">
                  {primarySelected.info.attributes.id}
                </code>
              </div>
            )}
            {(() => {
              const classList = primarySelected.info.attributes?.class?.split(/\s+/).filter(Boolean) ?? [];
              return classList.length > 0 ? (
                <div className="space-y-0.5">
                  <span className="text-muted-foreground">classes:</span>
                  <div className="flex flex-wrap gap-0.5">
                    {classList.slice(0, 8).map((cls) => (
                      <code
                        key={cls}
                        className="rounded bg-muted px-1 py-0.5 font-mono text-xs"
                      >
                        {cls}
                      </code>
                    ))}
                    {classList.length > 8 && (
                      <span className="text-muted-foreground text-xs">
                        +{classList.length - 8} more
                      </span>
                    )}
                  </div>
                </div>
              ) : null;
            })()}
            {primarySelected.info.inner_text && (
              <div className="space-y-0.5">
                <span className="text-muted-foreground">text:</span>
                <p className="truncate text-xs">
                  {primarySelected.info.inner_text}
                </p>
              </div>
            )}
            {primarySelected.info.component_file && (
              <div className="flex items-center gap-1">
                <span className="text-muted-foreground">file:</span>
                <code className="font-mono text-xs truncate">
                  {primarySelected.info.component_file}
                </code>
              </div>
            )}
          </div>

          {/* Multi-selection indicator */}
          {selectedElements.length > 1 && (
            <div className="px-2 pb-2">
              <Badge variant="secondary" className="text-xs">
                {selectedElements.length} elements selected (shift+click to
                add/remove)
              </Badge>
            </div>
          )}

          {/* Instruction input */}
          <div className="p-2 border-t space-y-2">
            <Textarea
              placeholder="Describe what to change..."
              value={instruction}
              onChange={(e) => setInstruction(e.target.value)}
              className="text-xs min-h-[60px] resize-none"
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  handleSendFeedback();
                }
              }}
            />
            <div className="flex items-center gap-1">
              <Button
                size="sm"
                onClick={handleSendFeedback}
                disabled={
                  feedbackMutation.isPending ||
                  (!instruction.trim() && selectedElements.length === 0)
                }
                loading={feedbackMutation.isPending}
                className="flex-1"
              >
                <Send className="size-3" />
                Send to agent
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              {typeof navigator !== "undefined" && /Mac|iPhone|iPad|iPod/.test(navigator.userAgent) ? "Cmd" : "Ctrl"}+Enter to
              send
            </p>
          </div>
        </div>
      )}

      {/* Visual editing panel */}
      {showEditPanel && primarySelected && (
        <div className="absolute bottom-2 left-[304px] pointer-events-auto">
          <VisualEditingPanel
            key={primarySelected.selector}
            sessionId={sessionId}
            element={primarySelected.info}
            selector={primarySelected.selector}
            onClose={() => setShowEditPanel(false)}
          />
        </div>
      )}
    </div>
  );
}
