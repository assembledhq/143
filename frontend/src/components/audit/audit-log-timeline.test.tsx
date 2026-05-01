import { describe, it, expect, vi } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import { AuditLogTimeline } from './audit-log-timeline';
import type { User } from '@/lib/types';

const { auditLogListMock } = vi.hoisted(() => ({
  auditLogListMock: vi.fn(),
}));

vi.mock('@/lib/api', () => ({
  api: {
    auditLogs: {
      list: auditLogListMock,
    },
  },
}));

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

describe('AuditLogTimeline', () => {
  it('shows loading state initially', () => {
    auditLogListMock.mockReturnValue(new Promise(() => {}));

    renderWithProviders(<AuditLogTimeline members={mockMembers} />);

    expect(screen.getByText('Loading activity...')).toBeInTheDocument();
  });

  it('shows empty state when no entries', async () => {
    auditLogListMock.mockResolvedValue({ data: [], meta: {} });

    renderWithProviders(<AuditLogTimeline members={mockMembers} />);

    await waitFor(() => {
      expect(screen.getByText('No activity yet')).toBeInTheDocument();
    });
  });

  it('renders audit log entries', async () => {
    auditLogListMock.mockResolvedValue({
      data: [
        {
          id: 'audit-1',
          actor_type: 'user',
          user_id: 'user-1',
          actor_id: 'user-1',
          action: 'session.created',
          resource_type: 'session',
          details: null,
          created_at: new Date(Date.now() - 5 * 60000).toISOString(),
        },
      ],
      meta: {},
    });

    renderWithProviders(<AuditLogTimeline members={mockMembers} />);

    await waitFor(() => {
      expect(screen.getByText('Alice')).toBeInTheDocument();
      expect(screen.getByText('created session')).toBeInTheDocument();
    });
  });

  it('shows error state on API failure', async () => {
    auditLogListMock.mockRejectedValue(new Error('Network error'));

    renderWithProviders(<AuditLogTimeline members={mockMembers} />);

    await waitFor(() => {
      expect(screen.getByText('Failed to load activity.')).toBeInTheDocument();
    });
  });

  it('passes filters to the API call', async () => {
    auditLogListMock.mockResolvedValue({ data: [], meta: {} });

    renderWithProviders(
      <AuditLogTimeline
        filters={{ session_id: 'sess-1' }}
        members={mockMembers}
        pageSize={5}
      />
    );

    await waitFor(() => {
      expect(auditLogListMock).toHaveBeenCalledWith(
        expect.objectContaining({ session_id: 'sess-1', limit: 5 })
      );
    });
  });

  it('loads older activity without replacing existing entries', async () => {
    auditLogListMock
      .mockResolvedValueOnce({
        data: [
          {
            id: 'audit-1',
            actor_type: 'user',
            user_id: 'user-1',
            actor_id: 'user-1',
            action: 'session.created',
            resource_type: 'session',
            details: null,
            created_at: new Date(Date.now() - 5 * 60000).toISOString(),
          },
        ],
        meta: { next_cursor: 'cursor-2' },
      })
      .mockResolvedValueOnce({
        data: [
          {
            id: 'audit-2',
            actor_type: 'user',
            user_id: 'user-1',
            actor_id: 'user-1',
            action: 'project.created',
            resource_type: 'project',
            details: null,
            created_at: new Date(Date.now() - 10 * 60000).toISOString(),
          },
        ],
        meta: {},
      });

    renderWithProviders(<AuditLogTimeline members={mockMembers} />);

    await waitFor(() => {
      expect(screen.getByText('created session')).toBeInTheDocument();
    });

    await userEvent.click(screen.getByRole('button', { name: 'Load older' }));

    await waitFor(() => {
      expect(screen.getByText('created session')).toBeInTheDocument();
      expect(screen.getByText('created project')).toBeInTheDocument();
    });
  });
});
