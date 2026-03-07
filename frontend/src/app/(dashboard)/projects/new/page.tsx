"use client";

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";

export default function NewProjectPage() {
  const router = useRouter();

  const [title, setTitle] = useState("");
  const [goal, setGoal] = useState("");
  const [scope, setScope] = useState("");
  const [completionCriteria, setCompletionCriteria] = useState("");
  const [repositoryId, setRepositoryId] = useState("");
  const [executionMode, setExecutionMode] = useState("sequential");
  const [maxConcurrent, setMaxConcurrent] = useState(2);
  const [priority, setPriority] = useState(50);
  const [baseBranch, setBaseBranch] = useState("main");

  const { data: reposData } = useQuery({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });

  const repos = reposData?.data ?? [];

  const createMutation = useMutation({
    mutationFn: () =>
      api.projects.create({
        title: title.trim(),
        goal: goal.trim(),
        repository_id: repositoryId,
        scope: scope.trim() || undefined,
        completion_criteria: completionCriteria.trim() || undefined,
        execution_mode: executionMode,
        max_concurrent: executionMode === "parallel" ? maxConcurrent : undefined,
        priority,
        base_branch: baseBranch.trim() || undefined,
      }),
    onSuccess: (response) => {
      router.push(`/projects/${response.data.id}`);
    },
  });

  const canSubmit = title.trim().length > 0 && goal.trim().length > 0 && repositoryId.length > 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title="New Project"
        description="Create a multi-task project for the PM agent to manage."
        action={
          <Button variant="outline" asChild>
            <Link href="/projects">
              <ArrowLeft className="mr-2 h-4 w-4" />
              Back to Projects
            </Link>
          </Button>
        }
      />

      <Card>
        <CardContent className="space-y-5 pt-6">
          <div className="space-y-2">
            <Label htmlFor="title">Title</Label>
            <Input
              id="title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Project title"
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="goal">Goal</Label>
            <Textarea
              id="goal"
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder="What should this project accomplish?"
              rows={3}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="scope">Scope (optional)</Label>
            <Textarea
              id="scope"
              value={scope}
              onChange={(e) => setScope(e.target.value)}
              placeholder="What files/areas are in scope?"
              rows={2}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="completion-criteria">Completion Criteria (optional)</Label>
            <Textarea
              id="completion-criteria"
              value={completionCriteria}
              onChange={(e) => setCompletionCriteria(e.target.value)}
              placeholder="How do we know the project is done?"
              rows={2}
            />
          </div>

          <div className="space-y-2">
            <Label>Repository</Label>
            <Select value={repositoryId} onValueChange={setRepositoryId}>
              <SelectTrigger>
                <SelectValue placeholder="Select a repository" />
              </SelectTrigger>
              <SelectContent>
                {repos.map((repo) => (
                  <SelectItem key={repo.id} value={repo.id}>
                    {repo.full_name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <Label>Execution Mode</Label>
            <RadioGroup value={executionMode} onValueChange={setExecutionMode} className="flex gap-4">
              <div className="flex items-center space-x-2">
                <RadioGroupItem value="sequential" id="exec-sequential" />
                <Label htmlFor="exec-sequential" className="font-normal">Sequential</Label>
              </div>
              <div className="flex items-center space-x-2">
                <RadioGroupItem value="parallel" id="exec-parallel" />
                <Label htmlFor="exec-parallel" className="font-normal">Parallel</Label>
              </div>
            </RadioGroup>
          </div>

          {executionMode === "parallel" && (
            <div className="space-y-2">
              <Label htmlFor="max-concurrent">Max Concurrent Tasks</Label>
              <Input
                id="max-concurrent"
                type="number"
                min={1}
                max={10}
                value={maxConcurrent}
                onChange={(e) => setMaxConcurrent(Number(e.target.value))}
              />
            </div>
          )}

          <div className="space-y-2">
            <Label htmlFor="priority">Priority (0-100)</Label>
            <Input
              id="priority"
              type="number"
              min={0}
              max={100}
              value={priority}
              onChange={(e) => setPriority(Number(e.target.value))}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="base-branch">Base Branch</Label>
            <Input
              id="base-branch"
              value={baseBranch}
              onChange={(e) => setBaseBranch(e.target.value)}
              placeholder="main"
            />
          </div>

          <div className="flex items-center gap-3 pt-2">
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!canSubmit || createMutation.isPending}
            >
              {createMutation.isPending ? "Creating..." : "Create Project"}
            </Button>
            {createMutation.isError && (
              <p className="text-xs text-destructive">Failed to create project. Please try again.</p>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
