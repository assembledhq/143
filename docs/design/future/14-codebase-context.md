# Design: Codebase Context Layer

> **Status:** Not Started | **Last reviewed:** 2026-04-21

This document describes how 143.dev builds, maintains, and serves deep codebase context to coding agents. This is the single most important factor in agent success — an agent with rich context about a repo's architecture, conventions, and file ownership produces dramatically better fixes.

## Overview

143.dev introduces a first-class concept of a **Repository Context Package** — a structured body of knowledge about a codebase that gets built automatically, maintained incrementally, and injected into every agent run. The system actively helps teams build and maintain this context rather than requiring them to do it manually.

The context package has six components:

1. **Architecture Docs** — CLAUDE.md, AGENTS.md, directory-level guidance files
2. **Coding Conventions** — style rules extracted from past PRs and enforced patterns
3. **Feature-to-File Map** — which files own which features, so agents don't explore blindly
4. **Test Infrastructure** — how to run tests, what test patterns the team uses
5. **Dependency Map** — which services/packages talk to which, what's safe to change in isolation
6. **Context Quality Score** — a measurable metric for how well-documented a repo is for agent work

## Data Model

### `repo_context_packages`

One record per repository. Stores the full context package metadata and quality score.

```sql
CREATE TABLE repo_context_packages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id   uuid NOT NULL REFERENCES repositories(id) UNIQUE,
    org_id          uuid NOT NULL REFERENCES organizations(id),
    version         int NOT NULL DEFAULT 1,        -- incremented on each rebuild
    status          text NOT NULL DEFAULT 'building', -- building, ready, stale, error
    quality_score   float,                          -- 0-100, composite quality metric
    quality_details jsonb,                          -- breakdown per dimension
    file_coverage   float,                          -- % of files covered by context
    last_built_at   timestamptz,
    build_duration  interval,
    commit_sha      text,                           -- repo HEAD when context was built
    error           text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_repo_context_org ON repo_context_packages (org_id);
```

### `repo_context_entries`

Individual context entries within a package. Each entry is a piece of knowledge about the repo — an architecture doc, a convention rule, a file mapping, etc.

```sql
CREATE TABLE repo_context_entries (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id      uuid NOT NULL REFERENCES repo_context_packages(id) ON DELETE CASCADE,
    org_id          uuid NOT NULL REFERENCES organizations(id),
    entry_type      text NOT NULL,      -- 'architecture_doc', 'convention', 'file_map', 'test_config', 'dependency_map'
    scope           text,               -- file path or directory this entry applies to (null = repo-wide)
    title           text NOT NULL,      -- human-readable title
    content         text NOT NULL,      -- the actual context content (markdown)
    source          text NOT NULL,      -- 'discovered', 'generated', 'user_authored', 'review_pattern'
    confidence      float NOT NULL DEFAULT 0.5, -- how confident we are this is accurate
    last_validated  timestamptz,        -- when this was last verified against the codebase
    stale           boolean NOT NULL DEFAULT false,
    metadata        jsonb,              -- source-specific metadata
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_context_entries_package ON repo_context_entries (package_id, entry_type);
CREATE INDEX idx_context_entries_scope ON repo_context_entries (package_id, scope);
```

### `repo_file_map`

Maps files and directories to features and ownership. This is the "feature-to-file map" that prevents agents from exploring blindly.

```sql
CREATE TABLE repo_file_map (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id      uuid NOT NULL REFERENCES repo_context_packages(id) ON DELETE CASCADE,
    org_id          uuid NOT NULL REFERENCES organizations(id),
    file_path       text NOT NULL,
    feature         text,                -- which feature this file belongs to
    component       text,                -- architectural component (e.g., "api", "auth", "billing")
    description     text,                -- what this file does
    change_frequency float,              -- commits per month (from git log)
    last_modified   timestamptz,
    dependencies    text[],              -- files this file imports or depends on
    dependents      text[],              -- files that import or depend on this file
    test_files      text[],              -- associated test files
    test_coverage_pct float,             -- line coverage %, from test health system (doc 19). null = unknown
    owners          text[],              -- CODEOWNERS entries or inferred owners
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_file_map_path ON repo_file_map (package_id, file_path);
CREATE INDEX idx_file_map_feature ON repo_file_map (package_id, feature);
CREATE INDEX idx_file_map_component ON repo_file_map (package_id, component);
```

## Component 1: Architecture Docs

### Discovery

The system scans the repo for existing architecture documentation:

