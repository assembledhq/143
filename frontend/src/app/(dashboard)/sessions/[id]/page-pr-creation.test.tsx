import { beforeEach, describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor, within } from '@/test/test-utils';
import { act } from '@testing-library/react';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers, mockPR } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Session, User, SingleResponse } from '@/lib/types';
import { installSessionDetailPageTestHooks, mockSessionDetailWithLazyDiff } from './session-detail-test-kit';

const { toast } = vi.hoisted(() => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));
const { routerPush } = vi.hoisted(() => ({
  routerPush: vi.fn(),
}));

vi.mock('@/lib/notify', () => ({
  notify: toast,
}));

vi.mock('@/components/markdown', () => ({
  MarkdownContent: ({ content, className }: { content: string; className?: string }) => (
    <div className={className}>{content}</div>
  ),
}));

vi.mock('@/components/session-keyboard-help-overlay', () => ({
  SessionKeyboardHelpOverlay: ({ open }: { open: boolean }) => (
    open ? <div role="dialog" aria-label="Session keyboard shortcuts" /> : null
  ),
}));

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: routerPush,
  }),
  useSearchParams: () => new URLSearchParams(),
}));

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

// Mock next/image to render a plain img
vi.mock('next/image', () => ({
  default: ({ src, alt, className, width, height }: { src: string; alt: string; className?: string; width?: number; height?: number }) => (
    <span data-next-image={src} aria-label={alt} className={className} data-width={width} data-height={height} />
  ),
}));

installSessionDetailPageTestHooks({ toast, routerPush });

function freshReadinessHandlers() {
  const latest = {
    id: 'readiness-run-1',
    org_id: 'org-1',
    session_id: 'session-abcdef12-3456-7890',
    status: 'passed',
    evaluated_snapshot_key: 'snap-abc',
    summary: 'Ready',
    review_packet: { checked_at: '2026-02-17T07:10:00Z', bypasses: [] },
    started_at: '2026-02-17T07:10:00Z',
    completed_at: '2026-02-17T07:10:01Z',
    created_at: '2026-02-17T07:10:00Z',
    updated_at: '2026-02-17T07:10:01Z',
    checks: [],
    bypasses: [],
  };
  return [
    http.get('/api/v1/sessions/:id/readiness', () => HttpResponse.json({ data: { latest } })),
    http.get('/api/v1/sessions/:id/pr-readiness-runs/latest', () => HttpResponse.json({ data: { latest } })),
  ];
}

