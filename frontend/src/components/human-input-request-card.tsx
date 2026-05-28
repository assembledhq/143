"use client";

import { useId, useMemo, useState } from "react";
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  CircleHelp,
  ShieldAlert,
} from "lucide-react";

import type { HumanInputAnswerBody, HumanInputRequest } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

interface HumanInputRequestCardProps {
  request: HumanInputRequest;
  autoOpen?: boolean;
  answerable?: boolean;
  submitting?: boolean;
  onAnswer: (body: HumanInputAnswerBody) => Promise<void> | void;
  onCancel?: () => Promise<void> | void;
  onAutoOpenDismiss?: () => void;
}

export function HumanInputRequestCard({
  request,
  autoOpen,
  answerable = true,
  submitting,
  onAnswer,
  onCancel,
  onAutoOpenDismiss,
}: HumanInputRequestCardProps) {
  const payloadFieldId = useId();
  const [open, setOpen] = useState(false);
  const [answerText, setAnswerText] = useState("");
  const [payloadText, setPayloadText] = useState("");
  const [payloadError, setPayloadError] = useState<string | null>(null);
  const [selectedChoice, setSelectedChoice] = useState("");
  const [selectedChoices, setSelectedChoices] = useState<string[]>([]);
  const isPending = request.status === "pending";
  const hasChoices = request.choices.length > 0;
  const isMulti = request.request_kind === "multi_choice";
  const isFreeText = request.request_kind === "free_text";
  const hasHiddenChoices = request.choices.length > 3;
  const requiresDecision =
    request.request_kind === "tool_approval" ||
    request.request_kind === "action_choice";
  const sensitive =
    request.request_kind === "tool_approval" ||
    request.choices.some((choice) => choice.destructive);
  const selectedIDs = isMulti
    ? selectedChoices
    : selectedChoice
      ? [selectedChoice]
      : [];
  const selectedChoiceDetails = request.choices.filter((choice) =>
    selectedIDs.includes(choice.id),
  );
  const payloadEditor = getPayloadEditorState(request, selectedChoiceDetails);
  const hasAnswerText = answerText.trim().length > 0;
  const hasPayloadText = payloadText.trim().length > 0;
  const hasBaseAnswer = isFreeText
    ? hasAnswerText
    : requiresDecision
      ? selectedIDs.length > 0 || (!hasChoices && hasPayloadText)
      : selectedIDs.length > 0 || hasAnswerText || hasPayloadText;
  const canSubmit =
    answerable && hasBaseAnswer && (!payloadEditor.required || hasPayloadText);
  const Icon = sensitive ? ShieldAlert : hasChoices ? CheckCircle2 : CircleHelp;
  const dialogOpen = isPending && answerable && (open || Boolean(autoOpen));

  const kindLabel = useMemo(
    () => request.request_kind.replaceAll("_", " "),
    [request.request_kind],
  );

  function handleOpenChange(nextOpen: boolean) {
    setOpen(nextOpen);
    if (!nextOpen && autoOpen && isPending) {
      onAutoOpenDismiss?.();
    }
  }

  async function submit() {
    if (!canSubmit || submitting || !answerable) return;
    setPayloadError(null);
    const parsedPayload = parsePayloadText(payloadText);
    if (parsedPayload instanceof Error) {
      setPayloadError(parsedPayload.message);
      return;
    }
    const answerPayload = buildAnswerPayload({
      request,
      selectedIDs,
      answerText,
      parsedPayload,
    });
    await onAnswer({
      answer_text: answerText.trim() || undefined,
      selected_choice_ids: selectedIDs.length ? selectedIDs : undefined,
      answer_payload: answerPayload,
    });
    setOpen(false);
  }

  function toggleChoice(id: string, checked: boolean) {
    setSelectedChoices((current) =>
      checked
        ? [...current, id]
        : current.filter((choiceID) => choiceID !== id),
    );
  }

  function selectInlineChoice(id: string) {
    if (isMulti) {
      toggleChoice(id, !selectedChoices.includes(id));
      return;
    }
    setSelectedChoice(id);
  }

  function handlePrimaryAction() {
    if (canSubmit && !payloadEditor.required) {
      void submit();
      return;
    }
    setOpen(true);
  }

  const content = (
    <div className="space-y-4">
      {request.context ? (
        <p className="text-sm text-muted-foreground">{request.context}</p>
      ) : null}
      {sensitive ? (
        <div className="flex gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <p>
            This action needs an explicit decision before the agent continues.
          </p>
        </div>
      ) : null}
      {hasChoices ? (
        isMulti ? (
          <div className="space-y-2">
            {request.choices.map((choice) => {
              const checked = selectedChoices.includes(choice.id);
              return (
                <Label
                  key={choice.id}
                  data-testid={`human-input-dialog-choice-${choice.id}`}
                  className={cn(
                    choiceTileClasses({
                      destructive: choice.destructive,
                      selected: checked,
                    }),
                    "relative",
                  )}
                >
                  <Checkbox
                    aria-label={choice.label}
                    checked={checked}
                    className="absolute inset-0 h-full w-full cursor-pointer rounded-lg opacity-0"
                    onCheckedChange={(value) =>
                      toggleChoice(choice.id, value === true)
                    }
                  />
                  <ChoiceSelectionMark
                    destructive={choice.destructive}
                    selected={checked}
                  />
                  <ChoiceText choice={choice} />
                </Label>
              );
            })}
          </div>
        ) : (
          <RadioGroup value={selectedChoice} onValueChange={setSelectedChoice}>
            {request.choices.map((choice) => (
              <Label
                key={choice.id}
                data-testid={`human-input-dialog-choice-${choice.id}`}
                className={cn(
                  choiceTileClasses({
                    destructive: choice.destructive,
                    selected: selectedChoice === choice.id,
                  }),
                  "relative",
                )}
              >
                <RadioGroupItem
                  value={choice.id}
                  aria-label={choice.label}
                  className="absolute inset-0 h-full w-full cursor-pointer rounded-lg opacity-0"
                />
                <ChoiceSelectionMark
                  destructive={choice.destructive}
                  selected={selectedChoice === choice.id}
                />
                <ChoiceText choice={choice} />
              </Label>
            ))}
          </RadioGroup>
        )
      ) : null}
      {isFreeText || request.request_kind === "tool_approval" ? (
        <Textarea
          value={answerText}
          onChange={(event) => setAnswerText(event.target.value)}
          placeholder="Type your answer..."
          className="min-h-24"
        />
      ) : null}
      {payloadEditor.visible ? (
        <div className="space-y-2">
          <Label htmlFor={payloadFieldId} className="text-xs font-medium">
            Response payload
          </Label>
          <Textarea
            id={payloadFieldId}
            aria-label="Structured response payload"
            value={payloadText}
            onChange={(event) => setPayloadText(event.target.value)}
            placeholder={payloadEditor.placeholder}
            className="min-h-24 font-mono text-xs"
          />
          {payloadError ? (
            <p className="text-xs text-destructive">{payloadError}</p>
          ) : null}
        </div>
      ) : null}
    </div>
  );

  return (
    <>
      <div className="w-full">
        <Card className="w-full rounded-lg border-amber-300/70 bg-amber-50/60 shadow-sm dark:border-amber-800/60 dark:bg-amber-950/20">
          <CardHeader className="border-b border-amber-200/70 bg-amber-50/80 pb-4 dark:border-amber-900/60 dark:bg-amber-950/20">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div className="flex min-w-0 items-start gap-3">
                <div className="rounded-md bg-surface-raised p-2 text-amber-700 shadow-sm dark:text-amber-300">
                  <Icon className="h-4 w-4" />
                </div>
                <div className="min-w-0 flex-1 space-y-1.5">
                  <CardTitle className="text-base leading-snug">
                    {request.title}
                  </CardTitle>
                  <p className="max-w-4xl text-sm leading-6 text-muted-foreground">
                    {request.body}
                  </p>
                </div>
              </div>
              <Badge variant="outline" className="w-fit shrink-0 capitalize">
                {kindLabel}
              </Badge>
            </div>
          </CardHeader>
          <CardContent className="space-y-4 p-4">
            {request.choices.length > 0 ? (
              <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                {request.choices.slice(0, 3).map((choice) => {
                  const selected = selectedIDs.includes(choice.id);
                  return isPending ? (
                    <Button
                      key={choice.id}
                      type="button"
                      variant="outline"
                      aria-pressed={selected}
                      data-testid={`human-input-choice-${choice.id}`}
                      className={cn(
                        choiceTileClasses({
                          destructive: choice.destructive,
                          selected,
                        }),
                        "h-auto w-full justify-start whitespace-normal text-left",
                      )}
                      onClick={() => selectInlineChoice(choice.id)}
                      disabled={!answerable || submitting}
                    >
                      <ChoiceSelectionMark
                        destructive={choice.destructive}
                        selected={selected}
                      />
                      <ChoiceText choice={choice} compact />
                    </Button>
                  ) : (
                    <div
                      key={choice.id}
                      className="rounded-md border border-border bg-surface-raised/80 px-3 py-2"
                    >
                      <ChoiceText choice={choice} compact />
                    </div>
                  );
                })}
              </div>
            ) : null}
            {isPending ? (
              <div className="flex flex-wrap items-center justify-end gap-2">
                <Button
                  type="button"
                  size="sm"
                  onClick={handlePrimaryAction}
                  disabled={
                    !answerable ||
                    submitting ||
                    (requiresDecision &&
                      hasChoices &&
                      !hasHiddenChoices &&
                      selectedIDs.length === 0)
                  }
                >
                  Respond
                </Button>
                {onCancel ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => void onCancel()}
                    disabled={!answerable}
                  >
                    Cancel
                  </Button>
                ) : null}
              </div>
            ) : null}
          </CardContent>
        </Card>
      </div>
      <Dialog open={dialogOpen} onOpenChange={handleOpenChange}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{request.title}</DialogTitle>
            <DialogDescription>{request.body}</DialogDescription>
          </DialogHeader>
          {content}
          <DialogFooter>
            {onCancel ? (
              <Button
                type="button"
                variant="ghost"
                onClick={() => void onCancel()}
                disabled={!answerable || submitting}
              >
                Cancel request
              </Button>
            ) : null}
            <Button
              type="button"
              onClick={() => void submit()}
              disabled={!canSubmit || submitting}
            >
              Submit answer
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function parsePayloadText(value: string): Record<string, unknown> | undefined | Error {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
      return new Error("Answer payload must be a JSON object.");
    }
    return parsed as Record<string, unknown>;
  } catch {
    return new Error("Answer payload must be valid JSON.");
  }
}

