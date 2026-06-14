import { describe, it, expect, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen, userEvent, waitFor, within } from '@/test/test-utils';
import { act } from '@testing-library/react';
import { server } from '@/test/mocks/server';
import { mockSessions, mockMembers } from '@/test/mocks/handlers';
import { SessionDetailContent } from './session-detail-content';
import type { Session, SessionMessage, SessionReviewComment, SessionThread, SingleResponse, ListResponse } from '@/lib/types';
import {
  installSessionDetailPageTestHooks,
  changeFieldValue,
  submitFieldWithEnter,
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

describe('SessionDetailPage review mode and mobile diff', () => {
  it('enters review mode and shows review diff view with file tree', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();

    // Click Changes tab first
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await user.click(changesTab);

    expect(screen.queryByRole('button', { name: /Review 1 file/ })).not.toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: /app\.ts/ }, { timeout: 3000 }));

    // Should show the file content in the review diff view
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);

    // The detail panel toggle should be disabled during review
    const toggleButton = screen.getByTitle('File tree required during review');
    expect(toggleButton).toBeDisabled();

    await user.hover(toggleButton.parentElement as HTMLElement);

    expect(await screen.findByRole('tooltip', { name: 'File tree required during review' })).toBeInTheDocument();
  });

  it('opens the mobile diff view immediately when the chat files-changed summary is clicked', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByText('1 file changed'));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('uses a single mobile top bar and moves thread controls into the session actions sheet', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const threads: SessionThread[] = [
      {
        id: 'thread-1',
        session_id: 'session-abcdef12-3456-7890',
        org_id: 'org-1',
        agent_type: 'codex',
        label: 'Main tab',
        status: 'running',
        current_turn: 1,
        diff: 'diff --git a/src/app.ts b/src/app.ts',
        created_at: '2026-02-17T07:00:00Z',
        cost_cents: 15,
        pending_message_count: 0,
      },
      {
        id: 'thread-2',
        session_id: 'session-abcdef12-3456-7890',
        org_id: 'org-1',
        agent_type: 'claude_code',
        label: 'Review',
        status: 'awaiting_input',
        created_at: '2026-02-17T07:02:00Z',
        current_turn: 1,
        cost_cents: 10,
        pending_message_count: 1,
      },
    ];

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({
          data: {
            ...mockSessions[0],
            threads,
          },
        } satisfies SingleResponse<Session>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    expect(screen.getByRole('button', { name: 'Open session details' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Open session actions' })).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Open session actions' }));

    const actionsSheet = await screen.findByRole('dialog', { name: 'Session actions' });
    expect(within(actionsSheet).getByRole('button', { name: 'Switch to Main tab' })).toBeInTheDocument();
    expect(within(actionsSheet).getByRole('button', { name: 'Switch to Review' })).toBeInTheDocument();
    expect(within(actionsSheet).getByRole('button', { name: 'Add agent tab' })).toBeInTheDocument();
    expect(within(actionsSheet).getByRole('button', { name: 'Rename session' })).toBeInTheDocument();
  });

  it('opens a full-screen mobile diff when a file is selected from the Changes sheet', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Open session details' }));

    const detailSheet = await screen.findByRole('dialog');
    await user.click(within(detailSheet).getByRole('tab', { name: /^Changes/ }));
    await user.click(within(detailSheet).getByRole('button', { name: /app\.ts/ }));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('reopens the mobile files list from the diff reader', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByText('2 files changed'));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.getByText('1 of 2')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Open files list' }));
    const detailSheet = await screen.findByRole('dialog');
    expect(within(detailSheet).getByText('2 files changed')).toBeInTheDocument();
    expect(within(detailSheet).getByText('Browse session details, changed files, and preview on mobile.')).toBeInTheDocument();
  });

  it('uses the Changes sheet as a mobile file index instead of showing a review-all action', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Open session details' }));

    const detailSheet = await screen.findByRole('dialog');
    await user.click(within(detailSheet).getByRole('tab', { name: /^Changes/ }));

    expect(within(detailSheet).queryByText(/Review 2 files/)).not.toBeInTheDocument();
    expect(within(detailSheet).getByText('2 files changed')).toBeInTheDocument();
    expect(within(detailSheet).getByPlaceholderText('Filter files...')).toBeInTheDocument();
  });

  it('keeps the shared session composer off-canvas but available from the dedicated mobile diff reader', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      snapshot_key: 'snapshot/test',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    await user.click(await screen.findByText('1 file changed'));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.queryByPlaceholderText('Send a follow-up message...')).not.toBeInTheDocument();
    expect(screen.queryByTitle('Send message')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Message agent' }));

    expect(await screen.findByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
  });

  it('shows the session warning state inside the mobile composer sheet while reviewing', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const destroyedSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'destroyed',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(destroyedSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Session environment has expired and can no longer be continued');

    const user = userEvent.setup();
    await user.click(await screen.findByText('1 file changed'));
    await user.click(screen.getByRole('button', { name: 'Message agent' }));

    expect(await screen.findByText(/This session's environment has expired/i)).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Session environment has expired and can no longer be continued')).toBeInTheDocument();
  });

  it('opens mobile review comment edits in a sheet instead of inline in the diff row', async () => {
    vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
      matches: query === '(max-width: 767px)',
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    const comments: SessionReviewComment[] = [{
      id: 'comment-mobile-edit',
      session_id: 'session-abcdef12-3456-7890',
      org_id: 'org-1',
      user_id: 'user-1',
      file_path: 'src/app.ts',
      line_number: 2,
      diff_side: 'new',
      body: 'Add a guard before using this import.',
      resolved: false,
      pass_number: 0,
      created_at: '2026-02-17T07:04:00Z',
      updated_at: '2026-02-17T07:04:00Z',
    }];

    mockSessionDetailWithLazyDiff(sessionWithDiff);
    server.use(
      http.get('/api/v1/sessions/:id/review-comments', () => {
        return HttpResponse.json({
          data: comments,
          meta: {},
        } satisfies ListResponse<SessionReviewComment>);
      }),
    );

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(await screen.findByText('1 file changed'));

    expect((await screen.findAllByText('Add a guard before using this import.')).length).toBeGreaterThan(0);
    await user.click(screen.getByTitle('Edit'));

    expect(await screen.findByText('Edit review comment')).toBeInTheDocument();
    expect(screen.getByDisplayValue('Add a guard before using this import.')).toBeInTheDocument();
    expect(screen.queryByTestId('inline-comment-composer-anchor')).not.toBeInTheDocument();
  });

  it('exits review mode when clicking a non-changes tab', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();

    // Enter review mode via diff stats badge
    const viewChangesButtons = screen.getAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);

    // Click Overview tab — should exit review mode
    const overviewTab = screen.getByRole('tab', { name: 'Overview' });
    await user.click(overviewTab);

    // Review mode should be exited — chat panel should be visible again
    await waitFor(() => {
      expect(screen.getByTitle('Hide details')).toBeInTheDocument();
    });
  });

  it('exits review mode when browser history removes the review query param', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    await user.click(screen.getAllByTitle('View changes')[0]);
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
    expect(screen.getByTitle('File tree required during review')).toBeInTheDocument();

    act(() => {
      window.history.pushState(null, '', '/sessions/session-abcdef12-3456-7890?review=active');
      window.history.pushState(null, '', '/sessions/session-abcdef12-3456-7890');
      window.dispatchEvent(new PopStateEvent('popstate'));
    });

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

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Send a follow-up message...');

    const user = userEvent.setup();
    // Enter review mode
    const viewChangesButtons = screen.getAllByTitle('View changes');
    await user.click(viewChangesButtons[0]);

    // The standard shared composer should remain present in review mode.
    expect(await screen.findByPlaceholderText('Send a follow-up message...')).toBeInTheDocument();
    expect(screen.getByTitle('Send message')).toBeInTheDocument();
  });

  it('shows hover tooltips for disabled composer actions when the session environment has expired', async () => {
    const destroyedSession: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'destroyed',
    };

    server.use(
      http.get('/api/v1/sessions/:id', () => {
        return HttpResponse.json({ data: destroyedSession } satisfies SingleResponse<Session>);
      }),
    );

    const user = userEvent.setup();
    const { container } = renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    await screen.findByPlaceholderText('Session environment has expired and can no longer be continued');

    const attachButton = container.querySelector('button[title="Add files, photos, or a Linear issue"]') as HTMLButtonElement | null;
    expect(attachButton).not.toBeNull();
    expect(attachButton).toBeDisabled();
    await user.hover(attachButton?.parentElement as HTMLElement);
    expect(await screen.findByRole('tooltip', { name: 'Session environment has expired and can no longer be continued.' })).toBeInTheDocument();

    const sendButton = container.querySelector('button[title="Send message"]') as HTMLButtonElement | null;
    expect(sendButton).not.toBeNull();
    expect(sendButton).toBeDisabled();
    await user.hover(sendButton?.parentElement as HTMLElement);
    expect(await screen.findByRole('tooltip', { name: 'Session environment has expired and can no longer be continued.' })).toBeInTheDocument();
  });

  it('keeps the expired sandbox warning visible in review mode with the shared composer', async () => {
    const destroyedSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'destroyed',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(destroyedSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findByPlaceholderText('Session environment has expired and can no longer be continued');

    const user = userEvent.setup();
    await user.click(screen.getAllByTitle('View changes')[0]);

    expect(await screen.findByText(/environment has expired/i)).toBeVisible();
  });

  it('does not show the no-headless-resume warning for Amp in review mode', async () => {
    const ampSessionWithDiff: Session = {
      ...mockSessions[0],
      agent_type: 'amp',
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(ampSessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    expect(await screen.findAllByText('Fixed TypeError by adding null check')).not.toHaveLength(0);
    expect(screen.queryByText(/doesn't support headless conversation resume/i)).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getAllByTitle('View changes')[0]);

    expect(screen.queryByText(/doesn't support headless conversation resume/i)).not.toBeInTheDocument();
  });

  it('shares composer draft state and review comment attachments between chat and review mode', async () => {
    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };
    const comments: SessionReviewComment[] = [{
      id: 'comment-1',
      session_id: 'session-abcdef12-3456-7890',
      org_id: 'org-1',
      user_id: mockMembers[0].id,
      file_path: 'src/app.ts',
      line_number: 2,
      diff_side: 'new',
      body: 'Handle the null edge case',
      resolved: false,
      pass_number: 1,
      created_at: '2026-02-17T07:10:00Z',
      updated_at: '2026-02-17T07:10:00Z',
    }];

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);
    server.use(
      http.get('/api/v1/sessions/:id/review-comments', () => {
        return HttpResponse.json({
          data: comments,
          meta: {},
        } satisfies ListResponse<SessionReviewComment>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const composer = await screen.findByPlaceholderText('Send a follow-up message...') as HTMLTextAreaElement;
    changeFieldValue(composer, 'Please fix this and add tests');
    expect(composer.value).toBe('Please fix this and add tests');

    await user.click(screen.getAllByTitle('View changes')[0]);
    const sharedComposerInReview = await screen.findByDisplayValue('Please fix this and add tests');
    expect(sharedComposerInReview).toBeInTheDocument();

    expect(await screen.findByText('1 comment attached')).toBeInTheDocument();
    expect(screen.getAllByText('Handle the null edge case').length).toBeGreaterThan(0);

    await user.click(screen.getByRole('tab', { name: 'Overview' }));

    expect(await screen.findByDisplayValue('Please fix this and add tests')).toBeInTheDocument();
    expect(screen.getByText('1 comment attached')).toBeInTheDocument();
    expect(screen.getAllByText('Handle the null edge case').length).toBeGreaterThan(0);
  });

  it('returns to the main chat view after sending from review mode', async () => {
    const idleSessionWithDiff: Session = {
      ...mockSessions[0],
      status: 'idle',
      completed_at: undefined,
      current_turn: 1,
      sandbox_state: 'snapshotted',
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);',
      diff_stats: { added: 1, removed: 0, files_changed: 1 },
    };

    mockSessionDetailWithLazyDiff(idleSessionWithDiff);
    server.use(
      http.post('/api/v1/sessions/:id/messages', async ({ request }) => {
        const body = await request.json() as { message: string };
        return HttpResponse.json({
          data: {
            id: 99,
            session_id: idleSessionWithDiff.id,
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

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const textarea = await screen.findByPlaceholderText('Send a follow-up message...');
    await user.click(screen.getAllByTitle('View changes')[0]);
    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);

    changeFieldValue(textarea, 'Hello from review');
    submitFieldWithEnter(textarea);

    await waitFor(() => {
      expect(screen.queryByText('src/app.ts')).not.toBeInTheDocument();
    });
    expect(screen.getByTitle('Hide details')).toBeInTheDocument();
  });

  it('uses the Changes tab file list as the desktop review entry point', async () => {
    const sessionWithDiff: Session = {
      ...mockSessions[0],
      diff: 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,3 +1,4 @@\n import express from "express";\n+import cors from "cors";\n const app = express();\n app.listen(3000);\ndiff --git a/src/new.ts b/src/new.ts\n--- /dev/null\n+++ b/src/new.ts\n@@ -0,0 +1 @@\n+export const x = 1;',
      diff_stats: { added: 2, removed: 0, files_changed: 2 },
    };

    mockSessionDetailWithLazyDiff(sessionWithDiff);

    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');

    const user = userEvent.setup();
    const changesTab = screen.getByRole('tab', { name: /^Changes/ });
    await user.click(changesTab);

    expect(screen.queryByRole('button', { name: /Review 2 files/ })).not.toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: /app\.ts/ }, { timeout: 3000 }));

    expect((await screen.findAllByText('src/app.ts')).length).toBeGreaterThan(0);
  });

  it('opens review from the Changes tab without a tab attribution filter', async () => {
    const sessionId = 'session-abcdef12-3456-7890';
    const codexThread: SessionThread = {
      id: 'thread-codex',
      session_id: sessionId,
      org_id: 'org-1',
      agent_type: 'codex',
      label: 'Codex',
      status: 'completed',
      current_turn: 1,
      created_at: '2026-02-17T07:00:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    };
    const claudeThread: SessionThread = {
      id: 'thread-claude',
      session_id: sessionId,
      org_id: 'org-1',
      agent_type: 'claude_code',
      label: 'Claude review',
      status: 'completed',
      current_turn: 1,
      created_at: '2026-02-17T07:01:00Z',
      cost_cents: 0,
      pending_message_count: 0,
    };
    const sessionWithThreadsAndDiff: Session = {
      ...mockSessions[0],
      id: sessionId,
      threads: [codexThread, claudeThread],
      diff: [
        'diff --git a/frontend/src/app.ts b/frontend/src/app.ts',
        '--- a/frontend/src/app.ts',
        '+++ b/frontend/src/app.ts',
        '@@ -1 +1,2 @@',
        ' export const app = true;',
        '+export const codex = true;',
        'diff --git a/frontend/src/lib/helpers.ts b/frontend/src/lib/helpers.ts',
        '--- a/frontend/src/lib/helpers.ts',
        '+++ b/frontend/src/lib/helpers.ts',
        '@@ -1 +1,2 @@',
        ' export const helper = true;',
        '+export const shared = true;',
        'diff --git a/frontend/src/components/automation-model-select.tsx b/frontend/src/components/automation-model-select.tsx',
        '--- a/frontend/src/components/automation-model-select.tsx',
        '+++ b/frontend/src/components/automation-model-select.tsx',
        '@@ -1 +1,2 @@',
        ' export function AutomationModelSelect() {',
        '+  return null;',
        ' }',
      ].join('\n'),
      diff_stats: { added: 3, removed: 0, files_changed: 3 },
    };

    mockSessionDetailWithLazyDiff(sessionWithThreadsAndDiff);
    server.use(
      http.get('/api/v1/sessions/:id/thread-file-events', () => {
        return HttpResponse.json({
          data: [
            {
              id: 1,
              org_id: 'org-1',
              session_id: sessionId,
              thread_id: codexThread.id,
              turn: 1,
              path: 'frontend/src/app.ts',
              event_type: 'modified',
              observed_at: '2026-02-17T07:02:00Z',
            },
            {
              id: 2,
              org_id: 'org-1',
              session_id: sessionId,
              thread_id: codexThread.id,
              turn: 1,
              path: 'frontend/src/lib/helpers.ts',
              event_type: 'modified',
              observed_at: '2026-02-17T07:02:30Z',
            },
            {
              id: 3,
              org_id: 'org-1',
              session_id: sessionId,
              thread_id: claudeThread.id,
              turn: 1,
              path: 'frontend/src/lib/helpers.ts',
              event_type: 'modified',
              observed_at: '2026-02-17T07:03:00Z',
            },
            {
              id: 4,
              org_id: 'org-1',
              session_id: sessionId,
              thread_id: claudeThread.id,
              turn: 1,
              path: 'frontend/src/components/automation-model-select.tsx',
              event_type: 'modified',
              observed_at: '2026-02-17T07:03:30Z',
            },
          ],
          meta: {},
        } satisfies ListResponse<import('@/lib/types').SessionThreadFileEvent>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/messages', () => {
        return HttpResponse.json({ data: [] as SessionMessage[], meta: {} } satisfies ListResponse<SessionMessage>);
      }),
      http.get('/api/v1/sessions/:id/threads/:threadId/logs', () => {
        return HttpResponse.json({ data: [], meta: {} });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<SessionDetailContent id={sessionId} />);

    await screen.findAllByText('Fixed TypeError by adding null check');
    await user.click(screen.getByRole('tab', { name: /^Changes/ }));

    const changesPanel = screen.getByRole('tabpanel', { name: /^Changes/ });
    expect(within(changesPanel).queryByRole('combobox')).not.toBeInTheDocument();

    expect(screen.queryByRole('button', { name: 'Review 3 files' })).not.toBeInTheDocument();

    await user.click(await screen.findByRole('button', { name: /app\.ts/ }, { timeout: 3000 }));

    expect(await screen.findByText('frontend/src/app.ts')).toBeInTheDocument();
    expect(screen.getByText('frontend/src/lib/helpers.ts')).toBeInTheDocument();
    expect(screen.getByText('frontend/src/components/automation-model-select.tsx')).toBeInTheDocument();
  });
});
