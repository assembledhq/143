"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ErrorText } from "@/components/ui/error-notice";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Slider } from "@/components/ui/slider";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { ArrowLeft, Plus, Trash2 } from "lucide-react";
import type { EvalComplexity, GraderType, ListResponse, Repository, ScoringCriterion } from "@/lib/types";

interface CriterionForm {
  name: string;
  notes: string;
  grader_type: GraderType;
  command: string;
  timeout_seconds: number;
  model: string;
  weight: number;
  required: boolean;
}

function emptyCriterion(): CriterionForm {
  return {
    name: "",
    notes: "",
    grader_type: "code_check",
    command: "make test",
    timeout_seconds: 300,
    model: "claude-sonnet-4-6",
    weight: 1,
    required: false,
  };
}

function criterionFormToApi(c: CriterionForm): ScoringCriterion {
  return {
    name: c.name,
    notes: c.notes,
    grader_type: c.grader_type,
    grader_config:
      c.grader_type === "code_check"
        ? { command: c.command, timeout_seconds: c.timeout_seconds }
        : { model: c.model },
    weight: c.weight,
    required: c.required,
  };
}

type Step = 1 | 2 | 3 | 4;

export default function CreateEvalTaskPage() {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [step, setStep] = useState<Step>(1);

  // Step 1: Source
  const [repoId, setRepoId] = useState("");
  const [baseCommitSha, setBaseCommitSha] = useState("");
  const [solutionCommitSha, setSolutionCommitSha] = useState("");

  // Step 2: Problem
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [issueDescription, setIssueDescription] = useState("");
  const [complexity, setComplexity] = useState<EvalComplexity>("moderate");
  const [tags, setTags] = useState("");

  // Step 3: Scoring
  const [criteria, setCriteria] = useState<CriterionForm[]>([emptyCriterion()]);
  const [passThreshold, setPassThreshold] = useState(0.7);
  const [isNavigatingAfterCreate, setIsNavigatingAfterCreate] = useState(false);

  const { data: reposResponse } = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });
  const repos = reposResponse?.data ?? [];

  const createMutation = useMutation({
    mutationFn: () =>
      api.evals.createTask({
        repo_id: repoId,
        name,
        description,
        base_commit_sha: baseCommitSha,
        solution_commit_sha: solutionCommitSha || undefined,
        issue_description: issueDescription,
        scoring_criteria: criteria.map(criterionFormToApi),
        pass_threshold: passThreshold,
        source: "manual",
        complexity,
        tags: tags
          .split(",")
          .map((t) => t.trim())
          .filter(Boolean),
      }),
    onMutate: () => {
      setIsNavigatingAfterCreate(false);
    },
    onSuccess: (response) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.tasks() });
      setIsNavigatingAfterCreate(true);
      router.push(`/settings/evals/${response.data.id}`);
    },
    onError: () => {
      setIsNavigatingAfterCreate(false);
    },
  });
  const isCreatingEvalTask = createMutation.isPending || isNavigatingAfterCreate;

  const stepLabels = ["Source", "Problem", "Scoring", "Review"];

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <div>
          <Button variant="ghost" size="sm" className="mb-3 -ml-2 text-muted-foreground" asChild>
            <Link href="/settings/evals">
              <ArrowLeft className="mr-1 h-3.5 w-3.5" />
              Back to evals
            </Link>
          </Button>
          <PageHeader title="Create eval task" description="Pin a base commit and task to benchmark how well a coding agent handles real work from your repo." />
        </div>

        {/* Step indicator */}
        <div className="flex items-center gap-2">
          {stepLabels.map((label, idx) => {
            const stepNum = (idx + 1) as Step;
            const isActive = step === stepNum;
            const isCompleted = step > stepNum;
            return (
              <button
                key={label}
                className={`flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-medium transition-all duration-150 ${
                  isActive
                    ? "bg-primary text-primary-foreground"
                    : isCompleted
                    ? "bg-primary/10 text-primary"
                    : "bg-muted text-muted-foreground"
                }`}
                onClick={() => {
                  if (isCompleted) setStep(stepNum);
                }}
                disabled={!isCompleted && !isActive}
              >
                <span className="text-xs">{stepNum}</span>
                {label}
              </button>
            );
          })}
        </div>

        {/* Step 1: Source */}
        {step === 1 && (
          <Card>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label>Repository</Label>
                <Select value={repoId} onValueChange={setRepoId}>
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
                <Label htmlFor="base-commit">Base commit SHA</Label>
                <Input
                  id="base-commit"
                  placeholder="e.g. a1b2c3d"
                  value={baseCommitSha}
                  onChange={(e) => setBaseCommitSha(e.target.value)}
                  className="font-mono"
                />
                <p className="text-xs text-muted-foreground">
                  The checkout point before the change. The agent starts from this state.
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="solution-commit">Solution commit SHA (optional)</Label>
                <Input
                  id="solution-commit"
                  placeholder="e.g. d4e5f6a"
                  value={solutionCommitSha}
                  onChange={(e) => setSolutionCommitSha(e.target.value)}
                  className="font-mono"
                />
                <p className="text-xs text-muted-foreground">
                  The known-good merged result for comparison grading.
                </p>
              </div>
              <div className="flex justify-end">
                <Button onClick={() => setStep(2)} disabled={!repoId || !baseCommitSha}>
                  Next
                </Button>
              </div>
            </CardContent>
          </Card>
        )}

        {/* Step 2: Problem */}
        {step === 2 && (
          <Card>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="task-name">Name</Label>
                <Input
                  id="task-name"
                  placeholder="e.g. Auth token refresh regression"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="task-description">Description</Label>
                <Input
                  id="task-description"
                  placeholder="What this eval tests"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="issue-desc">Issue description</Label>
                <Textarea
                  id="issue-desc"
                  placeholder="What the agent should be told to fix/build..."
                  rows={5}
                  value={issueDescription}
                  onChange={(e) => setIssueDescription(e.target.value)}
                />
                <p className="text-xs text-muted-foreground">
                  This is the problem statement given to the agent.
                </p>
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label>Complexity</Label>
                  <Select value={complexity} onValueChange={(v) => setComplexity(v as EvalComplexity)}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="trivial">Trivial</SelectItem>
                      <SelectItem value="simple">Simple</SelectItem>
                      <SelectItem value="moderate">Moderate</SelectItem>
                      <SelectItem value="complex">Complex</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="tags">Tags (comma-separated)</Label>
                  <Input
                    id="tags"
                    placeholder="auth, regression, api"
                    value={tags}
                    onChange={(e) => setTags(e.target.value)}
                  />
                </div>
              </div>
              <div className="flex justify-between">
                <Button variant="outline" onClick={() => setStep(1)}>Back</Button>
                <Button onClick={() => setStep(3)} disabled={!name || !issueDescription}>
                  Next
                </Button>
              </div>
            </CardContent>
          </Card>
        )}

        {/* Step 3: Scoring */}
        {step === 3 && (
          <Card>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label>Pass threshold: {(passThreshold * 100).toFixed(0)}%</Label>
                <Slider
                  value={[passThreshold]}
                  onValueChange={([v]) => setPassThreshold(v)}
                  min={0}
                  max={1}
                  step={0.05}
                />
                <p className="text-xs text-muted-foreground">
                  Minimum weighted score to pass the eval.
                </p>
              </div>

              <div className="space-y-3">
                {criteria.map((criterion, idx) => (
                  <CriterionEditor
                    key={idx}
                    criterion={criterion}
                    index={idx}
                    onChange={(updated) => {
                      const next = [...criteria];
                      next[idx] = updated;
                      setCriteria(next);
                    }}
                    onRemove={criteria.length > 1 ? () => setCriteria(criteria.filter((_, i) => i !== idx)) : undefined}
                  />
                ))}
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setCriteria([...criteria, emptyCriterion()])}
                >
                  <Plus className="mr-1 h-3.5 w-3.5" />
                  Add criterion
                </Button>
              </div>

              <div className="flex justify-between">
                <Button variant="outline" onClick={() => setStep(2)}>Back</Button>
                <Button onClick={() => setStep(4)}>Next</Button>
              </div>
            </CardContent>
          </Card>
        )}

        {/* Step 4: Review */}
        {step === 4 && (
          <Card>
            <CardContent className="space-y-4">
              <h3 className="text-sm font-semibold">Review your eval task</h3>
              <div className="grid gap-3 md:grid-cols-2 text-xs">
                <div><span className="text-muted-foreground">Name:</span> {name}</div>
                <div><span className="text-muted-foreground">Complexity:</span> {complexity}</div>
                <div><span className="text-muted-foreground">Base commit:</span> <span className="font-mono">{baseCommitSha.slice(0, 8)}</span></div>
                <div><span className="text-muted-foreground">Pass threshold:</span> {(passThreshold * 100).toFixed(0)}%</div>
                <div className="md:col-span-2"><span className="text-muted-foreground">Criteria:</span> {criteria.length} ({criteria.filter(c => c.grader_type === "code_check").length} code checks, {criteria.filter(c => c.grader_type === "llm_judge").length} LLM judges)</div>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">Issue description</span>
                <p className="mt-1 text-xs whitespace-pre-wrap line-clamp-4">{issueDescription}</p>
              </div>
              {createMutation.isError && (
                <ErrorText className="rounded-md bg-destructive/10 px-3 py-2">
                  {createMutation.error instanceof Error
                    ? createMutation.error.message
                    : "Failed to create eval task. Please try again."}
                </ErrorText>
              )}
              <div className="flex justify-between">
                <Button variant="outline" onClick={() => setStep(3)}>Back</Button>
                <Button onClick={() => createMutation.mutate()} disabled={isCreatingEvalTask}>
                  {isCreatingEvalTask ? "Creating..." : "Create eval task"}
                </Button>
              </div>
            </CardContent>
          </Card>
        )}
      </div>
    </PageContainer>
  );
}