function buildAnswerPayload({
  request,
  selectedIDs,
  answerText,
  parsedPayload,
}: {
  request: HumanInputRequest;
  selectedIDs: string[];
  answerText: string;
  parsedPayload?: Record<string, unknown>;
}): Record<string, unknown> | undefined {
  const payload: Record<string, unknown> = { ...(parsedPayload ?? {}) };
  if (
    (request.request_kind === "tool_approval" ||
      request.request_kind === "action_choice") &&
    selectedIDs.length > 0 &&
    typeof payload.decision !== "string"
  ) {
    payload.decision = selectedIDs[0];
  }
  const reason = answerText.trim();
  if (reason && typeof payload.reason !== "string") {
    payload.reason = reason;
  }
  return Object.keys(payload).length > 0 ? payload : undefined;
}

type PayloadEditorState = {
  visible: boolean;
  required: boolean;
  placeholder: string;
};

const implicitPayloadFields = new Set(["decision", "reason", "cancelled"]);

function getPayloadEditorState(
  request: HumanInputRequest,
  selectedChoices: HumanInputRequest["choices"],
): PayloadEditorState {
  const schemaFields = responseSchemaFields(request.response_schema);
  const structuredFields = schemaFields.properties.filter(
    (field) => !implicitPayloadFields.has(field),
  );
  const requiredStructuredFields = schemaFields.required.filter(
    (field) => !implicitPayloadFields.has(field),
  );
  const selectedEditChoice = selectedChoices.find(isEditChoice);
  const visible =
    Boolean(request.response_schema) &&
    (request.choices.length === 0 ||
      Boolean(selectedEditChoice) ||
      structuredFields.length > 0);

  return {
    visible,
    required:
      visible &&
      (Boolean(selectedEditChoice) || requiredStructuredFields.length > 0),
    placeholder: payloadPlaceholder(structuredFields, selectedEditChoice),
  };
}

