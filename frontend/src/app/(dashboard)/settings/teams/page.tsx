"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, Plus, Trash2, UserPlus } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Button } from "@/components/ui/button";
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
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import type { Team, TeamWithMembers, User, ListResponse, SingleResponse } from "@/lib/types";

export default function TeamsSettingsPage() {
  const queryClient = useQueryClient();
  const [newTeamName, setNewTeamName] = useState("");
  const [newTeamDesc, setNewTeamDesc] = useState("");
  const [selectedTeamId, setSelectedTeamId] = useState<string | null>(null);
  const [deleteTeamId, setDeleteTeamId] = useState<string | null>(null);
  const [addMemberUserId, setAddMemberUserId] = useState("");

  // Fetch all teams
  const { data: teamsData, isLoading: teamsLoading } = useQuery({
    queryKey: queryKeys.teams.all,
    queryFn: () => api.teams.list(),
  });

  // Fetch selected team details
  const { data: teamDetailData } = useQuery({
    queryKey: queryKeys.teams.detail(selectedTeamId ?? ""),
    queryFn: () => api.teams.get(selectedTeamId!),
    enabled: !!selectedTeamId,
  });

  // Fetch org members for the add-member dropdown
  const { data: membersData } = useQuery({
    queryKey: queryKeys.team.members,
    queryFn: () => api.team.listMembers() as Promise<ListResponse<User>>,
  });

  const teams = teamsData?.data ?? [];
  const teamDetail = teamDetailData?.data;
  const orgMembers = membersData?.data ?? [];

  const invalidateTeams = () => {
    queryClient.invalidateQueries({ queryKey: queryKeys.teams.all });
    queryClient.invalidateQueries({ queryKey: queryKeys.teams.mine });
    if (selectedTeamId) {
      queryClient.invalidateQueries({ queryKey: queryKeys.teams.detail(selectedTeamId) });
    }
  };

  const createMutation = useMutation({
    mutationFn: () => api.teams.create({ name: newTeamName, description: newTeamDesc || undefined }),
    onSuccess: (data: SingleResponse<Team>) => {
      toast.success(`Team "${data.data.name}" created`);
      setNewTeamName("");
      setNewTeamDesc("");
      invalidateTeams();
    },
    onError: () => toast.error("Failed to create team"),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.teams.del(id),
    onSuccess: () => {
      toast.success("Team deleted");
      if (selectedTeamId === deleteTeamId) setSelectedTeamId(null);
      setDeleteTeamId(null);
      invalidateTeams();
    },
    onError: () => toast.error("Failed to delete team"),
  });

  const syncGitHubMutation = useMutation({
    mutationFn: () => api.teams.syncGitHub(),
    onSuccess: () => {
      toast.success("Teams synced from GitHub");
      invalidateTeams();
    },
    onError: () => toast.error("Failed to sync teams from GitHub"),
  });

  const addMemberMutation = useMutation({
    mutationFn: ({ teamId, userId }: { teamId: string; userId: string }) =>
      api.teams.addMember(teamId, userId),
    onSuccess: () => {
      toast.success("Member added");
      setAddMemberUserId("");
      invalidateTeams();
    },
    onError: () => toast.error("Failed to add member"),
  });

  const removeMemberMutation = useMutation({
    mutationFn: ({ teamId, userId }: { teamId: string; userId: string }) =>
      api.teams.removeMember(teamId, userId),
    onSuccess: () => {
      toast.success("Member removed");
      invalidateTeams();
    },
    onError: () => toast.error("Failed to remove member"),
  });

  // Members not yet in the selected team
  const availableMembers = teamDetail
    ? orgMembers.filter((m) => !teamDetail.members.some((tm) => tm.id === m.id))
    : [];

  return (
    <PageContainer>
      <PageHeader
        title="Teams"
        description="Organize your team into groups. Teams can be synced from GitHub and used to filter sessions and projects."
        action={
          <Button
            size="sm"
            variant="outline"
            onClick={() => syncGitHubMutation.mutate()}
            disabled={syncGitHubMutation.isPending}
          >
            <RefreshCw className={`h-4 w-4 mr-1.5 ${syncGitHubMutation.isPending ? "animate-spin" : ""}`} />
            Sync from GitHub
          </Button>
        }
      />

      <div className="mt-6 grid gap-6 lg:grid-cols-[1fr_1.5fr]">
        {/* Left column: team list + create */}
        <div className="space-y-4">
          {/* Create team form */}
          <Card>
            <CardContent className="pt-6 space-y-3">
              <div>
                <Label htmlFor="team-name" className="text-xs">Team name</Label>
                <Input
                  id="team-name"
                  value={newTeamName}
                  onChange={(e) => setNewTeamName(e.target.value)}
                  placeholder="e.g. Frontend, Platform, Mobile"
                  className="mt-1"
                />
              </div>
              <div>
                <Label htmlFor="team-desc" className="text-xs">Description (optional)</Label>
                <Input
                  id="team-desc"
                  value={newTeamDesc}
                  onChange={(e) => setNewTeamDesc(e.target.value)}
                  placeholder="What does this team work on?"
                  className="mt-1"
                />
              </div>
              <Button
                size="sm"
                onClick={() => createMutation.mutate()}
                disabled={!newTeamName.trim() || createMutation.isPending}
              >
                <Plus className="h-4 w-4 mr-1" />
                Create team
              </Button>
            </CardContent>
          </Card>

          {/* Team list */}
          {teamsLoading && (
            <p className="text-xs text-muted-foreground px-1">Loading teams...</p>
          )}

          {teams.map((team) => (
            <Card
              key={team.id}
              className={`cursor-pointer transition-colors ${selectedTeamId === team.id ? "ring-2 ring-primary" : ""}`}
              onClick={() => setSelectedTeamId(team.id)}
            >
              <CardContent className="pt-4 pb-4">
                <div className="flex items-center justify-between">
                  <div>
                    <p className="text-sm font-medium">{team.name}</p>
                    <p className="text-xs text-muted-foreground">
                      {team.member_count} member{team.member_count !== 1 ? "s" : ""}
                      {team.github_team_slug && (
                        <span className="ml-2 text-muted-foreground/60">
                          (GitHub: {team.github_team_slug})
                        </span>
                      )}
                    </p>
                  </div>
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    className="text-muted-foreground hover:text-destructive"
                    onClick={(e) => {
                      e.stopPropagation();
                      setDeleteTeamId(team.id);
                    }}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}

          {!teamsLoading && teams.length === 0 && (
            <p className="text-xs text-muted-foreground px-1">
              No teams yet. Create one above or sync from GitHub.
            </p>
          )}
        </div>

        {/* Right column: team detail */}
        <div>
          {selectedTeamId && teamDetail ? (
            <Card>
              <CardContent className="pt-6 space-y-4">
                <div>
                  <h3 className="text-sm font-medium">{teamDetail.name}</h3>
                  {teamDetail.description && (
                    <p className="text-xs text-muted-foreground mt-1">{teamDetail.description}</p>
                  )}
                </div>

                {/* Add member */}
                <div className="flex items-end gap-2">
                  <div className="flex-1">
                    <Label className="text-xs">Add member</Label>
                    <Select value={addMemberUserId} onValueChange={setAddMemberUserId}>
                      <SelectTrigger className="mt-1" size="sm">
                        <SelectValue placeholder="Select a member" />
                      </SelectTrigger>
                      <SelectContent>
                        {availableMembers.map((m) => (
                          <SelectItem key={m.id} value={m.id}>
                            {m.name} ({m.email})
                          </SelectItem>
                        ))}
                        {availableMembers.length === 0 && (
                          <SelectItem value="__none__" disabled>
                            All org members are already in this team
                          </SelectItem>
                        )}
                      </SelectContent>
                    </Select>
                  </div>
                  <Button
                    size="sm"
                    disabled={!addMemberUserId || addMemberMutation.isPending}
                    onClick={() => {
                      if (addMemberUserId) {
                        addMemberMutation.mutate({ teamId: selectedTeamId, userId: addMemberUserId });
                      }
                    }}
                  >
                    <UserPlus className="h-4 w-4 mr-1" />
                    Add
                  </Button>
                </div>

                {/* Members list */}
                <div className="space-y-1">
                  <p className="text-xs font-medium text-muted-foreground">
                    Members ({teamDetail.members.length})
                  </p>
                  {teamDetail.members.map((member) => (
                    <div
                      key={member.id}
                      className="flex items-center justify-between py-2 px-2 rounded-md hover:bg-muted/50"
                    >
                      <div className="flex items-center gap-2">
                        {member.avatar_url ? (
                          <img
                            src={member.avatar_url}
                            alt=""
                            className="h-6 w-6 rounded-full"
                          />
                        ) : (
                          <div className="h-6 w-6 rounded-full bg-muted flex items-center justify-center text-xs font-medium">
                            {member.name?.[0] || "?"}
                          </div>
                        )}
                        <div>
                          <p className="text-xs font-medium">{member.name}</p>
                          <p className="text-xs text-muted-foreground">{member.email}</p>
                        </div>
                      </div>
                      <Button
                        size="icon-xs"
                        variant="ghost"
                        className="text-muted-foreground hover:text-destructive"
                        onClick={() =>
                          removeMemberMutation.mutate({ teamId: selectedTeamId, userId: member.id })
                        }
                      >
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    </div>
                  ))}
                  {teamDetail.members.length === 0 && (
                    <p className="text-xs text-muted-foreground py-2">
                      No members yet. Add members above.
                    </p>
                  )}
                </div>
              </CardContent>
            </Card>
          ) : (
            <Card>
              <CardContent className="py-12 text-center">
                <p className="text-xs text-muted-foreground">
                  Select a team to view and manage its members.
                </p>
              </CardContent>
            </Card>
          )}
        </div>
      </div>

      {/* Delete confirmation dialog */}
      <AlertDialog open={!!deleteTeamId} onOpenChange={() => setDeleteTeamId(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete team</AlertDialogTitle>
            <AlertDialogDescription>
              This will remove the team and unset team assignments on sessions and projects.
              Members will not be removed from the organization. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                if (deleteTeamId) deleteMutation.mutate(deleteTeamId);
              }}
            >
              Delete team
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </PageContainer>
  );
}
