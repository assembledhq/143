"use client";

import { useQuery } from "@tanstack/react-query";
import { Loader2, Sparkles } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { SessionSummary } from "@/lib/types";
import { AGENTS_BY_KEY } from "@/lib/agents";

// SessionSummaryPanel renders the "summarize all tabs" affordance as a side
// sheet so it never competes with the chat. The user clicks the trigger;
// we fetch the rolled-up summary; the sheet shows per-tab cards plus the
// overlap-by-path table.
//
// This is a deliberately read-only surface. Actions (cancel/fork/revert) live
// on the tab strip itself so we keep one canonical entry point per action.

interface SessionSummaryPanelProps {
  sessionId: string;
  open: boolean;
  onOpenChange: (next: boolean) => void;
}

export function SessionSummaryPanel({ sessionId, open, onOpenChange }: SessionSummaryPanelProps) {
  const summaryQuery = useQuery({
    queryKey: queryKeys.sessions.summary(sessionId),
    queryFn: () => api.sessions.summarizeSession(sessionId),
    enabled: open,
    staleTime: 5_000,
  });
  const summary = summaryQuery.data?.data;

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-[440px] sm:max-w-[480px] overflow-y-auto">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <Sparkles className="h-4 w-4" />
            Session summary
          </SheetTitle>
          <SheetDescription>
            One-glance view of every tab in this sandbox: status, latest result, cost, and file overlap.
          </SheetDescription>
        </SheetHeader>
        <div className="mt-4 space-y-4 px-4 pb-4">
          {summaryQuery.isLoading && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" /> Loading summary…
            </div>
          )}
          {summaryQuery.isError && (
            <div className="text-sm text-destructive">Failed to load summary.</div>
          )}
          {summary && <SummaryBody summary={summary} />}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function SummaryBody({ summary }: { summary: SessionSummary }) {
  return (
    <>
      <section>
        <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {summary.threads.length} tab{summary.threads.length === 1 ? "" : "s"} · {summary.active_count} active
        </div>
        <div className="mt-2 space-y-3">
          {summary.threads.map((tab) => (
            <article key={tab.id} className="rounded-md border p-3">
              <header className="flex items-center justify-between">
                <div className="font-medium text-sm">{tab.label}</div>
                <Badge variant="secondary" className="text-xs">{tab.status}</Badge>
              </header>
              <div className="mt-1 text-xs text-muted-foreground">
                {AGENTS_BY_KEY[tab.agent_type]?.label ?? tab.agent_type} · turn {tab.current_turn}
                {tab.cost_cents > 0 && (
                  <> · ${(tab.cost_cents / 100).toFixed(2)}</>
                )}
              </div>
              {tab.result_summary && (
                <p className="mt-2 text-sm leading-snug whitespace-pre-wrap">{tab.result_summary}</p>
              )}
              {tab.touched_paths && tab.touched_paths.length > 0 && (
                <details className="mt-2 text-xs text-muted-foreground">
                  <summary className="cursor-pointer">{tab.touched_paths.length} touched file{tab.touched_paths.length === 1 ? "" : "s"}</summary>
                  <ul className="mt-1 ml-3 list-disc space-y-0.5">
                    {tab.touched_paths.slice(0, 25).map((p) => (
                      <li key={p} className="truncate">{p}</li>
                    ))}
                    {tab.touched_paths.length > 25 && (
                      <li className="text-muted-foreground/80">…and {tab.touched_paths.length - 25} more</li>
                    )}
                  </ul>
                </details>
              )}
            </article>
          ))}
        </div>
      </section>
      {summary.touched_files.length > 0 && (
        <section>
          <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            Touched files ({summary.touched_files.length})
          </div>
          <ul className="mt-2 space-y-1 text-xs">
            {summary.touched_files.map((file) => (
              <li key={file.path} className="flex items-center justify-between gap-2 rounded border-b py-1">
                <span className="truncate font-mono">{file.path}</span>
                <span className="shrink-0 text-muted-foreground">
                  {file.last_event_type}
                  {file.owner_thread_ids.length > 1 && (
                    <Badge variant="destructive" className="ml-2 text-xs">overlap</Badge>
                  )}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}

// SummarizeButton is the one-click trigger that opens the summary panel.
// Designed for the session header; takes an onClick that toggles the sheet.
export function SummarizeButton({ onClick, disabled }: { onClick: () => void; disabled?: boolean }) {
  return (
    <Button
      type="button"
      size="sm"
      variant="outline"
      onClick={onClick}
      disabled={disabled}
      className="h-8 gap-1.5"
    >
      <Sparkles className="h-3.5 w-3.5" />
      <span>Summarize tabs</span>
    </Button>
  );
}
