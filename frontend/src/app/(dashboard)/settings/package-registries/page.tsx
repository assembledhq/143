"use client";

import { useState, type FormEvent } from "react";
import Link from "next/link";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Package } from "lucide-react";
import { EmptyState } from "@/components/empty-state";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ErrorNotice, ErrorText } from "@/components/ui/error-notice";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useAuth } from "@/hooks/use-auth";
import { usePageTitle } from "@/hooks/use-page-title";
import { api, ApiError } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { PackageRegistryCredentialSummary } from "@/lib/types";

function credentialError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return "Administrator access is required to manage package credentials.";
    if (error.code === "PACKAGE_REGISTRY_CREDENTIAL_EXISTS") return "This registry origin or package scope already has a credential. Rotate the existing credential instead.";
    if (error.status === 409) return "This credential changed. Refresh the page before trying again.";
    if (error.code === "PACKAGE_REGISTRY_ENCRYPTION_UNAVAILABLE") return "Package credential storage is unavailable. Try again later.";
  }
  return "Unable to update the package credential. Try again.";
}

function validRegistry(value: string): boolean {
  try {
    const url = new URL(value);
    const host = url.hostname;
    const validHost = host.length <= 253 && host.includes(".") && !/^[0-9.]+$/.test(host) && host.split(".").every((label) => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label));
    return value.startsWith("https://") && validHost && url.port === "" && !url.username && !url.password && url.pathname === "/" && !value.includes("?") && !value.includes("#");
  } catch {
    return false;
  }
}

export default function PackageRegistriesPage() {
  usePageTitle("Package registries");
  const { user, isLoading } = useAuth();
  const admin = user?.role === "admin";
  const [selectedRepositoryId, setSelectedRepositoryId] = useState("");
  const repositories = useQuery({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
    enabled: admin,
  });
  const repositoryId = selectedRepositoryId || repositories.data?.data[0]?.id || "";

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader title="Package registries" description="Install private npm dependencies during controlled repository setup." />
        {!isLoading && !admin ? <ErrorNotice title="Administrator access is required to manage package credentials" /> : admin && (
          <>
            <p className="text-muted-foreground">Credentials are used only for dependency installation and are unavailable to coding agents. Preview secrets are managed separately. <Link href="/docs/guides/private-packages" className="text-primary underline underline-offset-4">Setup guide</Link></p>
            {repositories.isError ? (
              <ErrorNotice
                title="Repositories could not be loaded"
                description="Retry the request to manage package credentials."
                action={{ label: "Retry", onClick: () => void repositories.refetch() }}
              />
            ) : repositories.isPending ? <p role="status">Loading repositories…</p> : !repositories.data?.data.length ? (
              <EmptyState icon={Package} title="No repositories connected" description="Connect a repository before adding a package credential." />
            ) : (
              <>
                <div className="max-w-md space-y-2">
                  <Label htmlFor="package-repository">Repository</Label>
                  <Select value={repositoryId} onValueChange={setSelectedRepositoryId}>
                    <SelectTrigger id="package-repository"><SelectValue placeholder="Select repository" /></SelectTrigger>
                    <SelectContent>{repositories.data.data.map((repo) => <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>)}</SelectContent>
                  </Select>
                </div>
                <RepositoryCredentials key={repositoryId} repositoryId={repositoryId} />
              </>
            )}
          </>
        )}
      </div>
    </PageContainer>
  );
}

