"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import {
  X,
  Paintbrush,
  Type,
  Box,
  LayoutGrid,
  Ruler,
  Circle,
  Send,
  AlertTriangle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { ErrorText } from "@/components/ui/error-notice";
import { Slider } from "@/components/ui/slider";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { ElementInfo, StyleEdit } from "@/lib/preview-types";

interface VisualEditingPanelProps {
  sessionId: string;
  element: ElementInfo;
  selector: string;
  onClose: () => void;
}

interface EditState {
  color: string;
  backgroundColor: string;
  borderColor: string;
  marginTop: number;
  marginRight: number;
  marginBottom: number;
  marginLeft: number;
  paddingTop: number;
  paddingRight: number;
  paddingBottom: number;
  paddingLeft: number;
  fontSize: number;
  fontSizeUnit: string;
  fontWeight: string;
  lineHeight: number;
  letterSpacing: number;
  flexDirection: string;
  justifyContent: string;
  alignItems: string;
  gap: number;
  width: string;
  widthUnit: string;
  height: string;
  heightUnit: string;
  borderRadius: number;
}

function parsePixelValue(val: string | undefined): number {
  if (!val) return 0;
  const match = val.match(/^([\d.]+)/);
  return match ? parseFloat(match[1]) : 0;
}

function initStateFromElement(element: ElementInfo): EditState {
  const s = element.computed_styles ?? {};
  return {
    color: s.color || "#000000",
    backgroundColor: s["background-color"] || "transparent",
    borderColor: s["border-color"] || "transparent",
    marginTop: parsePixelValue(s["margin-top"]),
    marginRight: parsePixelValue(s["margin-right"]),
    marginBottom: parsePixelValue(s["margin-bottom"]),
    marginLeft: parsePixelValue(s["margin-left"]),
    paddingTop: parsePixelValue(s["padding-top"]),
    paddingRight: parsePixelValue(s["padding-right"]),
    paddingBottom: parsePixelValue(s["padding-bottom"]),
    paddingLeft: parsePixelValue(s["padding-left"]),
    fontSize: parsePixelValue(s["font-size"]),
    fontSizeUnit: "px",
    fontWeight: s["font-weight"] || "400",
    lineHeight: parsePixelValue(s["line-height"]),
    letterSpacing: parsePixelValue(s["letter-spacing"]),
    flexDirection: s["flex-direction"] || "row",
    justifyContent: s["justify-content"] || "flex-start",
    alignItems: s["align-items"] || "stretch",
    gap: parsePixelValue(s.gap),
    width: s.width?.replace("px", "") || "auto",
    widthUnit: "px",
    height: s.height?.replace("px", "") || "auto",
    heightUnit: "px",
    borderRadius: parsePixelValue(s["border-radius"]),
  };
}

const FONT_WEIGHTS = [
  { value: "100", label: "Thin" },
  { value: "200", label: "Extra Light" },
  { value: "300", label: "Light" },
  { value: "400", label: "Normal" },
  { value: "500", label: "Medium" },
  { value: "600", label: "Semibold" },
  { value: "700", label: "Bold" },
  { value: "800", label: "Extra Bold" },
  { value: "900", label: "Black" },
];

const FLEX_DIRECTIONS = ["row", "column", "row-reverse", "column-reverse"];
const JUSTIFY_OPTIONS = [
  "flex-start",
  "flex-end",
  "center",
  "space-between",
  "space-around",
  "space-evenly",
];
const ALIGN_OPTIONS = ["flex-start", "flex-end", "center", "stretch", "baseline"];
const SIZE_UNITS = ["px", "%", "rem", "em", "vw", "vh", "auto"];

function isValidHexColor(value: string): boolean {
  if (value === "transparent") return true;
  if (!value.startsWith("#")) return false;
  const hex = value.slice(1);
  return /^[0-9a-fA-F]{3}$|^[0-9a-fA-F]{4}$|^[0-9a-fA-F]{6}$|^[0-9a-fA-F]{8}$/.test(hex);
}

function isValidNumericSize(value: string, unit: string): boolean {
  if (unit === "auto" || value === "auto" || value === "") return true;
  const num = parseFloat(value);
  return !isNaN(num) && num >= 0;
}

