"use client";

import { LinearIcon } from "@/components/linear-icon";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import type { Session } from "@/lib/types";

type LinkedIssue = NonNullable<Session["linked_issues"]>[number];

function linearIssueURL(externalID: string, workspaceSlug?: string): string {
  return workspaceSlug
    ? `https://linear.app/${encodeURIComponent(workspaceSlug)}/issue/${encodeURIComponent(externalID)}`
    : `https://linear.app/issue/${encodeURIComponent(externalID)}`;
}

function safeHTTPURL(value?: string): string | undefined {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:" ? value : undefined;
  } catch {
    return undefined;
  }
}

function pagerDutyChipLabel(link: LinkedIssue): string {
  if (link.pagerduty_incident_number) {
    return `PagerDuty #${link.pagerduty_incident_number}`;
  }
  return link.pagerduty_incident_id ?? link.external_id ?? "PagerDuty incident";
}

function pagerDutyTooltip(link: LinkedIssue, baseTooltip: string): string {
  const details = [baseTooltip];
  if (link.pagerduty_service_name || link.pagerduty_service_id) {
    details.push(link.pagerduty_service_name ?? link.pagerduty_service_id ?? "");
  }
  return details.filter(Boolean).join(" · ");
}

function linearSkipReasonDetail(reason: string): string {
  switch (reason) {
    case "disabled_by_user":
      return "This session was created with Linear workflow-state sync disabled.";
    case "per_team_disabled":
      return "Workflow state sync is disabled by org or team Linear automation settings.";
    case "user_recent_edit":
      return "143 skipped the state move because someone recently edited the issue's workflow state in Linear.";
    case "linear_github_integration_active":
      return "Linear's native GitHub integration is already handling PR-driven workflow transitions for this issue.";
    case "already_in_target_state":
      return "The issue was already in the target Linear workflow state when 143 evaluated the transition.";
    case "already_past_target":
      return "The issue was already past the target workflow state, so 143 refused to move it backwards.";
    case "workspace_mismatch":
      return "The linked issue's workspace no longer matches the connected Linear workspace.";
    case "no_target_state":
      return "The linked Linear team has no matching target workflow state for this milestone.";
    case "private_session":
      return "This session is private, so 143 does not sync Linear state changes.";
    case "not_primary":
      return "Only the primary linked Linear issue drives workflow-state sync.";
    case "debounced":
      return "143 intentionally debounced a duplicate Linear state transition attempt.";
    default:
      return `143 skipped a Linear workflow-state update: ${reason}.`;
  }
}

