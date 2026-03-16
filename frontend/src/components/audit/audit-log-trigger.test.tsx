import { describe, it, expect, vi } from 'vitest';
import { renderWithProviders, screen, waitFor } from '@/test/test-utils';
import { AuditLogTrigger } from './audit-log-trigger';
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

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => ({
    user: { id: 'user-1', role: 'admin' },
    isLoading: false,
  }),
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

describe('AuditLogTrigger', () => {
  it('renders nothing when there are no audit entries', async () => {
    auditLogListMock.mockResolvedValue({ data: [], meta: {} });

    const { container } = renderWithProviders(
      <AuditLogTrigger filters={{ session_id: 'sess-1' }} members={mockMembers} />
    );

    await waitFor(() => {
      expect(auditLogListMock).toHaveBeenCalled();
    });

    expect(container.innerHTML).toBe('');
  });

  it('renders trigger with actor name and relative time', async () => {
    auditLogListMock.mockResolvedValue({
      data: [{
        id: 'audit-1',
        actor_type: 'user',
        user_id: 'user-1',
        action: 'session.created',
        created_at: new Date(Date.now() - 3 * 60000).toISOString(),
      }],
      meta: {},
    });

    renderWithProviders(
      <AuditLogTrigger filters={{ session_id: 'sess-1' }} members={mockMembers} />
    );

    await waitFor(() => {
      expect(screen.getByText(/Updated.*ago by Alice/)).toBeInTheDocument();
    });
  });

  it('renders system actor label for non-user actors', async () => {
    auditLogListMock.mockResolvedValue({
      data: [{
        id: 'audit-2',
        actor_type: 'system',
        actor_id: 'system',
        action: 'session.completed',
        created_at: new Date(Date.now() - 10 * 60000).toISOString(),
      }],
      meta: {},
    });

    renderWithProviders(
      <AuditLogTrigger filters={{ session_id: 'sess-1' }} members={mockMembers} />
    );

    await waitFor(() => {
      expect(screen.getByText(/by System/)).toBeInTheDocument();
    });
  });
});
