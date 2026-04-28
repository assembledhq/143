import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
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
    // Falls back to issue_id slice when external_id is missing.
    expect(screen.getByText('a1b2c3d4')).toBeInTheDocument();
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('renders the prepare-failed warning chip with sr-only detail', () => {
    const session = makeSession({ linear_prepare_state: 'failed' });
    render(<LinkedIssueChips session={session} />);
    const chip = screen.getByRole('status');
    expect(chip).toHaveTextContent(/Linear: prepare failed/);
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
});