function responseSchemaFields(schema: unknown): {
  properties: string[];
  required: string[];
} {
  if (!schema || typeof schema !== "object" || Array.isArray(schema)) {
    return { properties: [], required: [] };
  }
  const record = schema as Record<string, unknown>;
  const properties =
    record.properties &&
    typeof record.properties === "object" &&
    !Array.isArray(record.properties)
      ? Object.keys(record.properties as Record<string, unknown>)
      : [];
  const required = Array.isArray(record.required)
    ? record.required.filter((field): field is string => typeof field === "string")
    : [];
  return { properties, required };
}

function isEditChoice(choice: HumanInputRequest["choices"][number]): boolean {
  const haystack = [choice.id, choice.kind ?? "", choice.label]
    .join(" ")
    .toLowerCase();
  return haystack.includes("edit");
}

function payloadPlaceholder(
  structuredFields: string[],
  selectedEditChoice?: HumanInputRequest["choices"][number],
): string {
  if (structuredFields.includes("edited_command")) {
    return `{"edited_command":"${escapeJSONPlaceholder(selectedEditChoice?.preview ?? "")}"}`;
  }
  if (structuredFields.includes("edited_input")) {
    return '{"edited_input":{}}';
  }
  if (selectedEditChoice?.preview) {
    return `{"edited_command":"${escapeJSONPlaceholder(selectedEditChoice.preview)}"}`;
  }
  return '{"decision":"approve"}';
}

