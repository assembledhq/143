"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
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
import { useAuth } from "@/hooks/use-auth";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
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
  const [inviteGitHubUsername, setInviteGitHubUsername] = useState("");
  const [inviteRole, setInviteRole] = useState("member");
  const [inviteMode, setInviteMode] = useState<"email" | "github">("email");
  const [inviteError, setInviteError] = useState("");
  const [actionError, setActionError] = useState("");
  const [isInviteDialogOpen, setIsInviteDialogOpen] = useState(false);
  const [removingMember, setRemovingMember] = useState<User | null>(null);
  const [ghSearchQuery, setGhSearchQuery] = useState("");
  const [debouncedGhQuery, setDebouncedGhQuery] = useState("");

  const { data: membersData, isLoading: membersLoading } = useQuery<ListResponse<User>>({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
  });

  const { data: invitationsData } = useQuery<ListResponse<InvitationResponse>>({
    queryKey: ["team-invitations"],
    queryFn: () => api.team.listInvitations(),
  });

  const { data: ghStatusData } = useQuery<SingleResponse<GitHubInviteStatus>>({
    queryKey: ["team-github-status"],
    queryFn: () => api.team.githubInviteStatus(),
    enabled: canManageTeam,
  });
  const githubConnected = ghStatusData?.data.connected ?? false;

  useEffect(() => {
    const handle = setTimeout(() => setDebouncedGhQuery(ghSearchQuery.trim()), 200);
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
    mutationFn: (body: { email?: string; github_username?: string; role: string }) =>
      api.team.createInvitation(body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["team-invitations"] });
      setInviteEmail("");
      setInviteGitHubUsername("");
      setGhSearchQuery("");
      setInviteRole("member");
      setInviteError("");
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

  function handleInvite(e: React.FormEvent) {
    e.preventDefault();
    setInviteError("");
    if (inviteMode === "email") {
      const email = inviteEmail.trim();
      if (!email) return;
      inviteMutation.mutate({ email, role: inviteRole });
    } else {
      const username = inviteGitHubUsername.trim().replace(/^@/, "");
      if (!username) {
        setInviteError("Enter a GitHub username.");
        return;
      }
      inviteMutation.mutate({ github_username: username, role: inviteRole });
    }
  }

  const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

  const roleBadgeVariant = (role: string) => {
    switch (role) {
      case "admin":
        return "default" as const;
      case "member":
        return "secondary" as const;
      default:
        return "outline" as const;
    }
  };

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
          Only admins can manage member roles, invitations, and removals.
        </div>
      )}

      {/* Members List */}
      <section className="space-y-3">
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-xs font-medium text-foreground">Members</h2>
          {canManageTeam && (
            <Button
              size="sm"
              onClick={() => {
                setInviteError("");
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
                      <div className="min-w-0 text-xs text-muted-foreground truncate">
                        {member.email}
                      </div>
                      <div className="flex items-center">
                        {isSelf || !canManageTeam ? (
                          <Badge variant={roleBadgeVariant(member.role)}>
                            {capitalize(member.role)}
                          </Badge>
                        ) : (
                          <Select
                            value={member.role}
                            onValueChange={(role) =>
                              changeRoleMutation.mutate({
                                id: member.id,
                                role,
                              })
                            }
                          >
                            <SelectTrigger
                              size="default"
                              className="h-9 w-full max-w-28"
                              aria-label={`Role for ${member.name}`}
                            >
                              <SelectValue>
                                {capitalize(member.role)}
                              </SelectValue>
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="admin">Admin</SelectItem>
                              <SelectItem value="member">Member</SelectItem>
                              <SelectItem value="viewer">Viewer</SelectItem>
                            </SelectContent>
                          </Select>
                        )}
                      </div>
                      <div className="flex items-center justify-start">
                        {canManageTeam && !isSelf ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-destructive hover:text-destructive"
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
                    className="flex items-center justify-between px-4 py-3"
                  >
                    <div className="min-w-0">
                      <div className="text-xs font-medium truncate">
                        {inv.email
                          ? inv.email
                          : inv.github_username
                            ? `@${inv.github_username}`
                            : "Unknown invitee"}
                      </div>
                      <div className="text-xs text-muted-foreground">
                        {inv.github_username && inv.email && (
                          <span className="mr-1">@{inv.github_username} ·</span>
                        )}
                        Invited by {inv.invited_by.name} as{" "}
                        <Badge variant="outline" className="ml-0.5">
                          {capitalize(inv.role)}
                        </Badge>
                      </div>
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="text-destructive hover:text-destructive ml-4"
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

      {/* Invite Member Dialog */}
      {canManageTeam && (
        <AlertDialog
          open={isInviteDialogOpen}
          onOpenChange={(open) => {
            setIsInviteDialogOpen(open);
            if (!open) {
              setInviteError("");
            }
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Invite a member</AlertDialogTitle>
              <AlertDialogDescription>
                Invite by email or GitHub username and choose the member&apos;s initial role.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <form onSubmit={handleInvite} className="space-y-4">
              <Tabs
                value={inviteMode}
                onValueChange={(v) => {
                  setInviteMode(v as "email" | "github");
                  setInviteError("");
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
                  <div className="space-y-1.5">
                    <Label htmlFor="invite-email">Email</Label>
                    <Input
                      id="invite-email"
                      type="email"
                      placeholder="colleague@company.com"
                      value={inviteEmail}
                      onChange={(e) => setInviteEmail(e.target.value)}
                      className="h-9"
                    />
                  </div>
                </TabsContent>
                <TabsContent value="github" className="mt-3">
                  <div className="space-y-1.5">
                    <Label htmlFor="invite-github">GitHub username</Label>
                    {githubConnected ? (
                      <div className="rounded-md border border-input">
                        <Command shouldFilter={false}>
                          <CommandInput
                            id="invite-github"
                            placeholder="Search GitHub users..."
                            value={ghSearchQuery}
                            onValueChange={(value) => {
                              setGhSearchQuery(value);
                              setInviteGitHubUsername(value.replace(/^@/, ""));
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
                                      onSelect={() => {
                                        setInviteGitHubUsername(user.login);
                                        setGhSearchQuery(user.login);
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
                    ) : (
                      <>
                        <Input
                          id="invite-github"
                          placeholder="octocat"
                          value={inviteGitHubUsername}
                          onChange={(e) =>
                            setInviteGitHubUsername(e.target.value)
                          }
                          className="h-9"
                        />
                        <p className="text-xs text-muted-foreground">
                          Connect a GitHub App to search for users.
                        </p>
                      </>
                    )}
                  </div>
                </TabsContent>
              </Tabs>
              <div className="space-y-1.5">
                <Label htmlFor="invite-role">Role</Label>
                <Select value={inviteRole} onValueChange={setInviteRole}>
                  <SelectTrigger id="invite-role" className="h-9 w-full">
                    <SelectValue>{capitalize(inviteRole)}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="admin">Admin</SelectItem>
                    <SelectItem value="member">Member</SelectItem>
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
                  className="h-9"
                  disabled={inviteMutation.isPending}
                >
                  {inviteMutation.isPending ? "Sending..." : "Send invite"}
                </Button>
              </AlertDialogFooter>
            </form>
          </AlertDialogContent>
        </AlertDialog>
      )}

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
