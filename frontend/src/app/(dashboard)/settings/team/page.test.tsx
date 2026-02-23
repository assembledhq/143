import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import TeamSettingsPage from './page';

const {
  listMembersMock,
  listInvitationsMock,
  changeRoleMock,
  removeMemberMock,
  createInvitationMock,
  revokeInvitationMock,
} = vi.hoisted(() => ({
  listMembersMock: vi.fn().mockResolvedValue({
    data: [
      {
        id: 'user-1',
        org_id: 'org-1',
        email: 'admin@example.com',
        name: 'Admin User',
        role: 'admin',
        avatar_url: 'https://example.com/avatar.png',
        created_at: '2026-01-01T00:00:00Z',
      },
      {
        id: 'user-2',
        org_id: 'org-1',
        email: 'member@example.com',
        name: 'Member User',
        role: 'member',
        avatar_url: null,
        created_at: '2026-01-02T00:00:00Z',
      },
    ],
    meta: {},
  }),
  listInvitationsMock: vi.fn().mockResolvedValue({
    data: [
      {
        id: 'inv-1',
        email: 'new@example.com',
        role: 'member',
        status: 'pending',
        invited_by: { id: 'user-1', name: 'Admin User' },
        expires_at: '2026-03-01T00:00:00Z',
        created_at: '2026-02-01T00:00:00Z',
      },
    ],
    meta: {},
  }),
  changeRoleMock: vi.fn().mockResolvedValue({ data: {} }),
  removeMemberMock: vi.fn().mockResolvedValue(undefined),
  createInvitationMock: vi.fn().mockResolvedValue({ data: {} }),
  revokeInvitationMock: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('@/lib/api', () => ({
  api: {
    team: {
      listMembers: listMembersMock,
      listInvitations: listInvitationsMock,
      changeRole: changeRoleMock,
      removeMember: removeMemberMock,
      createInvitation: createInvitationMock,
      revokeInvitation: revokeInvitationMock,
    },
    auth: {
      me: vi.fn().mockResolvedValue({ data: { id: 'user-1', email: 'admin@example.com', name: 'Admin User', role: 'admin' } }),
    },
  },
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => ({
    user: { id: 'user-1', email: 'admin@example.com', name: 'Admin User', role: 'admin' },
    isLoading: false,
  }),
}));

describe('TeamSettingsPage', () => {
  beforeEach(() => {
    listMembersMock.mockClear();
    listInvitationsMock.mockClear();
    changeRoleMock.mockClear();
    removeMemberMock.mockClear();
    createInvitationMock.mockClear();
    revokeInvitationMock.mockClear();
  });

  it('renders the Members section with team members', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Admin User')).toBeInTheDocument();
    });

    expect(screen.getByText('Member User')).toBeInTheDocument();
    expect(screen.getByText('admin@example.com')).toBeInTheDocument();
    expect(screen.getByText('member@example.com')).toBeInTheDocument();
  });

  it('shows (you) label for the current user', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('(you)')).toBeInTheDocument();
    });
  });

  it('renders the Invite a Member form', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Invite a Member')).toBeInTheDocument();
    });

    expect(screen.getByLabelText('Email')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send Invite' })).toBeInTheDocument();
  });

  it('renders pending invitations', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Pending Invitations')).toBeInTheDocument();
    });

    expect(screen.getByText('new@example.com')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Revoke' })).toBeInTheDocument();
  });

  it('submits invitation form', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByLabelText('Email')).toBeInTheDocument();
    });

    await user.type(screen.getByLabelText('Email'), 'newuser@test.com');
    await user.click(screen.getByRole('button', { name: 'Send Invite' }));

    await waitFor(() => {
      expect(createInvitationMock).toHaveBeenCalledWith('newuser@test.com', 'member');
    });
  });

  it('shows Remove button for non-self members', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Member User')).toBeInTheDocument();
    });

    // Remove button should exist for the other member
    expect(screen.getByRole('button', { name: 'Remove' })).toBeInTheDocument();
  });

  it('shows confirmation dialog when Remove is clicked and calls removeMember on confirm', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Member User')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Remove' }));

    await waitFor(() => {
      expect(screen.getByText('Remove member')).toBeInTheDocument();
    });

    expect(screen.getByText(/Are you sure you want to remove Member User/)).toBeInTheDocument();

    // Confirm removal
    const confirmButtons = screen.getAllByRole('button', { name: 'Remove' });
    const confirmButton = confirmButtons[confirmButtons.length - 1];
    await user.click(confirmButton);

    await waitFor(() => {
      expect(removeMemberMock).toHaveBeenCalledWith('user-2');
    });
  });

  it('cancels member removal when Cancel is clicked in confirmation dialog', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Member User')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Remove' }));

    await waitFor(() => {
      expect(screen.getByText('Remove member')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    await waitFor(() => {
      expect(screen.queryByText('Remove member')).not.toBeInTheDocument();
    });

    expect(removeMemberMock).not.toHaveBeenCalled();
  });

  it('calls revoke when Revoke button is clicked on pending invitation', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Revoke' })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Revoke' }));

    await waitFor(() => {
      expect(revokeInvitationMock).toHaveBeenCalledWith('inv-1');
    });
  });

  it('shows role badge for self and role select for others', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Admin User')).toBeInTheDocument();
    });

    // Both admin badge and select option contain "Admin" text
    expect(screen.getAllByText('Admin').length).toBeGreaterThanOrEqual(1);
    // Other user should show "Member" role
    expect(screen.getAllByText('Member').length).toBeGreaterThanOrEqual(1);
  });

  it('shows loading state when members are loading', () => {
    listMembersMock.mockReturnValueOnce(new Promise(() => {})); // Never resolves
    renderWithProviders(<TeamSettingsPage />);

    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it('shows avatar fallback for member without avatar_url', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Member User')).toBeInTheDocument();
    });

    // The avatar fallback shows the first letter of the name
    const avatarFallbacks = document.querySelectorAll('.rounded-full.bg-muted');
    expect(avatarFallbacks.length).toBeGreaterThanOrEqual(1);
  });
});