```go
var architectureFiles = []string{
    "CLAUDE.md",
    "AGENTS.md",
    ".claude/settings.json",
    ".github/CODEOWNERS",
    "README.md",
    "CONTRIBUTING.md",
    "ARCHITECTURE.md",
    "docs/architecture.md",
    "docs/ARCHITECTURE.md",
    "**/AGENTS.md",        // directory-level guidance
    "**/README.md",        // directory-level docs
}
```

For each discovered file, the system creates a `repo_context_entries` record with `entry_type = 'architecture_doc'` and `source = 'discovered'`.

### Auto-Generation

For repos without architecture docs, the system generates them by analyzing the codebase:

```go
func (b *ContextBuilder) GenerateArchitectureDocs(ctx context.Context, repo *models.Repository) error {
    // 1. Read directory structure (top 3 levels)
    tree := b.sandbox.Exec(ctx, "find", "/workspace", "-maxdepth", "3", "-type", "f")

    // 2. Read key files (package.json, go.mod, Cargo.toml, etc.) for stack detection
    stackFiles := b.detectStackFiles(tree)

    // 3. LLM generates architecture summary
    prompt := fmt.Sprintf(`Analyze this codebase structure and key configuration files.
Generate a concise architecture document covering:
- Tech stack and primary languages
- Project structure (what each top-level directory contains)
- Key entry points (main files, API routes, etc.)
- Build and run commands
- Important patterns or conventions visible from the structure

Directory tree:
%s

Configuration files:
%s`, tree, stackFiles)

    doc, err := b.llm.Generate(ctx, prompt)
    // Store as architecture_doc entry with source = 'generated'
}
```

### Directory-Level Context

For large repos, directory-level AGENTS.md files are critical. The system generates these for directories that agents frequently work in:

```go
func (b *ContextBuilder) GenerateDirectoryContext(ctx context.Context, dirPath string) (*ContextEntry, error) {
    // Read files in directory
    files := b.listFiles(ctx, dirPath)
    samples := b.readSampleFiles(ctx, dirPath, 5) // read up to 5 representative files

    prompt := fmt.Sprintf(`Analyze this directory and its files.
Generate a brief guide for an AI coding agent working in this directory:
- What this directory contains and its purpose
- Key patterns used (naming, structure, error handling)
- Important interfaces or types defined here
- Common pitfalls or things to be careful about

Directory: %s
Files: %s
Sample contents: %s`, dirPath, files, samples)

    return b.llm.Generate(ctx, prompt)
}
```

## Component 2: Coding Conventions

Coding conventions are extracted from two sources:

### Source 1: Codebase Analysis

The system analyzes the repo's existing code to infer conventions:

```go
func (b *ContextBuilder) ExtractConventions(ctx context.Context, repo *models.Repository) ([]Convention, error) {
    // 1. Detect linter configs (.eslintrc, .golangci.yml, .rubocop.yml, etc.)
    lintConfigs := b.detectLintConfigs(ctx)

    // 2. Detect formatter configs (.prettierrc, gofmt, rustfmt.toml, etc.)
    formatConfigs := b.detectFormatConfigs(ctx)

    // 3. Sample recent files to infer naming patterns, error handling, import style
    samples := b.sampleRecentFiles(ctx, 20)

    // 4. LLM extracts conventions
    prompt := fmt.Sprintf(`Analyze these code samples and configuration files from a single repository.
Extract the coding conventions being followed. Focus on:
- Naming conventions (variables, functions, files, packages)
- Error handling patterns
- Import/dependency organization
- Logging approach
- Test naming and structure
- Comment style
- Common patterns (dependency injection, factory pattern, etc.)

For each convention, output:
- rule: A clear instruction an AI agent should follow
- category: naming, error_handling, imports, logging, testing, comments, patterns
- confidence: How confident you are this is a deliberate convention (0-1)
- evidence: Brief evidence from the code

Linter configs: %s
Formatter configs: %s
Code samples: %s`, lintConfigs, formatConfigs, samples)

    return b.llm.ExtractConventions(ctx, prompt)
}
```

### Source 2: Review Patterns

Active review patterns from the review feedback loop (doc 11) are automatically included as conventions:

