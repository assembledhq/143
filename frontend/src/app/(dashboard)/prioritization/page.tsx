"use client";

import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Slider } from "@/components/ui/slider";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { X, Plus, Pencil, Trash2, FileText, Check, ExternalLink, Link as LinkIcon } from "lucide-react";
import type { Organization, OrgSettings, SingleResponse, Repository, ListResponse, RepoSettings, PMDocument } from "@/lib/types";
import { DEFAULT_PM_MODEL, PM_MODELS_BY_PROVIDER } from "@/lib/model-constants";

const DOC_TYPE_LABELS: Record<string, string> = {
  roadmap: "Roadmap",
  philosophy: "Product Philosophy",
  strategy: "Strategy",
  market: "Market Context",
  architecture: "Architecture",
  reference: "Reference",
};

const DOC_TYPE_COLORS: Record<string, string> = {
  roadmap: "bg-blue-500/10 text-blue-700 dark:text-blue-400",
  philosophy: "bg-purple-500/10 text-purple-700 dark:text-purple-400",
  strategy: "bg-amber-500/10 text-amber-700 dark:text-amber-400",
  market: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
  architecture: "bg-cyan-500/10 text-cyan-700 dark:text-cyan-400",
  reference: "bg-muted text-muted-foreground",
};

const SOURCE_TYPE_LABELS: Record<string, string> = {
  manual: "Manual",
  url: "URL",
  notion: "Notion",
  google_docs: "Google Docs",
  confluence: "Confluence",
  file_upload: "File Upload",
};

const DEFAULT_SETTINGS: Pick<
  Required<OrgSettings>,
  "pm_schedule_hours" | "pm_model" | "priority_weights" | "product_direction" | "product_context"
> = {
  pm_schedule_hours: 4,
  pm_model: DEFAULT_PM_MODEL,
  priority_weights: {
    customer_impact: 0.35,
    severity: 0.25,
    recency: 0.2,
    revenue_risk: 0.2,
  },
  product_direction: "",
  product_context: {
    philosophy: "",
    direction: "",
    focus_areas: [],
    avoid_areas: [],
  },
};

