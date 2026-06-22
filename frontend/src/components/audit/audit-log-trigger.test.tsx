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
    team: {
      listMembers: vi.fn().mockResolvedValue({ data: [], meta: {} }),
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

  it('inline variant: renders a leading separator dot and no icon', async () => {
    auditLogListMock.mockResolvedValue({
      data: [{
        id: 'audit-inline',
        actor_type: 'user',
        user_id: 'user-1',
        action: 'session.created',
        created_at: new Date(Date.now() - 2 * 60000).toISOString(),
      }],
      meta: {},
    });

    const { container } = renderWithProviders(
      <AuditLogTrigger filters={{ session_id: 'sess-1' }} members={mockMembers} variant="inline" />
    );

    await waitFor(() => {
      expect(screen.getByText(/Updated.*ago by Alice/)).toBeInTheDocument();
    });

    const separator = container.querySelector('span[aria-hidden="true"]');
    expect(separator?.textContent).toBe('·');
    expect(container.querySelector('svg')).toBeNull();
  });

  it('footer variant: renders as low-priority last activity text', async () => {
    auditLogListMock.mockResolvedValue({
      data: [{
        id: 'audit-footer',
        actor_type: 'user',
        user_id: 'user-1',
        action: 'settings.updated',
        created_at: new Date(Date.now() - 2 * 60000).toISOString(),
      }],
      meta: {},
    });

    const { container } = renderWithProviders(
      <AuditLogTrigger filters={{ resource_type: 'settings' }} members={mockMembers} variant="footer" />
    );

    await waitFor(() => {
      expect(screen.getByText(/Last activity:/)).toBeInTheDocument();
      expect(screen.getByText(/Updated.*ago by Alice/)).toBeInTheDocument();
    });

    expect(container.querySelector('footer')).not.toBeNull();
    expect(container.querySelector('svg')).not.toBeNull();
  });

  it('footer variant: chooses the newest entry across multiple resource scopes', async () => {
    auditLogListMock.mockImplementation((params: { resource_type?: string }) => {
      if (params.resource_type === 'settings') {
        return Promise.resolve({
          data: [{
            id: 'audit-settings',
            actor_type: 'user',
            user_id: 'user-1',
            action: 'settings.updated',
            created_at: new Date(Date.now() - 10 * 60000).toISOString(),
          }],
          meta: {},
        });
      }
      return Promise.resolve({
        data: [{
          id: 'audit-credential',
          actor_type: 'system',
          actor_id: 'system',
          action: 'credential.updated',
          created_at: new Date(Date.now() - 2 * 60000).toISOString(),
        }],
        meta: {},
      });
    });

    renderWithProviders(
      <AuditLogTrigger
        filters={[{ resource_type: 'settings' }, { resource_type: 'credential' }]}
        members={mockMembers}
        variant="footer"
      />
    );

    await waitFor(() => {
      expect(screen.getByText(/Last activity:/)).toBeInTheDocument();
      expect(screen.getByText(/Updated.*ago by System/)).toBeInTheDocument();
    });
    expect(auditLogListMock).toHaveBeenCalledWith({ resource_type: 'settings', limit: 1 });
    expect(auditLogListMock).toHaveBeenCalledWith({ resource_type: 'credential', limit: 1 });
  });

  it('default variant: renders the Clock icon and no separator', async () => {
    auditLogListMock.mockResolvedValue({
      data: [{
        id: 'audit-default',
        actor_type: 'user',
        user_id: 'user-1',
        action: 'session.created',
        created_at: new Date(Date.now() - 2 * 60000).toISOString(),
      }],
      meta: {},
    });

    const { container } = renderWithProviders(
      <AuditLogTrigger filters={{ session_id: 'sess-1' }} members={mockMembers} />
    );

    await waitFor(() => {
      expect(screen.getByText(/Updated.*ago by Alice/)).toBeInTheDocument();
    });

    expect(container.querySelector('span[aria-hidden="true"]')).toBeNull();
    expect(container.querySelector('svg')).not.toBeNull();
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
