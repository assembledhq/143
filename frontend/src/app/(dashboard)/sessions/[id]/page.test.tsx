import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers, mockIssues } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Issue, Session, SessionLog, SessionMessage, User, SingleResponse, ListResponse } from '@/lib/types';

// Mock EventSource (not available in jsdom)
class MockEventSource {
  static instances: MockEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  readyState = 0;
  url: string;
  withCredentials = false;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  constructor(url: string | URL) {
    this.url = String(url);
    MockEventSource.instances.push(this);
  }
  addEventListener = vi.fn();
  removeEventListener = vi.fn();
  close = vi.fn();
  dispatchEvent = vi.fn(() => true);
}
beforeAll(() => {
  global.EventSource = MockEventSource as unknown as typeof EventSource;
});

afterEach(() => {
  MockEventSource.instances = [];
});

// Mock next/link to render a plain anchor
vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

// Mock next/image to render a plain img
vi.mock('next/image', () => ({
  default: ({ src, alt, className, width, height }: { src: string; alt: string; className?: string; width?: number; height?: number }) => (
    <img src={src} alt={alt} className={className} width={width} height={height} />
  ),
}));

describe('SessionDetailPage', () => {
  it('shows loading state initially', () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(screen.getByText('Loading session...')).toBeInTheDocument();
  });

  it('renders session with result summary as title', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const elements = await screen.findAllByText('Fixed TypeError by adding null check');
    expect(elements.length).toBeGreaterThanOrEqual(1);
  });

  it('shows agent type label', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText(/Claude Code/).length).toBeGreaterThanOrEqual(1);
  });

  it('renders the session header title at text-sm size', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const headerTitle = await screen.findByRole('heading', {
      level: 1,
      name: 'Fixed TypeError by adding null check',
    });

    expect(headerTitle.className).toContain('text-sm');
    expect(headerTitle.className).not.toContain('text-xs');
  });

  it('shows overview tab with status in detail panel', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
  });

  it('shows detail panel tabs for Overview, Changes, Validation', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Changes' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Validation' })).toBeInTheDocument();
  });

  it('does not hide vertical overflow on the detail tablist', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const tabList = screen.getByRole('tablist');
    expect(tabList.className).not.toContain('overflow-y-hidden');
  });

  it('renders failed session with failure details', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            failure_explanation: 'Could not reproduce the error in test environment',
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getByText('Could not reproduce the error in test environment')).toBeInTheDocument();
  });

  it('keeps failed and updated timestamps visually aligned', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[1],
            failure_explanation: 'Could not reproduce the error in test environment',
          },
        });
      }),
      http.get('/api/v1/audit-logs', () => {
        return HttpResponse.json({
          data: [{
            id: 'audit-1',
            actor_type: 'user',
            user_id: mockMembers[0].id,
            action: 'session.failed',
            created_at: new Date(Date.now() - 12 * 60000).toISOString(),
          }],
          meta: {},
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    const updatedTrigger = await screen.findByRole('button', { name: /Updated.*ago by/i });
    const metadataRow = updatedTrigger.parentElement;

    // The audit trigger now shares the timestamps flex row so "Failed X ago"
    // and "Updated X ago by Y" sit on the same baseline.
    expect(metadataRow?.className).toContain('flex');
    expect(metadataRow?.className).toContain('items-center');
    expect(metadataRow?.textContent).toMatch(/Failed.*ago/);

    // Inline variant: zero horizontal padding, muted color, preceded by a
    // decorative middle-dot separator marked aria-hidden.
    expect(updatedTrigger.className).toContain('px-0');
    expect(updatedTrigger.className).toContain('text-muted-foreground');
    expect(updatedTrigger.previousElementSibling?.getAttribute('aria-hidden')).toBe('true');
    expect(updatedTrigger.previousElementSibling?.textContent).toBe('·');
  });

  it('shows error state when session not found', async () => {
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'Session not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="nonexistent" />);
    expect(
      await screen.findByText('Failed to load session'),
    ).toBeInTheDocument();
  });

  it('shows result summary card in overview', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Result')).toBeInTheDocument();
  });

  it('shows triggered by user name when triggered_by_user_id is set', async () => {
    const sessionWithTrigger: Session = {
      ...mockSessions[0],
      triggered_by_user_id: 'user-1',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithTrigger } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/team/members', () => {
        return HttpResponse.json({
          data: mockMembers,
          meta: {},
        } satisfies ListResponse<User>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findByText('Alice Smith')).toBeInTheDocument();
  });

  it('shows System when triggered_by_user_id is not set and no pm_plan_id', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('System')).toBeInTheDocument();
  });

  it('shows chat messages for idle multi-turn session', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 2,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/messages', () => {
        const msgs: SessionMessage[] = [
          { id: 1, session_id: idleSession.id, org_id: 'org-1', user_id: 'user-1', turn_number: 1, role: 'user', content: 'Fix the bug', created_at: '2026-02-17T07:01:00Z' },
          { id: 2, session_id: idleSession.id, org_id: 'org-1', turn_number: 1, role: 'assistant', content: 'Done fixing', created_at: '2026-02-17T07:02:00Z' },
        ];
        return HttpResponse.json({ data: msgs, meta: {} } satisfies ListResponse<SessionMessage>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);
    expect(await screen.findByText('Fix the bug')).toBeInTheDocument();
    expect(screen.getByText('Done fixing')).toBeInTheDocument();
    // Turn indicator shown in header and footer
    const turnElements = screen.getAllByText(/Turn 2/);
    expect(turnElements.length).toBeGreaterThanOrEqual(1);
  });

  it('shows empty message state when no messages', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      // Return issue without description so no synthetic message is added to timeline
      http.get('/api/v1/issues/:id', ({ params }) => {
        const issue = mockIssues.find((i) => i.id === params.id);
        if (!issue) {
          return HttpResponse.json(
            { error: { code: 'NOT_FOUND', message: 'Issue not found' } },
            { status: 404 },
          );
        }
        return HttpResponse.json({ data: { ...issue, description: '' } } satisfies SingleResponse<typeof issue>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);
    expect(await screen.findByText('No activity yet')).toBeInTheDocument();
    expect(screen.getByText('The session is processing its initial turn.')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
  });

  it('shows running indicator for running session', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Agent is responding...')).toBeDisabled();
  });

  it('disables input for pending session', async () => {
    const pendingSession: Session = {
      ...mockSessions[0],
      status: 'pending',
      completed_at: undefined,
      current_turn: 0,
      sandbox_state: 'none',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: pendingSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={pendingSession.id} />);
    expect(await screen.findByPlaceholderText('Session is not active')).toBeDisabled();
    expect(screen.getByText('Setting up environment')).toBeInTheDocument();
    expect(screen.getByText('Preparing the container and getting the agent ready to run.')).toBeInTheDocument();
  });

  it('keeps polling logs and reconnects after an SSE error while the session is active', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      id: 'session-running-reconnect',
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };

    let logFetchCount = 0;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/logs', () => {
        logFetchCount += 1;
        return HttpResponse.json({
          data: logFetchCount >= 2
            ? [{
                id: 101,
                session_id: runningSession.id,
                level: 'error',
                message: 'late log after reconnect',
                metadata: null,
                turn_number: 1,
                created_at: '2026-02-17T07:03:00Z',
              }]
            : [],
          meta: {},
        } satisfies ListResponse<SessionLog>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);

    expect(await screen.findByText('Agent is working...')).toBeInTheDocument();
    expect(logFetchCount).toBe(1);
    expect(MockEventSource.instances).toHaveLength(1);

    MockEventSource.instances[0].onerror?.(new Event('error'));

    await waitFor(() => {
      expect(MockEventSource.instances).toHaveLength(2);
    }, { timeout: 2500 });

    expect(await screen.findByText('late log after reconnect')).toBeInTheDocument();

    await waitFor(() => {
      expect(logFetchCount).toBeGreaterThanOrEqual(2);
    });
  });

  it('shows validation tab with check results for non-manual sessions', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    // Click the Validation tab button
    const user = userEvent.setup();
    const validationTab = screen.getByRole('tab', { name: 'Validation' });
    await user.click(validationTab);
    expect(await screen.findByText('Direction check')).toBeInTheDocument();
    expect(screen.getByText('Correctness check')).toBeInTheDocument();
    expect(screen.getByText('Changes align with issue description')).toBeInTheDocument();
  });

  it('hides validation tab for manual sessions', async () => {
    const manualSession: Session = {
      ...mockSessions[0],
      triggered_by_user_id: 'user-1',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: manualSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /^Changes/ })).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Validation' })).not.toBeInTheDocument();
  });

  it('shows View PR button in tab bar when PR exists', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
      diff_stats: { added: 1, removed: 1, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
      // Default handler returns mockPR for GET /sessions/:id/pr
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(await screen.findByText('View PR')).toBeInTheDocument();
  });

  it('shows failure next steps and retry button', async () => {
    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: 'Something broke',
      failure_category: 'test_failure',
      failure_next_steps: ['Check logs', 'Retry with debug'],
      failure_retry_advised: true,
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
    expect(screen.getByText('Retry')).toBeInTheDocument();
  });

  it('shows duration for completed session', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    // 5m 30s duration between started_at and completed_at (shown in timestamp row)
    expect(screen.getByText('5m 30s')).toBeInTheDocument();
  });

  it('shows pass selector when session has diff_history with multiple passes', async () => {
    const pass1Diff = 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);';
    const pass2Diff = 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;';

    const sessionWithHistory: Session = {
      ...mockSessions[0],
      diff: pass2Diff,
      diff_history: [
        { pass: 1, diff: pass1Diff, diff_stats: { added: 1, removed: 0, files_changed: 1 }, created_at: '2026-03-19T10:00:00Z' },
        { pass: 2, diff: pass2Diff, diff_stats: { added: 2, removed: 0, files_changed: 2 }, created_at: '2026-03-19T10:05:00Z' },
      ],
      current_turn: 2,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithHistory } satisfies SingleResponse<Session>);
      }),
    );

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

  it('does not show Create PR button when session has no diff', async () => {
    // Default mockSessions[0] has no diff_stats
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
  });

  it('does not show Create PR button when session is running', async () => {
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
    expect(screen.queryByRole('button', { name: /Create PR/ })).not.toBeInTheDocument();
  });

  it('calls createPR API when Create PR button is clicked', async () => {
    let createPRCalled = false;

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

  it('shows file count badge on Changes tab when session has diff', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    // Changes tab should show file count badge
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    expect(changesTab).toHaveTextContent('Changes2');
  });

  it('does not show file count badge on Changes tab when session has no diff', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const changesTab = screen.getByRole('tab', { name: 'Changes' });
    expect(changesTab).toHaveTextContent('Changes');
    expect(changesTab).not.toHaveTextContent(/\d/);
  });

  it('shows diff stats badge with file count in header when session has diff', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    // Header and footer should show clickable diff stats badges
    const viewChangesButtons = screen.getAllByTitle('View changes');
    expect(viewChangesButtons.length).toBeGreaterThanOrEqual(1);
    expect(viewChangesButtons[0]).toHaveTextContent('+1');
  });

  it('clicking diff stats badge in header opens Changes tab', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const viewChangesButtons = screen.getAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);

    // Should show the diff content in the Changes tab
    expect(await screen.findByText('src/app.ts')).toBeInTheDocument();
  });

  it('shows contextual empty state for completed session with no changes', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: 'Changes' });
    await user.click(changesTab);

    expect(await screen.findByText('No changes yet')).toBeInTheDocument();
    expect(screen.getByText('This session did not produce any file changes.')).toBeInTheDocument();
  });

  it('shows contextual empty state for running session with no changes', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: 'Changes' });
    await user.click(changesTab);

    expect(await screen.findByText('No changes yet')).toBeInTheDocument();
    expect(screen.getByText('Changes will appear here as the agent modifies files.')).toBeInTheDocument();
  });

  it('shows stop button instead of send button when session is running', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    await screen.findByText('Agent is working...');
    expect(screen.getByTitle('Cancel session')).toBeInTheDocument();
    expect(screen.queryByTitle('Send message')).not.toBeInTheDocument();
  });

  it('shows send button instead of stop button when session is idle', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={idleSession.id} />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
    expect(screen.queryByTitle('Cancel session')).not.toBeInTheDocument();
  });

  it('shows send button for completed session (not stop)', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
    expect(screen.queryByTitle('Cancel session')).not.toBeInTheDocument();
  });

  it('calls cancel session API when cancel button is clicked during running state', async () => {
    let cancelSessionCalled = false;

    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
      http.post('/api/v1/sessions/:id/cancel', () => {
        cancelSessionCalled = true;
        return HttpResponse.json({ data: { ...runningSession, status: 'cancelled' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    await screen.findByText('Agent is working...');

    const user = userEvent.setup();
    const cancelButton = screen.getByTitle('Cancel session');
    await user.click(cancelButton);

    await waitFor(() => {
      expect(cancelSessionCalled).toBe(true);
    });
  });

  it('shows PM context when pm_plan_id is set', async () => {
    const sessionWithPM: Session = {
      ...mockSessions[0],
      pm_plan_id: 'plan-1',
      pm_reasoning: 'High impact bug',
      pm_approach: 'Quick null check fix',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithPM } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('PM context');
    expect(screen.getByText('High impact bug')).toBeInTheDocument();
    expect(screen.getAllByText('Quick null check fix').length).toBeGreaterThanOrEqual(1);
  });

  it('shows Preview tab and renders PreviewPanel when clicked', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const previewTab = screen.getByRole('tab', { name: /Preview/ });
    expect(previewTab).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(previewTab);

    // PreviewPanel is rendered inside the preview tab content
    // It will show some loading/content from the preview component
    await waitFor(() => {
      expect(previewTab).toHaveAttribute('data-state', 'active');
    });
  });

  it('shows snapshot expired banner for destroyed sandbox', async () => {
    const expiredSession: Session = {
      ...mockSessions[0],
      status: 'completed',
      sandbox_state: 'destroyed',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: expiredSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(
      await screen.findByText(/environment has expired/),
    ).toBeInTheDocument();
  });

  it('disables input for destroyed sandbox session', async () => {
    const expiredSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      sandbox_state: 'destroyed',
      current_turn: 1,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: expiredSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Session environment has expired and can no longer be continued');
    expect(textarea).toBeDisabled();
  });

  it('hides input bar for pm_agent sessions', async () => {
    const pmSession: Session = {
      ...mockSessions[0],
      agent_type: 'pm_agent',
      status: 'running',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: pmSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('Agent is working...');
    // No textarea should be present for pm_agent
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
  });

  it('shows PM Agent as trigger label when pm_plan_id is set without user', async () => {
    const pmTriggeredSession: Session = {
      ...mockSessions[0],
      pm_plan_id: 'plan-1',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: pmTriggeredSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('PM Agent')).toBeInTheDocument();
  });

  it('shows "Started" timestamp for in-progress session without completed_at', async () => {
    const runningSession: Session = {
      ...mockSessions[0],
      status: 'running',
      completed_at: undefined,
      started_at: '2026-02-17T07:00:00Z',
      current_turn: 1,
      sandbox_state: 'running',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('Agent is working...');
    expect(screen.getByText(/Started/)).toBeInTheDocument();
  });

  it('shows raw agent type when not in known labels', async () => {
    const customSession: Session = {
      ...mockSessions[0],
      agent_type: 'my_custom_agent',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: customSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('my_custom_agent')).toBeInTheDocument();
  });

  it('shows no validation data state when validation query returns error', async () => {
    server.use(
      http.get('/api/v1/sessions/:id/validation', () => {
        return HttpResponse.json(
          { error: { code: 'NOT_FOUND', message: 'not found' } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const validationTab = screen.getByRole('tab', { name: 'Validation' });
    await user.click(validationTab);

    expect(await screen.findByText('No validation data')).toBeInTheDocument();
  });

  it('shows Failed badge when validation overall status is failed', async () => {
    server.use(
      http.get('/api/v1/sessions/:id/validation', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: 'val-2',
            session_id: 'session-abcdef12-3456-7890',
            org_id: 'org-1',
            status: 'failed',
            direction_check: 'fail',
            direction_check_details: 'Bad direction',
            correctness_check: null,
            correctness_check_details: null,
            quality_check: null,
            quality_check_details: null,
            security_scan: null,
            security_scan_details: null,
            regression_test_check: null,
            regression_test_check_details: null,
            ci_check: null,
            ci_check_details: null,
            created_at: '2026-02-17T07:06:00Z',
            updated_at: '2026-02-17T07:06:00Z',
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const validationTab = screen.getByRole('tab', { name: 'Validation' });
    await user.click(validationTab);

    expect(await screen.findByText('Failed')).toBeInTheDocument();
  });

  it('shows non-pass/fail overall validation status as-is', async () => {
    server.use(
      http.get('/api/v1/sessions/:id/validation', () => {
        return HttpResponse.json({
          data: {
            id: 'val-3',
            session_id: 'session-abcdef12-3456-7890',
            org_id: 'org-1',
            status: 'in_progress',
            direction_check: null,
            direction_check_details: null,
            correctness_check: null,
            correctness_check_details: null,
            quality_check: null,
            quality_check_details: null,
            security_scan: null,
            security_scan_details: null,
            regression_test_check: null,
            regression_test_check_details: null,
            ci_check: null,
            ci_check_details: null,
            created_at: '2026-02-17T07:06:00Z',
            updated_at: '2026-02-17T07:06:00Z',
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const validationTab = screen.getByRole('tab', { name: 'Validation' });
    await user.click(validationTab);

    expect(await screen.findByText('in_progress')).toBeInTheDocument();
  });

  it('shows codex auth failure with re-authenticate button', async () => {
    const codexAuthSession: Session = {
      ...mockSessions[1],
      failure_category: 'codex_auth_expired',
      failure_explanation: 'Codex token expired',
      agent_type: 'codex',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: codexAuthSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/settings/codex-auth/status', () => {
        return HttpResponse.json({ data: { status: 'none' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getByText('codex_auth_expired')).toBeInTheDocument();
    expect(screen.getByText('Re-authenticate with ChatGPT')).toBeInTheDocument();
    // Should NOT show failure_next_steps for codex auth failures
    expect(screen.queryByText('Next steps')).not.toBeInTheDocument();
  });

  it('shows connected message when codex auth is completed after failure', async () => {
    const codexAuthSession: Session = {
      ...mockSessions[1],
      failure_category: 'codex_auth_expired',
      failure_explanation: 'Codex token expired',
      agent_type: 'codex',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: codexAuthSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/settings/codex-auth/status', () => {
        return HttpResponse.json({ data: { status: 'completed' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(
      await screen.findByText(/ChatGPT connected/),
    ).toBeInTheDocument();
  });

  it('can toggle the detail panel visibility', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    // Detail panel should be visible initially (has Overview tab)
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();

    const user = userEvent.setup();
    // Find the toggle button by its title
    const hideButton = screen.getByTitle('Hide details');
    await user.click(hideButton);

    // After hiding, Overview tab should no longer be in the document
    expect(screen.queryByRole('tab', { name: 'Overview' })).not.toBeInTheDocument();

    // Show details button should now be available
    const showButton = screen.getByTitle('Show details');
    await user.click(showButton);

    // Tabs should be back
    expect(screen.getByRole('tab', { name: 'Overview' })).toBeInTheDocument();
  });

  it('shows pr_created status badge', async () => {
    const prCreatedSession: Session = {
      ...mockSessions[0],
      status: 'pr_created',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: prCreatedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('PR created').length).toBeGreaterThanOrEqual(1);
  });

  it('shows skipped session with disabled input', async () => {
    const skippedSession: Session = {
      ...mockSessions[0],
      status: 'skipped',
      started_at: undefined,
      completed_at: undefined,
      current_turn: 0,
      sandbox_state: 'none',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: skippedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(await screen.findByPlaceholderText('Session is not active')).toBeDisabled();
    // Status badge should show "Skipped"
    expect(screen.getAllByText('Skipped').length).toBeGreaterThanOrEqual(1);
  });

  it('shows cancelled session status', async () => {
    const cancelledSession: Session = {
      ...mockSessions[0],
      status: 'cancelled',
      completed_at: '2026-02-17T07:03:00Z',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: cancelledSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Cancelled').length).toBeGreaterThanOrEqual(1);
  });

  it('shows issue description as synthetic message in timeline', async () => {
    server.use(
      http.get('/api/v1/issues/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockIssues[0],
            description: 'This is the issue description for the agent',
          },
        } satisfies SingleResponse<Issue>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(
      await screen.findByText('This is the issue description for the agent'),
    ).toBeInTheDocument();
  });

  it('shows awaiting_input session status with active indicator', async () => {
    const awaitingSession: Session = {
      ...mockSessions[0],
      status: 'awaiting_input',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: awaitingSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Awaiting input').length).toBeGreaterThanOrEqual(1);
  });

  it('shows needs_human_guidance status', async () => {
    const guidanceSession: Session = {
      ...mockSessions[0],
      status: 'needs_human_guidance',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: guidanceSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getAllByText('Needs guidance').length).toBeGreaterThanOrEqual(1);
  });

  it('shows plan mode toggle for claude_code idle sessions', async () => {
    const idleClaudeSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleClaudeSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    // Plan button should be present for claude_code
    expect(screen.getByTitle('Switch to plan mode (Shift+Tab)')).toBeInTheDocument();
  });

  it('shows failed session with error field when no failure_explanation', async () => {
    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: undefined,
      error: 'Internal server error during execution',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getByText('Internal server error during execution')).toBeInTheDocument();
  });

  it('shows plan mode indicator when plan button is clicked', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    const planButton = screen.getByTitle('Switch to plan mode (Shift+Tab)');
    await user.click(planButton);

    // Plan mode indicator should appear
    expect(screen.getByText('Plan Mode')).toBeInTheDocument();
    expect(screen.getByText('Agent will create a plan for review before making changes')).toBeInTheDocument();
    // Placeholder should change
    expect(screen.getByPlaceholderText('Describe what you want to plan...')).toBeInTheDocument();
    // Plan mode exit button should be visible
    expect(screen.getByTitle('Exit plan mode')).toBeInTheDocument();
  });

  it('sends message when Enter key is pressed with content', async () => {
    let messageSent = false;

    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.post('/api/v1/sessions/:id/messages', () => {
        messageSent = true;
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: idleSession.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: 'Hello agent',
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    await user.type(textarea, 'Hello agent');
    await user.keyboard('{Enter}');

    await waitFor(() => {
      expect(messageSent).toBe(true);
    });
  });

  it('opens review mode when clicking diff stats badge in footer', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    // Click the diff stats badge to enter review mode
    const viewChangesButtons = screen.getAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);

    // After entering review mode, the review diff view should be shown
    // and the file should be visible
    expect(await screen.findByText('src/app.ts')).toBeInTheDocument();
  });

  it('exits plan mode when exit button is clicked', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    // Enter plan mode
    const planButton = screen.getByTitle('Switch to plan mode (Shift+Tab)');
    await user.click(planButton);
    expect(screen.getByText('Plan Mode')).toBeInTheDocument();

    // Exit plan mode
    const exitButton = screen.getByTitle('Exit plan mode');
    await user.click(exitButton);
    expect(screen.queryByText('Plan Mode')).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
  });

  it('shows send plan request button title when in plan mode', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    // Enter plan mode
    const planButton = screen.getByTitle('Switch to plan mode (Shift+Tab)');
    await user.click(planButton);

    // Send button title should change to "Send plan request"
    expect(screen.getByTitle('Send plan request')).toBeInTheDocument();
    // Plan button should be hidden when in plan mode
    expect(screen.queryByTitle('Switch to plan mode (Shift+Tab)')).not.toBeInTheDocument();
  });

  it('shows duration in seconds for short sessions', async () => {
    const quickSession: Session = {
      ...mockSessions[0],
      started_at: '2026-02-17T07:00:00Z',
      completed_at: '2026-02-17T07:00:45Z',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: quickSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('45s')).toBeInTheDocument();
  });

  it('hides plan button for non-claude_code agents', async () => {
    const codexSession: Session = {
      ...mockSessions[0],
      agent_type: 'codex',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: codexSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    // Plan button should not exist for codex
    expect(screen.queryByTitle('Switch to plan mode (Shift+Tab)')).not.toBeInTheDocument();
  });

  it('shows file upload button for active sessions', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    expect(screen.getByTitle('Attach files or images')).toBeInTheDocument();
    expect(screen.getByTitle('Attach files or images')).not.toBeDisabled();
  });

  it('shows Codex agent type label', async () => {
    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getAllByText('Codex').length).toBeGreaterThanOrEqual(1);
  });

  it('disables file upload button when session is not active', async () => {
    const skippedSession: Session = {
      ...mockSessions[0],
      status: 'skipped',
      current_turn: 0,
      sandbox_state: 'none',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: skippedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Session is not active');
    expect(screen.getByTitle('Attach files or images')).toBeDisabled();
  });

  it('enters review mode and shows review diff view with file tree', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();

    // Click Changes tab first
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await user.click(changesTab);

    // Click "Review 1 file" button to enter review mode
    const reviewButton = await screen.findByText(/Review 1 file/);
    await user.click(reviewButton);

    // Should show the file content in the review diff view
    expect(await screen.findByText('src/app.ts')).toBeInTheDocument();

    // The detail panel toggle should be disabled during review
    const toggleButton = screen.getByTitle('File tree required during review');
    expect(toggleButton).toBeDisabled();
  });

  it('exits review mode when clicking a non-changes tab', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();

    // Enter review mode via diff stats badge
    const viewChangesButtons = screen.getAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);
    expect(await screen.findByText('src/app.ts')).toBeInTheDocument();

    // Click Overview tab — should exit review mode
    const overviewTab = screen.getByRole('tab', { name: 'Overview' });
    await user.click(overviewTab);

    // Review mode should be exited — chat panel should be visible again
    await waitFor(() => {
      expect(screen.getByTitle('Hide details')).toBeInTheDocument();
    });
  });

  it('shows review comment input in review mode for active session', async () => {
    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    // Enter review mode
    const viewChangesButtons = screen.getAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);

    // Review comment input should be present
    expect(await screen.findByPlaceholderText('Ask to make changes, @mention files...')).toBeInTheDocument();
    expect(screen.getByTitle('Send to agent')).toBeInTheDocument();
  });

  it('shows review file count in Changes tab and file click works', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await user.click(changesTab);

    // Should show "Review 2 files" button
    expect(await screen.findByText(/Review 2 files/)).toBeInTheDocument();
  });

  it('shows model selector for agents with available models', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    // Model override selector should be present
    expect(screen.getByLabelText('Model override')).toBeInTheDocument();
  });

  it('shows Gemini CLI agent type label', async () => {
    const geminiSession: Session = {
      ...mockSessions[0],
      agent_type: 'gemini_cli',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: geminiSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Gemini CLI')).toBeInTheDocument();
  });

  it('renders Unknown user when triggered_by_user_id does not match any member', async () => {
    const sessionWithUnknownUser: Session = {
      ...mockSessions[0],
      triggered_by_user_id: 'user-nonexistent',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithUnknownUser } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Unknown user')).toBeInTheDocument();
  });

  it('calls retry API when Retry button is clicked on failed session', async () => {
    let retryCalled = false;

    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: 'Something broke',
      failure_retry_advised: true,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
      http.post('/api/v1/sessions/:id/retry', () => {
        retryCalled = true;
        return HttpResponse.json({ data: { ...failedSession, status: 'pending' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');

    const user = userEvent.setup();
    const retryButton = screen.getByText('Retry');
    await user.click(retryButton);

    await waitFor(() => {
      expect(retryCalled).toBe(true);
    });
  });

  it('renders SessionFooter with turn number for multi-turn session', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 3,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    // Turn indicator and diff stats should appear in footer
    const turnElements = screen.getAllByText(/Turn 3/);
    expect(turnElements.length).toBeGreaterThanOrEqual(1);
  });

  it('shows Shift+Tab toggle for plan mode in claude_code session', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    // Shift+Tab should toggle plan mode
    await user.click(textarea);
    await user.keyboard('{Shift>}{Tab}{/Shift}');

    // Plan mode should now be active
    expect(screen.getByText('Plan Mode')).toBeInTheDocument();

    // Shift+Tab again should exit plan mode
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(screen.queryByText('Plan Mode')).not.toBeInTheDocument();
  });

  it('shows idle status badge', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');
    expect(screen.getAllByText('Idle').length).toBeGreaterThanOrEqual(1);
  });

  it('shows session title from title field when available', async () => {
    const sessionWithTitle: Session = {
      ...mockSessions[0],
      title: 'Custom session title',
      result_summary: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithTitle } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(await screen.findByText('Custom session title')).toBeInTheDocument();
  });

  it('shows only pm_approach without pm_reasoning in PM context', async () => {
    const sessionWithPM: Session = {
      ...mockSessions[0],
      pm_plan_id: 'plan-1',
      pm_reasoning: undefined,
      pm_approach: 'Direct fix approach',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithPM } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByText('PM context');
    expect(screen.getAllByText('Direct fix approach').length).toBeGreaterThanOrEqual(1);
    expect(screen.queryByText('Why this was prioritized')).not.toBeInTheDocument();
  });

  it('clears message after successful send', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...') as HTMLTextAreaElement;

    const user = userEvent.setup();
    await user.type(textarea, 'Test message');
    expect(textarea.value).toBe('Test message');

    // Click send button
    const sendButton = screen.getByTitle('Send message');
    await user.click(sendButton);

    // After send, the textarea should be cleared
    await waitFor(() => {
      expect(textarea.value).toBe('');
    });
  });

  it('shows validation check with unknown result type', async () => {
    server.use(
      http.get('/api/v1/sessions/:id/validation', () => {
        return HttpResponse.json({
          data: {
            id: 'val-4',
            session_id: 'session-abcdef12-3456-7890',
            org_id: 'org-1',
            status: 'passed',
            direction_check: 'warning',
            direction_check_details: 'Needs review',
            correctness_check: 'pass',
            correctness_check_details: null,
            quality_check: null,
            quality_check_details: null,
            security_scan: null,
            security_scan_details: null,
            regression_test_check: null,
            regression_test_check_details: null,
            ci_check: null,
            ci_check_details: null,
            created_at: '2026-02-17T07:06:00Z',
            updated_at: '2026-02-17T07:06:00Z',
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const validationTab = screen.getByRole('tab', { name: 'Validation' });
    await user.click(validationTab);

    // "warning" is not "pass" or "fail", so checkResultBadge renders the raw value
    expect(await screen.findByText('warning')).toBeInTheDocument();
  });
});