// LinkedIssueChips renders a compact chip per linked issue in the session
// header. Primary issue first, related issues after, ordered by position.
//
// We render Linear identifiers as clickable badges that deep-link back to
// Linear via the persisted external_id. Other sources (Sentry/Slack) just
// show a labeled chip without a deep link in v1.
//
// See design 62 §"Session detail view" for the full LinkedIssueCard spec
// (state name + color, priority, assignee). v1 ships this minimal chip set
// to surface the linkage immediately; the richer card lives in the right
// detail panel and follows in a polish pass.
//
// When linear_prepare_state === 'failed' the prepare worker gave up before
// linking the primary; turn 1 has been allowed to proceed without Linear
// context (per design 62 §"Failure modes"). We render a faint warning chip
// in that case so dogfooders see the missing-context signal instead of
// silently shipping a session that lost its Linear thread.
export function LinkedIssueChips({ session }: { session: Session }) {
  const links = session.linked_issues ?? [];
  const prepareFailed = session.linear_prepare_state === "failed";
  const linearIdentifierHint = session.linear_identifier_hint?.trim() ?? "";
  const hasRenderableLinearLink = links.some((link) => link.issue_source === "linear" && Boolean(link.external_id));
  const showFallbackLinearHint = linearIdentifierHint.length > 0 && !hasRenderableLinearLink;
  if (links.length === 0 && !prepareFailed && !showFallbackLinearHint) return null;

  return (
    <TooltipProvider delayDuration={150}>
      <div className="flex flex-wrap items-center gap-1 ml-2 shrink-0">
        {prepareFailed && (
          <Tooltip>
            <TooltipTrigger asChild>
              {/* Clickable link rather than a plain status chip: the most common
                  root cause we've seen for prepare-failed is a revoked Linear OAuth
                  token, and the integrations settings page now surfaces the
                  specific reason + a Reconnect CTA. Sending the user there beats
                  a dead-end "prepare failed" badge with no path forward. */}
              <a
                key="linear-prepare-failed"
                href="/settings/integrations"
                className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium bg-warning/10 text-warning border border-warning/30 hover:bg-warning/20"
                aria-describedby="linear-prepare-failed-detail"
              >
                Linear: prepare failed
                {/* sr-only sibling instead of `title=…`: most screen readers ignore
                    title attributes, so the detail goes through aria-describedby. */}
                <span id="linear-prepare-failed-detail" className="sr-only">
                  Linear context preparation failed; turn 1 ran without the primary
                  issue snapshot. The most common cause is a revoked or expired
                  Linear access token — open Settings → Integrations to verify the
                  connection, or re-paste the Linear URL in a follow-up message
                  to re-trigger detection.
                </span>
              </a>
            </TooltipTrigger>
            <TooltipContent sideOffset={6} className="max-w-80">
              Linear context preparation failed; turn 1 ran without the primary
              issue snapshot. The most common cause is a revoked or expired
              Linear access token. Open Settings → Integrations to verify the
              connection, or re-paste the Linear URL in a follow-up message to
              re-trigger detection.
            </TooltipContent>
          </Tooltip>
        )}
        {showFallbackLinearHint && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Badge
                asChild
                variant="secondary"
                className="bg-muted/80 text-foreground border-border/70 [a&]:hover:bg-accent/80"
              >
                <a
                  href={linearIssueURL(linearIdentifierHint)}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  <LinearIcon className="h-3 w-3 opacity-70" />
                  <span>{linearIdentifierHint}</span>
                </a>
              </Badge>
            </TooltipTrigger>
            <TooltipContent sideOffset={6}>Open primary Linear issue in Linear</TooltipContent>
          </Tooltip>
        )}
        {links.map((link) => {
          const isLinear = link.issue_source === "linear";
          const isPagerDuty = link.issue_source === "pagerduty";
          const isPrimary = link.role === "primary";
          // Linear links must always carry an external_id (the Linear key
          // like "ACS-1234"). Falling back to a UUID slice would surface a
          // bug as a confusing chip; render an explicit placeholder instead
          // so the missing key is visible during dogfooding. Non-Linear
          // sources without an external_id render a labeled placeholder
          // rather than a UUID slice — leaking the issue UUID into the UI
          // confuses users and isn't a stable identifier they can search.
          const ident = isLinear
            ? (link.external_id ?? "Linear (no key)")
            : isPagerDuty
              ? pagerDutyChipLabel(link)
              : (link.external_id ?? "Issue (no key)");
          const baseTooltip =
            (link.issue_title ?? "") +
            (isPrimary ? " (primary)" : " (related)") +
            (link.issue_status ? ` · ${link.issue_status}` : "");
          const tooltip = isPagerDuty
            ? pagerDutyTooltip(link, baseTooltip)
            : baseTooltip;

          const chipClasses = isLinear
            ? isPrimary
              ? "bg-muted/80 text-foreground border-border/70 [a&]:hover:bg-accent/80"
              : "bg-muted/60 text-muted-foreground border-border/60 [a&]:hover:bg-accent/70 [a&]:hover:text-foreground"
            : isPagerDuty
              ? isPrimary
                ? "bg-destructive/10 text-destructive border-destructive/30 [a&]:hover:bg-destructive/15"
                : "bg-muted/60 text-muted-foreground border-border/60 [a&]:hover:bg-accent/70 [a&]:hover:text-foreground"
            : isPrimary
              ? "bg-blue-500/10 text-blue-700 dark:text-blue-300 border-blue-500/30"
              : "bg-muted text-muted-foreground border-border";
          const linearSkipDetail =
            isLinear && isPrimary && link.linear_last_skipped_reason
              ? linearSkipReasonDetail(link.linear_last_skipped_reason)
              : "";
          const chipContent = (
            <>
              {isLinear ? <LinearIcon className="h-3 w-3 opacity-70" /> : null}
              <span>{ident}</span>
            </>
          );

          if (isLinear && link.external_id) {
            // Prefer the workspace-qualified URL when we cached the slug;
            // it resolves correctly regardless of which workspace the user
            // last viewed in Linear. Fall back to the universal redirect
            // for legacy links written before slug caching landed.
            const url = linearIssueURL(link.external_id, link.issue_workspace_slug);
            // The link text (the Linear identifier) remains the accessible
            // name. Avoid overriding it with the long context string.
            return (
              <div key={link.id} className="inline-flex items-center gap-1">
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Badge asChild variant="secondary" className={chipClasses}>
                      <a
                        href={url}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        {chipContent}
                      </a>
                    </Badge>
                  </TooltipTrigger>
                  <TooltipContent sideOffset={6}>{tooltip}</TooltipContent>
                </Tooltip>
                {linearSkipDetail && (
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <span
                        className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium bg-warning/10 text-warning border border-warning/30"
                        aria-label={linearSkipDetail}
                      >
                        Linear sync skipped
                      </span>
                    </TooltipTrigger>
                    <TooltipContent sideOffset={6} className="max-w-80">
                      {linearSkipDetail}
                    </TooltipContent>
                  </Tooltip>
                )}
              </div>
            );
          }

          if (isPagerDuty) {
            const url = safeHTTPURL(link.pagerduty_incident_url);
            if (url) {
              return (
                <div key={link.id} className="inline-flex items-center gap-1">
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Badge asChild variant="secondary" className={chipClasses}>
                        <a
                          href={url}
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          {chipContent}
                        </a>
                      </Badge>
                    </TooltipTrigger>
                    <TooltipContent sideOffset={6}>{tooltip}</TooltipContent>
                  </Tooltip>
                </div>
              );
            }
          }

          // Non-interactive chip: most screen readers ignore `title` on
          // non-link/non-button elements, so mirror the tooltip into
          // aria-label so SR users get the issue title + role context.
          return (
            <div key={link.id} className="inline-flex items-center gap-1">
              <Tooltip>
                <TooltipTrigger asChild>
                  <Badge
                    variant="secondary"
                    className={chipClasses}
                    aria-label={tooltip}
                  >
                    {chipContent}
                  </Badge>
                </TooltipTrigger>
                <TooltipContent sideOffset={6}>{tooltip}</TooltipContent>
              </Tooltip>
              {linearSkipDetail && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <span
                      className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium bg-warning/10 text-warning border border-warning/30"
                      aria-label={linearSkipDetail}
                    >
                      Linear sync skipped
                    </span>
                  </TooltipTrigger>
                  <TooltipContent sideOffset={6} className="max-w-80">
                    {linearSkipDetail}
                  </TooltipContent>
                </Tooltip>
              )}
            </div>
          );
        })}
      </div>
    </TooltipProvider>
  );
}