```go
func (b *ContextBuilder) ImportReviewPatterns(ctx context.Context, repo *models.Repository) error {
    patterns, _ := b.db.GetActiveReviewPatterns(ctx, repo.OrgID, repo.FullName)
    for _, pattern := range patterns {
        b.db.UpsertContextEntry(ctx, &models.ContextEntry{
            PackageID: repo.ContextPackageID,
            EntryType: "convention",
            Title:     fmt.Sprintf("Review pattern: %s", pattern.Category),
            Content:   pattern.Rule,
            Source:    "review_pattern",
            Confidence: pattern.Confidence,
            Metadata:  jsonb{"pattern_id": pattern.ID, "occurrence_count": pattern.OccurrenceCount},
        })
    }
    return nil
}
```

## Component 3: Feature-to-File Map

The feature-to-file map prevents agents from wasting tokens exploring the codebase to find relevant files. Instead, the system pre-maps files to features and components.

### Building the Map

```go
func (b *ContextBuilder) BuildFileMap(ctx context.Context, repo *models.Repository) error {
    // 1. List all source files (exclude vendor, node_modules, etc.)
    files := b.listSourceFiles(ctx)

    // 2. Parse CODEOWNERS for ownership info
    owners := b.parseCODEOWNERS(ctx)

    // 3. Analyze git log for change frequency
    changeFreq := b.analyzeGitLog(ctx, files)

    // 4. Analyze imports for dependency graph
    deps := b.analyzeImports(ctx, files)

    // 5. Find test file associations
    testMap := b.findTestFiles(ctx, files)

    // 6. Batch-classify files into features and components via LLM
    // Process in batches of 50 file paths to avoid token limits
    for batch := range chunk(files, 50) {
        prompt := fmt.Sprintf(`Given these file paths from a %s project, classify each into:
- feature: the product feature this file belongs to (e.g., "auth", "billing", "notifications")
- component: the architectural layer (e.g., "api", "service", "model", "view", "test", "config", "migration")
- description: one sentence describing what this file does

File paths:
%s

Existing components detected: %s`, repo.Language, formatPaths(batch), b.detectComponents(files))

        classifications := b.llm.ClassifyFiles(ctx, prompt)
        for _, c := range classifications {
            b.db.UpsertFileMap(ctx, &models.FileMapEntry{
                PackageID:       repo.ContextPackageID,
                FilePath:        c.Path,
                Feature:         c.Feature,
                Component:       c.Component,
                Description:     c.Description,
                ChangeFrequency: changeFreq[c.Path],
                Dependencies:    deps[c.Path].Imports,
                Dependents:      deps[c.Path].ImportedBy,
                TestFiles:       testMap[c.Path],
                Owners:          owners[c.Path],
            })
        }
    }
    return nil
}
```

### Using the Map in Agent Runs

When an agent run starts, the orchestrator queries the file map to provide targeted file context:

```go
func (o *Orchestrator) GetRelevantFiles(ctx context.Context, repo *models.Repository, issue *models.Issue) ([]string, error) {
    var files []string

    // 1. Files from stack traces (Sentry issues)
    if issue.Source == "sentry" {
        stackFiles := extractStackTraceFiles(issue.RawData)
        files = append(files, stackFiles...)
    }

    // 2. Files from the same feature as stack trace files
    for _, f := range files {
        entry, _ := o.db.GetFileMapEntry(ctx, repo.ContextPackageID, f)
        if entry != nil && entry.Feature != "" {
            featureFiles, _ := o.db.GetFilesByFeature(ctx, repo.ContextPackageID, entry.Feature)
            files = append(files, featureFiles...)
        }
    }

    // 3. Dependencies and dependents of affected files
    for _, f := range files {
        entry, _ := o.db.GetFileMapEntry(ctx, repo.ContextPackageID, f)
        if entry != nil {
            files = append(files, entry.Dependencies...)
            files = append(files, entry.Dependents...)
        }
    }

    // 4. Associated test files
    for _, f := range files {
        entry, _ := o.db.GetFileMapEntry(ctx, repo.ContextPackageID, f)
        if entry != nil {
            files = append(files, entry.TestFiles...)
        }
    }

    return dedupe(files), nil
}
```

## Component 4: Test Infrastructure

Understanding how to run tests is critical for agents to verify their fixes. The system discovers and documents the test setup.

