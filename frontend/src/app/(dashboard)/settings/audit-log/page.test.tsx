import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import AuditLogPage from './page';

const {
  auditLogListMock,
  listMembersMock,
  currentUserMock,
} = vi.hoisted(() => ({
  auditLogListMock: vi.fn().mockResolvedValue({
    data: [
      {
        id: 'audit-1',
        org_id: 'org-1',
        actor_type: 'user',
        actor_id: 'user-1',
        user_id: 'user-1',
        action: 'session.created',
        resource_type: 'session',
        resource_id: 'sess-1',
        details: null,
        ip_address: null,
        user_agent: null,
        request_id: null,
        session_id: null,
        project_id: null,
        created_at: '2026-03-15T10:00:00Z',
      },
    ],
    meta: {},
  }),
  listMembersMock: vi.fn().mockResolvedValue({
    data: [
      {
        id: 'user-1',
        org_id: 'org-1',
        email: 'admin@example.com',
        name: 'Admin User',
        role: 'admin',
        created_at: '2026-01-01T00:00:00Z',
      },
    ],
    meta: {},
  }),
  currentUserMock: {
    id: 'user-1',
    email: 'admin@example.com',
    name: 'Admin User',
    role: 'admin',
  },
}));

vi.mock('@/lib/api', () => ({
  api: {
    auditLogs: {
      list: auditLogListMock,
    },
    team: {
      listMembers: listMembersMock,
    },
  },
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => ({
    user: currentUserMock,
    isLoading: false,
  }),
}));

describe('AuditLogPage', () => {
  beforeEach(() => {
    auditLogListMock.mockClear();
    listMembersMock.mockClear();
    currentUserMock.role = 'admin';
  });

  it('renders the page header', async () => {
    renderWithProviders(<AuditLogPage />);

    await waitFor(() => {
      expect(screen.getByText('Audit log')).toBeInTheDocument();
    });
  });

  it('renders audit log entries from the API', async () => {
    renderWithProviders(<AuditLogPage />);

    await waitFor(() => {
      expect(screen.getByText('Admin User')).toBeInTheDocument();
      expect(screen.getByText('created session')).toBeInTheDocument();
    });
  });

  it('shows empty state when no entries exist', async () => {
    auditLogListMock.mockResolvedValue({ data: [], meta: {} });

    renderWithProviders(<AuditLogPage />);

    await waitFor(() => {
      expect(screen.getByText('No audit log entries found')).toBeInTheDocument();
    });
  });

  it('blocks non-admin users from viewing audit logs', async () => {
    currentUserMock.role = 'member';

    renderWithProviders(<AuditLogPage />);

    await waitFor(() => {
      expect(screen.getByText('Only admins can view audit logs.')).toBeInTheDocument();
    });

    expect(auditLogListMock).not.toHaveBeenCalled();
  });

  it('renders filter dropdowns', async () => {
    renderWithProviders(<AuditLogPage />);

    await waitFor(() => {
      expect(screen.getByText('All resources')).toBeInTheDocument();
      expect(screen.getByText('All actions')).toBeInTheDocument();
      expect(screen.getByText('All actors')).toBeInTheDocument();
    });
  });

  it('appends older entries instead of replacing the feed', async () => {
    auditLogListMock
      .mockResolvedValueOnce({
        data: [
          {
            id: 'audit-1',
            org_id: 'org-1',
            actor_type: 'user',
            actor_id: 'user-1',
            user_id: 'user-1',
            action: 'session.created',
            resource_type: 'session',
            resource_id: 'sess-1',
            details: null,
            ip_address: null,
            user_agent: null,
            request_id: null,
            session_id: null,
            project_id: null,
            created_at: '2026-03-15T10:00:00Z',
          },
        ],
        meta: { next_cursor: 'cursor-2' },
      })
      .mockResolvedValueOnce({
        data: [
          {
            id: 'audit-2',
            org_id: 'org-1',
            actor_type: 'user',
            actor_id: 'user-1',
            user_id: 'user-1',
            action: 'project.created',
            resource_type: 'project',
            resource_id: 'proj-1',
            details: null,
            ip_address: null,
            user_agent: null,
            request_id: null,
            session_id: null,
            project_id: null,
            created_at: '2026-03-15T09:00:00Z',
          },
        ],
        meta: {},
      });

    renderWithProviders(<AuditLogPage />);

    await waitFor(() => {
      expect(screen.getByText('created session')).toBeInTheDocument();
    });

    expect(screen.queryByText('Latest first')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Back to newest' })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: 'Load more' }));

    await waitFor(() => {
      expect(screen.getByText('created session')).toBeInTheDocument();
      expect(screen.getByText('created project')).toBeInTheDocument();
    });
  });
});
