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
import { ErrorText } from "@/components/ui/error-notice";
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
import { VerifiedDomainsSection } from "@/components/settings/verified-domains-section";
import { CLIJoinTokensCard } from "@/components/cli-join-tokens-card";
import { useAuth } from "@/hooks/use-auth";
import { SettingsLastActivity } from "@/components/settings/settings-last-activity";
import { ExternalIdentitiesCard } from "@/components/settings/external-identities-card";
import { roleLabel } from "@/lib/roles";
import type {
  User,
  InvitationResponse,
  ListResponse,
  SingleResponse,
  GitHubInviteStatus,
  GitHubUserSuggestion,
} from "@/lib/types";

export default function TeamSettingsPage() {
  const queryClient = useQueryClient();
  const { user: currentUser } = useAuth();
  const canManageTeam = currentUser?.role === "admin";
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRole, setInviteRole] = useState("member");
  const [inviteMode, setInviteMode] = useState<"email" | "github">("github");
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
  const [selectedGitHubAvatarUrl, setSelectedGitHubAvatarUrl] = useState<string | undefined>();
  const [ghUserSelected, setGhUserSelected] = useState(false);

  const { data: membersData, isLoading: membersLoading } = useQuery<ListResponse<User>>({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
  });

  const { data: invitationsData } = useQuery<ListResponse<InvitationResponse>>({
    queryKey: ["team-invitations"],
    queryFn: () => api.team.listInvitations(),
    enabled: canManageTeam,
  });
  const { data: externalLinksData } = useQuery({
    queryKey: ["external-identities", "admin"],
    queryFn: () => api.integrations.listExternalUserLinks(),
    enabled: canManageTeam,
  });
  const identityFor = (memberID: string, provider: "slack" | "linear") =>
    externalLinksData?.data.find((link) => link.user_id === memberID && link.provider === provider && link.status === "active");

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
    debouncedGhQuery.length > 0 &&
    !ghUserSelected;

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
    setInviteMode("github");
    setInviteError("");
    setGhSearchQuery("");
    setDebouncedGhQuery("");
    setSelectedGitHubAvatarUrl(undefined);
    setGhUserSelected(false);
  };

  function handleInvite(e: React.FormEvent) {
    e.preventDefault();
    setInviteError("");
    const email = inviteEmail.trim();
    const githubUsername = ghSearchQuery.trim().replace(/^@/, "");

    if (inviteMode === "email") {
      if (!email) {
        setInviteError("Add an email address to the invite.");
        return;
      }

      inviteMutation.mutate({ email, role: inviteRole });
      return;
    }

    if (!githubUsername) {
      setInviteError("Add a GitHub username to the invite.");
      return;
    }

    inviteMutation.mutate({
      ...(email ? { email } : {}),
      github_username: githubUsername,
      acceptance_method: "github",
      role: inviteRole,
    });
  }

  const normalizedGitHubUsername = ghSearchQuery.trim().replace(/^@/, "");
  const trimmedInviteEmail = inviteEmail.trim();
  const inviteeLabel = inviteMode === "github"
    ? normalizedGitHubUsername
      ? `@${normalizedGitHubUsername}`
      : ""
    : trimmedInviteEmail;
  const submitInviteLabel = inviteeLabel
    ? `Send invite to ${inviteeLabel}`
    : "Send invite";
  const canSubmitInvite = inviteMode === "github"
    ? normalizedGitHubUsername.length > 0
    : trimmedInviteEmail.length > 0;

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

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Team"
          description="Manage your team members and roles."
        />
      {actionError && (
        <ErrorText className="rounded-md bg-destructive/10 px-3 py-2">
          {actionError}
        </ErrorText>
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
              className="min-h-11 w-full sm:min-h-0 sm:w-auto"
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
                <div className="hidden items-center gap-4 bg-muted/30 px-4 py-2 text-xs font-medium uppercase tracking-wider text-muted-foreground md:grid md:grid-cols-[minmax(0,1.2fr)_minmax(0,1.2fr)_110px_110px_110px_90px]">
                  <div>Name</div>
                  <div>Email</div>
                  <div>Slack</div>
                  <div>Linear</div>
                  <div>Role</div>
                  <div>Actions</div>
                </div>
                {members.map((member) => {
                  const isSelf = currentUser?.id === member.id;
                  return (
                    <div
                      key={member.id}
                      data-testid="team-member-row"
                      className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-2 px-3 py-3 transition-colors hover:bg-muted/40 md:grid-cols-[minmax(0,1.2fr)_minmax(0,1.2fr)_110px_110px_110px_90px] md:items-center md:gap-4 md:px-4 dark:hover:bg-primary/[0.03]"
                    >
                      <div className="col-span-2 flex min-w-0 items-center gap-3 md:col-span-1">
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
                      <div className="min-w-0 pl-11 md:pl-0">
                        <div className="text-xs text-muted-foreground truncate">
                          {member.email}
                        </div>
                      </div>
                      {(["slack", "linear"] as const).map((provider) => {
                        const identity = identityFor(member.id, provider);
                        return <div key={provider} className="hidden min-w-0 md:block"><Badge variant="outline" className="max-w-full truncate">{identity ? identity.external_handle || identity.external_display_name || "Linked" : "Unlinked"}</Badge></div>;
                      })}
                      <div className="flex items-center justify-self-end md:justify-self-auto">
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
                              className="h-11 w-full max-w-28 md:h-9"
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
                      <div className="col-span-2 flex min-h-8 items-center justify-end border-t border-border/60 pt-2 md:col-span-1 md:min-h-0 md:justify-start md:border-t-0 md:pt-0">
                        {canManageTeam && !isSelf ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="min-h-11 text-destructive hover:text-destructive md:min-h-0 md:px-0"
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
                  );
                })}
              </div>
            )}
          </CardContent>
        </Card>
      </section>

      {canManageTeam && <ExternalIdentitiesCard admin members={members} />}

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
                      className="min-h-11 w-full text-destructive hover:text-destructive sm:ml-4 sm:min-h-0 sm:w-auto"
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

      {/* Verified domains (domain capture / auto-join) */}
      {canManageTeam && <VerifiedDomainsSection />}

      {/* CLI install links (admin-only: creating one hands out membership) */}
      {canManageTeam && <CLIJoinTokensCard />}

      <SettingsLastActivity
        scopes={[
          { resource_type: "team_member" },
          { resource_type: "invitation" },
          { resource_type: "organization_domain" },
          { resource_type: "org_join_token" },
        ]}
        members={members}
        title="Team activity"
      />

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
          <AlertDialogContent className="max-h-[calc(100dvh-1rem)] overflow-y-auto p-4 sm:p-6">
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
                  setInviteEmail("");
                  setGhSearchQuery("");
                  setDebouncedGhQuery("");
                  setSelectedGitHubAvatarUrl(undefined);
                  setGhUserSelected(false);
                }}
              >
                <TabsList className="w-full">
                  <TabsTrigger value="github" className="flex-1">
                    GitHub username
                  </TabsTrigger>
                  <TabsTrigger value="email" className="flex-1">
                    Email
                  </TabsTrigger>
                </TabsList>
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
                                onValueChange={(value) => {
                                  setGhSearchQuery(value);
                                  setSelectedGitHubAvatarUrl(undefined);
                                  setGhUserSelected(false);
                                  setInviteError("");
                                }}
                              />
                              {debouncedGhQuery.length > 0 && !ghUserSelected && (
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
                                          onSelect={() => {
                                            setGhSearchQuery(user.login);
                                            setSelectedGitHubAvatarUrl(user.avatar_url);
                                            setGhUserSelected(true);
                                            setInviteError("");
                                          }}
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
                          <p className="text-xs text-muted-foreground">
                            Search GitHub and choose a result, or type a
                            username directly.
                          </p>
                        </>
                      ) : (
                        <>
                          <Input
                            id="invite-github"
                            placeholder="octocat"
                            value={ghSearchQuery}
                            onChange={(e) => {
                              setGhSearchQuery(e.target.value);
                              setSelectedGitHubAvatarUrl(undefined);
                              setInviteError("");
                            }}
                            className="h-11 sm:h-9"
                          />
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
                        value={inviteEmail}
                        onChange={(e) => {
                          setInviteEmail(e.target.value);
                          setInviteError("");
                        }}
                        className="h-11 sm:h-9"
                      />
                      <p className="text-xs text-muted-foreground">
                        We&apos;ll send the invite link here, but acceptance still requires the matching GitHub account.
                      </p>
                    </div>
                  </div>
                </TabsContent>
                <TabsContent value="email" className="mt-3">
                  <div className="space-y-3">
                    <div className="space-y-1.5">
                      <Label htmlFor="invite-email">Email</Label>
                      <Input
                        id="invite-email"
                        type="email"
                        placeholder="colleague@company.com"
                        value={inviteEmail}
                        onChange={(e) => {
                          setInviteEmail(e.target.value);
                          setInviteError("");
                        }}
                        className="h-11 sm:h-9"
                      />
                      <p className="text-xs text-muted-foreground">
                        The invitee will accept with this email address.
                      </p>
                    </div>
                  </div>
                </TabsContent>
              </Tabs>
              <div className="space-y-2 rounded-lg border border-dashed border-border bg-muted/20 p-4">
                <div>
                  <p className="text-sm font-medium text-foreground">
                    Invite setup
                  </p>
                  <p className="text-xs text-muted-foreground">
                    This updates as you edit the fields above.
                  </p>
                </div>
                {inviteeLabel ? (
                  <div className="flex items-center gap-3 rounded-lg border border-border bg-background px-3 py-3">
                    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
                      {inviteMode === "github" ? (
                        selectedGitHubAvatarUrl ? (
                          /* eslint-disable-next-line @next/next/no-img-element */
                          <img
                            src={selectedGitHubAvatarUrl}
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
                        {inviteeLabel}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {inviteMode === "github"
                          ? trimmedInviteEmail
                            ? `Email notification will go to ${trimmedInviteEmail}.`
                            : "Acceptance will require this GitHub account."
                          : "Acceptance will require this email address."}
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
                          ? "Enter an email above to send an invite."
                          : "Choose or type a GitHub username above to send an invite."}
                      </p>
                    </div>
                  </div>
                )}
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="invite-role">Role</Label>
                <Select value={inviteRole} onValueChange={setInviteRole}>
                  <SelectTrigger id="invite-role" className="h-11 w-full sm:h-9">
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
                <ErrorText>{inviteError}</ErrorText>
              )}
              <AlertDialogFooter>
                <AlertDialogCancel type="button">Cancel</AlertDialogCancel>
                <Button
                  type="submit"
                  className="min-h-11 sm:min-h-0"
                  disabled={
                    inviteMutation.isPending ||
                    !canSubmitInvite
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
              {removingMember?.captured_github_org_login ? (
                <span className="mt-2 block">
                  They&apos;re a member of {removingMember.captured_github_org_login} on GitHub and will rejoin on their next sign-in. Remove them from the GitHub organization too, or turn off auto-join.
                </span>
              ) : null}
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