```go
func (b *ContextBuilder) DiscoverTestInfra(ctx context.Context, repo *models.Repository) (*TestConfig, error) {
    // 1. Detect test framework from config files
    frameworks := b.detectTestFrameworks(ctx) // jest.config.js, pytest.ini, go test, etc.

    // 2. Detect CI configuration
    ciConfig := b.detectCIConfig(ctx) // .github/workflows/, .circleci/, Jenkinsfile

    // 3. Detect test commands from package.json scripts, Makefile targets, etc.
    testCommands := b.detectTestCommands(ctx)

    // 4. Analyze test file patterns
    testPatterns := b.analyzeTestPatterns(ctx) // *_test.go, *.test.ts, test_*.py

    // 5. Sample existing tests to understand patterns
    testSamples := b.sampleTestFiles(ctx, 5)

    // 6. LLM generates test infrastructure summary
    prompt := fmt.Sprintf(`Analyze this project's test infrastructure and generate a guide for an AI agent.
Cover:
- How to run all tests (exact commands)
- How to run a single test file
- How to run tests for a specific directory/package
- Test file naming conventions
- Test structure patterns (describe/it, TestXxx, etc.)
- Common test utilities or helpers used
- Fixtures or test data patterns
- Mocking/stubbing approaches used

Test frameworks: %s
CI config: %s
Test commands: %s
Sample tests: %s`, frameworks, ciConfig, testCommands, testSamples)

    return b.llm.GenerateTestConfig(ctx, prompt)
}
```

The test config is stored as a `repo_context_entries` record with `entry_type = 'test_config'` and injected into the agent prompt's system instructions.

### Coverage Data Integration

Per-file test coverage data can be stored on `repo_file_map.test_coverage_pct` and included in agent prompts when available. This lets agents know when they're working in a poorly-tested area.

## Component 5: Dependency Map

The dependency map tracks which services, packages, and modules interact, helping agents understand blast radius and safe change boundaries.

### Internal Dependencies (Import Graph)

Built from import/require/use statements:

```go
func (b *ContextBuilder) BuildImportGraph(ctx context.Context, repo *models.Repository) error {
    files := b.listSourceFiles(ctx)

    for _, file := range files {
        imports := b.parseImports(ctx, file, repo.Language)
        b.db.UpdateFileMapDependencies(ctx, repo.ContextPackageID, file, imports)
    }
    return nil
}
```

Language-specific import parsing:

| Language | Method |
|----------|--------|
| Go | Parse `import (...)` blocks |
| TypeScript/JavaScript | Parse `import` and `require()` |
| Python | Parse `import` and `from ... import` |
| Rust | Parse `use` and `mod` |
| Ruby | Parse `require` and `require_relative` |

### External Service Dependencies

Detected from configuration files, environment variables, and code patterns:

```go
func (b *ContextBuilder) DetectServiceDependencies(ctx context.Context) ([]ServiceDep, error) {
    // 1. Scan docker-compose.yml for service definitions
    // 2. Scan .env files for service URLs (DATABASE_URL, REDIS_URL, etc.)
    // 3. Scan code for HTTP client calls, database connections, queue producers/consumers
    // 4. Scan infrastructure configs (terraform, k8s manifests)

    prompt := `Given these configuration files and code patterns, identify:
- External services this project depends on (databases, caches, queues, APIs)
- How they're configured (env vars, config files)
- Which parts of the codebase interact with each service
- What's safe to change without affecting other services`

    return b.llm.DetectServiceDeps(ctx, prompt)
}
```

### Component Boundary Analysis

```go
func (b *ContextBuilder) AnalyzeComponentBoundaries(ctx context.Context) (*BoundaryAnalysis, error) {
    // Group files by component (from file map)
    components := b.db.GetComponentGroups(ctx, repo.ContextPackageID)

    // Analyze cross-component dependencies
    for _, comp := range components {
        for _, file := range comp.Files {
            entry, _ := b.db.GetFileMapEntry(ctx, repo.ContextPackageID, file)
            for _, dep := range entry.Dependencies {
                depEntry, _ := b.db.GetFileMapEntry(ctx, repo.ContextPackageID, dep)
                if depEntry.Component != comp.Name {
                    // Cross-component dependency detected
                    b.recordCrossComponentDep(comp.Name, depEntry.Component, file, dep)
                }
            }
        }
    }

    // Generate isolation assessment
    // "Component X can be modified in isolation" vs "Component Y has tight coupling to Z"
}
```

## Context Quality Score

The quality score measures how well-documented a repo is for agent work. It's a composite metric that helps teams understand where to invest in documentation.

### Scoring Algorithm

```go
type QualityDimension struct {
    Name    string
    Weight  float64
    Score   float64 // 0-100
    Details string
}

