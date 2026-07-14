import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import { act } from '@testing-library/react';
import { server } from '@/test/mocks/server';
import { mockSessions, mockIssues } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Issue, Session, SessionDiff, SessionMessage, SingleResponse } from '@/lib/types';
import {
  installSessionDetailPageTestHooks,
  MockEventSource,
  changeFieldValue,
  sessionWithoutRawDiff,
  mockSessionDetailWithLazyDiff,
} from './session-detail-test-kit';

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

describe('SessionDetailPage session states', () => {
  it('shows file count badge on Changes tab when session has diff', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    // Changes tab should show file count badge
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await waitFor(() => {
      expect(changesTab).toHaveTextContent('Changes2');
    });
  });

  it('keeps the active Changes underline inside the fixed header height when the file count badge is shown', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByLabelText('Session detail tabs')).toHaveClass('h-full');
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
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    // Header and footer should show clickable diff stats badges
    const viewChangesButtons = await screen.findAllByTitle('View changes');
    expect(viewChangesButtons.length).toBeGreaterThanOrEqual(1);
    expect(viewChangesButtons[0]).toHaveTextContent('+1');
  });

  it('clicking diff stats badge in header opens Changes tab', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const viewChangesButtons = await screen.findAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);

    // Should show the diff content in the Changes tab
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
  });

  it('refetches stale empty diff data when the conversation files-changed button opens review', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };
    let diffRequestCount = 0;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithoutRawDiff(sessionWithDiff) } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/diff', () => {
        diffRequestCount += 1;
        return HttpResponse.json({
          data: {
            session_id: sessionWithDiff.id,
            diff: diffRequestCount === 1 ? '' : sessionWithDiff.diff,
            diff_stats: sessionWithDiff.diff_stats,
            diff_history: [],
            diff_truncated: false,
            diff_history_truncated: false,
          },
        } satisfies SingleResponse<SessionDiff>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByText('1 file changed'));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    await waitFor(() => {
      expect(diffRequestCount).toBe(2);
    });
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
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
  });

  it('explains that a pending session is waiting on the org concurrency limit', async () => {
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
      http.get('/api/v1/settings/runtime/status', () => {
        return HttpResponse.json({
          data: {
            static_egress: { available: true, enabled: false },
            capacity: {
              state: 'limited',
              active_agent_runs: 2,
              max_concurrent_agent_runs: 2,
              active_previews: 0,
              max_previews_per_user: 5,
            },
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id={pendingSession.id} />);

    expect(await screen.findByText('Waiting for capacity')).toBeInTheDocument();
    expect(await screen.findByText('Your organization is already at its max concurrency limit of 2 running sessions.')).toBeInTheDocument();
    expect(screen.getByText('This session will start automatically when another session finishes or the limit is raised.')).toBeInTheDocument();
    expect(screen.queryByText('Setting up environment')).not.toBeInTheDocument();
  });

  it('shows the environment setup message for a pending session when the org is below its concurrency limit', async () => {
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
      http.get('/api/v1/settings/runtime/status', () => {
        return HttpResponse.json({
          data: {
            static_egress: { available: true, enabled: false },
            capacity: {
              state: 'normal',
              active_agent_runs: 0,
              max_concurrent_agent_runs: 2,
              active_previews: 0,
              max_previews_per_user: 5,
            },
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id={pendingSession.id} />);

    expect(await screen.findByText('Setting up environment')).toBeInTheDocument();
    expect(screen.queryByText('Waiting for capacity')).not.toBeInTheDocument();
    expect(screen.queryByText('Max concurrency reached')).not.toBeInTheDocument();
  });

  it('shows the environment setup message for a pending session when capacity is limited only by the preview quota', async () => {
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
      http.get('/api/v1/settings/runtime/status', () => {
        return HttpResponse.json({
          data: {
            static_egress: { available: true, enabled: false },
            capacity: {
              // `state` is "limited" because previews are maxed, but agent-run
              // concurrency still has headroom, so this pending session is just
              // setting up — not queued behind the concurrency limit.
              state: 'limited',
              active_agent_runs: 0,
              max_concurrent_agent_runs: 2,
              active_previews: 5,
              max_previews_per_user: 5,
            },
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id={pendingSession.id} />);

    expect(await screen.findByText('Setting up environment')).toBeInTheDocument();
    expect(screen.queryByText('Waiting for capacity')).not.toBeInTheDocument();
    expect(screen.queryByText('Max concurrency reached')).not.toBeInTheDocument();
  });

  it('keeps the composer enabled and sends follow-up messages while the session is running', async () => {
    let postedMessage = '';
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
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string };
        postedMessage = body.message;
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: runningSession.id,
            org_id: 'org-1',
            user_id: 'user-1',
            turn_number: 2,
            role: 'user' as const,
            content: body.message,
            created_at: '2026-02-17T07:10:00Z',
          },
        } satisfies SingleResponse<SessionMessage>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    expect(textarea).toBeEnabled();

    const user = userEvent.setup();
    changeFieldValue(textarea, 'Queue this behind the current work');
    await user.click(screen.getByTitle('Send message'));

    await waitFor(() => {
      expect(postedMessage).toBe('Queue this behind the current work');
    });
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

  it('shows stop-requested transcript state immediately after cancelling a running session', async () => {
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
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={runningSession.id} />);
    await screen.findByText('Agent is working...');

    const user = userEvent.setup();
    await user.click(screen.getByTitle('Cancel session'));

    await waitFor(() => {
      expect(cancelSessionCalled).toBe(true);
    });
    expect(screen.getByText('Stopping agent...')).toBeInTheDocument();
    expect(screen.queryByText('Agent is working...')).not.toBeInTheDocument();
    expect(screen.getByTitle('Cancel session')).toBeDisabled();
  });

  it('shows checkpointed stopped state when a cancelled run returns to idle', async () => {
    const sessionId = 'session-stop-returns-idle';
    const runningSession: Session = {
      ...mockSessions[0],
      id: sessionId,
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
        return HttpResponse.json({ data: runningSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id={sessionId} />);
    await screen.findByText('Agent is working...');

    const user = userEvent.setup();
    await user.click(screen.getByTitle('Cancel session'));
    expect(await screen.findByText('Stopping agent...')).toBeInTheDocument();

    await waitFor(() => {
      expect(MockEventSource.instances.length).toBeGreaterThan(0);
    });

    act(() => {
      MockEventSource.instances[0].emit('status', {
        ...runningSession,
        status: 'idle',
        sandbox_state: 'snapshotted',
        snapshot_key: 'snapshots/session-stop-returns-idle.tar',
      });
    });

    await waitFor(() => {
      expect(screen.queryByText('Stopping agent...')).not.toBeInTheDocument();
    });
    expect(screen.getByText('Stopped. You can send a follow-up when ready.')).toBeInTheDocument();
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
    expect(previewTab.querySelector('svg')).not.toBeInTheDocument();

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
    const startedLabel = screen.getByText(/Started/);
    expect(startedLabel).toBeInTheDocument();

    expect(startedLabel.textContent?.trim().startsWith('Started')).toBe(true);
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

  it('shows codex auth failure with re-authenticate button', async () => {
    const codexAuthSession: Session = {
      ...mockSessions[1],
      failure_category: 'codex_auth_expired',
      failure_explanation: 'Codex token expired',
      agent_type: 'codex',
    };
    let statusScope: string | null = null;
    let initiateBody: Record<string, unknown> | null = null;

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: codexAuthSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/settings/codex-auth/status', ({ request }) => {
        statusScope = new URL(request.url).searchParams.get('scope');
        return HttpResponse.json({ data: { status: 'none' } });
      }),
      http.post('/api/v1/settings/codex-auth/initiate', async ({ request }) => {
        initiateBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          data: {
            user_code: 'TEST-CODE',
            verification_uri: 'https://auth.openai.com/codex/device',
            expires_in: 900,
          },
        });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(screen.getByText('codex_auth_expired')).toBeInTheDocument();
    await waitFor(() => {
      expect(statusScope).toBe('personal');
    });
    const user = userEvent.setup();
    await user.click(screen.getByText('Re-authenticate with ChatGPT'));
    await waitFor(() => {
      expect(initiateBody).toMatchObject({ scope: 'personal' });
    });
    // Should NOT show failure_next_steps for codex auth failures
    expect(screen.queryByText('Next steps')).not.toBeInTheDocument();
  });

  it('shows connected message when codex auth is completed after failure', async () => {
    const codexAuthSession: Session = {
      ...mockSessions[1],
      failure_category: 'codex_auth_expired',
      failure_explanation: 'Codex token expired',
      failure_retry_advised: true,
      agent_type: 'codex',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: codexAuthSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/settings/codex-auth/status', ({ request }) => {
        expect(new URL(request.url).searchParams.get('scope')).toBe('personal');
        return HttpResponse.json({ data: { status: 'completed' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(
      await screen.findByText('ChatGPT connected — open the retry menu and choose Start over from beginning.'),
    ).toBeInTheDocument();
  });

  it('points codex auth users to Retry when saved progress exists', async () => {
    const codexAuthSession: Session = {
      ...mockSessions[1],
      failure_category: 'codex_auth_expired',
      failure_explanation: 'Codex token expired',
      failure_retry_advised: true,
      agent_type: 'codex',
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: codexAuthSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/settings/codex-auth/status', ({ request }) => {
        expect(new URL(request.url).searchParams.get('scope')).toBe('personal');
        return HttpResponse.json({ data: { status: 'completed' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');
    expect(
      await screen.findByText('ChatGPT connected — click Retry to continue this session.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Retry$/i })).toBeEnabled();
  });

  it('shows runtime restoration while keeping follow-up input enabled', async () => {
    const recoveringSession: Session = {
      ...mockSessions[0],
      status: 'running',
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
      recovery_state: 'queued',
      recovery_queued_at: '2026-05-28T12:00:00Z',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: recoveringSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    expect(await screen.findAllByText('Restoring runtime from checkpoint')).not.toHaveLength(0);
    expect(screen.getAllByText('Follow-up messages will be queued and delivered after the runtime is restored.')).not.toHaveLength(0);
    const composer = screen.getByPlaceholderText('Send a follow-up message...');
    expect(composer).toBeEnabled();
  });

  it('disables checkpoint retry while recovery is active', async () => {
    const recoveringFailedSession: Session = {
      ...mockSessions[1],
      failure_retry_advised: true,
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
      recovery_state: 'recovering',
      recovery_started_at: '2026-05-28T12:00:00Z',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: recoveringFailedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);

    await screen.findByText('Failure details');
    expect(screen.getAllByText('Restoring runtime from checkpoint')).not.toHaveLength(0);
    expect(screen.getByRole('button', { name: /^Retry$/i })).toBeDisabled();
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
    expect(screen.getByText('Plan mode')).toBeInTheDocument();
    expect(screen.getByText('Agent will create a plan for review before making changes')).toBeInTheDocument();
    // Placeholder should change
    expect(screen.getByPlaceholderText('Describe what you want to plan...')).toBeInTheDocument();
    // Plan mode exit button should be visible
    expect(screen.getByTitle('Exit plan mode')).toBeInTheDocument();
  });
});
