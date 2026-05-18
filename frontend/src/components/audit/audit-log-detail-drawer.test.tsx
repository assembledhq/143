import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { AuditLogDetailDrawer } from './audit-log-detail-drawer';
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
    created_at: '2026-04-13T21:57:21Z',
    ...overrides,
  };
}

describe('AuditLogDetailDrawer', () => {
  it('returns null when entry is null', () => {
    const { container } = render(
      <AuditLogDetailDrawer entry={null} onClose={vi.fn()} members={mockMembers} />
    );
    expect(container.innerHTML).toBe('');
  });

  it('renders core event details', () => {
    render(
      <AuditLogDetailDrawer entry={makeEntry()} onClose={vi.fn()} members={mockMembers} />
    );

    expect(screen.getByText('Event details')).toBeInTheDocument();
    expect(screen.getByText('session.created')).toBeInTheDocument();
    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('user')).toBeInTheDocument();
    expect(screen.getByText('session')).toBeInTheDocument();
    expect(screen.getByText('sess-1')).toBeInTheDocument();
  });

  it('renders request metadata when present', () => {
    render(
      <AuditLogDetailDrawer
        entry={makeEntry({
          ip_address: '192.168.1.1',
          user_agent: 'TestAgent/1.0',
          request_id: 'req-123',
        })}
        onClose={vi.fn()}
        members={mockMembers}
      />
    );

    expect(screen.getByText('Request info')).toBeInTheDocument();
    expect(screen.getByText('192.168.1.1')).toBeInTheDocument();
    expect(screen.getByText('TestAgent/1.0')).toBeInTheDocument();
    expect(screen.getByText('req-123')).toBeInTheDocument();
  });

  it('renders details payload when present', () => {
    render(
      <AuditLogDetailDrawer
        entry={makeEntry({ details: { changed: 'value', count: 42 } })}
        onClose={vi.fn()}
        members={mockMembers}
      />
    );

    expect(screen.getByText('Details')).toBeInTheDocument();
    expect(screen.getByText('changed')).toBeInTheDocument();
    expect(screen.getByText('value')).toBeInTheDocument();
    expect(screen.getByText('42')).toBeInTheDocument();
  });

  it('renders role detail values with user-facing labels', () => {
    render(
      <AuditLogDetailDrawer
        entry={makeEntry({
          action: 'team.member_role_changed',
          details: { previous_role: 'member', new_role: 'builder' },
        })}
        onClose={vi.fn()}
        members={mockMembers}
      />
    );

    expect(screen.getByText(/changed user role/)).toBeInTheDocument();
    expect(screen.getByText('Engineer')).toBeInTheDocument();
    expect(screen.getByText('Builder')).toBeInTheDocument();
    expect(screen.queryByText('member')).not.toBeInTheDocument();
    expect(screen.queryByText(/changed member role/)).not.toBeInTheDocument();
  });

  it('renders automation update changes as before/after diff', () => {
    render(
      <AuditLogDetailDrawer
        entry={makeEntry({
          action: 'automation.updated',
          resource_type: 'automation',
          details: {
            name: 'My automation',
            changes: {
              name: { before: 'old name', after: 'new name' },
              priority: { before: 50, after: 75 },
            },
          },
        })}
        onClose={vi.fn()}
        members={mockMembers}
      />
    );

    expect(screen.getByText('old name')).toBeInTheDocument();
    expect(screen.getByText('new name')).toBeInTheDocument();
    expect(screen.getByText('50')).toBeInTheDocument();
    expect(screen.getByText('75')).toBeInTheDocument();
  });

  it('renders correlation IDs when present', () => {
    render(
      <AuditLogDetailDrawer
        entry={makeEntry({ session_id: 'sess-abc', project_id: 'proj-xyz' })}
        onClose={vi.fn()}
        members={mockMembers}
      />
    );

    expect(screen.getByText('Related')).toBeInTheDocument();
    expect(screen.getByText('sess-abc')).toBeInTheDocument();
    expect(screen.getByText('proj-xyz')).toBeInTheDocument();
  });
});
