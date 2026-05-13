"use client";

import { useMemo, useState } from "react";
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
  submitting?: boolean;
  onAnswer: (body: HumanInputAnswerBody) => Promise<void> | void;
  onCancel?: () => Promise<void> | void;
  onAutoOpenDismiss?: () => void;
}

export function HumanInputRequestCard({
  request,
  autoOpen,
  submitting,
  onAnswer,
  onCancel,
  onAutoOpenDismiss,
}: HumanInputRequestCardProps) {
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
  const sensitive =
    request.request_kind === "tool_approval" ||
    request.choices.some((choice) => choice.destructive);
  const selectedIDs = isMulti
    ? selectedChoices
    : selectedChoice
      ? [selectedChoice]
      : [];
  const canSubmit = isFreeText
    ? answerText.trim().length > 0
    : selectedIDs.length > 0 ||
      answerText.trim().length > 0 ||
      payloadText.trim().length > 0;
  const Icon = sensitive ? ShieldAlert : hasChoices ? CheckCircle2 : CircleHelp;
  const dialogOpen = isPending && (open || Boolean(autoOpen));

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
    if (!canSubmit || submitting) return;
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
      {request.response_schema && !hasChoices ? (
        <div className="space-y-2">
          <Textarea
            value={payloadText}
            onChange={(event) => setPayloadText(event.target.value)}
            placeholder='{"decision":"approve"}'
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
                <Button type="button" size="sm" onClick={() => setOpen(true)}>
                  Respond
                </Button>
                {onCancel ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => void onCancel()}
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
                disabled={submitting}
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