export function VisualEditingPanel({
  sessionId,
  element,
  selector,
  onClose,
}: VisualEditingPanelProps) {
  const [editState, setEditState] = useState<EditState>(() =>
    initStateFromElement(element)
  );
  const [dirtyFields, setDirtyFields] = useState<Set<string>>(new Set());
  const [applyError, setApplyError] = useState<string | null>(null);

  // State is reset when a different element is selected because the
  // parent renders this component with key={selector}, causing a remount.

  // Validation
  const validationErrors = useMemo(() => {
    const errors: string[] = [];
    if (dirtyFields.has("color") && !isValidHexColor(editState.color)) {
      errors.push("Invalid text color hex value");
    }
    if (dirtyFields.has("backgroundColor") && !isValidHexColor(editState.backgroundColor)) {
      errors.push("Invalid background color hex value");
    }
    if (dirtyFields.has("borderColor") && !isValidHexColor(editState.borderColor)) {
      errors.push("Invalid border color hex value");
    }
    if (dirtyFields.has("width") && !isValidNumericSize(editState.width, editState.widthUnit)) {
      errors.push("Width must be a non-negative number");
    }
    if (dirtyFields.has("height") && !isValidNumericSize(editState.height, editState.heightUnit)) {
      errors.push("Height must be a non-negative number");
    }
    return errors;
  }, [editState, dirtyFields]);
  const hasValidationErrors = validationErrors.length > 0;

  const update = useCallback(
    <K extends keyof EditState>(key: K, value: EditState[K]) => {
      setEditState((prev) => ({ ...prev, [key]: value }));
      setDirtyFields((prev) => new Set(prev).add(key));
    },
    []
  );

  // Memoize the computed styles reference to avoid unnecessary useCallback recreation
  const s = useMemo(() => element.computed_styles ?? {}, [element.computed_styles]);

  const buildStyleEdits = useCallback((): StyleEdit[] => {
    const edits: StyleEdit[] = [];
    const edit = (property: string, oldValue: string, newValue: string) => {
      edits.push({ property, old_value: oldValue, new_value: newValue });
    };

    if (dirtyFields.has("color"))
      edit("color", s.color || "", editState.color);
    if (dirtyFields.has("backgroundColor"))
      edit("background-color", s["background-color"] || "", editState.backgroundColor);
    if (dirtyFields.has("borderColor"))
      edit("border-color", s["border-color"] || "", editState.borderColor);

    if (dirtyFields.has("marginTop"))
      edit("margin-top", s["margin-top"] || "0px", `${editState.marginTop}px`);
    if (dirtyFields.has("marginRight"))
      edit("margin-right", s["margin-right"] || "0px", `${editState.marginRight}px`);
    if (dirtyFields.has("marginBottom"))
      edit("margin-bottom", s["margin-bottom"] || "0px", `${editState.marginBottom}px`);
    if (dirtyFields.has("marginLeft"))
      edit("margin-left", s["margin-left"] || "0px", `${editState.marginLeft}px`);

    if (dirtyFields.has("paddingTop"))
      edit("padding-top", s["padding-top"] || "0px", `${editState.paddingTop}px`);
    if (dirtyFields.has("paddingRight"))
      edit("padding-right", s["padding-right"] || "0px", `${editState.paddingRight}px`);
    if (dirtyFields.has("paddingBottom"))
      edit("padding-bottom", s["padding-bottom"] || "0px", `${editState.paddingBottom}px`);
    if (dirtyFields.has("paddingLeft"))
      edit("padding-left", s["padding-left"] || "0px", `${editState.paddingLeft}px`);

    if (dirtyFields.has("fontSize"))
      edit("font-size", s["font-size"] || "", `${editState.fontSize}${editState.fontSizeUnit}`);
    if (dirtyFields.has("fontWeight"))
      edit("font-weight", s["font-weight"] || "", editState.fontWeight);
    if (dirtyFields.has("lineHeight"))
      edit("line-height", s["line-height"] || "", `${editState.lineHeight}px`);
    if (dirtyFields.has("letterSpacing"))
      edit("letter-spacing", s["letter-spacing"] || "0px", `${editState.letterSpacing}px`);

    if (dirtyFields.has("flexDirection"))
      edit("flex-direction", s["flex-direction"] || "", editState.flexDirection);
    if (dirtyFields.has("justifyContent"))
      edit("justify-content", s["justify-content"] || "", editState.justifyContent);
    if (dirtyFields.has("alignItems"))
      edit("align-items", s["align-items"] || "", editState.alignItems);
    if (dirtyFields.has("gap"))
      edit("gap", s.gap || "0px", `${editState.gap}px`);

    if (dirtyFields.has("width")) {
      const val = editState.widthUnit === "auto" ? "auto" : `${editState.width}${editState.widthUnit}`;
      edit("width", s.width || "", val);
    }
    if (dirtyFields.has("height")) {
      const val = editState.heightUnit === "auto" ? "auto" : `${editState.height}${editState.heightUnit}`;
      edit("height", s.height || "", val);
    }

    if (dirtyFields.has("borderRadius"))
      edit("border-radius", s["border-radius"] || "0px", `${editState.borderRadius}px`);

    return edits;
  }, [editState, dirtyFields, s]);

  const buildStyleEditsRef = useRef(buildStyleEdits);
  useEffect(() => {
    buildStyleEditsRef.current = buildStyleEdits;
  }, [buildStyleEdits]);

  const applyMutation = useMutation({
    mutationFn: () => {
      const styleEdits = buildStyleEditsRef.current();
      if (styleEdits.length === 0) return Promise.resolve();

      return api.sessions.preview.designFeedback(sessionId, {
        type: "visual_edit",
        instruction: `Apply visual edits to ${selector}`,
        elements: [element],
        style_edits: styleEdits,
      });
    },
    onSuccess: () => {
      setApplyError(null);
      setDirtyFields(new Set());
    },
    onError: (err) => {
      setApplyError(`Failed to apply edits: ${err.message}`);
    },
  });

  return (
    <div className="w-64 rounded-lg border bg-background/95 backdrop-blur shadow-lg overflow-hidden">
      <div className="flex items-center justify-between p-2 border-b">
        <span className="text-xs font-medium">Visual Editor</span>
        <Button size="icon-xs" variant="ghost" onClick={onClose}>
          <X className="size-3" />
        </Button>
      </div>

      <Tabs defaultValue="colors" className="w-full">
        <TabsList size="sm" className="w-full px-2 pt-2">
          <TabsTrigger value="colors">
            <Paintbrush className="size-3" />
          </TabsTrigger>
          <TabsTrigger value="spacing">
            <Box className="size-3" />
          </TabsTrigger>
          <TabsTrigger value="typography">
            <Type className="size-3" />
          </TabsTrigger>
          <TabsTrigger value="layout">
            <LayoutGrid className="size-3" />
          </TabsTrigger>
          <TabsTrigger value="size">
            <Ruler className="size-3" />
          </TabsTrigger>
        </TabsList>

        {/* Colors tab */}
        <TabsContent value="colors" className="p-2 space-y-3">
          <ColorField
            label="Color"
            value={editState.color}
            onChange={(v) => update("color", v)}
          />
          <ColorField
            label="Background"
            value={editState.backgroundColor}
            onChange={(v) => update("backgroundColor", v)}
          />
          <ColorField
            label="Border"
            value={editState.borderColor}
            onChange={(v) => update("borderColor", v)}
          />
        </TabsContent>

        {/* Spacing tab */}
        <TabsContent value="spacing" className="p-2 space-y-3">
          <div className="space-y-1.5">
            <Label className="text-xs">Margin</Label>
            <div className="grid grid-cols-2 gap-1.5">
              <SpacingSlider
                label="T"
                value={editState.marginTop}
                onChange={(v) => update("marginTop", v)}
              />
              <SpacingSlider
                label="R"
                value={editState.marginRight}
                onChange={(v) => update("marginRight", v)}
              />
              <SpacingSlider
                label="B"
                value={editState.marginBottom}
                onChange={(v) => update("marginBottom", v)}
              />
              <SpacingSlider
                label="L"
                value={editState.marginLeft}
                onChange={(v) => update("marginLeft", v)}
              />
            </div>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Padding</Label>
            <div className="grid grid-cols-2 gap-1.5">
              <SpacingSlider
                label="T"
                value={editState.paddingTop}
                onChange={(v) => update("paddingTop", v)}
              />
              <SpacingSlider
                label="R"
                value={editState.paddingRight}
                onChange={(v) => update("paddingRight", v)}
              />
              <SpacingSlider
                label="B"
                value={editState.paddingBottom}
                onChange={(v) => update("paddingBottom", v)}
              />
              <SpacingSlider
                label="L"
                value={editState.paddingLeft}
                onChange={(v) => update("paddingLeft", v)}
              />
            </div>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Border Radius</Label>
            <div className="flex items-center gap-2">
              <Circle className="size-3 text-muted-foreground" />
              <Slider
                min={0}
                max={100}
                value={[editState.borderRadius]}
                onValueChange={([v]) => update("borderRadius", v)}
                className="flex-1"
              />
              <span className="text-xs text-muted-foreground w-8 text-right">
                {editState.borderRadius}px
              </span>
            </div>
          </div>
        </TabsContent>

        {/* Typography tab */}
        <TabsContent value="typography" className="p-2 space-y-3">
          <div className="space-y-1.5">
            <Label className="text-xs">Font Size</Label>
            <div className="flex items-center gap-1.5">
              <Slider
                min={8}
                max={96}
                value={[editState.fontSize]}
                onValueChange={([v]) => update("fontSize", v)}
                className="flex-1"
              />
              <span className="text-xs text-muted-foreground w-8 text-right">
                {editState.fontSize}
              </span>
              <Select
                value={editState.fontSizeUnit}
                onValueChange={(v) => {
                  update("fontSizeUnit", v);
                  setDirtyFields((prev) => new Set(prev).add("fontSize"));
                }}
              >
                <SelectTrigger className="w-14 h-6 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {["px", "rem", "em", "%"].map((u) => (
                    <SelectItem key={u} value={u} className="text-xs">
                      {u}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Font Weight</Label>
            <Select
              value={editState.fontWeight}
              onValueChange={(v) => update("fontWeight", v)}
            >
              <SelectTrigger className="h-7 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {FONT_WEIGHTS.map((fw) => (
                  <SelectItem key={fw.value} value={fw.value} className="text-xs">
                    {fw.label} ({fw.value})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Line Height</Label>
            <div className="flex items-center gap-2">
              <Slider
                min={0}
                max={100}
                value={[editState.lineHeight]}
                onValueChange={([v]) => update("lineHeight", v)}
                className="flex-1"
              />
              <span className="text-xs text-muted-foreground w-8 text-right">
                {editState.lineHeight}px
              </span>
            </div>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Letter Spacing</Label>
            <div className="flex items-center gap-2">
              <Slider
                min={-5}
                max={20}
                value={[editState.letterSpacing]}
                onValueChange={([v]) => update("letterSpacing", v)}
                className="flex-1"
              />
              <span className="text-xs text-muted-foreground w-8 text-right">
                {editState.letterSpacing}px
              </span>
            </div>
          </div>
        </TabsContent>

        {/* Layout tab */}
        <TabsContent value="layout" className="p-2 space-y-3">
          <div className="space-y-1.5">
            <Label className="text-xs">Flex Direction</Label>
            <Select
              value={editState.flexDirection}
              onValueChange={(v) => update("flexDirection", v)}
            >
              <SelectTrigger className="h-7 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {FLEX_DIRECTIONS.map((d) => (
                  <SelectItem key={d} value={d} className="text-xs">
                    {d}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Justify Content</Label>
            <Select
              value={editState.justifyContent}
              onValueChange={(v) => update("justifyContent", v)}
            >
              <SelectTrigger className="h-7 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {JUSTIFY_OPTIONS.map((j) => (
                  <SelectItem key={j} value={j} className="text-xs">
                    {j}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Align Items</Label>
            <Select
              value={editState.alignItems}
              onValueChange={(v) => update("alignItems", v)}
            >
              <SelectTrigger className="h-7 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ALIGN_OPTIONS.map((a) => (
                  <SelectItem key={a} value={a} className="text-xs">
                    {a}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Gap</Label>
            <div className="flex items-center gap-2">
              <Slider
                min={0}
                max={64}
                value={[editState.gap]}
                onValueChange={([v]) => update("gap", v)}
                className="flex-1"
              />
              <span className="text-xs text-muted-foreground w-8 text-right">
                {editState.gap}px
              </span>
            </div>
          </div>
        </TabsContent>

        {/* Size tab */}
        <TabsContent value="size" className="p-2 space-y-3">
          <div className="space-y-1.5">
            <Label className="text-xs">Width</Label>
            <div className="flex items-center gap-1.5">
              <Input
                value={editState.width}
                onChange={(e) => update("width", e.target.value)}
                className="h-7 text-xs flex-1"
                placeholder="auto"
              />
              <Select
                value={editState.widthUnit}
                onValueChange={(v) => {
                  update("widthUnit", v);
                  setDirtyFields((prev) => new Set(prev).add("width"));
                }}
              >
                <SelectTrigger className="w-16 h-7 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SIZE_UNITS.map((u) => (
                    <SelectItem key={u} value={u} className="text-xs">
                      {u}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Height</Label>
            <div className="flex items-center gap-1.5">
              <Input
                value={editState.height}
                onChange={(e) => update("height", e.target.value)}
                className="h-7 text-xs flex-1"
                placeholder="auto"
              />
              <Select
                value={editState.heightUnit}
                onValueChange={(v) => {
                  update("heightUnit", v);
                  setDirtyFields((prev) => new Set(prev).add("height"));
                }}
              >
                <SelectTrigger className="w-16 h-7 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SIZE_UNITS.map((u) => (
                    <SelectItem key={u} value={u} className="text-xs">
                      {u}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
        </TabsContent>
      </Tabs>

      {/* Validation errors */}
      {hasValidationErrors && (
        <div className="px-2 py-1.5 text-xs text-destructive space-y-0.5">
          {validationErrors.map((err) => (
            <div key={err} className="flex min-w-0 items-start gap-1">
              <AlertTriangle className="size-3 shrink-0" />
              <ErrorText className="text-xs">{err}</ErrorText>
            </div>
          ))}
        </div>
      )}

      {/* Apply error */}
      {applyError && (
        <div className="mx-2 mb-1 flex min-w-0 items-start gap-1.5 rounded border border-destructive/20 bg-destructive/5 p-1.5 text-xs text-destructive">
          <AlertTriangle className="mt-0.5 size-3 shrink-0" />
          <ErrorText className="flex-1 text-xs">{applyError}</ErrorText>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="Dismiss error"
            onClick={() => setApplyError(null)}
            className="rounded p-0.5 hover:bg-destructive/10"
          >
            <X className="size-3" aria-hidden="true" />
          </Button>
        </div>
      )}

      {/* Apply button */}
      <div className="p-2 border-t">
        <Button
          size="sm"
          className="w-full"
          onClick={() => applyMutation.mutate()}
          disabled={dirtyFields.size === 0 || applyMutation.isPending || hasValidationErrors}
          loading={applyMutation.isPending}
        >
          <Send className="size-3" />
          Apply {dirtyFields.size > 0 ? `(${dirtyFields.size} changes)` : ""}
        </Button>
      </div>
    </div>
  );
}

