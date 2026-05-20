import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { AuditLogEntry } from './audit-log-entry';
import type { AuditLog, User } from '@/lib/types';

const mockMembers: User[] = [
  {
    id: 'user-1',
    org_id: 'org-1',
    email: 'alice@example.com',
    name: 'Alice',
    role: 'admin',
    created_at: '2026-01-01T00:00:00Z',
  },
];

function makeEntry(overrides: Partial<AuditLog> = {}): AuditLog {
  return {
    id: 1,
    org_id: 'org-1',
    actor_type: 'user',
    actor_id: 'user-1',
    user_id: 'user-1',
    action: 'session.created',
    resource_type: 'session',
    resource_id: 'sess-1',
    created_at: new Date(Date.now() - 5 * 60000).toISOString(),
    ...overrides,
  };
}

describe('AuditLogEntry', () => {
  it('renders actor name and action label', () => {
    render(<AuditLogEntry entry={makeEntry()} members={mockMembers} />);

    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('created session')).toBeInTheDocument();
  });

  it('labels team role changes without saying member role', () => {
    render(
      <AuditLogEntry
        entry={makeEntry({ action: 'team.member_role_changed' })}
        members={mockMembers}
      />
    );

    expect(screen.getByText('changed user role')).toBeInTheDocument();
    expect(screen.queryByText('changed member role')).not.toBeInTheDocument();
  });

  it('renders agent actor type label for non-user actors', () => {
    render(
      <AuditLogEntry
        entry={makeEntry({ actor_type: 'agent', actor_id: 'agent-1', user_id: undefined })}
        members={mockMembers}
      />
    );

    expect(screen.getByText('Agent')).toBeInTheDocument();
  });

  it('falls back to action string when no label mapping exists', () => {
    render(
      <AuditLogEntry
        entry={makeEntry({ action: 'custom.unknown.action' })}
        members={mockMembers}
      />
    );

    expect(screen.getByText('custom unknown action')).toBeInTheDocument();
  });

  it('expands details on click when details are present', async () => {
    const user = userEvent.setup();
    render(
      <AuditLogEntry
        entry={makeEntry({ details: { changed: 'value' } })}
        members={mockMembers}
      />
    );

    await user.click(screen.getByRole('button'));
    expect(screen.getByText('changed:')).toBeInTheDocument();
    expect(screen.getByText('value')).toBeInTheDocument();
  });

  it('renders role detail values with user-facing labels', async () => {
    const user = userEvent.setup();
    render(
      <AuditLogEntry
        entry={makeEntry({
          action: 'team.member_role_changed',
          details: { previous_role: 'member', new_role: 'viewer' },
        })}
        members={mockMembers}
      />
    );

    await user.click(screen.getByRole('button'));

    expect(screen.getByText('Engineer')).toBeInTheDocument();
    expect(screen.getByText('Viewer')).toBeInTheDocument();
    expect(screen.queryByText('member')).not.toBeInTheDocument();
  });

  it('calls onSelect instead of expanding when provided', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const entry = makeEntry({ details: { changed: 'value' } });
    render(
      <AuditLogEntry entry={entry} members={mockMembers} onSelect={onSelect} />
    );

    await user.click(screen.getByRole('button'));
    expect(onSelect).toHaveBeenCalledWith(entry);
    expect(screen.queryByText('changed:')).not.toBeInTheDocument();
  });

  it('disables the button when no onSelect and no details', () => {
    render(
      <AuditLogEntry entry={makeEntry()} members={mockMembers} />
    );

    expect(screen.getByRole('button')).toBeDisabled();
  });
});