function escapeJSONPlaceholder(value: string): string {
  return value.replaceAll("\\", "\\\\").replaceAll('"', '\\"');
}

function choiceTileClasses({
  destructive,
  selected,
}: {
  destructive?: boolean;
  selected: boolean;
}) {
  return cn(
    "flex min-h-14 cursor-pointer items-center gap-3 rounded-lg border border-border bg-surface-raised/80 px-3 py-2 text-foreground shadow-sm transition-[background-color,border-color,box-shadow] hover:border-primary/40 hover:bg-surface-hover focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/30",
    selected && "border-primary bg-surface-selected shadow",
    destructive && "hover:border-destructive/50",
    destructive && selected && "border-destructive bg-destructive/10",
  );
}

function ChoiceSelectionMark({
  destructive,
  selected,
}: {
  destructive?: boolean;
  selected: boolean;
}) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "flex size-5 shrink-0 items-center justify-center rounded-md border border-border bg-surface-raised text-transparent transition-colors",
        selected && "border-primary bg-primary text-primary-foreground",
        destructive &&
          selected &&
          "border-destructive bg-destructive text-destructive-foreground",
      )}
    >
      <Check className="size-3.5" />
    </span>
  );
}

function ChoiceText({
  choice,
  compact,
}: {
  choice: HumanInputRequest["choices"][number];
  compact?: boolean;
}) {
  return (
    <span className="min-w-0 flex-1 space-y-1">
      <span className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 text-sm font-medium leading-snug">
          {choice.label}
        </span>
        {choice.kind ? (
          <Badge variant="secondary" className="capitalize">
            {choice.kind}
          </Badge>
        ) : null}
      </span>
      {choice.description ? (
        <span className="block text-xs text-muted-foreground">
          {choice.description}
        </span>
      ) : null}
      {choice.preview && !compact ? (
        <span className="block rounded bg-muted px-2 py-1 font-mono text-xs text-muted-foreground">
          {choice.preview}
        </span>
      ) : null}
    </span>
  );
}
