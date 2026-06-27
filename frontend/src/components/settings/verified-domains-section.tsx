"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, ExternalLink, Github, Globe, Trash2 } from "lucide-react";

import { ApiError, api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { formatDateTime } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ErrorText } from "@/components/ui/error-notice";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
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
import type { GitHubInviteStatus, GitHubOrgAutoJoin, GitHubOrgAutoJoinResponse, ListResponse, OrganizationDomain, SingleResponse } from "@/lib/types";

// copyText writes to the clipboard, falling back to the legacy
// execCommand path when the async Clipboard API is unavailable —
// navigator.clipboard only exists in secure contexts, and self-hosted
// deployments reached over plain HTTP (e.g. a LAN IP) are a real audience
// for this screen. Returns whether the copy succeeded.
async function copyText(value: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
      return true;
    }
  } catch {
    // Permission denied or transient failure — try the legacy path.
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = value;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}

// CopyButton copies a DNS record fragment and flashes a checkmark. Local
// state per button so copying the name doesn't light up the value's button.
// On copy failure the record stays selectable as plain text, so the flow
// degrades to manual selection rather than dead-ending.
function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className="h-6 w-6 p-0 shrink-0"
      aria-label={`Copy ${label}`}
      onClick={() => {
        void copyText(value).then((ok) => {
          if (!ok) return;
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        });
      }}
    >
      {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
    </Button>
  );
}