describe('SessionDetailPage PR creation', () => {
  beforeEach(() => {
    server.use(...freshReadinessHandlers());
  });

  it('shows failure next steps and retry button', async () => {
    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: 'Something broke',
      failure_category: 'test_failure',
      failure_next_steps: ['Check logs', 'Retry with debug'],
      failure_retry_advised: true,
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getByText('test_failure')).toBeInTheDocument();
    expect(screen.getByText('Check logs')).toBeInTheDocument();
    expect(screen.getByText('Retry with debug')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Retry$/i })).toBeInTheDocument();
  });

  it('shows duration for completed session', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    // 5m 30s duration between started_at and completed_at (shown in timestamp row)
    expect(screen.getByText('5m 30s')).toBeInTheDocument();
  });

  it('keeps repository branch metadata separate so duration does not start with an orphan dot', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const repoBranchRow = screen.getByTestId('session-overview-repo-branch');
    expect(repoBranchRow).toHaveTextContent('assembledhq/143 · 143/feature-session-details');
    expect(repoBranchRow).not.toHaveTextContent(/feature-session-details\s*·/);

    const timingRow = screen.getByTestId('session-overview-timing');
    expect(timingRow).not.toHaveTextContent('assembledhq/143');
    expect(timingRow.textContent).toMatch(/^5m 30s/);
  });

  it('shows pass selector when session has diff_history with multiple passes', async () => {
    const pass1Diff = 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);';
    const pass2Diff = 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;';

    const sessionWithHistory: Session = {
      ...mockSessions[0],
      diff: pass2Diff,
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
      diff_history: [
        { pass: 1, diff: pass1Diff, diff_stats: { added: 1, removed: 0, files_changed: 1 }, created_at: '2026-03-19T10:00:00Z' },
        { pass: 2, diff: pass2Diff, diff_stats: { added: 2, removed: 0, files_changed: 2 }, created_at: '2026-03-19T10:05:00Z' },
      ],
      current_turn: 2,
    };

    mockSessionDetailWithLazyDiff(sessionWithHistory);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await user.click(changesTab);

    // Pass selector should be visible with "All changes" label
    expect(await screen.findByText('All changes')).toBeInTheDocument();
  });

  it('shows Create PR button for completed session with diff and no existing PR', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(await screen.findByRole('button', { name: /Create PR/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Create PR/ })).not.toBeDisabled();

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'More publish actions' }));

    expect(await screen.findByRole('menuitem', { name: /Create branch/ })).toHaveClass('text-xs');
  });

  it('shows a durable View branch link after branch-only publish succeeds', async () => {
    const sessionWithBranch: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      branch_creation_state: 'succeeded',
      branch_url: 'https://github.com/example/repo/tree/143/session-branch',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithBranch } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const viewBranchLink = await screen.findByRole('link', { name: 'View branch' });
    expect(viewBranchLink).toHaveAttribute('href', 'https://github.com/example/repo/tree/143/session-branch');
    expect(screen.getByRole('button', { name: /Create PR/ })).toBeInTheDocument();
  });

  it('shows Create PR button for completed session with snapshot even when diff stats are missing', async () => {
    const sessionWithSnapshotOnly: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff_stats: undefined,
      snapshot_key: 'snap-abc',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithSnapshotOnly } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(await screen.findByRole('button', { name: /Create PR/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Create PR/ })).not.toBeDisabled();
  });

  it('shows builder PR creation as review-gated and skips the team roster lookup', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };
    let teamRequestCount = 0;

    server.use(
      http.get('/api/v1/auth/me', () => {
        return HttpResponse.json({
          data: {
            ...mockMembers[0],
            role: 'builder',
          },
        } satisfies SingleResponse<User>);
      }),
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/sessions/:id/pr-readiness-runs/latest', () => {
        return HttpResponse.json({ data: {} });
      }),
      http.get('/api/v1/team/members', () => {
        teamRequestCount += 1;
        return HttpResponse.json({ error: { code: 'FORBIDDEN', message: 'insufficient permissions' } }, { status: 403 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    const createPRButton = await screen.findByRole('button', { name: /Create PR/ });
    expect(createPRButton).toBeDisabled();
    expect(createPRButton).toHaveAttribute(
      'title',
      expect.stringContaining('Run readiness checks successfully before creating a PR'),
    );
    expect(teamRequestCount).toBe(0);
  });

  it('allows builder PR creation when readiness policy disables builder enforcement', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };

    server.use(
      http.get('/api/v1/auth/me', () => {
        return HttpResponse.json({
          data: {
            ...mockMembers[0],
            role: 'builder',
          },
        } satisfies SingleResponse<User>);
      }),
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/sessions/:id/pr-readiness-runs/latest', () => {
        return HttpResponse.json({ data: {} });
      }),
      http.get('/api/v1/pr-readiness-policies', () => {
        return HttpResponse.json({
          data: {
            source: 'organization',
            config: {
              enabled_for_builders: false,
              checks: {
                freshness: { enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' } },
                agent_review_clean: { enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' } },
              },
              bypass: {
                enabled: true,
                allowed_roles: ['admin', 'member', 'builder'],
                scopes: ['completed_blocking_checks'],
              },
              auto_run: { after_session_completion: false, on_create_pr: false },
              sensitive_paths: [],
              large_diff_file_threshold: 25,
              large_diff_line_threshold: 500,
            },
            bypass_counts: { total: 0 },
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    await screen.findByRole('button', { name: /Create PR/ });
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /Create PR/ })).not.toBeDisabled();
    });
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /Create PR/ })).not.toHaveAttribute(
        'title',
        expect.stringContaining('Run readiness checks successfully before creating a PR'),
      );
    });
  });

  it('lets builders click Create PR to queue readiness when auto-run on Create PR is enabled', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };
    let createPRCalled = false;

    server.use(
      http.get('/api/v1/auth/me', () => {
        return HttpResponse.json({
          data: {
            ...mockMembers[0],
            role: 'builder',
          },
        } satisfies SingleResponse<User>);
      }),
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/sessions/:id/pr-readiness-runs/latest', () => {
        return HttpResponse.json({ data: {} });
      }),
      http.get('/api/v1/pr-readiness-policies', () => {
        return HttpResponse.json({
          data: {
            source: 'organization',
            config: {
              enabled_for_builders: true,
              checks: {
                freshness: { enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' } },
                agent_review_clean: { enforcement: { builder: 'blocking', engineer: 'advisory', admin: 'advisory' } },
              },
              bypass: {
                enabled: true,
                allowed_roles: ['admin', 'member', 'builder'],
                scopes: ['completed_blocking_checks'],
              },
              auto_run: { after_session_completion: false, on_create_pr: true },
              sensitive_paths: [],
              large_diff_file_threshold: 25,
              large_diff_line_threshold: 500,
            },
            bypass_counts: { total: 0 },
          },
        });
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        createPRCalled = true;
        return HttpResponse.json({ status: 'readiness_queued', readiness_run_id: 'readiness-run-1' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /Create PR/ })).not.toBeDisabled();
    });

    const user = userEvent.setup();
    const createPRButton = screen.getByRole('button', { name: /Create PR/ });
    await user.click(createPRButton);

    await waitFor(() => {
      expect(createPRCalled).toBe(true);
    });
    expect(toast.success).toHaveBeenCalledWith('Readiness checks queued');
  });

  it('does not render the issue-less readiness context editor in the review before PR card', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      linked_issues: [],
    };
    let contextSaveRequested = false;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/sessions/:id/pr-readiness-context', ({ params }) => {
        return HttpResponse.json({
          data: {
            org_id: 'org-1',
            session_id: params.id,
            issue_less_reason: 'Maintenance follow-up requested in Slack',
          },
        });
      }),
      http.post('/api/v1/sessions/:id/pr-readiness-context', () => {
        contextSaveRequested = true;
        return HttpResponse.json({
          data: {
            org_id: 'org-1',
            session_id: sessionWithDiff.id,
            issue_less_reason: 'Maintenance follow-up requested in Slack',
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('Review before PR')).toBeInTheDocument();
    expect(screen.queryByText('Issue-less context')).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue('Maintenance follow-up requested in Slack')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save context' })).not.toBeInTheDocument();

    expect(contextSaveRequested).toBe(false);
  });

  it('renders readiness check actions and expandable evidence', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/analytics/schema.json\n+++ b/analytics/schema.json\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };
    const latest = {
      id: 'readiness-run-1',
      org_id: 'org-1',
      session_id: sessionWithDiff.id,
      status: 'warnings',
      evaluated_workspace_revision: sessionWithDiff.workspace_revision,
      evaluated_snapshot_key: 'snap-abc',
      summary: 'Ready with warnings',
      review_packet: { checked_at: '2026-02-17T07:10:00Z', bypasses: [] },
      started_at: '2026-02-17T07:10:00Z',
      completed_at: '2026-02-17T07:10:01Z',
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:01Z',
      checks: [{
        id: 'check-risk',
        org_id: 'org-1',
        run_id: 'readiness-run-1',
        session_id: sessionWithDiff.id,
        check_key: 'risk_flags',
        check_type: 'risk_flags',
        status: 'warning',
        enforcement: 'advisory',
        effective_enforcement: 'advisory',
        title: 'Risk flags detected',
        summary: 'Sensitive paths changed.',
        details: { files: ['analytics/schema.json'] },
        action: 'View files',
        created_at: '2026-02-17T07:10:01Z',
      }],
      bypasses: [],
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>)),
      http.get('/api/v1/sessions/:id/pr', () => HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'pull request not found' } }, { status: 404 })),
      http.get('/api/v1/sessions/:id/pr-readiness-runs/latest', () => HttpResponse.json({ data: { latest } })),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('Risk flags detected')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Show evidence for Risk flags detected' }));
    expect(await screen.findByText(/analytics\/schema\.json/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'View files' }));
    expect(screen.getByRole('tab', { name: /^Changes/ })).toHaveAttribute('data-state', 'active');
  });

  it('shows stale readiness as a visible blocker in the card', async () => {
    const sessionWithNewRevision: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-new',
      workspace_revision: 4,
    };
    const latest = {
      id: 'readiness-run-1',
      org_id: 'org-1',
      session_id: sessionWithNewRevision.id,
      status: 'passed',
      evaluated_workspace_revision: 3,
      evaluated_snapshot_key: 'snap-old',
      summary: 'Ready',
      review_packet: { checked_at: '2026-02-17T07:10:00Z', bypasses: [] },
      started_at: '2026-02-17T07:10:00Z',
      completed_at: '2026-02-17T07:10:01Z',
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:01Z',
      checks: [{
        id: 'check-freshness',
        org_id: 'org-1',
        run_id: 'readiness-run-1',
        session_id: sessionWithNewRevision.id,
        check_key: 'freshness',
        check_type: 'freshness',
        status: 'passed',
        enforcement: 'blocking',
        effective_enforcement: 'blocking',
        title: 'Readiness is fresh',
        summary: 'Checked against the latest workspace revision.',
        action: 'Re-run',
        created_at: '2026-02-17T07:10:01Z',
      }],
      bypasses: [],
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => HttpResponse.json({ data: sessionWithNewRevision } satisfies SingleResponse<Session>)),
      http.get('/api/v1/sessions/:id/pr', () => HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'pull request not found' } }, { status: 404 })),
      http.get('/api/v1/sessions/:id/pr-readiness-runs/latest', () => HttpResponse.json({ data: { latest } })),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('Stale after latest changes')).toBeInTheDocument();
    expect(screen.getByText('Readiness is stale')).toBeInTheDocument();
  });

  it('creates a PR directly when readiness has advisory warnings', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };
    const latest = {
      id: 'readiness-run-1',
      org_id: 'org-1',
      session_id: sessionWithDiff.id,
      status: 'warnings',
      evaluated_snapshot_key: 'snap-abc',
      summary: 'Ready with warnings',
      review_packet: { checked_at: '2026-02-17T07:10:00Z', bypasses: [] },
      started_at: '2026-02-17T07:10:00Z',
      completed_at: '2026-02-17T07:10:01Z',
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:01Z',
      checks: [{
        id: 'check-tests',
        org_id: 'org-1',
        run_id: 'readiness-run-1',
        session_id: sessionWithDiff.id,
        check_key: 'test_evidence_present',
        check_type: 'test_evidence_present',
        status: 'warning',
        enforcement: 'advisory',
        effective_enforcement: 'advisory',
        title: 'No test evidence found',
        summary: 'No captured test output was found.',
        action: 'Run tests',
        created_at: '2026-02-17T07:10:01Z',
      }],
      bypasses: [],
    };

    let createPRCalled = false;

    server.use(
      http.get('/api/v1/sessions/:id', () => HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>)),
      http.get('/api/v1/sessions/:id/pr', () => HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'pull request not found' } }, { status: 404 })),
      http.get('/api/v1/sessions/:id/pr-readiness-runs/latest', () => HttpResponse.json({ data: { latest } })),
      http.post('/api/v1/sessions/:id/pr', () => {
        createPRCalled = true;
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    await waitFor(() => {
      expect(createPRCalled).toBe(true);
    });
    expect(screen.queryByRole('alertdialog', { name: 'Review readiness before creating PR?' })).not.toBeInTheDocument();
  });

  it('shows exact readiness blockers in the bypass dialog', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };
    const latest = {
      id: 'readiness-run-1',
      org_id: 'org-1',
      session_id: sessionWithDiff.id,
      status: 'blocked',
      evaluated_snapshot_key: 'snap-abc',
      summary: 'Blocked',
      review_packet: { checked_at: '2026-02-17T07:10:00Z', bypasses: [] },
      started_at: '2026-02-17T07:10:00Z',
      completed_at: '2026-02-17T07:10:01Z',
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:01Z',
      checks: [{
        id: 'check-review',
        org_id: 'org-1',
        run_id: 'readiness-run-1',
        session_id: sessionWithDiff.id,
        check_key: 'agent_review_clean',
        check_type: 'agent_review_clean',
        status: 'failed',
        enforcement: 'blocking',
        effective_enforcement: 'blocking',
        title: 'Agent review not clean',
        summary: 'Run Review must complete cleanly.',
        action: 'Fix with agent',
        created_at: '2026-02-17T07:10:01Z',
      }],
      bypasses: [],
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>)),
      http.get('/api/v1/sessions/:id/pr', () => HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'pull request not found' } }, { status: 404 })),
      http.get('/api/v1/sessions/:id/pr-readiness-runs/latest', () => HttpResponse.json({ data: { latest } })),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: 'Bypass blockers' }));

    const dialog = await screen.findByRole('dialog', { name: 'Bypass readiness blockers' });
    expect(within(dialog).getByText('Agent review not clean')).toBeInTheDocument();
    expect(within(dialog).getByText('Run Review must complete cleanly.')).toBeInTheDocument();
  });

  it('does not show Create PR button when PR already exists', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      // Default handler already returns mockPR for GET /sessions/:id/pr
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    // Wait for the PR query to resolve before asserting
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
    });
  });

  it('shows Push changes only when the session has unpushed changes', async () => {
    const sessionWithUnpushedChanges: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      has_unpushed_changes: true,
      pr_push_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithUnpushedChanges } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(await screen.findByRole('button', { name: 'Push changes' })).toBeInTheDocument();
  });

  it('hides Push changes when the PR already matches the latest session head', async () => {
    const sessionWithoutUnpushedChanges: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      has_unpushed_changes: false,
      pr_push_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithoutUnpushedChanges } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Push changes' })).not.toBeInTheDocument();
    });
  });

  it('does not show a PR snapshot error when the PR already exists', async () => {
    const sessionWithStalePRError: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: undefined,
      pr_creation_state: 'failed',
      pr_creation_error: 'session state expired — re-run to create a PR',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithStalePRError } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByRole('link', { name: /View PR/ })).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('does not show Create PR button when session has no diff', async () => {
    // Default mockSessions[0] has no diff_stats
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
  });

  it('shows checkpoint-missing notice when diff exists but no reusable snapshot was saved', async () => {
    const sessionWithMissingSnapshot: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'none',
      snapshot_key: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithMissingSnapshot } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('No reusable checkpoint saved');
    expect(alert).toHaveTextContent('This session finished without saving a reusable checkpoint for PR creation. Send a new message to rebuild the sandbox, then create the PR again.');
    expect(within(alert).queryByRole('button')).not.toBeInTheDocument();
  });

  it('shows snapshot-expired notice when the saved checkpoint was reaped', async () => {
    const expiredSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'destroyed',
      snapshot_key: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: expiredSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Session snapshot expired');
    expect(alert).toHaveTextContent('This session snapshot expired before a PR could be created. Send a new message to rebuild the sandbox, then create the PR again.');
  });

  it('shows a hover tooltip when Create PR is disabled', async () => {
    const sessionWithSnapshot: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'snapshotted',
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithSnapshot } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', async () => {
        await new Promise((resolve) => setTimeout(resolve, 1000));
        return HttpResponse.json({ data: { status: 'queued' } });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const createPRButton = await screen.findByRole('button', { name: 'Create PR' });
    await user.click(createPRButton);

    const queueingButton = await screen.findByRole('button', { name: 'Queueing PR…' });
    expect(queueingButton).toBeDisabled();

    await user.hover(queueingButton.parentElement as HTMLElement);

    expect(await screen.findByRole('tooltip', { name: 'Sending the PR request to the queue' })).toBeInTheDocument();
  });

  it('matches the snapshot expiry notice horizontal margins to overview cards', async () => {
    const sessionWithMissingSnapshot: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithMissingSnapshot } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const alert = await screen.findByRole('alert');
    expect(alert.className).toContain('mx-2');
  });

  it('shows a resume-specific PR error instead of session expiry when the GitHub resume token is stale', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: true,
          has_repo_scope: true,
          github_login: 'alice',
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          {
            error: {
              code: 'PR_RESUME_EXPIRED',
              message: 'GitHub authorization completed, but the PR resume request expired. Please click Create PR again.',
            },
          },
          { status: 409 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, {
      searchParams: { github_pr: 'connected', resume_pr: 'resume-123' },
    });
    await screen.findAllByText('Fixed TypeError by adding null check');

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("Couldn't resume PR creation");
    expect(alert).toHaveTextContent('GitHub authorization completed, but the PR resume request expired. Please click Create PR again.');
    expect(alert).not.toHaveTextContent('PR session expired');
  });

  it('keeps Create PR visible but disabled when session is running', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('Agent is working...');
    const createPRButton = screen.getByRole('button', { name: /Create PR/ });
    expect(createPRButton).toBeDisabled();
    expect(createPRButton).toHaveAttribute('title', expect.stringContaining('Wait for the session to finish before creating a PR'));
  });

  it('does not show a checkpoint-missing notice for an active run before a checkpoint is expected', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'running',
      snapshot_key: undefined,
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('Agent is working...');

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText('No reusable checkpoint saved')).not.toBeInTheDocument();
  });

  it('does not show a checkpoint-missing notice for an idle interactive session before PR creation starts', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'snapshotted',
      snapshot_key: undefined,
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText('No reusable checkpoint saved')).not.toBeInTheDocument();
  });

  it('does not show a checkpoint-missing notice while awaiting user input', async () => {
    const awaitingInputSession: Session = {
      ...mockSessions[0],
      status: 'awaiting_input',
      completed_at: undefined,
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'snapshotted',
      snapshot_key: undefined,
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: awaitingInputSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText('No reusable checkpoint saved')).not.toBeInTheDocument();
  });

  it('calls createPR API when Create PR button is clicked', async () => {
    let createPRCalled = false;

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        createPRCalled = true;
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const createPRButton = await screen.findByRole('button', { name: /Create PR/ });
    await user.click(createPRButton);

    await waitFor(() => {
      expect(createPRCalled).toBe(true);
    });
  });

  it('creates a PR directly when readiness is missing', async () => {
    const sessionWithSnapshot: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      sandbox_state: 'snapshotted',
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };
    let createPRCalled = false;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithSnapshot } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/sessions/:id/pr-readiness-runs/latest', () => {
        return HttpResponse.json({ data: {} });
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        createPRCalled = true;
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await user.click(await screen.findByRole('button', { name: 'Create PR' }));

    await waitFor(() => {
      expect(createPRCalled).toBe(true);
    });
    expect(screen.queryByRole('alertdialog', { name: 'Review readiness before creating PR?' })).not.toBeInTheDocument();
  });

  it('calls createPR API with merge_when_ready when Create PR and enable auto-merge is clicked', async () => {
    let createPRBody: unknown;

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
        createPRBody = await request.json();
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'More publish actions' }));
    await user.click(await screen.findByRole('menuitem', { name: /Create PR and enable auto-merge/ }));

    await waitFor(() => {
      expect(createPRBody).toEqual({ merge_when_ready: true });
    });
  });

  it('keeps Create PR at full opacity while the request is queueing', async () => {
    let releaseCreatePR: (() => void) | undefined;
    const createPRResponse = new Promise<Response>((resolve) => {
      releaseCreatePR = () => resolve(HttpResponse.json({ status: 'queued' }, { status: 202 }));
    });

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', async () => {
        return createPRResponse;
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const user = userEvent.setup();
    const createPRButton = await screen.findByRole('button', { name: /Create PR/ });
    await user.click(createPRButton);

    const queueingButton = await screen.findByRole('button', { name: /Queueing PR/ });
    expect(queueingButton).toBeDisabled();
    expect(queueingButton).toHaveAttribute('data-loading', 'true');
    expect(queueingButton).toHaveClass('disabled:data-[loading=true]:opacity-100');

    releaseCreatePR?.();
  });

  it('does not queue duplicate create PR requests from the keyboard while one is pending', async () => {
    let createPRCalls = 0;

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        createPRCalls += 1;
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByRole('button', { name: /Create PR/ });
    act(() => {
      (document.activeElement as HTMLElement | null)?.blur();
    });

    await userEvent.keyboard('pc');
    await waitFor(() => {
      expect(createPRCalls).toBe(1);
    });
    await userEvent.keyboard('pc');

    expect(createPRCalls).toBe(1);
  });

  it('keeps the button in a pending state after create PR starts without server PR state fields', async () => {
    const legacySession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: legacySession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    expect(await screen.findByRole('button', { name: /Creating PR…/ })).toBeDisabled();
  });

  it('shows an immediate queueing state while the create PR request is still being sent', async () => {
    let resolveCreatePR: (() => void) | undefined;

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', async () => {
        await new Promise<void>((resolve) => {
          resolveCreatePR = resolve;
        });
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    expect(await screen.findByRole('button', { name: /Queueing PR…/ })).toBeDisabled();

    resolveCreatePR?.();
  });

  it('shows the immediate create PR error in a 10-second toast', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'PUSH_FAILED', message: 'GitHub rejected the branch push.' } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith('PR creation failed', { duration: 10000 });
    });
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("Couldn't create the PR");
    expect(alert).toHaveTextContent('GitHub rejected the branch push.');
    expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument();
  });

  it('lets the detail header grow when showing a PR error notice', async () => {
    const sessionWithPRFailure: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'failed',
      pr_creation_error: 'No changes to push.',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithPRFailure } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("Couldn't create the PR");
    expect(screen.getByTestId('session-detail-header')).toHaveClass('min-h-14');
    expect(screen.getByTestId('session-detail-header')).not.toHaveClass('h-14');
    expect(screen.getByTestId('session-detail-header-bar')).toHaveClass('h-14');
  });

  it('shows the PR authorship modal and falls back to app mode when requested', async () => {
    const requestBodies: unknown[] = [];

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: false,
          has_repo_scope: false,
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
        requestBodies.push(await request.json().catch(() => undefined));
        if (requestBodies.length === 1) {
          return HttpResponse.json(
            {
              error: {
                code: 'GITHUB_PR_AUTHORSHIP_REQUIRED',
                message: 'Authorize GitHub to create this pull request as you.',
                details: {
                  connect_url: '/api/v1/users/me/github/connect?flow=pr_authorship',
                  resume_token: 'resume-123',
                  can_fallback_to_app: true,
                },
              },
            },
            { status: 409 },
          );
        }
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    expect(await screen.findByText('Open this pull request as yourself?')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Create as 143' }));

    await waitFor(() => {
      expect(requestBodies).toEqual([undefined, { author_mode: 'app' }]);
    });
  });

  it('opens the GitHub auth prompt when pressing p c and PR authorship is required', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: false,
          has_repo_scope: false,
          pr_authorship_mode: 'user_required',
          pr_draft_default: false,
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByRole('button', { name: /Create PR/ });
    act(() => {
      (document.activeElement as HTMLElement | null)?.blur();
    });

    await userEvent.keyboard('pc');

    expect(await screen.findByText('Open this pull request as yourself?')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Connect your GitHub account' })).toBeInTheDocument();
  });

  it('auto-resumes PR creation after GitHub auth callback', async () => {
    const requestBodies: unknown[] = [];

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: true,
          has_repo_scope: true,
          github_login: 'alice',
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
        requestBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, {
      searchParams: { github_pr: 'connected', resume_pr: 'resume-123' },
    });
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(requestBodies).toEqual([{ author_mode: 'user', resume_token: 'resume-123' }]);
    });
  });

  // Regression: when the OAuth callback signs an `Action` claim into the
  // resume token, the redirect forwards it as resume_action. The frontend
  // must dispatch the matching mutation (push vs create) deterministically,
  // even when the current PR state would otherwise lead to the opposite
  // branch — e.g. another tab created the PR during the OAuth round-trip,
  // so by the time we replay there's a PR but the original click was
  // "Create PR". We trust the signed action over the live state.
  it('auto-resumes Push changes when resume_action=push_changes is in the URL', async () => {
    const createBodies: unknown[] = [];
    const pushBodies: unknown[] = [];

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'succeeded',
      has_unpushed_changes: true,
      pr_push_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({
          data: {
            id: 'pr-1',
            session_id: 'session-abcdef12-3456-7890',
            org_id: sessionWithDiff.org_id,
            github_pr_number: 42,
            github_pr_url: 'https://github.com/example/repo/pull/42',
            github_repo: 'example/repo',
            title: 'Fix bug',
            status: 'open',
            review_status: 'pending',
            authored_by: 'app',
            ci_status: 'pending',
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          },
        });
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: true,
          has_repo_scope: true,
          github_login: 'alice',
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
        createBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
      http.post('/api/v1/sessions/:id/pr/push', async ({ request }) => {
        pushBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, {
      searchParams: { github_pr: 'connected', resume_pr: 'resume-push-1', resume_action: 'push_changes' },
    });
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(pushBodies).toEqual([{ author_mode: 'user', resume_token: 'resume-push-1' }]);
    });
    expect(createBodies).toEqual([]);
  });

  // Mirror of the push case: an explicit resume_action=create_pr must dispatch
  // the create mutation even if a PR somehow appeared during the OAuth
  // round-trip. The state-based fallback would route to push in that
  // scenario; the signed action overrides.
  it('auto-resumes Create PR when resume_action=create_pr is in the URL', async () => {
    const createBodies: unknown[] = [];
    const pushBodies: unknown[] = [];

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.get('/api/v1/users/me/github-status', () => {
        return HttpResponse.json({
          connected: true,
          has_repo_scope: true,
          github_login: 'alice',
          pr_authorship_mode: 'user_preferred',
          pr_draft_default: false,
        });
      }),
      http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
        createBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
      http.post('/api/v1/sessions/:id/pr/push', async ({ request }) => {
        pushBodies.push(await request.json().catch(() => undefined));
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />, {
      searchParams: { github_pr: 'connected', resume_pr: 'resume-create-1', resume_action: 'create_pr' },
    });
    await screen.findAllByText('Fixed TypeError by adding null check');

    await waitFor(() => {
      expect(createBodies).toEqual([{ author_mode: 'user', resume_token: 'resume-create-1' }]);
    });
    expect(pushBodies).toEqual([]);
  });

  it('keeps a creating state until the pull request exists, then swaps to View PR', async () => {
    let sessionFetchCount = 0;
    const queuedSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'queued',
    };
    const pushingSession: Session = {
      ...queuedSession,
      pr_creation_state: 'pushing',
    };
    const succeededSession: Session = {
      ...queuedSession,
      pr_creation_state: 'succeeded',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        sessionFetchCount += 1;
        if (sessionFetchCount <= 1) {
          return HttpResponse.json({
            data: { ...queuedSession, pr_creation_state: 'idle' },
          } satisfies SingleResponse<Session>);
        }
        if (sessionFetchCount === 2) {
          return HttpResponse.json({ data: queuedSession } satisfies SingleResponse<Session>);
        }
        if (sessionFetchCount === 3) {
          return HttpResponse.json({ data: pushingSession } satisfies SingleResponse<Session>);
        }
        return HttpResponse.json({ data: succeededSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        if (sessionFetchCount >= 4) {
          return HttpResponse.json({
            data: {
              id: 'pr-1',
              session_id: 'session-abcdef12-3456-7890',
              org_id: queuedSession.org_id,
              github_pr_number: 42,
              github_pr_url: 'https://github.com/example/repo/pull/42',
              github_repo: 'example/repo',
              title: 'Fix bug',
              status: 'open',
              review_status: 'pending',
              authored_by: 'app',
              ci_status: 'pending',
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          });
        }
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    expect(await screen.findByRole('button', { name: /Creating PR…/ })).toBeDisabled();

    await waitFor(
      async () => {
        expect(await screen.findByRole('link', { name: /View PR/ })).toBeInTheDocument();
      },
      { timeout: 12000 },
    );
    expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
  }, 15000);

  it('shows a clear toast and retry button when background PR creation fails', async () => {
    let sessionFetchCount = 0;
    const queuedSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'queued',
    };
    const failedSession: Session = {
      ...queuedSession,
      pr_creation_state: 'failed',
      pr_creation_error: 'GitHub rejected the branch push.',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        sessionFetchCount += 1;
        if (sessionFetchCount <= 1) {
          return HttpResponse.json({
            data: { ...queuedSession, pr_creation_state: 'idle' },
          } satisfies SingleResponse<Session>);
        }
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    await waitFor(
      () => {
        expect(toast.error).toHaveBeenCalledWith('PR creation failed', { duration: 10000 });
      },
      { timeout: 5000 },
    );
    await waitFor(
      () => {
        expect(screen.getByRole('button', { name: /Retry/ })).toBeInTheDocument();
      },
      { timeout: 5000 },
    );
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("Couldn't create the PR");
    expect(alert).toHaveTextContent('GitHub rejected the branch push.');
    expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument();
  }, 8000);

  it('refetches the PR row when auto-merge queueing fails after opening the PR', async () => {
    let sessionFetchCount = 0;
    let prFetchCount = 0;
    const queuedSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'queued',
    };
    const failedAfterOpenSession: Session = {
      ...queuedSession,
      pr_creation_state: 'failed',
      pr_creation_error: 'Could not enable auto-merge for this pull request.',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        sessionFetchCount += 1;
        if (sessionFetchCount <= 1) {
          return HttpResponse.json({
            data: { ...queuedSession, pr_creation_state: 'idle' },
          } satisfies SingleResponse<Session>);
        }
        return HttpResponse.json({ data: failedAfterOpenSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        prFetchCount += 1;
        if (prFetchCount <= 1) {
          return HttpResponse.json(
            { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
            { status: 404 },
          );
        }
        return HttpResponse.json({ data: mockPR });
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json({ status: 'queued' }, { status: 202 });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    await waitFor(
      async () => {
        expect(await screen.findByRole('link', { name: /View PR/ })).toBeInTheDocument();
      },
      { timeout: 8000 },
    );
    expect(prFetchCount).toBeGreaterThan(1);
  }, 10000);

  it('shows snapshot-unavailable guidance without a retry action when direct PR creation loses the snapshot', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'completed',
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
      snapshot_key: 'snap-abc',
      pr_creation_state: 'idle',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'pull request not found' } },
          { status: 404 },
        );
      }),
      http.post('/api/v1/sessions/:id/pr', () => {
        return HttpResponse.json(
          {
            error: {
              code: 'SNAPSHOT_NOT_CAPTURED',
              message: 'This session finished without saving a reusable checkpoint for PR creation. Send a new message to rebuild the sandbox, then create the PR again.',
            },
          },
          { status: 400 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Create PR/ }));

    await waitFor(
      () => {
        expect(toast.error).toHaveBeenCalledWith('PR creation failed', { duration: 10000 });
      },
      { timeout: 5000 },
    );

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('No reusable checkpoint saved');
    expect(alert).toHaveTextContent('This session finished without saving a reusable checkpoint for PR creation. Send a new message to rebuild the sandbox, then create the PR again.');
    expect(within(alert).queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument();
  });
});
