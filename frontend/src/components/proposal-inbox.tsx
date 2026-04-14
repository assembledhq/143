"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Card, CardContent } from "@/components/ui/card";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { api } from "@/lib/api";
import type { Project, ProjectTask } from "@/lib/types";
import { AlertTriangle, CheckCircle2, XCircle, Lightbulb, ListTodo, FileText, Loader2, AlertCircle } from "lucide-react";

interface ProposalInboxProps {
  proposals: Project[];
  onNavigateToProject?: (id: string) => void;
}

export function ProposalInbox({ proposals, onNavigateToProject }: ProposalInboxProps) {
  const [selectedProposal, setSelectedProposal] = useState<Project | null>(null);
  const [dismissReason, setDismissReason] = useState("");
  const queryClient = useQueryClient();

  // Fetch full project detail (including tasks) when a proposal is selected
  const { data: detailData, isLoading: detailLoading } = useQuery({
    queryKey: ["projects", selectedProposal?.id],
    queryFn: () => api.projects.get(selectedProposal!.id),
    enabled: !!selectedProposal,
  });

  const tasks: ProjectTask[] = detailData?.data?.tasks ?? [];

  const approveMutation = useMutation({
    mutationFn: (id: string) => api.projects.approve(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      queryClient.invalidateQueries({ queryKey: ["proposalSummary"] });
      setSelectedProposal(null);
    },
    onError: (error: Error) => {
      console.error("Failed to approve proposal:", error.message);
    },
  });

  const dismissMutation = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason?: string }) =>
      api.projects.dismiss(id, reason),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      queryClient.invalidateQueries({ queryKey: ["proposalSummary"] });
      setSelectedProposal(null);
      setDismissReason("");
    },
    onError: (error: Error) => {
      console.error("Failed to dismiss proposal:", error.message);
    },
  });

  const isError = approveMutation.isError || dismissMutation.isError;

  if (proposals.length === 0) return null;

  return (
    <>
      <div className="space-y-3">
        <div className="flex items-center gap-2">
          <Lightbulb className="h-4 w-4 text-purple-500" />
          <h3 className="text-sm font-semibold">PM proposals ({proposals.length})</h3>
        </div>
        {isError && (
          <div className="flex items-center gap-2 text-xs text-red-600 dark:text-red-400 bg-red-50 dark:bg-red-950/30 px-3 py-2 rounded-md">
            <AlertCircle className="h-3.5 w-3.5 shrink-0" />
            <span>
              {approveMutation.isError
                ? `Failed to approve: ${approveMutation.error?.message || "Unknown error"}`
                : `Failed to dismiss: ${dismissMutation.error?.message || "Unknown error"}`}
            </span>
          </div>
        )}
        {proposals.map((proposal) => {
          const overlaps = proposal.similar_projects ?? [];
          return (
          <Card key={proposal.id} className="border-purple-200 dark:border-purple-800/50">
            <CardContent className="py-3 px-4">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1 space-y-1">
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-sm truncate">{proposal.title}</span>
                    <Badge variant="outline" className="text-xs shrink-0">
                      Priority {proposal.priority}
                    </Badge>
                  </div>
                  <div className="flex items-center gap-3 text-xs text-muted-foreground">
                    {proposal.total_tasks > 0 && (
                      <span className="flex items-center gap-1">
                        <ListTodo className="h-3 w-3" />
                        {proposal.total_tasks} seed {proposal.total_tasks === 1 ? "task" : "tasks"}
                      </span>
                    )}
                    {proposal.source_issue_ids && proposal.source_issue_ids.length > 0 && (
                      <span className="flex items-center gap-1">
                        <FileText className="h-3 w-3" />
                        {proposal.source_issue_ids.length} {proposal.source_issue_ids.length === 1 ? "issue" : "issues"}
                      </span>
                    )}
                  </div>
                  {overlaps.length > 0 && (
                    <div className="flex items-center gap-1 text-xs text-amber-600 dark:text-amber-400">
                      <AlertTriangle className="h-3 w-3" />
                      <span>
                        Similar to: {overlaps[0].title}
                        {overlaps.length > 1 && ` +${overlaps.length - 1} more`}
                      </span>
                    </div>
                  )}
                </div>
                <div className="flex items-center gap-1.5 shrink-0">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => { setSelectedProposal(proposal); setDismissReason(""); }}
                  >
                    View details
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => approveMutation.mutate(proposal.id)}
                    disabled={approveMutation.isPending}
                  >
                    <CheckCircle2 className="h-3.5 w-3.5 mr-1" />
                    Approve
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => dismissMutation.mutate({ id: proposal.id })}
                    disabled={dismissMutation.isPending}
                    className="text-muted-foreground"
                  >
                    <XCircle className="h-3.5 w-3.5 mr-1" />
                    Dismiss
                  </Button>
                </div>
              </div>
            </CardContent>
          </Card>
          );
        })}
      </div>

      <Sheet open={!!selectedProposal} onOpenChange={(open) => !open && setSelectedProposal(null)}>
        <SheetContent className="overflow-y-auto">
          {selectedProposal && (
            <>
              <SheetHeader>
                <SheetTitle>{selectedProposal.title}</SheetTitle>
                <SheetDescription className="sr-only">Details for proposal: {selectedProposal.title}</SheetDescription>
              </SheetHeader>
              <div className="mt-6 space-y-6">
                {/* Goal */}
                <div className="space-y-1.5">
                  <h4 className="text-sm font-medium">Goal</h4>
                  <p className="text-sm text-muted-foreground">{selectedProposal.goal}</p>
                </div>

                {/* Scope */}
                {selectedProposal.scope && (
                  <div className="space-y-1.5">
                    <h4 className="text-sm font-medium">Scope</h4>
                    <p className="text-sm text-muted-foreground">{selectedProposal.scope}</p>
                  </div>
                )}

                {/* Completion criteria */}
                {selectedProposal.completion_criteria && (
                  <div className="space-y-1.5">
                    <h4 className="text-sm font-medium">Completion criteria</h4>
                    <p className="text-sm text-muted-foreground">{selectedProposal.completion_criteria}</p>
                  </div>
                )}

                {/* Reasoning */}
                {selectedProposal.proposal_reasoning && (
                  <div className="space-y-1.5">
                    <h4 className="text-sm font-medium">Reasoning</h4>
                    <p className="text-sm text-muted-foreground whitespace-pre-wrap">{selectedProposal.proposal_reasoning}</p>
                  </div>
                )}

                <Separator />

                {/* Source issues */}
                {selectedProposal.source_issue_ids && selectedProposal.source_issue_ids.length > 0 && (
                  <div className="space-y-1.5">
                    <h4 className="text-sm font-medium">Motivating issues</h4>
                    <div className="space-y-1">
                      {selectedProposal.source_issue_ids.map((id) => (
                        <div key={id} className="text-xs font-mono text-muted-foreground bg-muted px-2 py-1 rounded">
                          {id}
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {/* Similar projects */}
                {(selectedProposal.similar_projects ?? []).length > 0 && (
                  <div className="space-y-2">
                    <h4 className="text-sm font-medium flex items-center gap-1.5">
                      <AlertTriangle className="h-3.5 w-3.5 text-amber-500" />
                      Similar projects
                    </h4>
                    {(selectedProposal.similar_projects ?? []).map((sp) => (
                      <div key={sp.project_id} className="border rounded-md p-3 space-y-1">
                        <div className="flex items-center justify-between">
                          <span className="text-sm font-medium">{sp.title}</span>
                          <Badge variant="outline" className="text-xs">
                            {Math.round(sp.overlap_score * 100)}% {sp.overlap_type}
                          </Badge>
                        </div>
                        <p className="text-xs text-muted-foreground">{sp.explanation}</p>
                      </div>
                    ))}
                  </div>
                )}

                <Separator />

                {/* Seed tasks */}
                <div className="space-y-2">
                  <h4 className="text-sm font-medium">Seed tasks</h4>
                  {detailLoading ? (
                    <div className="flex items-center gap-2 text-xs text-muted-foreground py-2">
                      <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      Loading tasks...
                    </div>
                  ) : tasks.length === 0 ? (
                    <p className="text-xs text-muted-foreground">No tasks yet.</p>
                  ) : (
                    <div className="space-y-2">
                      {tasks.map((task) => (
                        <div key={task.id} className="border rounded-md p-3 space-y-1">
                          <div className="flex items-center gap-2">
                            <span className="text-sm font-medium">{task.title}</span>
                            {task.complexity && (
                              <Badge variant="outline" className="text-xs">
                                {task.complexity}
                              </Badge>
                            )}
                            {task.confidence && (
                              <Badge variant="outline" className="text-xs">
                                {task.confidence} confidence
                              </Badge>
                            )}
                          </div>
                          {task.description && (
                            <p className="text-xs text-muted-foreground">{task.description}</p>
                          )}
                        </div>
                      ))}
                    </div>
                  )}
                </div>

                <Separator />

                {/* Dismiss with reason */}
                <div className="space-y-3">
                  <div className="flex gap-2">
                    <Button
                      className="flex-1"
                      onClick={() => approveMutation.mutate(selectedProposal.id)}
                      disabled={approveMutation.isPending}
                    >
                      <CheckCircle2 className="h-4 w-4 mr-1.5" />
                      Approve proposal
                    </Button>
                    <Button
                      variant="outline"
                      className="flex-1"
                      onClick={() => dismissMutation.mutate({ id: selectedProposal.id, reason: dismissReason || undefined })}
                      disabled={dismissMutation.isPending}
                    >
                      <XCircle className="h-4 w-4 mr-1.5" />
                      Dismiss
                    </Button>
                  </div>
                  <Input
                    id="dismiss-reason"
                    aria-label="Reason for dismissal"
                    value={dismissReason}
                    onChange={(e) => setDismissReason(e.target.value)}
                    placeholder="Reason for dismissal (optional)"
                  />
                  {approveMutation.isError && (
                    <p className="text-xs text-red-600 dark:text-red-400">
                      Failed to approve: {approveMutation.error?.message || "Unknown error"}. Please try again.
                    </p>
                  )}
                  {dismissMutation.isError && (
                    <p className="text-xs text-red-600 dark:text-red-400">
                      Failed to dismiss: {dismissMutation.error?.message || "Unknown error"}. Please try again.
                    </p>
                  )}
                </div>
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </>
  );
}