// VerifiedDomainsSection is the admin surface for domain capture: claim a
// company domain, publish the DNS TXT record we hand out, verify, and from
// then on OAuth signups with a verified email on that domain auto-join the
// workspace as members.
export function VerifiedDomainsSection() {
  const queryClient = useQueryClient();
  const [newDomain, setNewDomain] = useState("");
  const [error, setError] = useState("");
  const [removing, setRemoving] = useState<OrganizationDomain | null>(null);
  const [disablingGitHubOrg, setDisablingGitHubOrg] = useState<GitHubOrgAutoJoin | null>(null);

  const { data, isLoading } = useQuery<ListResponse<OrganizationDomain>>({
    queryKey: queryKeys.team.domains,
    queryFn: () => api.team.listDomains(),
  });
  const domains = data?.data ?? [];

  const { data: githubOrgData, isLoading: githubOrgsLoading } = useQuery<GitHubOrgAutoJoinResponse>({
    queryKey: queryKeys.team.githubOrgs,
    queryFn: () => api.team.listGitHubOrgs(),
  });
  const githubOrgs = githubOrgData?.github_orgs ?? [];
  const { data: githubStatusData } = useQuery<SingleResponse<GitHubInviteStatus>>({
    queryKey: ["team-github-status"],
    queryFn: () => api.team.githubInviteStatus(),
  });
  const githubConnected = githubStatusData?.data.connected ?? false;

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.team.domains });
    void queryClient.invalidateQueries({ queryKey: queryKeys.team.githubOrgs });
  };

  const friendlyError = (err: unknown, fallback: string) =>
    err instanceof ApiError ? err.message : fallback;

  const addMutation = useMutation({
    mutationFn: (domain: string) => api.team.addDomain(domain),
    onSuccess: () => {
      setNewDomain("");
      setError("");
      invalidate();
    },
    onError: (err) => setError(friendlyError(err, "Couldn't add the domain. Please try again.")),
  });

  const verifyMutation = useMutation({
    mutationFn: (id: string) => api.team.verifyDomain(id),
    onSuccess: () => {
      setError("");
      invalidate();
    },
    onError: (err) => {
      setError(friendlyError(err, "Verification failed. Please try again."));
      // last_checked_at moved even on failure — refresh so it shows.
      invalidate();
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      api.team.updateDomain(id, { auto_join_enabled: enabled }),
    onSuccess: invalidate,
    onError: (err) => setError(friendlyError(err, "Couldn't update the domain. Please try again.")),
  });

  const removeMutation = useMutation({
    mutationFn: (id: string) => api.team.removeDomain(id),
    onSuccess: () => {
      setError("");
      invalidate();
    },
    onError: (err) => setError(friendlyError(err, "Couldn't remove the domain. Please try again.")),
  });

  const updateGitHubOrgMutation = useMutation({
    mutationFn: ({ installationId, enabled }: { installationId: number; enabled: boolean }) =>
      api.team.updateGitHubOrg(installationId, { auto_join_enabled: enabled }),
    onSuccess: () => {
      setError("");
      invalidate();
    },
    onError: (err) =>
      setError(friendlyError(err, "Couldn't update GitHub organization auto-join. Please try again.")),
  });

  const handleAdd = () => {
    const domain = newDomain.trim();
    if (!domain) return;
    addMutation.mutate(domain);
  };

  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-xs font-medium text-foreground">Auto-join</h2>
        <p className="mt-1 text-xs text-muted-foreground">
          People who match these rules join as members automatically — no invitation needed.
        </p>
      </div>

      {error && (
        <ErrorText className="rounded-md bg-destructive/10 px-3 py-2">
          {error}
        </ErrorText>
      )}

      <Card>
        <CardContent className="p-0">
          <div className="divide-y divide-border border-b border-border">
            {githubOrgsLoading ? (
              <div className="p-4 text-xs text-muted-foreground">Loading GitHub organizations...</div>
            ) : githubOrgs.length === 0 ? (
              <div className="flex flex-col gap-2 px-4 py-3 text-xs text-muted-foreground">
                <div className="flex items-start gap-2">
                  <Github className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <span>
                    {githubConnected
                      ? "GitHub is connected, but the app isn't installed on a GitHub organization yet. Auto-join only works when the app is installed on the organization account itself — not a personal account, and not just on individual repos — and is granted permission to read organization members. Re-run the install below and pick your organization as the target."
                      : "Connect GitHub and install the app on your organization so members can auto-join. Choose your organization (not a personal account) as the install target and grant access to read members."}
                  </span>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-7 self-start text-xs"
                  onClick={() => api.integrations.loginGitHub()}
                >
                  <Github className="mr-1.5 h-3 w-3" />
                  {githubConnected ? "Install on a GitHub organization" : "Connect GitHub"}
                  <ExternalLink className="ml-1.5 h-3 w-3" />
                </Button>
              </div>
            ) : (
              githubOrgs.map((org) => {
                const needsApproval = org.members_permission === "missing";
                const unavailable = org.captured_by_other_org || org.account_type === "User";
                const disabled = needsApproval || unavailable || updateGitHubOrgMutation.isPending;
                return (
                  <div key={org.installation_id} className="space-y-2 px-4 py-3">
                    <div className="flex items-center gap-2">
                      <Github className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-xs font-medium">GitHub organization {org.account_login}</div>
                        <div className="text-xs text-muted-foreground">
                          Anyone in {org.account_login} on GitHub can join this workspace.
                        </div>
                      </div>
                      <Switch
                        checked={org.auto_join_enabled}
                        disabled={disabled}
                        onCheckedChange={(enabled) => {
                          if (!enabled && org.auto_join_enabled) {
                            setDisablingGitHubOrg(org);
                            return;
                          }
                          updateGitHubOrgMutation.mutate({ installationId: org.installation_id, enabled });
                        }}
                        aria-label={`Auto-join for GitHub organization ${org.account_login}`}
                      />
                    </div>
                    {needsApproval && (
                      <div className="rounded-md bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400">
                        An owner of {org.account_login} needs to approve updated permissions on GitHub.
                        {org.settings_url && (
                          <Button asChild variant="link" size="sm" className="ml-1 h-auto p-0 text-xs text-amber-700 dark:text-amber-400">
                            <a href={org.settings_url} target="_blank" rel="noreferrer">
                              Review on GitHub <ExternalLink className="ml-1 h-3 w-3" />
                            </a>
                          </Button>
                        )}
                      </div>
                    )}
                    {unavailable && (
                      <div className="rounded-md bg-muted/60 px-3 py-2 text-xs text-muted-foreground">
                        {org.captured_by_other_org
                          ? "This GitHub organization is already connected to another workspace."
                          : "Auto-join is only available for GitHub organization installations."}
                      </div>
                    )}
                  </div>
                );
              })
            )}
          </div>
          <div className="flex flex-col gap-2 border-b border-border px-4 py-3 sm:flex-row">
            <Input
              type="text"
              placeholder="yourcompany.com"
              value={newDomain}
              onChange={(e) => setNewDomain(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") handleAdd();
              }}
              className="sm:max-w-xs"
              aria-label="Domain to verify"
            />
            <Button
              className="h-9"
              onClick={handleAdd}
              disabled={addMutation.isPending || !newDomain.trim()}
            >
              Add domain
            </Button>
          </div>

          {isLoading ? (
            <div className="p-6 text-center text-xs text-muted-foreground">Loading domains...</div>
          ) : domains.length === 0 ? (
            <div className="p-6 text-center text-xs text-muted-foreground">
              No domains yet. Add your company domain to enable automatic team joining.
            </div>
          ) : (
            <div className="divide-y divide-border">
              {domains.map((d) => (
                <div key={d.id} className="space-y-3 px-4 py-3">
                  <div className="flex items-center gap-2 min-w-0">
                    <Globe className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                    <span className="truncate text-xs font-medium">{d.domain}</span>
                    {d.status === "verified" ? (
                      <Badge variant="default">Verified</Badge>
                    ) : (
                      <Badge variant="outline">Pending verification</Badge>
                    )}
                    <div className="flex-1" />
                    {d.status === "pending" && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={verifyMutation.isPending && verifyMutation.variables === d.id}
                        onClick={() => verifyMutation.mutate(d.id)}
                      >
                        {verifyMutation.isPending && verifyMutation.variables === d.id
                          ? "Checking DNS..."
                          : "Verify"}
                      </Button>
                    )}
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 w-7 p-0 text-destructive hover:text-destructive"
                      aria-label={`Remove ${d.domain}`}
                      disabled={removeMutation.isPending}
                      onClick={() => setRemoving(d)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>

                  {d.status === "pending" && (
                    <div className="rounded-md bg-muted/50 px-3 py-2 text-xs space-y-2">
                      <p className="text-muted-foreground">
                        Add this TXT record to your DNS, then click Verify. DNS changes can
                        take a few minutes to propagate.
                      </p>
                      <div className="grid gap-1.5 sm:grid-cols-[60px_1fr_auto] sm:items-center">
                        <span className="text-muted-foreground">Name</span>
                        <code className="truncate font-mono text-xs">{d.dns_record_name}</code>
                        <CopyButton value={d.dns_record_name} label="record name" />
                        <span className="text-muted-foreground">Value</span>
                        <code className="truncate font-mono text-xs">{d.dns_record_value}</code>
                        <CopyButton value={d.dns_record_value} label="record value" />
                      </div>
                      {d.last_checked_at && (
                        <p className="text-muted-foreground">
                          Last checked {formatDateTime(d.last_checked_at)}
                        </p>
                      )}
                    </div>
                  )}

                  {d.status === "verified" && (
                    <>
                      {/* Daily re-check health. failed_checks counts consecutive
                          missing-record days; at 3 the server turns auto-join
                          off (expired/transferred-domain hygiene). Surfacing
                          both states here closes the loop without automatic
                          re-enabling. */}
                      {d.failed_checks > 0 && d.auto_join_enabled && (
                        <div
                          className="rounded-md bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400"
                          data-testid={`domain-recheck-warning-${d.id}`}
                        >
                          The verification record for {d.domain} has been missing for{" "}
                          {d.failed_checks} daily {d.failed_checks === 1 ? "check" : "checks"}.
                          Auto-join turns off automatically after 3.
                        </div>
                      )}
                      {!d.auto_join_enabled && d.failed_checks >= 3 && (
                        <div
                          className="rounded-md bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400"
                          data-testid={`domain-recheck-disabled-${d.id}`}
                        >
                          Auto-join was turned off automatically because the verification
                          record went missing. Restore the TXT record, then re-enable below.
                        </div>
                      )}
                      <div className="flex items-center justify-between gap-3">
                        <div className="text-xs text-muted-foreground">
                          Auto-join: new signups with a verified @{d.domain} email join this
                          workspace automatically as engineers.
                        </div>
                        <Switch
                          checked={d.auto_join_enabled}
                          disabled={updateMutation.isPending && updateMutation.variables?.id === d.id}
                          onCheckedChange={(enabled) =>
                            updateMutation.mutate({ id: d.id, enabled })
                          }
                          aria-label={`Auto-join for ${d.domain}`}
                        />
                      </div>
                    </>
                  )}
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <AlertDialog open={!!removing} onOpenChange={(open) => !open && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove domain</AlertDialogTitle>
            <AlertDialogDescription>
              Remove {removing?.domain}? New signups with @{removing?.domain} emails will no
              longer join this workspace automatically. Existing members are not affected.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (removing) {
                  removeMutation.mutate(removing.id);
                  setRemoving(null);
                }
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!disablingGitHubOrg} onOpenChange={(open) => !open && setDisablingGitHubOrg(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Turn off GitHub auto-join</AlertDialogTitle>
            <AlertDialogDescription>
              Stops new automatic joins from {disablingGitHubOrg?.account_login}. Nobody is removed.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (disablingGitHubOrg) {
                  updateGitHubOrgMutation.mutate({
                    installationId: disablingGitHubOrg.installation_id,
                    enabled: false,
                  });
                  setDisablingGitHubOrg(null);
                }
              }}
            >
              Turn off
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}
