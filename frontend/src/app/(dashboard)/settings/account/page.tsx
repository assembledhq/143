"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { apiKeyHelp, PERSONAL_PROVIDER_OPTIONS, type PersonalProvider } from "@/lib/coding-auth-metadata";
import { captureError } from "@/lib/errors";
import { APIKeyHelpTooltip } from "@/components/api-key-help-tooltip";
import { CodingAuthDialog } from "@/components/coding-auth-dialog";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ThemeSelect } from "@/components/theme-select";
import type { ListResponse, UserCredentialSummary } from "@/lib/types";

function providerLabel(provider: string) {
  switch (provider) {
    case "openai":
      return "Codex";
    case "anthropic":
      return "Claude Code";
    case "gemini":
      return "Gemini CLI";
    case "openrouter":
      return "OpenRouter";
    default:
      return provider;
  }
}

function statusLabel(status?: string) {
  switch (status) {
    case "active":
      return "Healthy";
    case "invalid":
      return "Invalid";
    case "pending_auth":
      return "Needs reauth";
    default:
      return "Never verified";
  }
}

export default function AccountPage() {
  const queryClient = useQueryClient();
  const [addOpen, setAddOpen] = useState(false);
  const [provider, setProvider] = useState<PersonalProvider>("openai");
  const [apiKey, setApiKey] = useState("");

  const { data: personalResp } = useQuery<ListResponse<UserCredentialSummary>>({
    queryKey: ["user-credentials", "personal"],
    queryFn: () => api.userCredentials.listPersonal(),
  });
  const personalCreds = personalResp?.data ?? [];
  const personalRows = personalCreds.filter((row) => row.configured);

  const createMutation = useMutation({
    mutationFn: () => api.userCredentials.upsertPersonal(provider, { api_key: apiKey }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setApiKey("");
      setAddOpen(false);
      toast.success("Personal auth saved");
    },
    onError: (error) => {
      captureError(error, { feature: "personal-coding-auth-save" });
      toast.error("Could not save personal auth");
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (targetProvider: string) => api.userCredentials.deletePersonal(targetProvider),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      toast.success("Personal auth removed");
    },
    onError: (error) => {
      captureError(error, { feature: "personal-coding-auth-delete" });
      toast.error("Could not remove personal auth");
    },
  });

  return (
    <PageContainer>
      <div className="space-y-8 pt-2">
        <PageHeader
          title="My settings"
          description="Manage your personal coding agent auths and appearance."
          action={(
            <Button onClick={() => setAddOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Add auth
            </Button>
          )}
        />

        <Card>
          <CardHeader>
            <CardTitle>Configured personal auths</CardTitle>
          </CardHeader>
          <CardContent className="pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Agent</TableHead>
                  <TableHead>Auth type</TableHead>
                  <TableHead>Notes</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="w-24 text-right">Action</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {personalRows.length > 0 ? personalRows.map((row) => (
                  <TableRow key={row.provider}>
                    <TableCell>{providerLabel(row.provider)}</TableCell>
                    <TableCell>API key</TableCell>
                    <TableCell>{row.masked_key ?? "Masked key unavailable"}</TableCell>
                    <TableCell>
                      <Badge variant="outline">{statusLabel(row.status)}</Badge>
                    </TableCell>
                    <TableCell className="text-right">
                      <Button variant="ghost" size="sm" onClick={() => deleteMutation.mutate(row.provider)}>
                        <Trash2 className="mr-2 h-4 w-4" />
                        Disable
                      </Button>
                    </TableCell>
                  </TableRow>
                )) : (
                  <TableRow>
                    <TableCell colSpan={5} className="text-muted-foreground">
                      No personal auth configured yet. Add one to take precedence over the organization stack.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Appearance</CardTitle>
          </CardHeader>
          <CardContent className="pb-6">
            <ThemeSelect />
          </CardContent>
        </Card>
      </div>

      <CodingAuthDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        title="Add auth"
        description="Add a personal API key that will be tried before the organization fallback stack."
        providerOptions={PERSONAL_PROVIDER_OPTIONS}
        provider={provider}
        onProviderChange={setProvider}
        primaryLabel="Save auth"
        onPrimary={() => createMutation.mutate()}
        primaryDisabled={!apiKey.trim()}
        onCancel={() => setAddOpen(false)}
      >
        <div className="space-y-2">
          <Label htmlFor="personal-api-key" className="flex items-center gap-2">
            API key
            <APIKeyHelpTooltip
              ariaLabel={`Where to get a ${apiKeyHelp(provider).label} API key`}
              description={apiKeyHelp(provider).description}
              href={apiKeyHelp(provider).href}
              linkLabel={apiKeyHelp(provider).linkLabel}
            />
          </Label>
          <Input
            id="personal-api-key"
            type="password"
            value={apiKey}
            onChange={(event) => setApiKey(event.target.value)}
            placeholder={provider === "anthropic" ? "sk-ant-..." : provider === "gemini" ? "AIza..." : "sk-..."}
          />
        </div>
      </CodingAuthDialog>
    </PageContainer>
  );
}
