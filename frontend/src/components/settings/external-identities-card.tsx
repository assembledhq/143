"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link2, Unlink } from "lucide-react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import type { ExternalUserLink, User } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/empty-state";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

const sourceLabels: Record<ExternalUserLink["source"], string> = {
  self_linked: "Connected by user",
  admin_linked: "Connected by admin",
  email_match: "Mapped by email",
  directory: "Directory mapping",
};

function identityLabel(link: ExternalUserLink) {
  return link.external_display_name || link.external_handle || link.external_email || link.provider_user_id;
}

export function ExternalIdentitiesCard({ admin = false, members = [] }: { admin?: boolean; members?: User[] }) {
  const queryClient = useQueryClient();
  const integrationsAPI = (api.integrations ?? {}) as Partial<typeof api.integrations>;
  const key = admin ? ["external-identities", "admin"] : ["external-identities", "me"];
  const linksQuery = useQuery({
    queryKey: key,
    queryFn: () => admin ? integrationsAPI.listExternalUserLinks?.() ?? Promise.resolve({ data: [], meta: {} }) : integrationsAPI.listMyExternalIdentities?.() ?? Promise.resolve({ data: [], meta: {} }),
  });
  const suggestionsQuery = useQuery({
    queryKey: ["external-identities", "suggestions"],
    queryFn: () => integrationsAPI.listExternalUserLinkSuggestions?.() ?? Promise.resolve({ data: [], meta: {} }),
    enabled: admin,
  });
  const integrationsQuery = useQuery({ queryKey: ["integrations"], queryFn: () => integrationsAPI.list?.() ?? Promise.resolve({ data: [], meta: {} }), enabled: admin });
  const unmappedQuery = useQuery({ queryKey: ["external-identities", "unmapped"], queryFn: () => integrationsAPI.listUnmappedExternalUsers?.() ?? Promise.resolve({ data: [], meta: {} }), enabled: admin });
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["external-identities"] });
  };
  const remove = useMutation({
    mutationFn: (link: ExternalUserLink) => admin ? integrationsAPI.deleteExternalUserLink!(link.id) : integrationsAPI.deleteMyExternalIdentity!(link.id),
    onSuccess: refresh,
    onError: (error: Error) => captureError(error, { feature: "external-identity-disconnect" }),
  });
  const approve = useMutation({
    mutationFn: (id: string) => integrationsAPI.approveExternalUserLinkSuggestion!(id),
    onSuccess: refresh,
    onError: (error: Error) => captureError(error, { feature: "external-identity-approve" }),
  });
  const dismiss = useMutation({
    mutationFn: (id: string) => integrationsAPI.dismissExternalUserLinkSuggestion!(id),
    onSuccess: refresh,
    onError: (error: Error) => captureError(error, { feature: "external-identity-dismiss" }),
  });
  const links = (linksQuery.data?.data ?? []).filter((link) => link.status === "active");
  const suggestions = suggestionsQuery.data?.data ?? [];
  const exactEmailSuggestions = suggestions.filter((suggestion) => suggestion.reason === "exact_verified_email" && suggestion.confidence === 80);
  const unmapped = unmappedQuery.data?.data ?? [];
  const memberName = (id: string) => members.find((member) => member.id === id)?.name || "Team member";
  const [provider, setProvider] = useState<"slack" | "linear">("slack");
  const [workspaceID, setWorkspaceID] = useState("");
  const [providerUserID, setProviderUserID] = useState("");
  const [userID, setUserID] = useState("");
  const connect = useMutation({
    mutationFn: () => integrationsAPI.createExternalUserLink!({ provider, provider_workspace_id: workspaceID.trim(), provider_user_id: providerUserID.trim(), user_id: userID, replace: true }),
    onSuccess: () => { setProviderUserID(""); refresh(); },
    onError: (error: Error) => captureError(error, { feature: "external-identity-admin-connect" }),
  });

  return (
    <Card id="external-identities">
      <CardHeader>
        <CardTitle>External identities</CardTitle>
        <CardDescription>
          {admin ? "Review Slack and Linear attribution across your team." : "Accounts connected to you for attribution, personal credentials, and approvals."}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {admin ? (
          <div className="flex flex-wrap gap-2" aria-label="Provider connection health">
            {(["slack", "linear"] as const).map((providerName) => {
              const integration = integrationsQuery.data?.data.find((item) => item.provider === providerName);
              return <Badge key={providerName} variant={integration?.status === "active" && !integration.auth_error ? "secondary" : "outline"} className="capitalize">{providerName}: {integration?.auth_error ? "Reconnect required" : integration?.status || "Not connected"}</Badge>;
            })}
          </div>
        ) : null}
        {admin ? (
          <div className="grid gap-2 rounded-md border border-border p-3 md:grid-cols-5">
            <Select value={provider} onValueChange={(value) => setProvider(value as "slack" | "linear")}>
              <SelectTrigger aria-label="Identity provider"><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="slack">Slack</SelectItem><SelectItem value="linear">Linear</SelectItem></SelectContent>
            </Select>
            <Input aria-label="Provider workspace ID" placeholder="Workspace ID" value={workspaceID} onChange={(event) => setWorkspaceID(event.target.value)} />
            <Input aria-label="Provider user ID" placeholder="External user ID" value={providerUserID} onChange={(event) => setProviderUserID(event.target.value)} />
            <Select value={userID} onValueChange={setUserID}>
              <SelectTrigger aria-label="143 team member"><SelectValue placeholder="Team member" /></SelectTrigger>
              <SelectContent>{members.map((member) => <SelectItem key={member.id} value={member.id}>{member.name}</SelectItem>)}</SelectContent>
            </Select>
            <Button disabled={!workspaceID.trim() || !providerUserID.trim() || !userID || connect.isPending} onClick={() => connect.mutate()}>Connect</Button>
          </div>
        ) : null}
        {links.length === 0 ? (
          <EmptyState variant="inline" icon={Link2} title="No connected identities" description={admin ? "Connections appear after a user connects an account or an admin approves a match." : "Use Connect account from Slack or Linear to link an external identity."} />
        ) : (
          <div className="divide-y divide-border rounded-md border border-border">
            {links.map((link) => (
              <div key={link.id} className="flex flex-col gap-3 p-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <Badge variant="outline" className="capitalize">{link.provider}</Badge>
                    <span className="truncate text-sm font-medium">{identityLabel(link)}</span>
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {admin ? `${memberName(link.user_id)} · ` : ""}{sourceLabels[link.source]}
                  </p>
                </div>
                <Button variant="ghost" size="sm" disabled={remove.isPending || (!admin && link.source === "admin_linked")} onClick={() => remove.mutate(link)}>
                  <Unlink className="mr-2 h-4 w-4" />Disconnect
                </Button>
              </div>
            ))}
          </div>
        )}
        {admin && suggestions.length > 0 ? (
          <div className="space-y-2">
            <h3 className="text-sm font-medium">Suggested matches</h3>
            {exactEmailSuggestions.length > 1 ? <Button size="sm" variant="outline" disabled={approve.isPending} onClick={() => exactEmailSuggestions.forEach((suggestion) => approve.mutate(suggestion.id))}>Approve exact email matches</Button> : null}
            {suggestions.map((suggestion) => (
              <div key={suggestion.id} className="flex flex-col gap-3 rounded-md border border-border p-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="text-sm"><span className="font-medium capitalize">{suggestion.provider}</span> {suggestion.external_display_name || suggestion.external_handle || suggestion.provider_user_id} → {memberName(suggestion.suggested_user_id)}</div>
                <div className="flex gap-2">
                  <Button size="sm" onClick={() => approve.mutate(suggestion.id)} disabled={approve.isPending}>Approve</Button>
                  <Button size="sm" variant="outline" onClick={() => dismiss.mutate(suggestion.id)} disabled={dismiss.isPending}>Dismiss</Button>
                </div>
              </div>
            ))}
          </div>
        ) : null}
        {admin && unmapped.length > 0 ? (
          <div className="space-y-2">
            <h3 className="text-sm font-medium">Recently seen unmapped users</h3>
            <div className="divide-y divide-border rounded-md border border-border">
              {unmapped.map((actor) => <div key={actor.id} className="flex items-center justify-between gap-3 p-3 text-sm"><span><span className="font-medium capitalize">{actor.provider}</span> {actor.external_display_name || actor.external_handle || actor.external_email || actor.provider_user_id}</span><span className="text-xs text-muted-foreground">{new Date(actor.last_seen_at).toLocaleDateString()}</span></div>)}
            </div>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
