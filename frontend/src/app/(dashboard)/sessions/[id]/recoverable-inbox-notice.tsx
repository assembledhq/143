import { AlertTriangle, Loader2, RefreshCw } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import type { ThreadInboxDeliverySummary, ThreadInboxEntry } from "@/lib/types";
import { cn } from "@/lib/utils";

const MAX_PREVIEW_LENGTH = 160;

function payloadPreview(entry: ThreadInboxEntry): string {
  const payload = entry.payload;
  const objectPayload = payload && typeof payload === "object" && !Array.isArray(payload) ? payload as Record<string, unknown> : null;
  const candidates = objectPayload
    ? [
        objectPayload.content,
        objectPayload.message,
        objectPayload.answer,
        objectPayload.text,
      ]
    : [];
  const text = candidates.find((value): value is string => typeof value === "string" && value.trim().length > 0);
  const preview = text ?? (typeof payload === "string" ? payload : JSON.stringify(payload ?? ""));

  if (preview.length <= MAX_PREVIEW_LENGTH) {
    return preview;
  }

  return `${preview.slice(0, MAX_PREVIEW_LENGTH - 1)}...`;
}

function stateLabel(entry: ThreadInboxEntry): string {
  if (entry.delivery_state === "unknown_delivery") {
    return "Uncertain";
  }
  if (entry.delivery_state === "dead_letter") {
    return "Failed";
  }
  return entry.delivery_state.replaceAll("_", " ");
}

type RecoverableInboxNoticeProps = {
  summary: ThreadInboxDeliverySummary;
  entries: ThreadInboxEntry[];
  isLoading: boolean;
  isRetrying: boolean;
  onRetryEntry: (entryId: string, replayUnknownDelivery?: boolean) => void;
  onRetryAll: () => void;
};

export function RecoverableInboxNotice({
  summary,
  entries,
  isLoading,
  isRetrying,
  onRetryEntry,
  onRetryAll,
}: RecoverableInboxNoticeProps) {
  const recoverableCount = summary.dead_letter_count + summary.unknown_delivery_count;
  const failedEntries = entries.filter((entry) => entry.delivery_state === "dead_letter");
  const canRetryAllFailed = failedEntries.length > 0 && !isLoading && !isRetrying;

  if (recoverableCount === 0) {
    return null;
  }

  return (
    <Card className="rounded-none border-x-0 border-b-0 border-destructive/25 bg-destructive/5 shadow-none">
      <CardContent className="space-y-3 px-4 py-3">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div className="flex min-w-0 gap-2">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
            <div className="min-w-0 space-y-1">
              <div className="flex flex-wrap items-center gap-2">
                <p className="text-sm font-medium text-foreground">Message delivery needs attention</p>
                {summary.dead_letter_count > 0 ? (
                  <Badge variant="destructive">{summary.dead_letter_count} failed</Badge>
                ) : null}
                {summary.unknown_delivery_count > 0 ? (
                  <Badge variant="outline">{summary.unknown_delivery_count} uncertain</Badge>
                ) : null}
              </div>
              <p className="text-xs text-muted-foreground">
                These messages were accepted for this tab but were not confirmed by the live runtime.
              </p>
            </div>
          </div>
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={!canRetryAllFailed}
            aria-label="Retry all failed messages"
            onClick={onRetryAll}
            className="w-full shrink-0 sm:w-auto"
          >
            {isRetrying ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <RefreshCw className="h-3.5 w-3.5" />
            )}
            Retry all
          </Button>
        </div>

        {isLoading ? (
          <div className="rounded-md border border-border/70 bg-background/60 px-3 py-2 text-xs text-muted-foreground">
            Loading recoverable messages...
          </div>
        ) : entries.length > 0 ? (
          <div className="space-y-2">
            {entries.map((entry) => (
              <div
                key={entry.id}
                className="flex flex-col gap-2 rounded-md border border-border/70 bg-background/75 px-3 py-2 sm:flex-row sm:items-start sm:justify-between"
              >
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge
                      variant={entry.delivery_state === "dead_letter" ? "destructive" : "outline"}
                      className={cn(entry.delivery_state !== "dead_letter" && "capitalize")}
                    >
                      {stateLabel(entry)}
                    </Badge>
                    <span className="text-xs text-muted-foreground">Entry {entry.sequence_no}</span>
                    {entry.delivery_attempts > 0 ? (
                      <span className="text-xs text-muted-foreground">{entry.delivery_attempts} attempts</span>
                    ) : null}
                  </div>
                  <p className="break-words text-sm text-foreground">{payloadPreview(entry)}</p>
                  {entry.last_error ? (
                    <p className="break-words text-xs text-muted-foreground">{entry.last_error}</p>
                  ) : null}
                </div>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={isRetrying}
                  aria-label={`${entry.delivery_state === "unknown_delivery" ? "Replay" : "Retry"} entry ${entry.sequence_no}`}
                  onClick={() => {
                    if (entry.delivery_state === "unknown_delivery") {
                      if (!window.confirm("Replay this message? It may already have reached the agent before the runtime stopped.")) {
                        return;
                      }
                      onRetryEntry(entry.id, true);
                      return;
                    }
                    onRetryEntry(entry.id, false);
                  }}
                  className="w-full shrink-0 sm:w-auto"
                >
                  <RefreshCw className={cn("h-3.5 w-3.5", isRetrying && "animate-spin")} />
                  {entry.delivery_state === "unknown_delivery" ? "Replay" : "Retry"}
                </Button>
              </div>
            ))}
          </div>
        ) : (
          <div className="rounded-md border border-border/70 bg-background/60 px-3 py-2 text-xs text-muted-foreground">
            Recoverable message details are not available yet. Refreshing this session will re-check delivery state.
          </div>
        )}
      </CardContent>
    </Card>
  );
}