function RepositoryCredentials({ repositoryId }: { repositoryId: string }) {
  const queryClient = useQueryClient();
  const credentials = useQuery({
    queryKey: queryKeys.repositories.packageRegistryCredentials(repositoryId),
    queryFn: () => api.repositories.packageRegistryCredentials.list(repositoryId),
  });
  const [dialog, setDialog] = useState<{ credential?: PackageRegistryCredentialSummary } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<PackageRegistryCredentialSummary | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const refresh = () => queryClient.invalidateQueries({ queryKey: queryKeys.repositories.packageRegistryCredentials(repositoryId) });

  async function removeCredential(event: React.MouseEvent) {
    event.preventDefault();
    if (!deleteTarget || deleting) return;
    setDeleting(true);
    setDeleteError("");
    try {
      await api.repositories.packageRegistryCredentials.delete(repositoryId, deleteTarget.id, deleteTarget.revision);
      setDeleteTarget(null);
      await refresh();
    } catch (error) {
      setDeleteError(credentialError(error));
    } finally {
      setDeleting(false);
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">Credentials</h2>
        <DisabledTooltip disabled={!credentials.isSuccess} content="Wait for credentials to load successfully.">
          <Button size="sm" disabled={!credentials.isSuccess} onClick={() => setDialog({})}>Add credential</Button>
        </DisabledTooltip>
      </div>
      {credentials.isPending ? <p role="status">Loading credentials…</p> : credentials.isError ? (
        <ErrorNotice
          title="Package credentials could not be loaded"
          description="Retry the request to manage credentials for this repository."
          action={{ label: "Retry", onClick: () => void credentials.refetch() }}
        />
      ) : !credentials.data?.data.length ? (
        <EmptyState icon={Package} title="No package credentials" description="Add a read-only token for an HTTPS npm registry and an explicit package scope." />
      ) : (
        <div className="overflow-x-auto rounded-lg border bg-card shadow-sm">
          <Table>
            <TableHeader><TableRow><TableHead>Registry</TableHead><TableHead>Package scope</TableHead><TableHead className="text-right">Updated</TableHead><TableHead><span className="sr-only">Actions</span></TableHead></TableRow></TableHeader>
            <TableBody>{credentials.data.data.map((credential) => (
              <TableRow key={credential.id}>
                <TableCell className="font-mono text-xs">{credential.registry_url}</TableCell>
                <TableCell className="font-mono text-xs">{credential.package_scope}</TableCell>
                <TableCell className="text-right tabular-nums">{new Date(credential.updated_at).toLocaleDateString()}</TableCell>
                <TableCell><div className="flex justify-end gap-3">
                  <Button variant="outline" size="sm" aria-label={`Rotate ${credential.package_scope} credential`} onClick={() => setDialog({ credential })}>Rotate</Button>
                  <Button variant="ghost" size="sm" className="text-destructive" aria-label={`Delete ${credential.package_scope} credential`} onClick={() => { setDeleteError(""); setDeleteTarget(credential); }}>Delete</Button>
                </div></TableCell>
              </TableRow>
            ))}</TableBody>
          </Table>
        </div>
      )}
      {dialog && <CredentialDialog repositoryId={repositoryId} credential={dialog.credential} onClose={() => setDialog((current) => current === dialog ? null : current)} onSaved={refresh} />}
      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => { if (!open && !deleting) setDeleteTarget(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader><AlertDialogTitle>Delete package credential?</AlertDialogTitle><AlertDialogDescription>Future setup for {deleteTarget?.package_scope} at {deleteTarget?.registry_url} will no longer have this credential. Existing installations are unaffected.</AlertDialogDescription></AlertDialogHeader>
          {deleteError && <ErrorText role="alert">{deleteError}</ErrorText>}
          <AlertDialogFooter>
            <DisabledTooltip disabled={deleting} content="Wait for deletion to finish."><AlertDialogCancel disabled={deleting}>Cancel</AlertDialogCancel></DisabledTooltip>
            <DisabledTooltip disabled={deleting} content="Wait for deletion to finish."><AlertDialogAction disabled={deleting} onClick={removeCredential} className="bg-destructive text-destructive-foreground hover:bg-destructive/90">{deleting ? "Deleting…" : "Delete credential"}</AlertDialogAction></DisabledTooltip>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function CredentialDialog({ repositoryId, credential, onClose, onSaved }: { repositoryId: string; credential?: PackageRegistryCredentialSummary; onClose: () => void; onSaved: () => Promise<unknown> }) {
  const [registry, setRegistry] = useState(credential?.registry_url ?? "");
  const [scope, setScope] = useState(credential?.package_scope ?? "");
  const [token, setToken] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const valid = validRegistry(registry) && /^@[a-z0-9][a-z0-9._-]{0,213}$/.test(scope) && token.length > 0 && token.length <= 8192 && !/[\s\p{Cc}]/u.test(token);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!valid || saving) return;
    setSaving(true);
    setError("");
    try {
      if (credential) {
        await api.repositories.packageRegistryCredentials.rotate(repositoryId, credential.id, { expected_revision: credential.revision, token });
      } else {
        await api.repositories.packageRegistryCredentials.create(repositoryId, { registry_url: registry, package_scope: scope, token });
      }
      setToken("");
      onClose();
      await onSaved();
    } catch (error) {
      setError(credentialError(error));
    } finally {
      setSaving(false);
    }
  }

  function close() {
    setToken("");
    onClose();
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) close(); }}>
      <DialogContent>
        <DialogHeader><DialogTitle>{credential ? "Rotate package credential" : "Add package credential"}</DialogTitle><DialogDescription>Use one read-only token per registry origin, limited to the packages this repository needs. Tokens cannot be revealed after saving.</DialogDescription></DialogHeader>
        <form onSubmit={submit} className="space-y-4">
          {credential ? <p className="break-all font-mono text-xs">{registry} · {scope}</p> : <>
            <div className="space-y-2"><Label htmlFor="package-registry">Registry URL</Label><Input id="package-registry" type="url" placeholder="https://registry.example.test" value={registry} onChange={(event) => setRegistry(event.target.value)} required /><p className="text-xs text-muted-foreground">HTTPS DNS origin on port 443 only, without a path, query, or embedded credentials.</p></div>
            <div className="space-y-2"><Label htmlFor="package-scope">Package scope</Label><Input id="package-scope" placeholder="@example" value={scope} onChange={(event) => setScope(event.target.value)} required /><p className="text-xs text-muted-foreground">An explicit lowercase npm scope, such as @example. One credential per origin and scope.</p></div>
          </>}
          <div className="space-y-2"><Label htmlFor="package-token">Token</Label><Input id="package-token" type="password" autoComplete="new-password" value={token} onChange={(event) => setToken(event.target.value)} required /><p className="text-xs text-muted-foreground">Used only by controlled setup. Never passed to agents or preview applications.</p></div>
          {error && <ErrorText role="alert">{error}</ErrorText>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={close}>Cancel</Button>
            <DisabledTooltip disabled={!valid || saving} content={saving ? "Wait for the credential to save." : "Enter an HTTPS registry origin, a valid npm scope, and a token."}><Button type="submit" disabled={!valid || saving}>{saving ? "Saving…" : credential ? "Rotate credential" : "Add credential"}</Button></DisabledTooltip>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
