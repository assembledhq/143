"use client";

import {
  type RefObject,
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
  ArrowUp,
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  RectangleHorizontal,
  MoveRight,
  Pencil,
  Trash2,
  AlertTriangle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import type {
  ElementInfo,
  Annotation,
  DesignModeFeedback,
} from "@/lib/preview-types";
import { VisualEditingPanel } from "./visual-editing-panel";

interface DesignModeOverlayProps {
  sessionId: string;
  iframeRef: RefObject<HTMLIFrameElement | null>;
  previewOrigin: string;
}

type AnnotationTool = "select" | "rectangle" | "arrow" | "freehand";

interface SelectedElement {
  info: ElementInfo;
  selector: string;
}

export function DesignModeOverlay({
  sessionId,
  iframeRef,
  previewOrigin,
}: DesignModeOverlayProps) {
  const overlayRef = useRef<HTMLDivElement>(null);
  const svgRef = useRef<SVGSVGElement>(null);

  const [selectedElements, setSelectedElements] = useState<SelectedElement[]>(
    []
  );
  const [instruction, setInstruction] = useState("");
  const [annotations, setAnnotations] = useState<Annotation[]>([]);
  const [activeTool, setActiveTool] = useState<AnnotationTool>("select");
  const [isDrawing, setIsDrawing] = useState(false);
  const [drawPoints, setDrawPoints] = useState<Array<{ x: number; y: number }>>(
    []
  );
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
      setAnnotations([]);
      setInstruction("");
    },
    onError: (err) => {
      setError(`Failed to send feedback: ${err.message}`);
    },
  });

  // Convert overlay coords to iframe-relative coords
  const toIframeCoords = useCallback(
    (clientX: number, clientY: number) => {
      const iframe = iframeRef.current;
      const overlay = overlayRef.current;
      if (!iframe || !overlay) return { x: 0, y: 0 };
      const rect = overlay.getBoundingClientRect();
      return {
        x: clientX - rect.left,
        y: clientY - rect.top,
      };
    },
    [iframeRef]
  );

  const handleClick = useCallback(
    (e: React.MouseEvent) => {
      if (activeTool !== "select") return;

      const { x, y } = toIframeCoords(e.clientX, e.clientY);

      inspectMutateRef.current(
        { x, y },
        {
          onSuccess: (info) => {
            const selector =
              info.id
                ? `#${CSS.escape(info.id)}`
                : info.class_list.length > 0
                  ? `${info.tag_name}.${info.class_list.map((c: string) => CSS.escape(c)).join(".")}`
                  : info.tag_name;

            const newElement: SelectedElement = { info, selector };

            if (e.shiftKey) {
              // Multi-select with shift
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
    [activeTool, toIframeCoords]
  );

  const handleMouseMove = useCallback(
    (e: React.MouseEvent) => {
      if (isDrawing && (activeTool === "freehand" || activeTool === "arrow")) {
        const { x, y } = toIframeCoords(e.clientX, e.clientY);
        setDrawPoints((prev) => [...prev, { x, y }]);
      }
    },
    [activeTool, isDrawing, toIframeCoords]
  );

  const handleMouseDown = useCallback(
    (e: React.MouseEvent) => {
      if (activeTool === "select") return;

      const { x, y } = toIframeCoords(e.clientX, e.clientY);
      setIsDrawing(true);
      setDrawPoints([{ x, y }]);
    },
    [activeTool, toIframeCoords]
  );

  const handleMouseUp = useCallback(() => {
    if (!isDrawing || drawPoints.length < 2) {
      setIsDrawing(false);
      setDrawPoints([]);
      return;
    }

    const newAnnotation: Annotation = {
      type:
        activeTool === "rectangle"
          ? "rectangle"
          : activeTool === "arrow"
            ? "arrow"
            : "freehand",
      points: [...drawPoints],
      color: "#ef4444",
    };

    setAnnotations((prev) => [...prev, newAnnotation]);
    setIsDrawing(false);
    setDrawPoints([]);
  }, [isDrawing, drawPoints, activeTool]);

  const handleSendFeedback = () => {
    if (!instruction.trim() && selectedElements.length === 0) return;

    feedbackMutation.mutate({
      instruction: instruction.trim(),
      selected_elements: selectedElements.map((el) => ({
        selector: el.selector,
        bounding_box: el.info.bounding_box,
      })),
      annotations,
      visual_edits: [],
    });
  };

  const removeAnnotation = (index: number) => {
    setAnnotations((prev) => prev.filter((_, i) => i !== index));
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
        onMouseMove={handleMouseMove}
        onMouseDown={handleMouseDown}
        onMouseUp={handleMouseUp}
      >
        {/* SVG layer for annotations and highlights */}
        <svg
          ref={svgRef}
          className="absolute inset-0 w-full h-full pointer-events-none"
        >
          {/* Selected element highlights */}
          {selectedElements.map((el, i) => (
            <rect
              key={i}
              x={el.info.bounding_box.x}
              y={el.info.bounding_box.y}
              width={el.info.bounding_box.width}
              height={el.info.bounding_box.height}
              fill="rgba(99, 102, 241, 0.1)"
              stroke="rgba(99, 102, 241, 0.8)"
              strokeWidth={2}
            />
          ))}

          {/* Saved annotations */}
          {annotations.map((ann, i) => (
            <AnnotationPath key={i} annotation={ann} />
          ))}

          {/* Current drawing */}
          {isDrawing && drawPoints.length > 1 && (
            <AnnotationPath
              annotation={{
                type:
                  activeTool === "rectangle"
                    ? "rectangle"
                    : activeTool === "arrow"
                      ? "arrow"
                      : "freehand",
                points: drawPoints,
                color: "#ef4444",
              }}
            />
          )}
        </svg>
      </div>

      {/* Toolbar (top-left) */}
      <div className="absolute top-2 left-2 flex items-center gap-1 rounded-md border bg-background/95 backdrop-blur p-1 shadow-sm pointer-events-auto">
        <ToolButton
          active={activeTool === "select"}
          onClick={() => setActiveTool("select")}
          title="Select element"
        >
          <MousePointer2 className="size-3.5" />
        </ToolButton>
        <ToolButton
          active={activeTool === "rectangle"}
          onClick={() => setActiveTool("rectangle")}
          title="Draw rectangle"
        >
          <RectangleHorizontal className="size-3.5" />
        </ToolButton>
        <ToolButton
          active={activeTool === "arrow"}
          onClick={() => setActiveTool("arrow")}
          title="Draw arrow"
        >
          <MoveRight className="size-3.5" />
        </ToolButton>
        <ToolButton
          active={activeTool === "freehand"}
          onClick={() => setActiveTool("freehand")}
          title="Freehand draw"
        >
          <Pencil className="size-3.5" />
        </ToolButton>

        {annotations.length > 0 && (
          <>
            <div className="w-px h-4 bg-border mx-0.5" />
            <Button
              size="icon-xs"
              variant="ghost"
              onClick={() => setAnnotations([])}
              title="Clear annotations"
            >
              <Trash2 className="size-3.5" />
            </Button>
          </>
        )}
      </div>

      {/* Error banner */}
      {error && (
        <div className="absolute top-2 right-2 flex items-center gap-2 max-w-xs rounded-md border border-destructive/20 bg-destructive/5 backdrop-blur p-2 text-xs text-destructive pointer-events-auto shadow-sm">
          <AlertTriangle className="size-3.5 shrink-0" />
          <span className="flex-1">{error}</span>
          <button
            onClick={() => setError(null)}
            className="rounded p-0.5 hover:bg-destructive/10"
          >
            <X className="size-3" />
          </button>
        </div>
      )}

      {/* Element info panel (bottom-left) */}
      {primarySelected && (
        <div className="absolute bottom-2 left-2 w-72 rounded-lg border bg-background/95 backdrop-blur shadow-lg pointer-events-auto">
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
            {primarySelected.info.id && (
              <div className="flex items-center gap-1">
                <span className="text-muted-foreground">id:</span>
                <code className="font-mono text-xs">
                  {primarySelected.info.id}
                </code>
              </div>
            )}
            {primarySelected.info.class_list.length > 0 && (
              <div className="space-y-0.5">
                <span className="text-muted-foreground">classes:</span>
                <div className="flex flex-wrap gap-0.5">
                  {primarySelected.info.class_list.slice(0, 8).map((cls) => (
                    <code
                      key={cls}
                      className="rounded bg-muted px-1 py-0.5 font-mono text-xs"
                    >
                      {cls}
                    </code>
                  ))}
                  {primarySelected.info.class_list.length > 8 && (
                    <span className="text-muted-foreground text-xs">
                      +{primarySelected.info.class_list.length - 8} more
                    </span>
                  )}
                </div>
              </div>
            )}
            {primarySelected.info.text_content && (
              <div className="space-y-0.5">
                <span className="text-muted-foreground">text:</span>
                <p className="truncate text-xs">
                  {primarySelected.info.text_content}
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

            {/* Reorder controls */}
            <div className="flex items-center gap-1 pt-1 border-t">
              <span className="text-muted-foreground text-xs mr-1">
                Reorder:
              </span>
              {[
                { icon: ArrowUp, dir: "up" },
                { icon: ArrowDown, dir: "down" },
                { icon: ArrowLeft, dir: "left" },
                { icon: ArrowRight, dir: "right" },
              ].map(({ icon: Icon, dir }) => (
                <Button
                  key={dir}
                  size="icon-xs"
                  variant="ghost"
                  title={`Move ${dir}`}
                  onClick={() => {
                    // Reorder feedback will be sent with the instruction
                    setInstruction(
                      (prev) =>
                        `${prev ? prev + "\n" : ""}Move the ${primarySelected.info.tag_name} element ${dir}.`
                    );
                  }}
                >
                  <Icon className="size-3" />
                </Button>
              ))}
            </div>
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
              {/Mac|iPhone|iPad|iPod/.test(navigator.userAgent) ? "Cmd" : "Ctrl"}+Enter to
              send
            </p>
          </div>
        </div>
      )}

      {/* Visual editing panel */}
      {showEditPanel && primarySelected && (
        <div className="absolute bottom-2 left-[304px] pointer-events-auto">
          <VisualEditingPanel
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

// Internal tool button
function ToolButton({
  active,
  onClick,
  title,
  children,
}: {
  active: boolean;
  onClick: () => void;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      className={cn(
        "rounded p-1.5 transition-colors",
        active
          ? "bg-primary text-white"
          : "text-muted-foreground hover:text-foreground hover:bg-muted"
      )}
    >
      {children}
    </button>
  );
}

// SVG annotation renderer
function AnnotationPath({ annotation }: { annotation: Annotation }) {
  const { type, points, color = "#ef4444" } = annotation;

  if (points.length < 2) return null;

  if (type === "rectangle") {
    const start = points[0];
    const end = points[points.length - 1];
    const x = Math.min(start.x, end.x);
    const y = Math.min(start.y, end.y);
    const width = Math.abs(end.x - start.x);
    const height = Math.abs(end.y - start.y);
    return (
      <rect
        x={x}
        y={y}
        width={width}
        height={height}
        fill="transparent"
        stroke={color}
        strokeWidth={2}
        strokeDasharray="6 3"
      />
    );
  }

  if (type === "arrow") {
    const start = points[0];
    const end = points[points.length - 1];
    const angle = Math.atan2(end.y - start.y, end.x - start.x);
    const headLen = 12;
    return (
      <g>
        <line
          x1={start.x}
          y1={start.y}
          x2={end.x}
          y2={end.y}
          stroke={color}
          strokeWidth={2}
        />
        <polygon
          points={`
            ${end.x},${end.y}
            ${end.x - headLen * Math.cos(angle - Math.PI / 6)},${end.y - headLen * Math.sin(angle - Math.PI / 6)}
            ${end.x - headLen * Math.cos(angle + Math.PI / 6)},${end.y - headLen * Math.sin(angle + Math.PI / 6)}
          `}
          fill={color}
        />
      </g>
    );
  }

  // Freehand
  const d = points.reduce(
    (acc, p, i) => (i === 0 ? `M ${p.x} ${p.y}` : `${acc} L ${p.x} ${p.y}`),
    ""
  );
  return (
    <path d={d} fill="none" stroke={color} strokeWidth={2} strokeLinecap="round" />
  );
}