func (b *ContextBuilder) ComputeQualityScore(ctx context.Context, pkg *models.ContextPackage) (*QualityResult, error) {
    dimensions := []QualityDimension{
        {
            Name:   "architecture_docs",
            Weight: 0.20,
            Score:  b.scoreArchDocs(ctx, pkg),
            // Score based on: CLAUDE.md exists (20pts), AGENTS.md exists (20pts),
            // directory-level docs for >50% of top-level dirs (30pts),
            // CODEOWNERS exists (15pts), CONTRIBUTING.md exists (15pts)
        },
        {
            Name:   "file_coverage",
            Weight: 0.25,
            Score:  b.scoreFileCoverage(ctx, pkg),
            // Score based on: % of source files with descriptions in file map
            // 100% coverage = 100, <20% = 20, linear scale between
        },
        {
            Name:   "convention_coverage",
            Weight: 0.15,
            Score:  b.scoreConventions(ctx, pkg),
            // Score based on: linter config exists (30pts), formatter config exists (20pts),
            // >5 convention rules extracted (25pts), >3 active review patterns (25pts)
        },
        {
            Name:   "test_infrastructure",
            Weight: 0.10,
            Score:  b.scoreTestInfra(ctx, pkg),
            // Score based on: test framework detected (30pts), CI config exists (30pts),
            // test commands documented (20pts), test patterns analyzed (20pts)
        },
        {
            Name:   "test_coverage",
            Weight: 0.10,
            Score:  b.scoreTestCoverage(ctx, pkg),
            // Score based on: overall coverage > 60% (40pts), no critical files at 0% (30pts),
            // coverage trending up (30pts). Data from test health system (doc 19).
        },
        {
            Name:   "dependency_clarity",
            Weight: 0.10,
            Score:  b.scoreDependencies(ctx, pkg),
            // Score based on: import graph built (40pts), service deps documented (30pts),
            // component boundaries analyzed (30pts)
        },
        {
            Name:   "freshness",
            Weight: 0.10,
            Score:  b.scoreFreshness(ctx, pkg),
            // Score based on: days since last rebuild
            // <1 day = 100, 1-7 days = 80, 7-30 days = 50, >30 days = 20
        },
    }

    totalScore := 0.0
    for _, d := range dimensions {
        totalScore += d.Weight * d.Score
    }

    return &QualityResult{
        Score:      totalScore,
        Dimensions: dimensions,
        FileCoverage: b.computeFileCoverage(ctx, pkg),
    }, nil
}
```

### Quality Insights

The system generates actionable insights from the quality score:

```go
func (b *ContextBuilder) GenerateInsights(ctx context.Context, quality *QualityResult) []Insight {
    var insights []Insight

    if quality.FileCoverage < 0.5 {
        insights = append(insights, Insight{
            Severity: "warning",
            Message:  fmt.Sprintf("Only %.0f%% of files have context. Agents working on undocumented files are 3x more likely to fail.", quality.FileCoverage*100),
            Action:   "Run a context rebuild or add AGENTS.md files to undocumented directories.",
        })
    }

    if quality.Dimensions["architecture_docs"].Score < 40 {
        insights = append(insights, Insight{
            Severity: "warning",
            Message:  "No CLAUDE.md or AGENTS.md found. Agents lack architectural guidance.",
            Action:   "Add a CLAUDE.md to your repo root with architecture overview and coding guidelines.",
        })
    }

    if quality.Dimensions["test_infrastructure"].Score < 30 {
        insights = append(insights, Insight{
            Severity: "info",
            Message:  "Test infrastructure is undocumented. Agents may not know how to run or write tests.",
            Action:   "Ensure your test commands are in package.json/Makefile and CI config is present.",
        })
    }

    // Agent success correlation
    successRate := b.computeAgentSuccessRate(ctx)
    if successRate < 0.5 && quality.Score < 60 {
        insights = append(insights, Insight{
            Severity: "critical",
            Message:  fmt.Sprintf("Agent success rate is %.0f%% and context quality is %.0f/100. Improving context quality is the highest-leverage action.", successRate*100, quality.Score),
            Action:   "Focus on file coverage and architecture docs first.",
        })
    }

    return insights
}
```

## Context Build Pipeline

### Initial Build

Triggered when a repo is first connected (see doc 13, Repository Onboarding):

```
Repository connected
        │
        ▼
  Enqueue "build_repo_context" job
        │
        ▼
  Clone repo into temporary sandbox
        │
        ▼
  ┌──────────────────────────────────────┐
  │  Phase 1: Discovery (parallel)       │
  │  - Scan for architecture docs        │
  │  - Detect test frameworks            │
  │  - Parse CODEOWNERS                  │
  │  - Detect linter/formatter configs   │
  │  - Analyze git log (change freq)     │
  └──────────┬───────────────────────────┘
             │
             ▼
  ┌──────────────────────────────────────┐
  │  Phase 2: Analysis (sequential)      │
  │  - Build import graph                │
  │  - Extract coding conventions        │
  │  - Generate file map (batched LLM)   │
  │  - Detect service dependencies       │
  └──────────┬───────────────────────────┘
             │
             ▼
  ┌──────────────────────────────────────┐
  │  Phase 3: Generation (parallel)      │
  │  - Auto-generate missing arch docs   │
  │  - Generate directory-level context  │
  │  - Generate test infrastructure doc  │
  │  - Analyze component boundaries      │
  └──────────┬───────────────────────────┘
             │
             ▼
  ┌──────────────────────────────────────┐
  │  Phase 4: Scoring                    │
  │  - Compute quality score             │
  │  - Generate insights                 │
  │  - Store all entries                 │
  │  - Update repository.context_quality │
  └──────────────────────────────────────┘
