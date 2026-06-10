"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Github, Mail, UserPlus } from "lucide-react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { pollMs } from "@/lib/poll-intervals";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { CLIJoinTokensCard } from "@/components/cli-join-tokens-card";
import { useAuth } from "@/hooks/use-auth";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { roleLabel } from "@/lib/roles";
import type {
  User,
  InvitationResponse,
  ListResponse,
  SingleResponse,
  GitHubInviteStatus,
  GitHubUserSuggestion,
} from "@/lib/types";

type InviteDraft =
  | { mode: "email"; value: string }
  | { mode: "github"; value: string; avatarUrl?: string; notificationEmail?: string };

export default function TeamSettingsPage() {
  const queryClient = useQueryClient();
  const { user: currentUser } = useAuth();
  const canManageTeam = currentUser?.role === "admin";
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRole, setInviteRole] = useState("member");
  const [inviteMode, setInviteMode] = useState<"email" | "github">("email");
  const [inviteDraft, setInviteDraft] = useState<InviteDraft | null>(null);
  const [githubNotificationEmail, setGithubNotificationEmail] = useState("");
  const [inviteError, setInviteError] = useState("");
  const [actionError, setActionError] = useState("");
  const [isInviteDialogOpen, setIsInviteDialogOpen] = useState(false);
  const [removingMember, setRemovingMember] = useState<User | null>(null);
  const [pendingRoleChange, setPendingRoleChange] = useState<{
    member: User;
    newRole: string;
  } | null>(null);
  const [ghSearchQuery, setGhSearchQuery] = useState("");
  const [debouncedGhQuery, setDebouncedGhQuery] = useState("");

  const { data: membersData, isLoading: membersLoading } = useQuery<ListResponse<User>>({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
  });

  const { data: invitationsData } = useQuery<ListResponse<InvitationResponse>>({
    queryKey: ["team-invitations"],
    queryFn: () => api.team.listInvitations(),
    enabled: canManageTeam,
  });

  const { data: ghStatusData } = useQuery<SingleResponse<GitHubInviteStatus>>({
    queryKey: ["team-github-status"],
    queryFn: () => api.team.githubInviteStatus(),
    enabled: canManageTeam,
  });
  const githubConnected = ghStatusData?.data.connected ?? false;

  useEffect(() => {
    const handle = setTimeout(() => setDebouncedGhQuery(ghSearchQuery.trim()), pollMs(200));
    return () => clearTimeout(handle);
  }, [ghSearchQuery]);

  const ghSearchEnabled =
    canManageTeam &&
    githubConnected &&
    inviteMode === "github" &&
    isInviteDialogOpen &&
    debouncedGhQuery.length > 0;

  const { data: ghSearchData, isFetching: ghSearchLoading } = useQuery<
    ListResponse<GitHubUserSuggestion>
  >({
    queryKey: ["team-github-search", debouncedGhQuery],
    queryFn: () => api.team.searchGitHubUsers(debouncedGhQuery),
    enabled: ghSearchEnabled,
  });
  const ghSuggestions = useMemo(
    () => ghSearchData?.data ?? [],
    [ghSearchData?.data],
  );

  const changeRoleMutation = useMutation({
    mutationFn: ({ id, role }: { id: string; role: string }) =>
      api.team.changeRole(id, role),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["team", "members"] });
      setActionError("");
    },
    onError: (error: Error) => {
      captureError(error, { feature: "team-change-role" });
      setActionError(error.message || "Failed to change role.");
    },
  });

  const removeMemberMutation = useMutation({
    mutationFn: (id: string) => api.team.removeMember(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["team", "members"] });
      setActionError("");
    },
    onError: (error: Error) => {
      captureError(error, { feature: "team-remove-member" });
      setActionError(error.message || "Failed to remove member.");
    },
  });

  const inviteMutation = useMutation({
    mutationFn: (body: { email?: string; github_username?: string; acceptance_method?: "email" | "github" | "either"; role: string }) =>
      api.team.createInvitation(body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["team-invitations"] });
      resetInviteForm();
      setIsInviteDialogOpen(false);
    },
    onError: (error: Error & { code?: string }) => {
      captureError(error, { feature: "team-invite" });
      if (error.message) {
        setInviteError(error.message);
      } else {
        setInviteError("Failed to send invitation.");
      }
    },
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => api.team.revokeInvitation(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["team-invitations"] });
    },
    onError: (error: Error) => {
      captureError(error, { feature: "team-revoke-invite" });
    },
  });

  const members = membersData?.data ?? [];
  const invitations = invitationsData?.data ?? [];

  const resetInviteForm = () => {
    setInviteEmail("");
    setInviteRole("member");
    setInviteMode("email");
    setInviteDraft(null);
    setGithubNotificationEmail("");
    setInviteError("");
    setGhSearchQuery("");
    setDebouncedGhQuery("");
  };

  const clearInviteDraft = () => {
    setInviteDraft(null);
    setInviteError("");
  };

  const addEmailDraft = () => {
    const email = inviteEmail.trim();
    if (!email) {
      setInviteError("Add an email address to the invite.");
      return;
    }

    setInviteDraft({ mode: "email", value: email });
    setInviteError("");
  };

  const addGitHubDraft = (username: string, avatarUrl?: string) => {
    const normalizedUsername = username.trim().replace(/^@/, "");
    const notificationEmail = githubNotificationEmail.trim();
    if (!normalizedUsername) {
      setInviteError("Add a GitHub username to the invite.");
      return;
    }

    setInviteDraft({
      mode: "github",
      value: normalizedUsername,
      avatarUrl,
      notificationEmail: notificationEmail || undefined,
    });
    setInviteError("");
    setGhSearchQuery("");
    setDebouncedGhQuery("");
  };

  function handleInvite(e: React.FormEvent) {
    e.preventDefault();
    setInviteError("");
    if (!inviteDraft || inviteDraft.mode !== inviteMode) {
      setInviteError(
        inviteMode === "email"
          ? "Add an email address to the invite."
          : "Add a GitHub username to the invite.",
      );
      return;
    }

    if (inviteDraft.mode === "email") {
      inviteMutation.mutate({ email: inviteDraft.value, role: inviteRole });
      return;
    }

    inviteMutation.mutate({
      ...(inviteDraft.notificationEmail ? { email: inviteDraft.notificationEmail } : {}),
      github_username: inviteDraft.value,
      acceptance_method: "github",
      role: inviteRole,
    });
  }

  const inviteDraftLabel = inviteDraft
    ? inviteDraft.mode === "github"
      ? `@${inviteDraft.value}`
      : inviteDraft.value
    : "";
  const submitInviteLabel = inviteDraft
    ? `Send invite to ${inviteDraftLabel}`
    : "Send invite";

  const roleBadgeVariant = (role: string) => {
    switch (role) {
      case "admin":
        return "default" as const;
      case "builder":
      case "member":
        return "secondary" as const;
      default:
        return "outline" as const;
    }
  };

  const invitationStatusBadge = (status: string) => {
    if (status === "expired") {
      return (
        <Badge variant="destructive" className="ml-2">
          Expired
        </Badge>
      );
    }

    return (
      <Badge variant="secondary" className="ml-2">
        Pending
      </Badge>
    );
  };

  const inviteDraftMatchesMode = inviteDraft?.mode === inviteMode;
  const emailDraftActive = inviteMode === "email" && inviteDraftMatchesMode;
  const githubDraftActive = inviteMode === "github" && inviteDraftMatchesMode;

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Team"
          description="Manage your team members and roles."
        />
        <AuditLogTrigger
          filters={{ resource_type: "team_member" }}
          members={members}
          title="Team activity"
        />
      {actionError && (
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {actionError}
        </div>
      )}

      {!canManageTeam && (
        <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
          Only admins can manage team roles, invitations, and removals.
        </div>
      )}

      {/* Members List */}
      <section className="space-y-3">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <h2 className="text-xs font-medium text-foreground">Members</h2>
          {canManageTeam && (
            <Button
              size="sm"
              className="w-full sm:w-auto"
              onClick={() => {
                resetInviteForm();
                setIsInviteDialogOpen(true);
              }}
            >
              Invite
            </Button>
          )}
        </div>
        <Card>
          <CardContent className="p-0">
            {membersLoading ? (
              <div className="p-6 text-center text-xs text-muted-foreground">
                Loading...
              </div>
            ) : members.length === 0 ? (
              <div className="p-6 text-center text-xs text-muted-foreground">
                No members found.
              </div>
            ) : (
              <div className="divide-y divide-border/50">
                <div className="hidden items-center gap-4 bg-muted/30 px-4 py-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground/70 md:grid md:grid-cols-[minmax(0,1.3fr)_minmax(0,1.3fr)_140px_100px]">
                  <div>Name</div>
                  <div>Email</div>
                  <div>Role</div>
                  <div>Actions</div>
                </div>
                {members.map((member) => {
                  const isSelf = currentUser?.id === member.id;
                  return (
                    <div
                      key={member.id}
                      className="grid gap-3 px-4 py-3 md:grid-cols-[minmax(0,1.3fr)_minmax(0,1.3fr)_140px_100px] md:items-center hover:bg-muted/40 dark:hover:bg-primary/[0.03] transition-colors"
                    >
                      <div className="flex items-center gap-3 min-w-0">
                        {member.avatar_url ? (
                          /* eslint-disable-next-line @next/next/no-img-element */
                          <img
                            src={member.avatar_url}
                            alt=""
                            className="h-8 w-8 rounded-full ring-1 ring-border/50"
                          />
                        ) : (
                          <div className="flex h-8 w-8 items-center justify-center rounded-full bg-muted/50 dark:bg-white/5 ring-1 ring-border/50 text-xs font-medium text-muted-foreground">
                            {member.name?.[0]?.toUpperCase() ?? "?"}
                          </div>
                        )}
                        <div className="min-w-0">
                          <div className="flex items-center gap-2">
                            <span className="text-xs font-medium truncate">
                              {member.name}
                            </span>
                            {isSelf && (
                              <span className="text-xs text-muted-foreground">
                                (you)
                              </span>
                            )}
                          </div>
                        </div>
                      </div>
                      <div className="min-w-0 space-y-1">
                        <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground md:hidden">
                          Email
                        </div>
                        <div className="text-xs text-muted-foreground truncate">
                          {member.email}
                        </div>
                      </div>
                      <div className="space-y-1">
                        <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground md:hidden">
                          Role
                        </div>
                        <div className="flex items-center">
                        {isSelf || !canManageTeam ? (
                          <Badge variant={roleBadgeVariant(member.role)}>
                            {roleLabel(member.role)}
                          </Badge>
                        ) : (
                          <Select
                            value={member.role}
                            onValueChange={(role) => {
                              if (role === member.role) return;
                              setPendingRoleChange({ member, newRole: role });
                            }}
                          >
                            <SelectTrigger
                              size="default"
                              className="h-9 w-full max-w-28"
                              aria-label={`Role for ${member.name}`}
                            >
                              <SelectValue>
                                {roleLabel(member.role)}
                              </SelectValue>
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="admin">Admin</SelectItem>
                              <SelectItem value="member">Engineer</SelectItem>
                              <SelectItem value="builder">Builder</SelectItem>
                              <SelectItem value="viewer">Viewer</SelectItem>
                            </SelectContent>
                            </Select>
                        )}
                        </div>
                      </div>
                      <div className="space-y-1">
                        <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground md:hidden">
                          Actions
                        </div>
                        <div className="flex items-center justify-start">
                        {canManageTeam && !isSelf ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="px-0 text-destructive hover:text-destructive"
                            disabled={removeMemberMutation.isPending}
                            onClick={() => setRemovingMember(member)}
                          >
                            Remove
                          </Button>
                        ) : isSelf ? (
                          <span className="text-xs text-muted-foreground">
                            Current user
                          </span>
                        ) : (
                          <span className="text-xs text-muted-foreground">
                            No access
                          </span>
                        )}
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </CardContent>
        </Card>
      </section>

      {/* Pending Invitations */}
      {canManageTeam && invitations.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-xs font-medium text-foreground">
            Pending invitations
          </h2>
          <Card>
            <CardContent className="p-0">
              <div className="divide-y divide-border">
                {invitations.map((inv) => (
                  <div
                    key={inv.id}
                    className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
                  >
                    <div className="min-w-0">
                      <div className="flex min-w-0 items-center">
                        <div className="truncate text-xs font-medium">
                          {inv.email
                            ? inv.email
                            : inv.github_username
                              ? `@${inv.github_username}`
                              : "Unknown invitee"}
                        </div>
                        {invitationStatusBadge(inv.status)}
                      </div>
                      <div className="text-xs text-muted-foreground">
                        {inv.github_username && inv.email && (
                          <span className="mr-1">@{inv.github_username} ·</span>
                        )}
                        Invited by {inv.invited_by.name} as{" "}
                        <Badge variant="outline" className="ml-0.5">
                          {roleLabel(inv.role)}
                        </Badge>
                      </div>
                      {inv.status === "expired" && (
                        <div className="mt-1 text-xs text-muted-foreground">
                          The invitee will not see this invite. Revoke it and send a new one.
                        </div>
                      )}
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="w-full text-destructive hover:text-destructive sm:ml-4 sm:w-auto"
                      disabled={revokeMutation.isPending}
                      onClick={() => revokeMutation.mutate(inv.id)}
                    >
                      Revoke
                    </Button>
                  </div>
                ))}
              </div>
            </CardContent>
          </Card>
        </section>
      )}

      {/* CLI install links (admin-only: creating one hands out membership) */}
      {canManageTeam && <CLIJoinTokensCard />}

      {/* Invite Member Dialog */}
      {canManageTeam && (
        <AlertDialog
          open={isInviteDialogOpen}
          onOpenChange={(open) => {
            setIsInviteDialogOpen(open);
            if (!open) {
              resetInviteForm();
            }
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Invite a member</AlertDialogTitle>
              <AlertDialogDescription>
                Invite by email or GitHub username and choose the teammate&apos;s initial role.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <form onSubmit={handleInvite} className="space-y-4">
              <Tabs
                value={inviteMode}
                onValueChange={(v) => {
                  const nextMode = v as "email" | "github";
                  setInviteMode(nextMode);
                  setInviteError("");
                  setInviteDraft(null);
                  setGithubNotificationEmail("");
                }}
              >
                <TabsList className="w-full">
                  <TabsTrigger value="email" className="flex-1">
                    Email
                  </TabsTrigger>
                  <TabsTrigger value="github" className="flex-1">
                    GitHub username
                  </TabsTrigger>
                </TabsList>
                <TabsContent value="email" className="mt-3">
                  <div className="space-y-3">
                    <div className="space-y-1.5">
                      <Label htmlFor="invite-email">Email</Label>
                      <div className="flex gap-2">
                        <Input
                          id="invite-email"
                          type="email"
                          placeholder="colleague@company.com"
                          value={inviteEmail}
                          disabled={emailDraftActive}
                          onChange={(e) => {
                            setInviteEmail(e.target.value);
                            setInviteError("");
                          }}
                          className="h-9"
                        />
                        <Button
                          type="button"
                          variant="secondary"
                          className="h-9 shrink-0"
                          disabled={emailDraftActive}
                          onClick={addEmailDraft}
                        >
                          Add email
                        </Button>
                      </div>
                      <p className="text-xs text-muted-foreground">
                        Add the email to the invite setup below before sending.
                      </p>
                    </div>
                  </div>
                </TabsContent>
                <TabsContent value="github" className="mt-3">
                  <div className="space-y-3">
                    <div className="space-y-1.5">
                      <Label htmlFor="invite-github">GitHub username</Label>
                      {githubConnected ? (
                        <>
                          <div className="rounded-md border border-input">
                            <Command shouldFilter={false}>
                              <CommandInput
                                id="invite-github"
                                placeholder="Search GitHub users..."
                                value={ghSearchQuery}
                                disabled={githubDraftActive}
                                onValueChange={(value) => {
                                  setGhSearchQuery(value);
                                  setInviteError("");
                                }}
                              />
                              {debouncedGhQuery.length > 0 && (
                                <CommandList className="max-h-48">
                                  {ghSearchLoading ? (
                                    <div className="py-3 text-center text-xs text-muted-foreground">
                                      Searching...
                                    </div>
                                  ) : ghSuggestions.length === 0 ? (
                                    <CommandEmpty>
                                      No users found. You can still invite{" "}
                                      <span className="font-mono">
                                        @{debouncedGhQuery}
                                      </span>
                                      .
                                    </CommandEmpty>
                                  ) : (
                                    <CommandGroup>
                                      {ghSuggestions.map((user) => (
                                        <CommandItem
                                          key={user.login}
                                          value={user.login}
                                          onSelect={() =>
                                            addGitHubDraft(
                                              user.login,
                                              user.avatar_url,
                                            )
                                          }
                                        >
                                          {user.avatar_url && (
                                            /* eslint-disable-next-line @next/next/no-img-element */
                                            <img
                                              src={user.avatar_url}
                                              alt=""
                                              className="h-5 w-5 rounded-full"
                                            />
                                          )}
                                          <span className="text-sm">
                                            @{user.login}
                                          </span>
                                        </CommandItem>
                                      ))}
                                    </CommandGroup>
                                  )}
                                </CommandList>
                              )}
                            </Command>
                          </div>
                          <div className="flex items-center justify-between gap-3">
                            <p className="text-xs text-muted-foreground">
                              Search GitHub and choose a result, or add the typed
                              username directly.
                            </p>
                            <Button
                              type="button"
                              variant="secondary"
                              className="h-9 shrink-0"
                              disabled={githubDraftActive}
                              onClick={() => addGitHubDraft(ghSearchQuery)}
                            >
                              Add GitHub username
                            </Button>
                          </div>
                        </>
                      ) : (
                        <>
                          <div className="flex gap-2">
                            <Input
                              id="invite-github"
                              placeholder="octocat"
                              value={ghSearchQuery}
                              disabled={githubDraftActive}
                              onChange={(e) => {
                                setGhSearchQuery(e.target.value);
                                setInviteError("");
                              }}
                              className="h-9"
                            />
                            <Button
                              type="button"
                              variant="secondary"
                              className="h-9 shrink-0"
                              disabled={githubDraftActive}
                              onClick={() => addGitHubDraft(ghSearchQuery)}
                            >
                              Add GitHub username
                            </Button>
                          </div>
                          <p className="text-xs text-muted-foreground">
                            Connect a GitHub App to search for users.
                          </p>
                        </>
                      )}
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="invite-github-notification-email">Notification email</Label>
                      <Input
                        id="invite-github-notification-email"
                        type="email"
                        placeholder="colleague@company.com"
                        value={githubNotificationEmail}
                        disabled={githubDraftActive}
                        onChange={(e) => {
                          setGithubNotificationEmail(e.target.value);
                          setInviteError("");
                        }}
                        className="h-9"
                      />
                      <p className="text-xs text-muted-foreground">
                        We&apos;ll send the invite link here, but acceptance still requires the matching GitHub account.
                      </p>
                    </div>
                  </div>
                </TabsContent>
              </Tabs>
              <div className="space-y-2 rounded-lg border border-dashed border-border bg-muted/20 p-4">
                <div className="flex items-center justify-between gap-2">
                  <div>
                    <p className="text-sm font-medium text-foreground">
                      Invite setup
                    </p>
                    <p className="text-xs text-muted-foreground">
                      Review the invitee below before sending the invite.
                    </p>
                  </div>
                  {inviteDraftMatchesMode && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="h-8"
                      onClick={clearInviteDraft}
                    >
                      Change
                    </Button>
                  )}
                </div>
                {inviteDraftMatchesMode ? (
                  <div className="flex items-center gap-3 rounded-lg border border-border bg-background px-3 py-3">
                    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
                      {inviteDraft.mode === "github" ? (
                        inviteDraft.avatarUrl ? (
                          /* eslint-disable-next-line @next/next/no-img-element */
                          <img
                            src={inviteDraft.avatarUrl}
                            alt=""
                            className="h-10 w-10 rounded-full"
                          />
                        ) : (
                          <Github className="h-4 w-4" />
                        )
                      ) : (
                        <Mail className="h-4 w-4" />
                      )}
                    </div>
                    <div className="min-w-0">
                      <p className="text-sm font-medium text-foreground">
                        {inviteDraftLabel}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {inviteDraft.mode === "github"
                          ? inviteDraft.notificationEmail
                            ? `Email notification will go to ${inviteDraft.notificationEmail}.`
                            : "GitHub invitee added to this invite."
                          : "Email invitee added to this invite."}
                      </p>
                    </div>
                  </div>
                ) : (
                  <div className="flex items-center gap-3 rounded-lg border border-border/60 bg-background/70 px-3 py-3 text-muted-foreground">
                    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-muted">
                      <UserPlus className="h-4 w-4" />
                    </div>
                    <div>
                      <p className="text-sm font-medium text-foreground">
                        No invitee added yet
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {inviteMode === "email"
                          ? "Add an email above to create the invite draft."
                          : "Choose a GitHub user above to create the invite draft."}
                      </p>
                    </div>
                  </div>
                )}
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="invite-role">Role</Label>
                <Select value={inviteRole} onValueChange={setInviteRole}>
                  <SelectTrigger id="invite-role" className="h-9 w-full">
                    <SelectValue>{roleLabel(inviteRole)}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="admin">Admin</SelectItem>
                    <SelectItem value="member">Engineer</SelectItem>
                    <SelectItem value="builder">Builder</SelectItem>
                    <SelectItem value="viewer">Viewer</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {inviteError && (
                <p className="text-xs text-destructive">{inviteError}</p>
              )}
              <AlertDialogFooter>
                <AlertDialogCancel type="button">Cancel</AlertDialogCancel>
                <Button
                  type="submit"
                  disabled={
                    inviteMutation.isPending ||
                    !inviteDraft ||
                    inviteDraft.mode !== inviteMode
                  }
                >
                  {inviteMutation.isPending ? "Sending..." : submitInviteLabel}
                </Button>
              </AlertDialogFooter>
            </form>
          </AlertDialogContent>
        </AlertDialog>
      )}

      {/* Change Role Confirmation Dialog */}
      <AlertDialog
        open={!!pendingRoleChange}
        onOpenChange={(open) => !open && setPendingRoleChange(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Change role</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingRoleChange?.member.id === currentUser?.id ? (
                <>
                  You&apos;re about to change your own role from{" "}
                  <span className="font-medium">
                    {roleLabel(pendingRoleChange?.member.role ?? "")}
                  </span>{" "}
                  to{" "}
                  <span className="font-medium">
                    {roleLabel(pendingRoleChange?.newRole ?? "")}
                  </span>
                  . You may lose access to admin features and won&apos;t be able to undo this
                  yourself.
                </>
              ) : (
                <>
                  Change {pendingRoleChange?.member.name}&apos;s role from{" "}
                  <span className="font-medium">
                    {roleLabel(pendingRoleChange?.member.role ?? "")}
                  </span>{" "}
                  to{" "}
                  <span className="font-medium">
                    {roleLabel(pendingRoleChange?.newRole ?? "")}
                  </span>
                  ? Their permissions will update immediately.
                </>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (pendingRoleChange) {
                  changeRoleMutation.mutate({
                    id: pendingRoleChange.member.id,
                    role: pendingRoleChange.newRole,
                  });
                  setPendingRoleChange(null);
                }
              }}
            >
              Confirm
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Remove Member Confirmation Dialog */}
      <AlertDialog open={!!removingMember} onOpenChange={(open) => !open && setRemovingMember(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove member</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to remove {removingMember?.name} ({removingMember?.email}) from the organization? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (removingMember) {
                  removeMemberMutation.mutate(removingMember.id);
                  setRemovingMember(null);
                }
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      </div>
    </PageContainer>
  );
}
