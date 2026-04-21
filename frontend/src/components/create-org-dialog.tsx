"use client";

import { FormEvent, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { setActiveOrgId } from "@/lib/active-org";

const MAX_NAME_LEN = 120;

// Duck-typed ApiError.code lookup — see hooks/use-auth.ts for rationale. The
// codes below mirror writeError calls in internal/api/handlers/organizations.go
// and the rate-limit middleware, so the user sees human copy instead of raw
// SCREAMING_SNAKE error codes.
function messageForError(err: unknown): string {
  const code = typeof err === "object" && err !== null ? (err as { code?: unknown }).code : undefined;
  switch (code) {
    case "CREATE_ORG_RATE_LIMITED":
      return "You've created too many organizations in a short time. Please wait a bit and try again.";
    case "NAME_TOO_LONG":
      return `Name must be ${MAX_NAME_LEN} characters or fewer.`;
    case "MISSING_NAME":
      return "Name is required.";
    case "UNAUTHORIZED":
      return "Your session expired. Please sign in again.";
    default:
      if (err instanceof Error && err.message) return err.message;
      return "Failed to create organization.";
  }
}

export interface CreateOrgDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CreateOrgDialog({ open, onOpenChange }: CreateOrgDialogProps) {
  const router = useRouter();
  const queryClient = useQueryClient();

  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);

  const reset = () => {
    setName("");
    setError(null);
  };

  const handleOpenChange = (next: boolean) => {
    if (!next) reset();
    onOpenChange(next);
  };

  const mutation = useMutation({
    mutationFn: (trimmed: string) => api.organizations.create(trimmed),
    onSuccess: async (response) => {
      const created = response.data;
      setActiveOrgId(created.id);
      // Everything (including memberships) is scoped to the previous org; nuke
      // the whole cache so the next render fetches fresh data for the new
      // workspace. The unqualified invalidate subsumes the memberships key.
      await queryClient.invalidateQueries();
      toast.success(`Created ${created.name}`);
      handleOpenChange(false);
      router.push("/sessions");
    },
    onError: (err: unknown) => {
      setError(messageForError(err));
    },
  });

  const handleSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError("Name is required.");
      return;
    }
    if ([...trimmed].length > MAX_NAME_LEN) {
      setError(`Name must be ${MAX_NAME_LEN} characters or fewer.`);
      return;
    }
    setError(null);
    mutation.mutate(trimmed);
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-md" data-testid="create-org-dialog">
        <DialogHeader>
          <DialogTitle>Create organization</DialogTitle>
          <DialogDescription>
            A new workspace with its own sessions, projects, and members. You&apos;ll be added as an admin.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="create-org-name">Name</Label>
            <Input
              id="create-org-name"
              type="text"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Acme Inc."
              maxLength={MAX_NAME_LEN}
              disabled={mutation.isPending}
            />
            {error && (
              <p className="text-xs text-destructive" role="alert">
                {error}
              </p>
            )}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => handleOpenChange(false)}
              disabled={mutation.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={mutation.isPending || name.trim().length === 0}>
              {mutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
