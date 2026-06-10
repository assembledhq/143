"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Globe, Trash2 } from "lucide-react";

import { ApiError, api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
import type { ListResponse, OrganizationDomain } from "@/lib/types";

// CopyButton copies a DNS record fragment and flashes a checkmark. Local
// state per button so copying the name doesn't light up the value's button.
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
        void navigator.clipboard.writeText(value).then(() => {
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

  const { data, isLoading } = useQuery<ListResponse<OrganizationDomain>>({
    queryKey: queryKeys.team.domains,
    queryFn: () => api.team.listDomains(),
  });
  const domains = data?.data ?? [];

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.team.domains });
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

  const handleAdd = () => {
    const domain = newDomain.trim();
    if (!domain) return;
    addMutation.mutate(domain);
  };

  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-xs font-medium text-foreground">Verified domains</h2>
        <p className="mt-1 text-xs text-muted-foreground">
          Verify ownership of your company domain and anyone who signs in with a
          verified <span className="font-medium">@yourdomain</span> email (via Google or GitHub)
          automatically joins this workspace as an engineer — no invitation needed.
        </p>
      </div>

      {error && (
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {error}
        </div>
      )}

      <Card>
        <CardContent className="p-0">
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
              size="sm"
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
                          Last checked {new Date(d.last_checked_at).toLocaleString()}
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
    </section>
  );
}
