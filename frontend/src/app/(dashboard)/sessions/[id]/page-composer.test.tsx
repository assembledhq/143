import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { fireEvent, renderWithProviders, screen, userEvent, waitFor, within } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Session, SessionMessage, SessionReviewComment, SessionThread, User, SingleResponse, ListResponse } from '@/lib/types';
import { installSessionDetailPageTestHooks, setMobileViewport, changeFieldValue } from './session-detail-test-kit';

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

describe('SessionDetailPage composer and session metadata', () => {
  it('clears draft composer text when the session id changes without remounting the page shell', async () => {
    const firstSession: Session = {
      ...mockSessions[0],
      id: 'session-first-draft-reset',
      result_summary: 'First draft reset session',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      threads: [],
    };
    const secondSession: Session = {
      ...mockSessions[0],
      id: 'session-second-draft-reset',
      result_summary: 'Second draft reset session',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      threads: [],
    };

    server.use(
      http.get('/api/v1/sessions/:id', ({ params }) => {
        const session = params.id === firstSession.id ? firstSession : secondSession;
        return HttpResponse.json({ data: session } satisfies SingleResponse<Session>);
      }),
    );

    const { rerender } = renderWithProviders(<SessionDetailContent id={firstSession.id} />);
    await screen.findAllByText('First draft reset session');
    const composer = await screen.findByPlaceholderText('Send a follow-up message...');
    changeFieldValue(composer, 'This belongs to the first session');
    expect(composer).toHaveValue('This belongs to the first session');

    rerender(<SessionDetailContent id={secondSession.id} />);

    await screen.findAllByText('Second draft reset session');
    await waitFor(() => {
      expect(screen.getByPlaceholderText('Send a follow-up message...')).toHaveValue('');
    });
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

  it('uses a mobile settings sheet for the resumed-session composer on small screens', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      agent_type: 'claude_code',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    setMobileViewport(true);
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    expect(screen.getByRole('button', { name: 'Session settings' })).toBeInTheDocument();
    expect(screen.queryByLabelText('Model override')).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Session settings' }));

    expect(await screen.findByRole('dialog', { name: 'Session settings' })).toBeInTheDocument();
    expect(screen.getByLabelText('Model override')).toBeInTheDocument();
  });

  it('does not render the session footer on mobile conversation view', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 3,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    setMobileViewport(true);
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-mobile-footer-hidden" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    expect(screen.queryByTestId('session-footer')).not.toBeInTheDocument();
  });

  it('keeps the mobile follow-up textarea collapsed until focused', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    setMobileViewport(true);
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-mobile-composer-height" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');

    expect(textarea).toHaveAttribute('data-mobile-composer-state', 'collapsed');
    expect(textarea).toHaveAttribute('rows', '1');

    const user = userEvent.setup();
    await user.click(textarea);

    expect(textarea).toHaveAttribute('data-mobile-composer-state', 'expanded');

    fireEvent.blur(textarea);

    await waitFor(() => {
      expect(textarea).toHaveAttribute('data-mobile-composer-state', 'collapsed');
    });
  });

  it('constrains attached review comment chips inside the mobile composer', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };
    const comment: SessionReviewComment = {
      id: 'comment-long-file',
      session_id: idleSession.id,
      org_id: 'org-1',
      user_id: mockMembers[0].id,
      file_path: 'docs/design/implemented/107-pagerduty-integration-with-a-very-long-file-name-that-used-to-overflow.md',
      line_number: 776,
      diff_side: 'new',
      body: 'Let\'s make an oauth integration with enough text to require truncation in the attached comment chip.',
      resolved: false,
      pass_number: 1,
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:00Z',
    };

    setMobileViewport(true);
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/review-comments', () => {
        return HttpResponse.json({ data: [comment], meta: {} } satisfies ListResponse<SessionReviewComment>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-mobile-attached-comment-chip" />);

    const filename = await screen.findByText('107-pagerduty-integration-with-a-very-long-file-name-that-used-to-overflow.md:776');
    const chip = filename.closest('div');
    expect(chip).not.toBeNull();
    const commentPreview = within(chip as HTMLElement).getByText((content) => (
      content.startsWith('Let\'s make an oauth integration') && content.endsWith('...')
    ));

    expect(chip).toHaveClass('max-w-full', 'min-w-0');
    expect(filename).toHaveClass('min-w-0', 'truncate');
    expect(commentPreview).toHaveClass('min-w-0', 'truncate');
  });

  it('autofocuses the follow-up textarea on desktop session detail pages', async () => {
    const idleSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
    };

    setMobileViewport(false);
    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: idleSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-desktop-autofocus" />);
    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');

    await waitFor(() => {
      expect(textarea).toHaveFocus();
    });
  });

  it('focuses the continue-session textarea after creating a new tab', async () => {
    const sessionId = 'session-create-tab-autofocus';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Codex',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            status: 'idle',
            sandbox_state: 'snapshotted',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async () => {
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: sessionId,
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    const addTabButtons = await screen.findAllByRole('button', { name: 'Add agent tab' });
    const stripAddButton = addTabButtons[addTabButtons.length - 1] as HTMLButtonElement;
    await user.click(stripAddButton);

    const textarea = await screen.findByPlaceholderText('Send a message to Codex 2...');

    await waitFor(() => {
      expect(textarea).toHaveFocus();
    });
  });

  it('keeps focus on the add-tab trigger after creating a tab when no composer is rendered', async () => {
    const sessionId = 'session-create-tab-no-composer';
    const threads: SessionThread[] = [
      {
        id: 'thread-main',
        session_id: sessionId,
        org_id: 'org-1',
        agent_type: 'pm_agent',
        label: 'Planner',
        status: 'idle',
        current_turn: 1,
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 0,
        pending_message_count: 0,
      },
    ];

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            id: sessionId,
            agent_type: 'pm_agent',
            status: 'idle',
            sandbox_state: 'snapshotted',
            threads,
          },
        } satisfies SingleResponse<Session & { threads: SessionThread[] }>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.post('/api/v1/sessions/:id/threads', async () => {
        const thread: SessionThread = {
          id: 'thread-new',
          session_id: sessionId,
          org_id: 'org-1',
          agent_type: 'codex',
          label: 'Codex 2',
          status: 'idle',
          current_turn: 0,
          created_at: '2026-02-17T07:04:00Z',
          cost_cents: 0,
          pending_message_count: 0,
        };
        threads.push(thread);
        return HttpResponse.json({ data: thread } satisfies SingleResponse<SessionThread>, { status: 201 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    const addTabButtons = await screen.findAllByRole('button', { name: 'Add agent tab' });
    const stripAddButton = addTabButtons[addTabButtons.length - 1] as HTMLButtonElement;
    await user.click(stripAddButton);

    await waitFor(() => {
      expect(stripAddButton).toHaveFocus();
    });
  });

  it('matches both trigger menus to the continue-session input width', async () => {
    const resumableSession: Session = {
      ...mockSessions[0],
      agent_type: 'codex',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      repository_id: 'repo-1',
      target_branch: 'main',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: resumableSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/sessions/:id/composer/files', ({ params }) => {
        expect(params.id).toBe('session-abcdef12-3456-7890');
        return HttpResponse.json({
          data: [
            {
              kind: 'directory',
              token: '@internal/services',
              path: 'internal/services',
              display: 'internal/services',
            },
          ],
          meta: {},
        } satisfies ListResponse<{ kind: 'file' | 'directory'; token?: string; path?: string; id?: string; display: string }>);
      }),
      http.get('/api/v1/session-composer/slash-commands', () => {
        return HttpResponse.json({
          groups: [
            {
              source: 'builtin',
              label: 'Codex commands',
              items: [
                {
                  kind: 'command',
                  agent_type: 'codex',
                  name: 'review',
                  token: '/review',
                  display: '/review',
                  description: 'Review pending changes',
                  source: 'builtin',
                },
              ],
            },
          ],
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const composer = await screen.findByPlaceholderText('Send a follow-up message...') as HTMLTextAreaElement;
    const composerShell = screen.getByTestId('session-composer-shell');
    const inputSurface = screen.getByTestId('session-composer-input-surface');

    vi.spyOn(composerShell, 'getBoundingClientRect').mockReturnValue({
      x: 0,
      y: 680,
      width: 760,
      height: 132,
      top: 680,
      right: 760,
      bottom: 812,
      left: 0,
      toJSON: () => ({}),
    });
    vi.spyOn(inputSurface, 'getBoundingClientRect').mockReturnValue({
      x: 48,
      y: 692,
      width: 640,
      height: 108,
      top: 692,
      right: 688,
      bottom: 800,
      left: 48,
      toJSON: () => ({}),
    });

    await user.type(composer, 'Inspect @serv');

    const mentionOverlay = await screen.findByTestId('trigger-picker-overlay');
    expect(mentionOverlay).toHaveStyle({ left: '48px', width: '640px' });

    await user.clear(composer);
    await user.type(composer, '/rev');

    expect(await screen.findByText('/review')).toBeInTheDocument();

    const commandOverlay = screen.getByTestId('trigger-picker-overlay');
    expect(commandOverlay).toHaveStyle({ left: '48px', width: '640px' });
  });

  it('renders slash command chips without a leading slash icon', async () => {
    const resumableSession: Session = {
      ...mockSessions[0],
      agent_type: 'codex',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      repository_id: 'repo-1',
      target_branch: 'main',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: resumableSession } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/session-composer/slash-commands', () => {
        return HttpResponse.json({
          groups: [
            {
              source: 'builtin',
              label: 'Codex commands',
              items: [
                {
                  kind: 'command',
                  agent_type: 'codex',
                  name: 'review',
                  token: '/review',
                  display: '/review',
                  description: 'Review pending changes',
                  source: 'builtin',
                },
              ],
            },
          ],
        });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const composer = await screen.findByPlaceholderText('Send a follow-up message...') as HTMLTextAreaElement;
    await user.type(composer, '/rev');
    await user.click((await screen.findByText('/review')).closest('button') as HTMLButtonElement);

    const chips = screen.getByLabelText('Selected references and commands');
    expect(within(chips).getByText('/review')).toBeInTheDocument();
    expect(chips.querySelector('svg.lucide-slash')).toBeNull();
  });

  it('shows OpenCode agent type label', async () => {
    const openCodeSession: Session = {
      ...mockSessions[0],
      agent_type: 'opencode',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: openCodeSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('OpenCode')).toBeInTheDocument();
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

  it('shows automation provenance in the overview tab for automation-created sessions', async () => {
    const automationSession: Session = {
      ...mockSessions[0],
      origin: 'automation',
      automation_run_id: 'automation-run-1',
      triggered_by_user_id: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: automationSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.getByText('Created by automation')).toBeInTheDocument();
    expect(screen.getByText('Automation run')).toBeInTheDocument();
  });

  it('does not show automation provenance for manually created sessions', async () => {
    const manualSession: Session = {
      ...mockSessions[0],
      origin: 'manual',
      automation_run_id: undefined,
      triggered_by_user_id: mockMembers[0].id,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: manualSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    expect(screen.queryByText('Created by automation')).not.toBeInTheDocument();
    expect(screen.queryByText('Automation run')).not.toBeInTheDocument();
  });

  it('falls back to github_login when triggering member has no display name', async () => {
    const memberWithoutName: User = {
      id: 'user-no-name',
      org_id: 'org-1',
      email: '249349663+nisarg-assembled@users.noreply.github.com',
      name: '',
      role: 'admin',
      github_login: 'nisarg-assembled',
      created_at: '2026-01-01T00:00:00Z',
    };
    const sessionWithNamelessTrigger: Session = {
      ...mockSessions[0],
      triggered_by_user_id: memberWithoutName.id,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: sessionWithNamelessTrigger } satisfies SingleResponse<Session>);
      }),
      http.get('/api/v1/team/members', () => {
        return HttpResponse.json({
          data: [memberWithoutName],
          meta: {},
        } satisfies ListResponse<User>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(await screen.findByText('nisarg-assembled')).toBeInTheDocument();
    expect(screen.queryByText('Unknown user')).not.toBeInTheDocument();
  });

  it('calls checkpoint retry API when primary retry button is clicked on failed session', async () => {
    let retryBody: unknown;

    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: 'Something broke',
      failure_retry_advised: true,
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
      http.post('/api/v1/sessions/:id/retry', async ({ request }) => {
        retryBody = await request.json();
        return HttpResponse.json({ data: { ...failedSession, status: 'pending' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');

    const user = userEvent.setup();
    const retryButton = screen.getByRole('button', { name: /^Retry$/i });
    await user.click(retryButton);

    await waitFor(() => {
      expect(retryBody).toEqual({ mode: 'checkpoint' });
    });
  });

  it('disables checkpoint retry without a checkpoint but keeps start-over available', async () => {
    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: 'Something broke',
      failure_retry_advised: true,
      sandbox_state: 'none',
      snapshot_key: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');

    expect(screen.getByRole('button', { name: /^Retry$/i })).toBeDisabled();

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: /More retry actions/i }));
    expect(await screen.findByRole('menuitem', { name: /Start over from beginning/i })).toBeInTheDocument();
  });

  it('requires confirmation before posting start-over retry mode', async () => {
    let retryBody: unknown;
    const failedSession: Session = {
      ...mockSessions[1],
      failure_explanation: 'Something broke',
      failure_retry_advised: true,
      sandbox_state: 'none',
      snapshot_key: undefined,
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: failedSession } satisfies SingleResponse<Session>);
      }),
      http.post('/api/v1/sessions/:id/retry', async ({ request }) => {
        retryBody = await request.json();
        return HttpResponse.json({ data: { ...failedSession, status: 'pending' } });
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-98765432-abcd-ef01" />);
    await screen.findByText('Failure details');

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: /More retry actions/i }));
    await user.click(await screen.findByRole('menuitem', { name: /Start over from beginning/i }));

    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
    expect(retryBody).toBeUndefined();

    await user.click(screen.getByRole('button', { name: /^Start over$/i }));

    await waitFor(() => {
      expect(retryBody).toEqual({ mode: 'start_over' });
    });
  });

  it('does not render the session footer for multi-turn sessions', async () => {
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
    expect(screen.queryByTestId('session-footer')).not.toBeInTheDocument();
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
    expect(screen.getByText('Plan mode')).toBeInTheDocument();

    // Shift+Tab again should exit plan mode
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(screen.queryByText('Plan mode')).not.toBeInTheDocument();
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
    expect(await screen.findByRole('heading', { level: 1, name: 'Custom session title' })).toBeInTheDocument();
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
    changeFieldValue(textarea, 'Test message');
    expect(textarea.value).toBe('Test message');

    // Click send button
    const sendButton = screen.getByTitle('Send message');
    await user.click(sendButton);

    // After send, the textarea should be cleared
    await waitFor(() => {
      expect(textarea.value).toBe('');
    });
  });
});