```

### Incremental Updates

The context package is updated incrementally on push events, rather than rebuilt from scratch:

```go
func (h *WebhookHandler) HandlePush(ctx context.Context, event *github.PushEvent) error {
    repo, err := h.db.GetRepositoryByFullName(ctx, event.GetRepo().GetFullName())
    if err != nil {
        return err
    }

    // Identify which files changed
    changedFiles := extractChangedFiles(event)

    // Check if any architecture docs changed
    archDocsChanged := containsArchDocs(changedFiles)

    // Check if config files changed (package.json, go.mod, CI config, etc.)
    configChanged := containsConfigFiles(changedFiles)

    if archDocsChanged || configChanged {
        // Full rebuild — architecture or config changed
        h.jobs.Enqueue(ctx, "build_repo_context", map[string]interface{}{
            "repository_id": repo.ID,
            "full_rebuild":  true,
        })
    } else if len(changedFiles) > 0 {
        // Incremental update — just update file map entries for changed files
        h.jobs.Enqueue(ctx, "update_repo_context", map[string]interface{}{
            "repository_id": repo.ID,
            "changed_files": changedFiles,
        })
    }
    return nil
}
```

Incremental update job:

```go
func (b *ContextBuilder) IncrementalUpdate(ctx context.Context, repo *models.Repository, changedFiles []string) error {
    // 1. Re-analyze changed files for the file map
    for _, file := range changedFiles {
        if isDeleted(file) {
            b.db.DeleteFileMapEntry(ctx, repo.ContextPackageID, file)
            continue
        }
        // Re-classify this file
        b.classifySingleFile(ctx, repo, file)
        // Update its dependencies
        b.updateFileDependencies(ctx, repo, file)
    }

    // 2. Check if conventions might have changed
    if containsLintOrFormatConfig(changedFiles) {
        b.ExtractConventions(ctx, repo)
    }

    // 3. Mark any stale context entries
    b.markStaleEntries(ctx, repo, changedFiles)

    // 4. Recompute quality score
    b.recomputeQuality(ctx, repo)

    return nil
}
```

### Scheduled Refresh

A periodic job rebuilds context for repos that haven't been updated recently:

```go
// Runs daily via scheduler
func (b *ContextBuilder) RefreshStaleContexts(ctx context.Context) error {
    staleRepos, _ := b.db.GetReposWithStaleContext(ctx, 7*24*time.Hour) // stale after 7 days
    for _, repo := range staleRepos {
        b.jobs.Enqueue(ctx, "build_repo_context", map[string]interface{}{
            "repository_id": repo.ID,
            "full_rebuild":  true,
        })
    }
    return nil
}
```

## Injecting Context into Agent Runs

The context package is assembled into the agent prompt at run time. The orchestrator selects relevant context based on the issue and target files.

### Context Assembly

```go
func (o *Orchestrator) AssembleContext(ctx context.Context, repo *models.Repository, issue *models.Issue) (*AgentContext, error) {
    pkg, _ := o.db.GetContextPackage(ctx, repo.ID)
    if pkg == nil || pkg.Status != "ready" {
        // No context available — agent runs without it (with warning)
        return &AgentContext{Warning: "no context package available"}, nil
    }

    ac := &AgentContext{}

    // 1. Always include: architecture docs (repo-wide)
    archDocs, _ := o.db.GetContextEntries(ctx, pkg.ID, "architecture_doc", nil)
    ac.ArchitectureDocs = archDocs

    // 2. Always include: coding conventions
    conventions, _ := o.db.GetContextEntries(ctx, pkg.ID, "convention", nil)
    ac.Conventions = conventions

    // 3. Always include: test infrastructure
    testConfig, _ := o.db.GetContextEntries(ctx, pkg.ID, "test_config", nil)
    ac.TestConfig = testConfig

    // 4. Targeted: directory-level docs for relevant directories
    relevantFiles := o.GetRelevantFiles(ctx, repo, issue)
    relevantDirs := uniqueDirs(relevantFiles)
    for _, dir := range relevantDirs {
        dirDocs, _ := o.db.GetContextEntries(ctx, pkg.ID, "architecture_doc", &dir)
        ac.DirectoryDocs = append(ac.DirectoryDocs, dirDocs...)
    }

    // 5. Targeted: file map entries for relevant files
    for _, f := range relevantFiles {
        entry, _ := o.db.GetFileMapEntry(ctx, pkg.ID, f)
        if entry != nil {
            ac.FileMap = append(ac.FileMap, entry)
        }
    }

    // 6. Targeted: dependency context for affected components
    components := uniqueComponents(ac.FileMap)
    for _, comp := range components {
        deps, _ := o.db.GetCrossComponentDeps(ctx, pkg.ID, comp)
        ac.ComponentDeps = append(ac.ComponentDeps, deps...)
    }

    // 7. Include review patterns (from doc 11)
    patterns, _ := o.db.GetActiveReviewPatterns(ctx, repo.OrgID, repo.FullName)
    ac.ReviewPatterns = patterns

    return ac, nil
}
```

### Prompt Construction

The assembled context is formatted into the agent prompt:

```go
func (a *ClaudeCodeAdapter) PreparePrompt(ctx context.Context, input *AgentInput) (*AgentPrompt, error) {
    prompt := &AgentPrompt{}

    // System prompt: architecture + conventions + test infra
    var systemParts []string

    if len(input.Context.ArchitectureDocs) > 0 {
        systemParts = append(systemParts, "## Repository Architecture\n\n")
        for _, doc := range input.Context.ArchitectureDocs {
            systemParts = append(systemParts, fmt.Sprintf("### %s\n%s\n\n", doc.Title, doc.Content))
        }
    }

    if len(input.Context.Conventions) > 0 {
        systemParts = append(systemParts, "## Coding Conventions\n\nFollow these conventions:\n\n")
        for _, conv := range input.Context.Conventions {
            systemParts = append(systemParts, fmt.Sprintf("- %s\n", conv.Content))
        }
    }

    if len(input.Context.TestConfig) > 0 {
        systemParts = append(systemParts, "## Test Infrastructure\n\n")
        for _, tc := range input.Context.TestConfig {
            systemParts = append(systemParts, tc.Content+"\n\n")
        }
    }

    if len(input.Context.ReviewPatterns) > 0 {
        systemParts = append(systemParts, "## Review Patterns (from past human reviews)\n\n")
        for _, p := range input.Context.ReviewPatterns {
            systemParts = append(systemParts, fmt.Sprintf("- %s\n", p.Rule))
        }
    }

    prompt.SystemPrompt = strings.Join(systemParts, "")

    // User prompt: issue details + relevant file context
    var userParts []string

    userParts = append(userParts, fmt.Sprintf("## Issue\n\n%s\n\n%s\n", input.Issue.Title, input.Issue.Description))

    if len(input.Context.FileMap) > 0 {
        userParts = append(userParts, "## Relevant Files\n\nThese files are most likely relevant to this issue:\n\n")
        for _, f := range input.Context.FileMap {
            userParts = append(userParts, fmt.Sprintf("- `%s` — %s (feature: %s, component: %s)\n",
                f.FilePath, f.Description, f.Feature, f.Component))
            if len(f.TestFiles) > 0 {
                userParts = append(userParts, fmt.Sprintf("  Tests: %s\n", strings.Join(f.TestFiles, ", ")))
            }
        }
    }

    if len(input.Context.DirectoryDocs) > 0 {
        userParts = append(userParts, "\n## Directory Context\n\n")
        for _, doc := range input.Context.DirectoryDocs {
            userParts = append(userParts, fmt.Sprintf("### %s\n%s\n\n", doc.Scope, doc.Content))
        }
    }

    if len(input.Context.ComponentDeps) > 0 {
        userParts = append(userParts, "\n## Component Dependencies\n\n")
        userParts = append(userParts, "Be aware of these cross-component dependencies when making changes:\n\n")
        for _, dep := range input.Context.ComponentDeps {
            userParts = append(userParts, fmt.Sprintf("- %s depends on %s via %s\n",
                dep.FromComponent, dep.ToComponent, dep.Interface))
        }
    }

    prompt.UserPrompt = strings.Join(userParts, "")
    prompt.Files = extractFilePaths(input.Context.FileMap)

    return prompt, nil
}
```

## User-Authored Context

Teams can author their own context entries via the UI or by committing files to the repo.

### Via UI

The settings page includes a "Repository Context" section where admins can:

- View all auto-generated context entries
- Edit or override generated entries
- Add custom entries (architecture notes, gotchas, etc.)
- Promote/dismiss convention rules
- View quality score and improvement suggestions

### Via Repo Files

The system treats these files as authoritative (highest confidence):

| File | Treated As |
|------|-----------|
| `CLAUDE.md` (root) | Repo-wide architecture doc |
| `AGENTS.md` (root) | Repo-wide agent instructions |
| `**/AGENTS.md` (subdirs) | Directory-level agent instructions |
| `.github/CODEOWNERS` | File ownership |
| `.claude/settings.json` | Agent configuration |

User-authored entries (`source = 'user_authored'`) always override generated entries (`source = 'generated'`) for the same scope.

## API Endpoints

```
/api/v1/repositories/:id/
├── /context
│   ├── GET    /                    # get context package summary + quality score
│   ├── POST   /rebuild             # trigger full context rebuild
│   └── GET    /insights            # get quality insights and suggestions
│
├── /context/entries
│   ├── GET    /                    # list all context entries (filterable by type)
│   ├── POST   /                    # add custom entry
│   ├── PATCH  /:entry_id          # edit entry
│   └── DELETE /:entry_id          # delete entry
│
├── /context/file-map
│   ├── GET    /                    # get full file map
│   ├── GET    /features            # list features with file counts
│   ├── GET    /components          # list components with file counts
│   └── GET    /:file_path          # get file map entry for specific file
│
├── /context/conventions
│   ├── GET    /                    # list all conventions
│   ├── PATCH  /:id                 # edit or dismiss a convention
│   └── POST   /                    # add custom convention
```

## Job Queue

New job types:

| Job Type | Queue | Trigger | Description |
|----------|-------|---------|-------------|
| `build_repo_context` | `context` | Repo connected, manual rebuild, scheduled refresh | Full context build |
| `update_repo_context` | `context` | Push webhook | Incremental context update for changed files |
| `refresh_stale_contexts` | `context` | Scheduled (daily) | Find and rebuild stale contexts |

## Build Order

This feature is built alongside **Phase 1 (Foundation)** since it's needed before agents can run effectively:

1. **Database tables** — `repositories`, `repo_context_packages`, `repo_context_entries`, `repo_file_map`
2. **GitHub App setup** — manifest-based creation or manual setup flow
3. **OAuth flow** — user authentication with GitHub
4. **Repo connection** — installation webhook handling, repo discovery
5. **Discovery phase** — scan for existing docs, configs, CODEOWNERS
6. **Analysis phase** — import graph, conventions, file map
7. **Generation phase** — auto-generate missing docs, test infra summary
8. **Quality scoring** — compute and display quality metrics
9. **Incremental updates** — push webhook handler, stale detection
10. **Context injection** — integrate with agent orchestrator (doc 06)

## Connection with Other Design Docs

**Repository Onboarding (doc 13)**:
- Repo connection triggers the initial `build_repo_context` job
- Repository table stores `context_quality` score from this system

**Agent Orchestrator (doc 06)**:
- `AgentInput` gains a `Context *AgentContext` field
- `PreparePrompt` now includes context injection
- `GetRelevantFiles` uses the file map instead of blind exploration

**Review Feedback Loop (doc 11)**:
- Active review patterns are imported as convention entries
- Review patterns feed the context quality score

**Validation Pipeline (doc 07)**:
- Validation checks can use conventions to verify code quality compliance

**Prioritization (doc 05)**:
- Context quality score can factor into complexity estimation — issues in well-documented areas are estimated as easier

**Database Schema (doc 01)**:
- Three new tables: `repo_context_packages`, `repo_context_entries`, `repo_file_map`
- `repositories` table (from doc 13) gains `context_quality` column
