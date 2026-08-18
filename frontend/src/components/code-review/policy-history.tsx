"use client";

import { useMemo, useState } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Clock, History, RotateCcw } from "lucide-react";

import { useAuth } from "@/hooks/use-auth";
import { ApiError, api } from "@/lib/api";
import { notify } from "@/lib/notify";
import { queryKeys } from "@/lib/query-keys";
import type {
  CodeReviewPolicyComparison,
  CodeReviewPolicyFieldChange,
  CodeReviewPolicyVersionSummary,
} from "@/lib/types";
import { cn, formatDateTime, formatTimeAgo } from "@/lib/utils";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ErrorNotice } from "@/components/ui/error-notice";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";

const HISTORY_PAGE_SIZE = 15;

type PolicySelection = {
  newerID: string;
  olderID: string;
};

type LineDiffPart = {
  type: "same" | "removed" | "added";
  text: string;
};

export function CodeReviewPolicyHistory() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [selectionOverride, setSelectionOverride] = useState<PolicySelection | null | undefined>(undefined);
  const [expandedVersionIDOverride, setExpandedVersionIDOverride] = useState<string | null | undefined>(undefined);
  const [restoreTarget, setRestoreTarget] = useState<CodeReviewPolicyVersionSummary | null>(null);

  const historyQuery = useInfiniteQuery({
    queryKey: queryKeys.codeReviews.policyVersions,
    queryFn: ({ pageParam }) => api.codeReviews.listPolicyVersions({ limit: HISTORY_PAGE_SIZE, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.meta?.next_cursor || undefined,
    enabled: isAdmin,
  });
  const policyQuery = useQuery({
    queryKey: queryKeys.codeReviews.policy,
    queryFn: () => api.codeReviews.getPolicy(),
    enabled: isAdmin,
  });
  const versions = useMemo(() => {
    const byID = new Map<string, CodeReviewPolicyVersionSummary>();
    for (const version of historyQuery.data?.pages.flatMap((page) => page.data ?? []) ?? []) {
      byID.set(version.id, version);
    }
    return [...byID.values()].sort((left, right) => right.version - left.version);
  }, [historyQuery.data]);
  const latestVersion = versions[0];

  const defaultSelection = useMemo<PolicySelection | null>(() => {
    if (versions.length < 2) return null;
    const latestWithPrevious = versions.find((version) => version.previous_policy_id);
    if (!latestWithPrevious?.previous_policy_id) return null;
    return { newerID: latestWithPrevious.id, olderID: latestWithPrevious.previous_policy_id };
  }, [versions]);
  const selection = selectionOverride === undefined ? defaultSelection : selectionOverride;

  const selectedNewer = versions.find((version) => version.id === selection?.newerID);
  const selectedOlder = versions.find((version) => version.id === selection?.olderID);
  const selectedOlderVersion = selectedOlder?.version ?? (
    selectedNewer && selectedNewer.previous_policy_id === selection?.olderID
      ? selectedNewer.previous_policy_version
      : undefined
  );
  const selectionIsValid = Boolean(
    selection?.newerID
      && selection?.olderID
      && selectedNewer
      && selectedOlderVersion
      && selectedNewer.version > selectedOlderVersion,
  );
  const comparisonQuery = useQuery({
    queryKey: queryKeys.codeReviews.policyComparison(selection?.newerID, selection?.olderID),
    queryFn: () => api.codeReviews.comparePolicyVersions(selection?.newerID ?? "", selection?.olderID ?? ""),
    enabled: open && selectionIsValid,
  });

  const restoreMutation = useMutation({
    mutationFn: (target: CodeReviewPolicyVersionSummary) => {
      const expectedVersion = policyQuery.data?.data.policy?.version;
      if (!expectedVersion) throw new Error("The active policy version is not available.");
      return api.codeReviews.restorePolicyVersion(target.id, expectedVersion);
    },
    onSuccess: (response) => {
      setRestoreTarget(null);
      // Let the refreshed history choose the newly-created version and its
      // actual predecessor. Comparing against the restored-from version would
      // show no changes and would not explain what this restore changed now.
      setSelectionOverride(undefined);
      setExpandedVersionIDOverride(response.data.policy.id);
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.policy });
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.policyVersions });
      void queryClient.invalidateQueries({ queryKey: ["audit-logs"] });
      notify.success(`Version ${response.data.restored_from.version} restored as version ${response.data.policy.version}`);
    },
    onError: (error) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.policy });
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.policyVersions });
      const message = error instanceof ApiError && error.status === 409
        ? "The policy changed before the restore completed. Review the latest version and try again."
        : error instanceof Error ? error.message : "Try again.";
      notify.error("Policy version could not be restored", { description: message });
    },
  });

  if (!isAdmin) return null;
  if (!latestVersion && !historyQuery.isError) return null;

  const selectVersion = (version: CodeReviewPolicyVersionSummary) => {
    setExpandedVersionIDOverride(version.id);
    if (!version.previous_policy_id) {
      setSelectionOverride(null);
      return;
    }
    setSelectionOverride({ newerID: version.id, olderID: version.previous_policy_id });
  };
  const expandedVersionID = expandedVersionIDOverride === undefined ? selection?.newerID : expandedVersionIDOverride;

  const actorName = (version: CodeReviewPolicyVersionSummary) => policyActorName(version);

  return (
    <>
      <footer className="flex border-t border-border/60 pt-4 text-xs text-muted-foreground">
        <Button
          variant="ghost"
          size="xs"
          onClick={() => setOpen(true)}
          className="inline-flex h-auto items-center gap-1.5 px-1 py-0.5 text-xs font-normal text-muted-foreground transition-colors hover:text-foreground"
        >
          <Clock className="size-3" />
          {latestVersion ? (
            <>
              <span>Last activity:</span>
              <span>
                Updated {formatTimeAgo(latestVersion.audit?.created_at ?? latestVersion.created_at)} by {actorName(latestVersion)}
              </span>
            </>
          ) : <span>Policy history unavailable</span>}
        </Button>
      </footer>

      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent className="w-[calc(100vw-1rem)] p-0 sm:max-w-2xl">
          <SheetHeader className="border-b border-border px-5 py-5 pr-12">
            <div className="flex items-center gap-2">
              <History className="size-4 text-muted-foreground" />
              <SheetTitle>Review policy history</SheetTitle>
            </div>
            <SheetDescription>See exactly what changed, compare versions, or restore an earlier policy as a new version.</SheetDescription>
          </SheetHeader>

          <div className="space-y-5 px-5 py-5">
            {historyQuery.isError ? (
              <ErrorNotice
                title="Policy history could not be loaded"
                description="Retry the request to inspect saved policy changes."
                action={{ label: "Retry", onClick: () => void historyQuery.refetch() }}
              />
            ) : null}

            {versions.length >= 2 ? (
              <section className="space-y-3" aria-labelledby="policy-compare-heading">
                <h3 id="policy-compare-heading" className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Compare versions</h3>
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <VersionSelect
                    label="Newer version"
                    value={selection?.newerID}
                    versions={versions.filter((version) => versions.some((candidate) => candidate.version < version.version))}
                    onValueChange={(newerID) => {
                      const newer = versions.find((version) => version.id === newerID);
                      if (!newer) return;
                      const currentOlder = versions.find((version) => version.id === selection?.olderID);
                      const older = currentOlder && currentOlder.version < newer.version
                        ? currentOlder
                        : versions.find((version) => version.version < newer.version);
                      if (older) {
                        setSelectionOverride({ newerID, olderID: older.id });
                        setExpandedVersionIDOverride(newerID);
                      }
                    }}
                  />
                  <VersionSelect
                    label="Compare with"
                    value={selection?.olderID}
                    versions={versions.filter((version) => selectedNewer && version.version < selectedNewer.version)}
                    onValueChange={(olderID) => {
                      if (selection?.newerID) {
                        setSelectionOverride({ newerID: selection.newerID, olderID });
                        setExpandedVersionIDOverride(selection.newerID);
                      }
                    }}
                    disabled={!selectedNewer}
                  />
                </div>
                <p className="text-xs text-muted-foreground">Older versions appear as you load more history.</p>
              </section>
            ) : null}

            <section className="space-y-3" aria-labelledby="policy-history-heading">
              <div className="flex items-center justify-between gap-3">
                <h3 id="policy-history-heading" className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Changes</h3>
                <span className="text-xs tabular-nums text-muted-foreground">{versions.length} loaded</span>
              </div>

              {historyQuery.isLoading ? <p className="text-sm text-muted-foreground">Loading policy history…</p> : null}
              <div className="space-y-2">
                {versions.map((version) => {
                  const expanded = expandedVersionID === version.id;
                  const canCompare = Boolean(version.previous_policy_id);
                  return (
                    <PolicyVersionCard
                      key={version.id}
                      version={version}
                      actorName={actorName(version)}
                      expanded={expanded}
                      comparison={expanded && selectionIsValid ? comparisonQuery.data?.data : undefined}
                      comparisonOlderVersion={expanded && selectionIsValid ? selectedOlderVersion : undefined}
                      comparisonNewerVersion={expanded && selectionIsValid ? selectedNewer?.version : undefined}
                      comparisonLoading={expanded && canCompare && comparisonQuery.isLoading}
                      comparisonError={expanded && canCompare && comparisonQuery.isError}
                      onToggle={() => expanded ? setExpandedVersionIDOverride(null) : selectVersion(version)}
                      onRestore={() => setRestoreTarget(version)}
                      restorePending={restoreMutation.isPending && restoreMutation.variables?.id === version.id}
                    />
                  );
                })}
              </div>

              {historyQuery.hasNextPage ? (
                <div className="flex justify-center pt-1">
                  <DisabledTooltip disabled={historyQuery.isFetchingNextPage} content="Loading older versions…">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={historyQuery.isFetchingNextPage}
                      onClick={() => void historyQuery.fetchNextPage()}
                    >
                      {historyQuery.isFetchingNextPage ? "Loading…" : "Load older versions"}
                    </Button>
                  </DisabledTooltip>
                </div>
              ) : null}
            </section>
          </div>
        </SheetContent>
      </Sheet>

      <AlertDialog open={Boolean(restoreTarget)} onOpenChange={(nextOpen) => !nextOpen && setRestoreTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Restore version {restoreTarget?.version}?</AlertDialogTitle>
            <AlertDialogDescription>
              This keeps the existing history and creates a new active version with the settings from version {restoreTarget?.version}.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={restoreMutation.isPending}
              onClick={() => restoreTarget && restoreMutation.mutate(restoreTarget)}
            >
              {restoreMutation.isPending ? "Restoring…" : "Restore as new version"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function VersionSelect({
  label,
  value,
  versions,
  onValueChange,
  disabled = false,
}: {
  label: string;
  value?: string;
  versions: CodeReviewPolicyVersionSummary[];
  onValueChange: (value: string) => void;
  disabled?: boolean;
}) {
  return (
    <div className="space-y-2">
      <span className="text-xs text-muted-foreground">{label}</span>
      <Select value={value ?? ""} onValueChange={onValueChange} disabled={disabled || versions.length === 0}>
        <SelectTrigger density="compact" aria-label={label} className="px-3">
          <SelectValue placeholder="Select a version" />
        </SelectTrigger>
        <SelectContent>
          {versions.map((version) => (
            <SelectItem key={version.id} value={version.id} className="py-2 pr-9 pl-3">
              Version {version.version}{version.active ? " · Latest" : ""} — {version.summary}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function PolicyVersionCard({
  version,
  actorName,
  expanded,
  comparison,
  comparisonOlderVersion,
  comparisonNewerVersion,
  comparisonLoading,
  comparisonError,
  onToggle,
  onRestore,
  restorePending,
}: {
  version: CodeReviewPolicyVersionSummary;
  actorName: string;
  expanded: boolean;
  comparison?: CodeReviewPolicyComparison;
  comparisonOlderVersion?: number;
  comparisonNewerVersion?: number;
  comparisonLoading: boolean;
  comparisonError: boolean;
  onToggle: () => void;
  onRestore: () => void;
  restorePending: boolean;
}) {
  const eventTime = version.audit?.created_at ?? version.created_at;
  const reason = policyReason(version);

  return (
    <Card className={cn("overflow-hidden py-0 shadow-none transition-colors", expanded && "border-primary/30 ring-1 ring-primary/10")}>
      <Button
        variant="ghost"
        onClick={onToggle}
        aria-expanded={expanded}
        className="h-auto w-full items-start justify-start whitespace-normal rounded-none px-4 py-3.5 text-left hover:bg-muted/40 sm:h-auto"
      >
        <span className="mr-2 mt-0.5 flex shrink-0 text-muted-foreground">
          {expanded ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-1.5">
          <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span className="min-w-0 break-words text-sm font-medium leading-5 text-foreground">
              {version.active ? `Latest change: ${version.summary}` : version.summary}
            </span>
            {version.active ? <Badge variant="secondary">Latest</Badge> : null}
            <Badge variant="outline" className="tabular-nums">v{version.version}</Badge>
          </span>
          <span className="block whitespace-normal break-words text-xs font-normal leading-4 text-muted-foreground">
            {actorName} · {formatDateTime(eventTime, { year: true })} · {reason}
          </span>
        </span>
      </Button>

      {expanded ? (
        <CardContent className="animate-in space-y-4 border-t border-border px-4 py-4 fade-in-0 slide-in-from-top-1 duration-150 motion-reduce:animate-none">
          {version.previous_policy_id ? (
            <div className="space-y-3">
              <p className="text-xs font-medium text-muted-foreground">
                Changes from version {comparisonOlderVersion ?? version.previous_policy_version} to version {comparisonNewerVersion ?? version.version}
              </p>
              {comparisonLoading ? <p className="text-sm text-muted-foreground">Loading exact changes…</p> : null}
              {comparisonError ? <ErrorNotice title="Changes could not be loaded" description="Close this version and try again." /> : null}
              {comparison ? <PolicyDiff changes={comparison.changes} /> : null}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">This is the first saved organization policy, so there is no earlier version to compare.</p>
          )}

          <div className="flex flex-wrap items-start justify-between gap-3 border-t border-border pt-3">
            <PolicyVersionDetails version={version} />
            {!version.active ? (
              <DisabledTooltip disabled={restorePending} content="Restoring this policy version…">
                <Button variant="outline" size="sm" disabled={restorePending} onClick={onRestore}>
                  <RotateCcw className="size-3.5" />
                  {restorePending ? "Restoring…" : "Restore as new version"}
                </Button>
              </DisabledTooltip>
            ) : null}
          </div>
        </CardContent>
      ) : null}
    </Card>
  );
}

function PolicyDiff({ changes }: { changes: CodeReviewPolicyFieldChange[] }) {
  if (changes.length === 0) {
    return <p className="text-sm text-muted-foreground">No effective policy settings changed.</p>;
  }
  return (
    <div className="space-y-3">
      {changes.map((change) => (
        <section key={change.path} className="space-y-1.5" aria-label={change.label}>
          <h4 className="text-sm font-medium text-foreground">{change.label}</h4>
          {change.kind === "text" ? <TextChange change={change} /> : null}
          {change.kind === "list" ? <ListChange change={change} /> : null}
          {change.kind === "value" ? <ValueChange change={change} /> : null}
        </section>
      ))}
    </div>
  );
}

function TextChange({ change }: { change: CodeReviewPolicyFieldChange }) {
  const parts = compactLineDiff(lineDiff(String(change.before ?? ""), String(change.after ?? "")));
  return (
    <div className="overflow-x-auto rounded-lg border border-border font-mono text-xs">
      {parts.map((part, index) => (
        <div
          key={`${part.type}-${index}`}
          className={cn(
            "grid min-w-max grid-cols-[1.5rem_1fr] px-2 py-1",
            part.type === "removed" && "bg-red-500/10 text-red-800 dark:text-red-300",
            part.type === "added" && "bg-green-500/10 text-green-800 dark:text-green-300",
            part.type === "same" && "text-muted-foreground",
          )}
        >
          <span aria-hidden="true">{part.type === "removed" ? "−" : part.type === "added" ? "+" : " "}</span>
          <span className="whitespace-pre-wrap break-words">{part.text || " "}</span>
        </div>
      ))}
    </div>
  );
}

function ListChange({ change }: { change: CodeReviewPolicyFieldChange }) {
  const before = Array.isArray(change.before) ? change.before : [];
  const after = Array.isArray(change.after) ? change.after : [];
  const removed = listValuesMissingFrom(before, after);
  const added = listValuesMissingFrom(after, before);
  const orderChanged = retainedListOrderChanged(before, after);
  return (
    <div className="overflow-hidden rounded-lg border border-border font-mono text-xs">
      {removed.map((value, index) => (
        <div key={`removed-${stableValueKey(value)}-${index}`} className="flex gap-2 bg-red-500/10 px-3 py-1.5 text-red-800 dark:text-red-300">
          <span aria-hidden="true">−</span><span>{formatPolicyValue(value)}</span>
        </div>
      ))}
      {added.map((value, index) => (
        <div key={`added-${stableValueKey(value)}-${index}`} className="flex gap-2 bg-green-500/10 px-3 py-1.5 text-green-800 dark:text-green-300">
          <span aria-hidden="true">+</span><span>{formatPolicyValue(value)}</span>
        </div>
      ))}
      {orderChanged ? <p className="px-3 py-2 text-muted-foreground">The list order changed.</p> : null}
      {removed.length === 0 && added.length === 0 && !orderChanged ? <p className="px-3 py-2 text-muted-foreground">The list changed.</p> : null}
    </div>
  );
}

function ValueChange({ change }: { change: CodeReviewPolicyFieldChange }) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-2 rounded-lg border border-border px-3 py-2 text-sm">
      <span className="break-words text-muted-foreground line-through">{formatPolicyValue(change.before)}</span>
      <span aria-hidden="true" className="text-muted-foreground">→</span>
      <span className="break-words font-medium text-foreground">{formatPolicyValue(change.after)}</span>
    </div>
  );
}

function PolicyVersionDetails({ version }: { version: CodeReviewPolicyVersionSummary }) {
  const [open, setOpen] = useState(false);
  const audit = version.audit;
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="min-w-0 flex-1">
      <CollapsibleTrigger asChild>
        <Button variant="ghost" size="sm" className="px-1 text-muted-foreground">
          {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
          Details
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent className="data-[state=open]:animate-in data-[state=open]:pt-3 data-[state=open]:fade-in-0 data-[state=open]:slide-in-from-top-1 data-[state=open]:duration-150 motion-reduce:animate-none">
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted-foreground">Policy ID</dt><dd className="break-all font-mono text-foreground">{version.id}</dd>
          <dt className="text-muted-foreground">Source</dt><dd className="text-foreground">{audit?.source || "Unknown"}</dd>
          {audit?.tool_name ? <><dt className="text-muted-foreground">Tool</dt><dd className="break-all text-foreground">{audit.tool_name}</dd></> : null}
          {audit?.request_id ? <><dt className="text-muted-foreground">Request ID</dt><dd className="break-all font-mono text-foreground">{audit.request_id}</dd></> : null}
          {audit?.session_id ? <><dt className="text-muted-foreground">Session ID</dt><dd className="break-all font-mono text-foreground">{audit.session_id}</dd></> : null}
          {audit?.ip_address ? <><dt className="text-muted-foreground">IP address</dt><dd className="break-all font-mono text-foreground">{audit.ip_address}</dd></> : null}
          {audit?.user_agent ? <><dt className="text-muted-foreground">User agent</dt><dd className="break-all text-foreground">{audit.user_agent}</dd></> : null}
        </dl>
      </CollapsibleContent>
    </Collapsible>
  );
}

function policyActorName(version: CodeReviewPolicyVersionSummary): string {
  if (version.audit?.actor_name) return version.audit.actor_name;
  const userID = version.audit?.user_id ?? version.created_by_user_id;
  if (version.audit?.actor_type === "agent") return version.audit.tool_name || "Agent";
  if (version.audit?.actor_type === "system") return version.audit.tool_name || "System";
  if (version.audit?.actor_type === "webhook") return "Webhook";
  return userID || version.audit?.actor_id || "Unknown actor";
}

function policyReason(version: CodeReviewPolicyVersionSummary): string {
  if (version.audit?.reason) return version.audit.reason;
  switch (version.audit?.source) {
    case "example": return "Applied an example";
    case "reset": return "Reset to defaults";
    case "restore": return "Restored an earlier version";
    case "manual": return "Manual edit";
    default: return version.version === 1 ? "Initial policy" : "Policy update";
  }
}

function formatPolicyValue(value: unknown): string {
  if (value === null || value === undefined || value === "") return "Not set";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  if (typeof value === "string" || typeof value === "number") return String(value);
  return JSON.stringify(value, null, 2);
}

function stableValueKey(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value);
}

function listValuesMissingFrom(values: unknown[], comparison: unknown[]): unknown[] {
  const available = new Map<string, number>();
  for (const value of comparison) {
    const key = stableValueKey(value);
    available.set(key, (available.get(key) ?? 0) + 1);
  }
  return values.filter((value) => {
    const key = stableValueKey(value);
    const count = available.get(key) ?? 0;
    if (count === 0) return true;
    available.set(key, count - 1);
    return false;
  });
}

function retainedListOrderChanged(before: unknown[], after: unknown[]): boolean {
  const retainedKeys = (values: unknown[], comparison: unknown[]) => {
    const available = new Map<string, number>();
    for (const value of comparison) {
      const key = stableValueKey(value);
      available.set(key, (available.get(key) ?? 0) + 1);
    }
    const result: string[] = [];
    for (const value of values) {
      const key = stableValueKey(value);
      const count = available.get(key) ?? 0;
      if (count === 0) continue;
      result.push(key);
      available.set(key, count - 1);
    }
    return result;
  };
  const beforeRetained = retainedKeys(before, after);
  const afterRetained = retainedKeys(after, before);
  return beforeRetained.length > 1 && beforeRetained.some((key, index) => key !== afterRetained[index]);
}

function lineDiff(before: string, after: string): LineDiffPart[] {
  const beforeLines = before.split("\n");
  const afterLines = after.split("\n");
  if (beforeLines.length * afterLines.length > 50_000) {
    return [
      ...beforeLines.map((text): LineDiffPart => ({ type: "removed", text })),
      ...afterLines.map((text): LineDiffPart => ({ type: "added", text })),
    ];
  }
  const lengths = Array.from({ length: beforeLines.length + 1 }, () => Array<number>(afterLines.length + 1).fill(0));
  for (let left = beforeLines.length - 1; left >= 0; left -= 1) {
    for (let right = afterLines.length - 1; right >= 0; right -= 1) {
      lengths[left][right] = beforeLines[left] === afterLines[right]
        ? lengths[left + 1][right + 1] + 1
        : Math.max(lengths[left + 1][right], lengths[left][right + 1]);
    }
  }
  const parts: LineDiffPart[] = [];
  let left = 0;
  let right = 0;
  while (left < beforeLines.length && right < afterLines.length) {
    if (beforeLines[left] === afterLines[right]) {
      parts.push({ type: "same", text: beforeLines[left] });
      left += 1;
      right += 1;
    } else if (lengths[left + 1][right] >= lengths[left][right + 1]) {
      parts.push({ type: "removed", text: beforeLines[left] });
      left += 1;
    } else {
      parts.push({ type: "added", text: afterLines[right] });
      right += 1;
    }
  }
  while (left < beforeLines.length) parts.push({ type: "removed", text: beforeLines[left++] });
  while (right < afterLines.length) parts.push({ type: "added", text: afterLines[right++] });
  return parts;
}

function compactLineDiff(parts: LineDiffPart[], contextLines = 2): LineDiffPart[] {
  const changedIndexes = parts.flatMap((part, index) => part.type === "same" ? [] : [index]);
  if (changedIndexes.length === 0) return parts;
  const visible = new Set<number>();
  for (const index of changedIndexes) {
    for (let candidate = Math.max(0, index - contextLines); candidate <= Math.min(parts.length - 1, index + contextLines); candidate += 1) {
      visible.add(candidate);
    }
  }
  const result: LineDiffPart[] = [];
  let omitted = false;
  for (let index = 0; index < parts.length; index += 1) {
    if (visible.has(index)) {
      if (omitted) result.push({ type: "same", text: "⋯ unchanged lines ⋯" });
      result.push(parts[index]);
      omitted = false;
    } else {
      omitted = true;
    }
  }
  if (omitted) result.push({ type: "same", text: "⋯ unchanged lines ⋯" });
  return result;
}
