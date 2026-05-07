import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { LinkedIssueChips } from './linked-issue-chips';
import type { Session } from '@/lib/types';

// LinkedIssueChips is a pure presentational component, so these tests render
// it directly without the providers TestProviders wires up.

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: 'sess-1',
    org_id: 'org-1',
    agent_type: 'codex',
    status: 'active',
    autonomy_level: 'auto',
    token_mode: 'standard',
    current_turn: 0,
    last_activity_at: '2026-04-28T00:00:00Z',
    sandbox_state: 'idle',
    created_at: '2026-04-28T00:00:00Z',
    ...overrides,
  };
}

describe('LinkedIssueChips', () => {
  it('returns null when no links and no failure', () => {
    const { container } = render(<LinkedIssueChips session={makeSession({})} />);
    expect(container.firstChild).toBeNull();
  });

  it('renders Linear chip with workspace-qualified deep link', () => {
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'primary',
          position: 0,
          issue_source: 'linear',
          external_id: 'ACS-1234',
          issue_title: 'Add OAuth callback',
          issue_status: 'In Progress',
          issue_workspace_slug: 'acs',
        },
      ],
    });
    render(<LinkedIssueChips session={session} />);
    const link = screen.getByRole('link', { name: 'ACS-1234' });
    expect(link).toHaveAttribute('href', 'https://linear.app/acs/issue/ACS-1234');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  });

  it('shows the shared tooltip on hover for a Linear chip instead of relying on the native title hover', async () => {
    const user = userEvent.setup();
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'primary',
          position: 0,
          issue_source: 'linear',
          external_id: 'ACS-1234',
          issue_title: 'Add OAuth callback',
          issue_status: 'In Progress',
          issue_workspace_slug: 'acs',
        },
      ],
    });

    render(<LinkedIssueChips session={session} />);

    await user.hover(screen.getByRole('link', { name: 'ACS-1234' }));

    expect(await screen.findByRole('tooltip')).toHaveTextContent('Add OAuth callback (primary) · In Progress');
  });

  it('renders Linear chip with a logo and subdued neutral styling', () => {
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'primary',
          position: 0,
          issue_source: 'linear',
          external_id: 'VIR-75',
        },
      ],
    });

    render(<LinkedIssueChips session={session} />);

    const link = screen.getByRole('link', { name: 'VIR-75' });
    expect(link.querySelector('img[aria-hidden="true"]')).not.toBeNull();
    expect(link.className).toContain('bg-muted');
    expect(link.className).not.toContain('bg-blue-500/10');
  });

  it('falls back to workspace-less Linear URL when slug missing', () => {
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'primary',
          position: 0,
          issue_source: 'linear',
          external_id: 'ACS-1234',
        },
      ],
    });
    render(<LinkedIssueChips session={session} />);
    expect(screen.getByRole('link', { name: 'ACS-1234' })).toHaveAttribute(
      'href',
      'https://linear.app/issue/ACS-1234',
    );
  });

  it('URL-encodes user-provided slug and external_id segments', () => {
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'primary',
          position: 0,
          issue_source: 'linear',
          external_id: 'ACS-1234',
          issue_workspace_slug: 'acs/evil',
        },
      ],
    });
    render(<LinkedIssueChips session={session} />);
    expect(screen.getByRole('link', { name: 'ACS-1234' })).toHaveAttribute(
      'href',
      'https://linear.app/acs%2Fevil/issue/ACS-1234',
    );
  });

  it('renders non-link chip for non-Linear sources', () => {
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'a1b2c3d4e5f6',
          role: 'related',
          position: 1,
          issue_source: 'sentry',
          issue_title: 'Sentry: NRE in foo',
        },
      ],
    });
    render(<LinkedIssueChips session={session} />);
    // Without external_id we surface a labeled placeholder rather than a
    // UUID slice — UUIDs aren't useful identifiers for users to search.
    expect(screen.getByText('Issue (no key)')).toBeInTheDocument();
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('mirrors tooltip into aria-label on non-link chips so screen readers announce issue context', () => {
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'related',
          position: 0,
          issue_source: 'sentry',
          external_id: 'SEN-77',
          issue_title: 'NRE in foo',
        },
      ],
    });
    render(<LinkedIssueChips session={session} />);
    // Most screen readers ignore `title` on non-interactive elements; the
    // span must surface its context through aria-label.
    expect(screen.getByLabelText('NRE in foo (related)')).toBeInTheDocument();
  });

  it('renders the prepare-failed recovery chip as a link with sr-only detail', () => {
    const session = makeSession({ linear_prepare_state: 'failed' });
    render(<LinkedIssueChips session={session} />);
    const chip = screen.getByRole('link', { name: /Linear: prepare failed/i });
    expect(chip).toHaveTextContent(/Linear: prepare failed/);
    expect(chip).toHaveAttribute('href', '/settings/integrations');
    // The detail element backs aria-describedby and must exist in the DOM
    // even though it's visually hidden.
    expect(chip).toHaveAttribute('aria-describedby', 'linear-prepare-failed-detail');
    const detail = document.getElementById('linear-prepare-failed-detail');
    expect(detail).not.toBeNull();
    expect(detail).toHaveTextContent(/preparation failed/i);
  });

  it('does not render warning chip when prepare state is not failed', () => {
    const session = makeSession({ linear_prepare_state: 'ready' });
    const { container } = render(<LinkedIssueChips session={session} />);
    expect(container.firstChild).toBeNull();
  });

  it('renders a clickable fallback Linear chip from linear_identifier_hint when linked issues are not hydrated yet', async () => {
    const user = userEvent.setup();
    const session = makeSession({ linear_identifier_hint: 'ENG-1234' });

    render(<LinkedIssueChips session={session} />);

    const link = screen.getByRole('link', { name: 'ENG-1234' });
    expect(link).toHaveAttribute('href', 'https://linear.app/issue/ENG-1234');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');

    await user.hover(link);

    expect(await screen.findByRole('tooltip')).toHaveTextContent('Open primary Linear issue in Linear');
  });

  it('renders an explicit placeholder when a Linear link lacks an external_id', () => {
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'primary',
          position: 0,
          issue_source: 'linear',
        },
      ],
    });
    render(<LinkedIssueChips session={session} />);
    // Without external_id the Linear branch's URL guard fails, so we fall
    // through to the non-link span and surface the placeholder.
    expect(screen.getByText('Linear (no key)')).toBeInTheDocument();
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('orders links as given (primary first, then related) and renders both', () => {
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'primary',
          position: 0,
          issue_source: 'linear',
          external_id: 'ACS-1',
        },
        {
          id: 'link-2',
          session_id: 'sess-1',
          issue_id: 'issue-2',
          role: 'related',
          position: 1,
          issue_source: 'linear',
          external_id: 'ACS-2',
        },
      ],
    });
    render(<LinkedIssueChips session={session} />);
    const links = screen.getAllByRole('link');
    expect(links.map((a) => a.textContent)).toEqual(['ACS-1', 'ACS-2']);
  });

  it('renders a visible sync-skipped chip for a primary Linear issue with a last skip reason', () => {
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'primary',
          position: 0,
          issue_source: 'linear',
          external_id: 'ACS-1',
          linear_last_skipped_reason: 'per_team_disabled',
        },
      ],
    });

    render(<LinkedIssueChips session={session} />);

    expect(screen.getByText('Linear sync skipped')).toBeInTheDocument();
    expect(screen.getByLabelText(/workflow state sync is disabled/i)).toBeInTheDocument();
  });

  it('shows the shared tooltip on hover for the sync-skipped chip', async () => {
    const user = userEvent.setup();
    const session = makeSession({
      linked_issues: [
        {
          id: 'link-1',
          session_id: 'sess-1',
          issue_id: 'issue-1',
          role: 'primary',
          position: 0,
          issue_source: 'linear',
          external_id: 'ACS-1',
          linear_last_skipped_reason: 'per_team_disabled',
        },
      ],
    });

    render(<LinkedIssueChips session={session} />);

    await user.hover(screen.getByText('Linear sync skipped'));

    expect(await screen.findByRole('tooltip')).toHaveTextContent('Workflow state sync is disabled by org or team Linear automation settings.');
  });
});