// Internal color field component
function ColorField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <div className="space-y-1">
      <Label className="text-xs">{label}</Label>
      <div className="flex items-center gap-1.5">
        <div className="relative">
          <input
            type="color"
            value={value === "transparent" ? "#ffffff" : value}
            onChange={(e) => onChange(e.target.value)}
            className="absolute inset-0 w-7 h-7 cursor-pointer opacity-0"
            aria-label={`${label} color picker`}
          />
          <div
            className="w-7 h-7 rounded border border-border"
            style={{
              backgroundColor: value === "transparent" ? undefined : value,
              backgroundImage:
                value === "transparent"
                  ? "repeating-conic-gradient(#ccc 0% 25%, transparent 0% 50%) 0 0 / 8px 8px"
                  : undefined,
            }}
          />
        </div>
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="h-7 text-xs font-mono flex-1"
          placeholder="#000000"
        />
      </div>
    </div>
  );
}

// Internal spacing slider component
function SpacingSlider({
  label,
  value,
  onChange,
}: {
  label: string;
  value: number;
  onChange: (value: number) => void;
}) {
  return (
    <div className="flex items-center gap-1">
      <span className="text-xs text-muted-foreground w-3">{label}</span>
      <Slider
        min={0}
        max={64}
        value={[value]}
        onValueChange={([v]) => onChange(v)}
        className="flex-1"
      />
      <span className="text-xs text-muted-foreground w-6 text-right">
        {value}
      </span>
    </div>
  );
}
