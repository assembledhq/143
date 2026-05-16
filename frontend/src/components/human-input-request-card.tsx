"use client";

import { useId, useMemo, useState } from "react";
import {
  AlertTriangle,
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
                  className={cn(
                    "flex cursor-pointer items-start gap-3 rounded-md border border-border p-3 transition-colors hover:bg-muted/60",
                    checked && "border-primary bg-primary/5",
                    choice.destructive && "border-destructive/40",
                  )}
                >
                  <Checkbox
                    aria-label={choice.label}
                    checked={checked}
                    onCheckedChange={(value) =>
                      toggleChoice(choice.id, value === true)
                    }
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
                className={cn(
                  "flex cursor-pointer items-start gap-3 rounded-md border border-border p-3 transition-colors hover:bg-muted/60",
                  selectedChoice === choice.id && "border-primary bg-primary/5",
                  choice.destructive && "border-destructive/40",
                )}
              >
                <RadioGroupItem
                  value={choice.id}
                  aria-label={choice.label}
                  className="mt-0.5"
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
      <div className="flex justify-start">
        <Card className="max-w-2xl border-amber-300/60 bg-amber-50/60 dark:border-amber-800/60 dark:bg-amber-950/20">
          <CardHeader className="space-y-2 pb-3">
            <div className="flex items-start gap-3">
              <div className="rounded-md bg-background p-2 text-amber-700 dark:text-amber-300">
                <Icon className="h-4 w-4" />
              </div>
              <div className="min-w-0 flex-1">
                <CardTitle className="text-sm">{request.title}</CardTitle>
                <p className="mt-1 text-sm text-muted-foreground">
                  {request.body}
                </p>
              </div>
              <Badge variant="outline" className="capitalize">
                {kindLabel}
              </Badge>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            {request.choices.slice(0, 3).map((choice) => (
              <div
                key={choice.id}
                className="rounded-md border border-border bg-background/70 px-3 py-2"
              >
                <ChoiceText choice={choice} compact />
              </div>
            ))}
            {isPending ? (
              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  size="sm"
                  onClick={() => setOpen(true)}
                  disabled={!answerable}
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

function ChoiceText({
  choice,
  compact,
}: {
  choice: HumanInputRequest["choices"][number];
  compact?: boolean;
}) {
  return (
    <span className="min-w-0 flex-1 space-y-1">
      <span className="flex items-center gap-2">
        <span className="text-sm font-medium leading-none">{choice.label}</span>
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