export default function PrioritizationPage() {
  const queryClient = useQueryClient();

  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const { data: agentDefaultsResponse } = useQuery({
    queryKey: ["agent-defaults"],
    queryFn: () => api.settings.getAgentDefaults(),
  });

  const { data: reposResponse } = useQuery<ListResponse<Repository>>({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });

  const reposWithCustomPM = (reposResponse?.data ?? []).filter((repo) => {
    const rs = (repo.settings ?? {}) as RepoSettings;
    return rs.pm != null;
  });

  const orgSettings = (settings?.data?.settings ?? {}) as OrgSettings;

  // Determine which providers are enabled (have an API key or are the default agent).
  const enabledPmModelGroups = useMemo(() => {
    const agentConfig = orgSettings.agent_config ?? {};
    const serverDefaults = agentDefaultsResponse?.data ?? {};
    const defaultAgent = orgSettings.default_agent_type || "codex";

    return Object.entries(PM_MODELS_BY_PROVIDER)
      .filter(([providerKey, { apiKeyVar }]) => {
        const orgKey = agentConfig[providerKey]?.[apiKeyVar];
        const serverKey = (serverDefaults[providerKey] ?? {})[apiKeyVar];
        return Boolean(orgKey) || Boolean(serverKey) || providerKey === defaultAgent;
      })
      .map(([, { label, models }]) => ({ label, models }));
  }, [orgSettings.agent_config, orgSettings.default_agent_type, agentDefaultsResponse?.data]);

  const [pmScheduleHours, setPmScheduleHours] = useState(String(DEFAULT_SETTINGS.pm_schedule_hours));
  const [pmModel, setPmModel] = useState(DEFAULT_SETTINGS.pm_model);
  const [productPhilosophy, setProductPhilosophy] = useState(DEFAULT_SETTINGS.product_context.philosophy);
  const [productDirection, setProductDirection] = useState(DEFAULT_SETTINGS.product_direction);
  const [focusAreas, setFocusAreas] = useState<string[]>(DEFAULT_SETTINGS.product_context.focus_areas ?? []);
  const [avoidAreas, setAvoidAreas] = useState<string[]>(DEFAULT_SETTINGS.product_context.avoid_areas ?? []);
  const [focusInput, setFocusInput] = useState("");
  const [avoidInput, setAvoidInput] = useState("");
  const [customerImpact, setCustomerImpact] = useState(String(DEFAULT_SETTINGS.priority_weights.customer_impact));
  const [severity, setSeverity] = useState(String(DEFAULT_SETTINGS.priority_weights.severity));
  const [recency, setRecency] = useState(String(DEFAULT_SETTINGS.priority_weights.recency));
  const [revenueRisk, setRevenueRisk] = useState(String(DEFAULT_SETTINGS.priority_weights.revenue_risk));
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");

  // Reference documents state
  const [showDocCreate, setShowDocCreate] = useState(false);
  const [docEditingId, setDocEditingId] = useState<string | null>(null);
  const [expandedDocId, setExpandedDocId] = useState<string | null>(null);
  const [docTitle, setDocTitle] = useState("");
  const [docContent, setDocContent] = useState("");
  const [docType, setDocType] = useState("roadmap");
  const [docSourceType, setDocSourceType] = useState("manual");
  const [docSourceUrl, setDocSourceUrl] = useState("");
  const [editDocTitle, setEditDocTitle] = useState("");
  const [editDocContent, setEditDocContent] = useState("");
  const [editDocType, setEditDocType] = useState("");
  const [editDocSourceUrl, setEditDocSourceUrl] = useState("");

  const { data: docsData } = useQuery<ListResponse<PMDocument>>({
    queryKey: ["pm", "documents"],
    queryFn: () => api.pm.listDocuments(),
  });
  const docs = docsData?.data ?? [];

  const createDocMutation = useMutation({
    mutationFn: (body: Parameters<typeof api.pm.createDocument>[0]) =>
      api.pm.createDocument(body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "documents"] });
      setShowDocCreate(false);
      setDocTitle("");
      setDocContent("");
      setDocType("roadmap");
      setDocSourceType("manual");
      setDocSourceUrl("");
    },
  });

  const updateDocMutation = useMutation({
    mutationFn: ({ id, body }: { id: string; body: Record<string, unknown> }) =>
      api.pm.updateDocument(id, body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "documents"] });
      setDocEditingId(null);
    },
  });

  const deleteDocMutation = useMutation({
    mutationFn: (id: string) => api.pm.deleteDocument(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "documents"] });
    },
  });

  // Sync server data into form state.
  const [prevSettingsRef, setPrevSettingsRef] = useState<unknown>(undefined);
  const settingsData = settings?.data?.settings;
  if (settingsData && settingsData !== prevSettingsRef) {
    setPrevSettingsRef(settingsData);
    const s = orgSettings;
    setPmScheduleHours(String(s.pm_schedule_hours ?? DEFAULT_SETTINGS.pm_schedule_hours));
    setPmModel(s.pm_model ?? DEFAULT_SETTINGS.pm_model);
    const productContext = s.product_context;
    setProductPhilosophy(productContext?.philosophy ?? DEFAULT_SETTINGS.product_context.philosophy);
    setProductDirection(
      productContext?.direction ??
        s.product_direction ??
        DEFAULT_SETTINGS.product_direction
    );
    setFocusAreas(productContext?.focus_areas ?? DEFAULT_SETTINGS.product_context.focus_areas ?? []);
    setAvoidAreas(productContext?.avoid_areas ?? DEFAULT_SETTINGS.product_context.avoid_areas ?? []);
    setCustomerImpact(String(s.priority_weights?.customer_impact ?? DEFAULT_SETTINGS.priority_weights.customer_impact));
    setSeverity(String(s.priority_weights?.severity ?? DEFAULT_SETTINGS.priority_weights.severity));
    setRecency(String(s.priority_weights?.recency ?? DEFAULT_SETTINGS.priority_weights.recency));
    setRevenueRisk(String(s.priority_weights?.revenue_risk ?? DEFAULT_SETTINGS.priority_weights.revenue_risk));
  }

  const mutation = useMutation({
    mutationFn: (data: Record<string, unknown>) => api.settings.update(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setSaveStatus("success");
      setTimeout(() => setSaveStatus("idle"), 2000);
    },
    onError: () => {
      setSaveStatus("error");
      setTimeout(() => setSaveStatus("idle"), 3000);
    },
  });

  const weightsSum =
    parseFloat(customerImpact || "0") +
    parseFloat(severity || "0") +
    parseFloat(recency || "0") +
    parseFloat(revenueRisk || "0");
  const weightsValid = Math.abs(weightsSum - 1.0) < 0.01;

  const addTag = (value: string, list: string[], setList: (v: string[]) => void) => {
    const trimmed = value.trim();
    if (!trimmed || list.includes(trimmed)) return;
    setList([...list, trimmed]);
  };

  const removeTag = (value: string, list: string[], setList: (v: string[]) => void) => {
    setList(list.filter((item) => item !== value));
  };

  function handleSave() {
    mutation.mutate({
      settings: {
        pm_schedule_hours: parseInt(pmScheduleHours, 10),
        pm_model: pmModel,
        priority_weights: {
          customer_impact: parseFloat(customerImpact),
          severity: parseFloat(severity),
          recency: parseFloat(recency),
          revenue_risk: parseFloat(revenueRisk),
        },
        product_direction: productDirection,
        product_context: {
          philosophy: productPhilosophy,
          direction: productDirection,
          focus_areas: focusAreas,
          avoid_areas: avoidAreas,
        },
      },
    });
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
      <PageHeader
        title="Prioritization"
        description="Define product context and how the PM agent prioritizes work."
      />

      {/* Org defaults notice */}
      <div className="rounded-md border border-border bg-muted/50 px-4 py-3">
        <p className="text-xs text-muted-foreground">
          These are <span className="font-medium text-foreground">organization defaults</span>.
          Individual repositories can override these settings from their repository settings page.
        </p>
        {reposWithCustomPM.length > 0 && (
          <div className="mt-2 flex flex-wrap gap-1.5">
            <span className="text-xs text-muted-foreground">Custom PM settings:</span>
            {reposWithCustomPM.map((repo) => (
              <Badge key={repo.id} variant="secondary" className="text-[11px]">
                {repo.full_name}
              </Badge>
            ))}
          </div>
        )}
      </div>

      {/* PM Agent */}
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">PM Agent</h2>
        <Card>
          <CardContent>
            <div className="grid gap-4 md:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="pm-schedule">Schedule (hours)</Label>
                <Input
                  id="pm-schedule"
                  type="number"
                  min={1}
                  max={24}
                  value={pmScheduleHours}
                  onChange={(e) => setPmScheduleHours(e.target.value)}
                  placeholder="4"
                />
                <p className="text-xs text-muted-foreground">
                  How often the PM agent runs automatically.
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="pm-model">PM Model</Label>
                <Select value={pmModel} onValueChange={setPmModel}>
                  <SelectTrigger id="pm-model" aria-label="PM Model">
                    <SelectValue placeholder="Select a model" />
                  </SelectTrigger>
                  <SelectContent>
                    {enabledPmModelGroups.length === 0 ? (
                      <SelectItem value={DEFAULT_PM_MODEL} disabled>
                        No providers configured
                      </SelectItem>
                    ) : (
                      enabledPmModelGroups.map((group) => (
                        <SelectGroup key={group.label}>
                          <SelectLabel>{group.label}</SelectLabel>
                          {group.models.map((model) => (
                            <SelectItem key={model} value={model}>
                              {model}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      ))
                    )}
                  </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground">
                  The LLM model used for PM planning.
                </p>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Product Context */}
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Product Context</h2>
        <Card>
          <CardContent>
            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="product-philosophy">Philosophy</Label>
                <Textarea
                  id="product-philosophy"
                  rows={4}
                  value={productPhilosophy}
                  onChange={(e) => setProductPhilosophy(e.target.value)}
                  placeholder="Describe how the PM should think about tradeoffs, risk, and fix style."
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="product-direction">Current Direction</Label>
                <Textarea
                  id="product-direction"
                  rows={3}
                  value={productDirection}
                  onChange={(e) => setProductDirection(e.target.value)}
                  placeholder="What is the team focused on this quarter?"
                />
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="focus-areas">Focus Areas</Label>
                  <Input
                    id="focus-areas"
                    value={focusInput}
                    onChange={(e) => setFocusInput(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === ",") {
                        e.preventDefault();
                        addTag(focusInput, focusAreas, setFocusAreas);
                        setFocusInput("");
                      }
                    }}
                    placeholder="Add focus area and press Enter"
                  />
                  <div className="flex flex-wrap gap-2">
                    {focusAreas.map((area) => (
                      <Badge key={area} variant="secondary" className="text-[11px]">
                        {area}
                        <Button
                          variant="ghost"
                          size="sm"
                          className="ml-1 h-4 w-4 p-0"
                          onClick={() => removeTag(area, focusAreas, setFocusAreas)}
                        >
                          <X className="h-3 w-3" />
                        </Button>
                      </Badge>
                    ))}
                  </div>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="avoid-areas">Avoid Areas</Label>
                  <Input
                    id="avoid-areas"
                    value={avoidInput}
                    onChange={(e) => setAvoidInput(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === ",") {
                        e.preventDefault();
                        addTag(avoidInput, avoidAreas, setAvoidAreas);
                        setAvoidInput("");
                      }
                    }}
                    placeholder="Add avoid area and press Enter"
                  />
                  <div className="flex flex-wrap gap-2">
                    {avoidAreas.map((area) => (
                      <Badge key={area} variant="secondary" className="text-[11px]">
                        {area}
                        <Button
                          variant="ghost"
                          size="sm"
                          className="ml-1 h-4 w-4 p-0"
                          onClick={() => removeTag(area, avoidAreas, setAvoidAreas)}
                        >
                          <X className="h-3 w-3" />
                        </Button>
                      </Badge>
                    ))}
                  </div>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Reference Documents */}
      <section className="space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-[13px] font-medium text-foreground">Reference Documents</h2>
            <p className="text-xs text-muted-foreground mt-0.5">
              Roadmaps, strategy docs, and other references the PM agent reads during planning.
            </p>
          </div>
          {!showDocCreate && (
            <Button variant="outline" size="sm" onClick={() => setShowDocCreate(true)}>
              <Plus className="h-3.5 w-3.5 mr-1" />
              Add
            </Button>
          )}
        </div>

        {showDocCreate && (
          <Card>
            <CardContent>
              <div className="space-y-4">
                <div className="grid grid-cols-3 gap-4">
                  <div className="space-y-2">
                    <Label htmlFor="doc-title">Title</Label>
                    <Input
                      id="doc-title"
                      placeholder="e.g. Q1 2026 Roadmap"
                      value={docTitle}
                      onChange={(e) => setDocTitle(e.target.value)}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="doc-type">Type</Label>
                    <Select value={docType} onValueChange={setDocType}>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {Object.entries(DOC_TYPE_LABELS).map(([value, label]) => (
                          <SelectItem key={value} value={value}>{label}</SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="doc-source">Source</Label>
                    <Select value={docSourceType} onValueChange={setDocSourceType}>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {Object.entries(SOURCE_TYPE_LABELS).map(([value, label]) => (
                          <SelectItem key={value} value={value}>{label}</SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                </div>
                {docSourceType !== "manual" && (
                  <div className="space-y-2">
                    <Label htmlFor="doc-source-url">Source URL</Label>
                    <Input
                      id="doc-source-url"
                      type="url"
                      placeholder="https://notion.so/... or link to original document"
                      value={docSourceUrl}
                      onChange={(e) => setDocSourceUrl(e.target.value)}
                    />
                    <p className="text-xs text-muted-foreground">
                      Link to the original document. Paste the content below — the PM agent uses the local copy.
                    </p>
                  </div>
                )}
                <div className="space-y-2">
                  <Label htmlFor="doc-content">Content</Label>
                  <Textarea
                    id="doc-content"
                    placeholder="Paste your document content here (Markdown supported)..."
                    rows={8}
                    value={docContent}
                    onChange={(e) => setDocContent(e.target.value)}
                  />
                </div>
                <div className="flex justify-end gap-2">
                  <Button variant="outline" size="sm" onClick={() => setShowDocCreate(false)}>
                    Cancel
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => {
                      if (!docTitle.trim()) return;
                      createDocMutation.mutate({
                        title: docTitle.trim(),
                        content: docContent,
                        doc_type: docType,
                        source_type: docSourceType,
                        source_url: docSourceUrl.trim() || undefined,
                      });
                    }}
                    disabled={!docTitle.trim() || createDocMutation.isPending}
                  >
                    {createDocMutation.isPending ? "Saving..." : "Save Document"}
                  </Button>
                </div>
              </div>
            </CardContent>
          </Card>
        )}

        {docs.length === 0 && !showDocCreate ? (
          <Card>
            <CardContent>
              <div className="flex flex-col items-center justify-center py-8 text-center">
                <FileText className="h-8 w-8 text-muted-foreground/40 mb-2" />
                <p className="text-xs text-muted-foreground">
                  No reference documents yet. Add roadmaps, strategy docs, or product philosophy to guide the PM agent.
                </p>
              </div>
            </CardContent>
          </Card>
        ) : (
          <div className="space-y-2">
            {docs.map((doc) => (
              <Card key={doc.id}>
                <CardContent className="py-3">
                  {docEditingId === doc.id ? (
                    <div className="space-y-3">
                      <div className="grid grid-cols-2 gap-3">
                        <div className="space-y-1.5">
                          <Label className="text-xs">Title</Label>
                          <Input value={editDocTitle} onChange={(e) => setEditDocTitle(e.target.value)} />
                        </div>
                        <div className="space-y-1.5">
                          <Label className="text-xs">Type</Label>
                          <Select value={editDocType} onValueChange={setEditDocType}>
                            <SelectTrigger><SelectValue /></SelectTrigger>
                            <SelectContent>
                              {Object.entries(DOC_TYPE_LABELS).map(([value, label]) => (
                                <SelectItem key={value} value={value}>{label}</SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                        </div>
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-xs">Source URL</Label>
                        <Input
                          type="url"
                          placeholder="https://... (optional)"
                          value={editDocSourceUrl}
                          onChange={(e) => setEditDocSourceUrl(e.target.value)}
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-xs">Content</Label>
                        <Textarea rows={8} value={editDocContent} onChange={(e) => setEditDocContent(e.target.value)} />
                      </div>
                      <div className="flex justify-end gap-2">
                        <Button variant="outline" size="sm" onClick={() => setDocEditingId(null)}>Cancel</Button>
                        <Button
                          size="sm"
                          onClick={() => {
                            if (!editDocTitle.trim()) return;
                            updateDocMutation.mutate({
                              id: doc.id,
                              body: { title: editDocTitle.trim(), content: editDocContent, doc_type: editDocType, source_url: editDocSourceUrl.trim() || null },
                            });
                          }}
                          disabled={!editDocTitle.trim() || updateDocMutation.isPending}
                        >
                          <Check className="h-3.5 w-3.5 mr-1" />
                          {updateDocMutation.isPending ? "Saving..." : "Save"}
                        </Button>
                      </div>
                    </div>
                  ) : (
                    <div>
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2 min-w-0">
                          <FileText className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                          <button
                            onClick={() => setExpandedDocId(expandedDocId === doc.id ? null : doc.id)}
                            className="text-[13px] font-medium hover:underline text-left truncate"
                          >
                            {doc.title}
                          </button>
                          <Badge variant="secondary" className={`text-[10px] ${DOC_TYPE_COLORS[doc.doc_type] ?? DOC_TYPE_COLORS.reference}`}>
                            {DOC_TYPE_LABELS[doc.doc_type] ?? doc.doc_type}
                          </Badge>
                          {doc.source_type !== "manual" && (
                            <Badge variant="outline" className="text-[10px] gap-0.5">
                              <LinkIcon className="h-2.5 w-2.5" />
                              {SOURCE_TYPE_LABELS[doc.source_type] ?? doc.source_type}
                            </Badge>
                          )}
                        </div>
                        <div className="flex items-center gap-0.5 shrink-0">
                          {doc.source_url && (
                            <Button variant="ghost" size="sm" className="h-7 w-7 p-0" asChild>
                              <a href={doc.source_url} target="_blank" rel="noopener noreferrer">
                                <ExternalLink className="h-3 w-3" />
                              </a>
                            </Button>
                          )}
                          <Button variant="ghost" size="sm" className="h-7 w-7 p-0" onClick={() => {
                            setDocEditingId(doc.id);
                            setEditDocTitle(doc.title);
                            setEditDocContent(doc.content);
                            setEditDocType(doc.doc_type);
                            setEditDocSourceUrl(doc.source_url ?? "");
                          }}>
                            <Pencil className="h-3 w-3" />
                          </Button>
                          <Button variant="ghost" size="sm" className="h-7 w-7 p-0" onClick={() => {
                            if (confirm("Delete this document?")) deleteDocMutation.mutate(doc.id);
                          }}>
                            <Trash2 className="h-3 w-3 text-destructive" />
                          </Button>
                        </div>
                      </div>
                      {expandedDocId === doc.id && (
                        <div className="mt-2 border-t pt-2">
                          {doc.source_url && (
                            <p className="text-[11px] text-muted-foreground mb-1.5">
                              Source: <a href={doc.source_url} target="_blank" rel="noopener noreferrer" className="text-primary hover:underline">{doc.source_url}</a>
                              {doc.last_synced_at && <span className="ml-1.5">(synced {new Date(doc.last_synced_at).toLocaleDateString()})</span>}
                            </p>
                          )}
                          <pre className="text-[11px] text-muted-foreground whitespace-pre-wrap font-mono leading-relaxed max-h-72 overflow-auto">
                            {doc.content || "(empty)"}
                          </pre>
                        </div>
                      )}
                      <p className="mt-0.5 text-[11px] text-muted-foreground ml-5.5">
                        Updated {new Date(doc.updated_at).toLocaleDateString()}
                        {doc.content && ` · ${doc.content.length.toLocaleString()} chars`}
                      </p>
                    </div>
                  )}
                </CardContent>
              </Card>
            ))}
          </div>
        )}
      </section>

      {/* Priority Weights */}
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Priority Weights</h2>
        <Card>
          <CardContent>
            <div className="space-y-4">
              <div className="flex items-center justify-between">
                <p className="text-xs text-muted-foreground">
                  Weights control how the PM agent scores and ranks issues.
                </p>
                <span className={`text-xs tabular-nums ${weightsValid ? "text-muted-foreground" : "text-destructive font-medium"}`}>
                  Sum: {weightsSum.toFixed(2)} / 1.00
                </span>
              </div>
              {!weightsValid && (
                <p className="text-xs text-destructive">
                  Weights must sum to 1.0
                </p>
              )}
              <div className="space-y-4">
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="w-customer" className="text-xs text-muted-foreground">Customer Impact</Label>
                    <span className="text-xs font-medium tabular-nums">{customerImpact}</span>
                  </div>
                  <Slider
                    id="w-customer"
                    min={0}
                    max={100}
                    step={5}
                    value={[Math.round(parseFloat(customerImpact) * 100)]}
                    onValueChange={([v]) => setCustomerImpact((v / 100).toFixed(2))}
                  />
                </div>
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="w-severity" className="text-xs text-muted-foreground">Severity</Label>
                    <span className="text-xs font-medium tabular-nums">{severity}</span>
                  </div>
                  <Slider
                    id="w-severity"
                    min={0}
                    max={100}
                    step={5}
                    value={[Math.round(parseFloat(severity) * 100)]}
                    onValueChange={([v]) => setSeverity((v / 100).toFixed(2))}
                  />
                </div>
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="w-recency" className="text-xs text-muted-foreground">Recency</Label>
                    <span className="text-xs font-medium tabular-nums">{recency}</span>
                  </div>
                  <Slider
                    id="w-recency"
                    min={0}
                    max={100}
                    step={5}
                    value={[Math.round(parseFloat(recency) * 100)]}
                    onValueChange={([v]) => setRecency((v / 100).toFixed(2))}
                  />
                </div>
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="w-revenue" className="text-xs text-muted-foreground">Revenue Risk</Label>
                    <span className="text-xs font-medium tabular-nums">{revenueRisk}</span>
                  </div>
                  <Slider
                    id="w-revenue"
                    min={0}
                    max={100}
                    step={5}
                    value={[Math.round(parseFloat(revenueRisk) * 100)]}
                    onValueChange={([v]) => setRevenueRisk((v / 100).toFixed(2))}
                  />
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      <div className="flex items-center gap-3">
        <Button onClick={handleSave} disabled={mutation.isPending || !weightsValid}>
          {mutation.isPending ? "Saving..." : "Save Settings"}
        </Button>
        {saveStatus === "success" && (
          <span className="text-[13px] text-emerald-600 dark:text-emerald-400">Settings saved.</span>
        )}
        {saveStatus === "error" && (
          <span className="text-[13px] text-destructive">Failed to save settings.</span>
        )}
      </div>
      </div>
    </PageContainer>
  );
}