function CriterionEditor({
  criterion,
  index,
  onChange,
  onRemove,
}: {
  criterion: CriterionForm;
  index: number;
  onChange: (c: CriterionForm) => void;
  onRemove?: () => void;
}) {
  return (
    <Card>
      <CardContent className="space-y-3">
        <div className="flex items-center justify-between">
          <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            Criterion {index + 1}
          </span>
          {onRemove && (
            <Button variant="ghost" size="sm" className="text-destructive h-6 px-2" onClick={onRemove}>
              <Trash2 className="h-3 w-3" />
            </Button>
          )}
        </div>
        <div className="grid gap-3 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Name</Label>
            <Input
              placeholder="e.g. tests_pass"
              value={criterion.name}
              onChange={(e) => onChange({ ...criterion, name: e.target.value })}
            />
          </div>
          <div className="space-y-2">
            <Label>Type</Label>
            <Select value={criterion.grader_type} onValueChange={(v) => onChange({ ...criterion, grader_type: v as GraderType })}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="code_check">Code check</SelectItem>
                <SelectItem value="llm_judge">LLM judge</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
        <div className="space-y-2">
          <Label>Notes</Label>
          <Textarea
            placeholder="Describe what good looks like and what would fail..."
            rows={2}
            value={criterion.notes}
            onChange={(e) => onChange({ ...criterion, notes: e.target.value })}
          />
        </div>
        {criterion.grader_type === "code_check" ? (
          <div className="grid gap-3 md:grid-cols-2">
            <div className="space-y-2">
              <Label>Command</Label>
              <Input
                placeholder="make test"
                value={criterion.command}
                onChange={(e) => onChange({ ...criterion, command: e.target.value })}
                className="font-mono"
              />
            </div>
            <div className="space-y-2">
              <Label>Timeout (seconds)</Label>
              <Input
                type="number"
                value={criterion.timeout_seconds}
                onChange={(e) => onChange({ ...criterion, timeout_seconds: parseInt(e.target.value, 10) || 300 })}
              />
            </div>
          </div>
        ) : (
          <div className="space-y-2">
            <Label>Judge model</Label>
            <Select value={criterion.model} onValueChange={(v) => onChange({ ...criterion, model: v })}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="claude-opus-4-6">claude-opus-4-6</SelectItem>
                <SelectItem value="claude-sonnet-4-6">claude-sonnet-4-6</SelectItem>
              </SelectContent>
            </Select>
          </div>
        )}
        <div className="grid gap-3 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Weight: {criterion.weight}</Label>
            <Slider
              value={[criterion.weight]}
              onValueChange={([v]) => onChange({ ...criterion, weight: v })}
              min={0.1}
              max={5}
              step={0.1}
            />
          </div>
          <div className="flex items-center gap-2 pt-5">
            <Switch
              checked={criterion.required}
              onCheckedChange={(checked) => onChange({ ...criterion, required: checked })}
            />
            <Label className="cursor-pointer">Required</Label>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
