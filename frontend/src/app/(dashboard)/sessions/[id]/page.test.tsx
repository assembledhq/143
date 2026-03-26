import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Session, SessionLog, SessionMessage, User, SingleResponse, ListResponse } from '@/lib/types';

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
    expect(screen.getByPlaceholderText('Send a message to the agent...')).toBeEnabled();
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

  it('shows validation tab with check results', async () => {
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

  it('shows changes tab with PR info and diff', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: '--- a/file.ts\n+++ b/file.ts\n@@ -1 +1 @@\n-old\n+new',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithDiff } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: 'Changes' });
    await user.click(changesTab);
    expect(await screen.findByText('GitHub')).toBeInTheDocument();
    expect(screen.getByText('example/repo #42')).toBeInTheDocument();
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
    expect(screen.getByTitle('Create PR')).toBeInTheDocument();
    expect(screen.getByTitle('Create PR')).not.toBeDisabled();
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
      expect(screen.queryByTitle('Create PR')).not.toBeInTheDocument();
    });
  });

  it('does not show Create PR button when session has no diff', async () => {
    // Default mockSessions[0] has no diff_stats
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.queryByTitle('Create PR')).not.toBeInTheDocument();
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
    expect(screen.queryByTitle('Create PR')).not.toBeInTheDocument();
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
    const createPRButton = screen.getByTitle('Create PR');
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

    // Header and footer should show clickable diff stats badges with file count
    const viewChangesButtons = screen.getAllByTitle('View changes');
    expect(viewChangesButtons.length).toBeGreaterThanOrEqual(1);
    expect(viewChangesButtons[0]).toHaveTextContent('1 file');
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
    expect(screen.getByTitle('Stop session')).toBeInTheDocument();
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
    expect(screen.queryByTitle('Stop session')).not.toBeInTheDocument();
  });

  it('shows send button for completed session (not stop)', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
    expect(screen.queryByTitle('Stop session')).not.toBeInTheDocument();
  });

  it('calls end session API when stop button is clicked during running state', async () => {
    let endSessionCalled = false;

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
      http.post('/api/v1/sessions/:id/end', () => {
        endSessionCalled = true;
        return HttpResponse.json({ data: { ...runningSession, status: 'idle' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    await screen.findByText('Agent is working...');

    const user = userEvent.setup();
    const stopButton = screen.getByTitle('Stop session');
    await user.click(stopButton);

    await waitFor(() => {
      expect(endSessionCalled).toBe(true);
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
});
