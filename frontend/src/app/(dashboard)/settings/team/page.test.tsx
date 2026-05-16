import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent, within } from '@/test/test-utils';
import TeamSettingsPage from './page';

const {
  listMembersMock,
  listInvitationsMock,
  changeRoleMock,
  removeMemberMock,
  createInvitationMock,
  revokeInvitationMock,
  githubInviteStatusMock,
  searchGitHubUsersMock,
  currentUserMock,
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
        acceptance_method: 'email',
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
  githubInviteStatusMock: vi.fn().mockResolvedValue({ data: { connected: false } }),
  searchGitHubUsersMock: vi.fn().mockResolvedValue({ data: [], meta: {} }),
  currentUserMock: {
    id: 'user-1',
    email: 'admin@example.com',
    name: 'Admin User',
    role: 'admin',
  },
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
      githubInviteStatus: githubInviteStatusMock,
      searchGitHubUsers: searchGitHubUsersMock,
    },
    auth: {
      me: vi.fn().mockResolvedValue({ data: { id: 'user-1', email: 'admin@example.com', name: 'Admin User', role: 'admin' } }),
    },
  },
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => ({
    user: currentUserMock,
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
    githubInviteStatusMock.mockClear();
    searchGitHubUsersMock.mockClear();
    currentUserMock.id = 'user-1';
    currentUserMock.email = 'admin@example.com';
    currentUserMock.name = 'Admin User';
    currentUserMock.role = 'admin';
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

  it('renders the members in list format with column headers', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Admin User')).toBeInTheDocument();
    });

    const membersSection = screen.getByRole('heading', { name: 'Members' }).closest('section');
    expect(membersSection).not.toBeNull();

    const membersQueries = within(membersSection!);
    expect(membersQueries.getAllByText('Name', { selector: 'div' }).length).toBeGreaterThan(0);
    expect(membersQueries.getAllByText('Email', { selector: 'div' }).length).toBeGreaterThan(0);
    expect(membersQueries.getAllByText('Role', { selector: 'div' }).length).toBeGreaterThan(0);
    expect(membersQueries.getAllByText('Actions', { selector: 'div' }).length).toBeGreaterThan(0);
  });

  it('shows (you) label for the current user', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('(you)')).toBeInTheDocument();
    });
  });

  it('renders an Invite button and opens the invite modal', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Invite' })).toBeInTheDocument();
    });

    await userEvent.click(screen.getByRole('button', { name: 'Invite' }));

    expect(screen.getByText('Invite a member')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Email' })).toBeInTheDocument();
    expect(screen.getByText('Invite setup')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add email' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send invite' })).toBeDisabled();
  });

  it('uses consistent compact sizing for invite email input in modal', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Invite' }));

    const emailInput = await screen.findByRole('textbox', { name: 'Email' });
    expect(emailInput).toHaveClass('h-9');
  });

  it('uses the shared modal action sizing for the invite footer buttons', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Invite' }));

    const cancelButton = await screen.findByRole('button', { name: 'Cancel' });
    const sendInviteButton = screen.getByRole('button', { name: 'Send invite' });

    expect(cancelButton).toHaveClass('h-8');
    expect(sendInviteButton).toHaveClass('h-8');
    expect(sendInviteButton).not.toHaveClass('h-9');
  });

  it('renders pending invitations', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Pending invitations')).toBeInTheDocument();
    });

    expect(screen.getByText('new@example.com')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Revoke' })).toBeInTheDocument();
  });

  it('labels expired invitations so admins know the invitee cannot accept them', async () => {
    listInvitationsMock.mockResolvedValueOnce({
      data: [
        {
          id: 'inv-expired',
          email: null,
          github_username: 'malfrine-assembled',
          acceptance_method: 'github',
          role: 'member',
          status: 'expired',
          invited_by: { id: 'user-1', name: 'Admin User' },
          expires_at: '2026-02-01T00:00:00Z',
          created_at: '2026-01-01T00:00:00Z',
        },
      ],
      meta: {},
    });

    renderWithProviders(<TeamSettingsPage />);

    expect(await screen.findByText('@malfrine-assembled')).toBeInTheDocument();
    expect(screen.getByText('Expired')).toBeInTheDocument();
    expect(
      screen.getByText('The invitee will not see this invite. Revoke it and send a new one.'),
    ).toBeInTheDocument();
  });

  it('renders compact member metadata labels for mobile layouts', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Admin User')).toBeInTheDocument();
    });

    expect(screen.getAllByText('Email').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Role').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Actions').length).toBeGreaterThan(0);
  });

  it('adds an email invite draft before submission', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await user.click(await screen.findByRole('button', { name: 'Invite' }));

    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Email' })).toBeInTheDocument();
    });

    await user.type(screen.getByRole('textbox', { name: 'Email' }), 'newuser@test.com');
    await user.click(screen.getByRole('button', { name: 'Add email' }));

    expect(await screen.findByText('newuser@test.com')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Email' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Add email' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Change' })).toBeInTheDocument();

    await user.click(
      screen.getByRole('button', { name: 'Send invite to newuser@test.com' }),
    );

    await waitFor(() => {
      expect(createInvitationMock).toHaveBeenCalledWith({ email: 'newuser@test.com', role: 'member' });
    });
  });

  it('offers builder as an invite role option', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await user.click(await screen.findByRole('button', { name: 'Invite' }));
    await user.click(await screen.findByLabelText('Role'));

    expect(await screen.findByRole('option', { name: 'Builder' })).toBeInTheDocument();
  });

  it('offers builder in the member role selector for admins', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await user.click(await screen.findByLabelText('Role for Member User'));

    expect(await screen.findByRole('option', { name: 'Builder' })).toBeInTheDocument();
  });

  it('shows informative self action text instead of a dash placeholder', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Admin User')).toBeInTheDocument();
    });

    expect(screen.queryByText('—')).not.toBeInTheDocument();
    expect(screen.getByText('Current user')).toBeInTheDocument();
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

  it('prompts for confirmation before changing another member role', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    const roleSelectTrigger = await screen.findByRole('combobox', {
      name: 'Role for Member User',
    });

    await user.click(roleSelectTrigger);
    await user.click(await screen.findByRole('option', { name: 'Viewer' }));

    // A confirmation AlertDialog must appear before the API is called.
    expect(await screen.findByRole('alertdialog')).toBeInTheDocument();
    expect(changeRoleMock).not.toHaveBeenCalled();

    // Confirm and verify the role change fires with the new value.
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    await waitFor(() => {
      expect(changeRoleMock).toHaveBeenCalledWith('user-2', 'viewer');
    });
  });

  it('does not change the role when the confirmation dialog is cancelled', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    const roleSelectTrigger = await screen.findByRole('combobox', {
      name: 'Role for Member User',
    });

    await user.click(roleSelectTrigger);
    await user.click(await screen.findByRole('option', { name: 'Viewer' }));

    await screen.findByRole('alertdialog');
    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    expect(changeRoleMock).not.toHaveBeenCalled();
  });

  it('disables management actions for non-admin users', async () => {
    currentUserMock.id = 'user-2';
    currentUserMock.email = 'member@example.com';
    currentUserMock.name = 'Member User';
    currentUserMock.role = 'member';

    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Member User')).toBeInTheDocument();
    });

    expect(screen.queryByRole('button', { name: 'Invite' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Remove' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Revoke' })).not.toBeInTheDocument();
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
    const avatarFallbacks = document.querySelectorAll('.h-8.w-8.rounded-full');
    expect(avatarFallbacks.length).toBeGreaterThanOrEqual(1);
  });

  it('adds a github invite draft via the fallback input when GitHub is not connected', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await user.click(await screen.findByRole('button', { name: 'Invite' }));
    await user.click(screen.getByRole('tab', { name: 'GitHub username' }));

    expect(
      await screen.findByText('Connect a GitHub App to search for users.'),
    ).toBeInTheDocument();

    const input = screen.getByPlaceholderText('octocat');
    await user.type(input, '@octocat');
    await user.click(screen.getByRole('button', { name: 'Add GitHub username' }));

    expect(await screen.findByText('@octocat')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Change' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Send invite to @octocat' }));

    await waitFor(() => {
      expect(createInvitationMock).toHaveBeenCalledWith({
        github_username: 'octocat',
        acceptance_method: 'github',
        role: 'member',
      });
    });
  });

  it('sends GitHub invites with a durable notification email while requiring GitHub acceptance', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await user.click(await screen.findByRole('button', { name: 'Invite' }));
    await user.click(screen.getByRole('tab', { name: 'GitHub username' }));

    await user.type(screen.getByPlaceholderText('octocat'), 'octocat');
    await user.type(screen.getByLabelText('Notification email'), 'octo@example.com');
    await user.click(screen.getByRole('button', { name: 'Add GitHub username' }));

    expect(await screen.findByText('@octocat')).toBeInTheDocument();
    expect(screen.getByText(/octo@example\.com/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Send invite to @octocat' }));

    await waitFor(() => {
      expect(createInvitationMock).toHaveBeenCalledWith({
        email: 'octo@example.com',
        github_username: 'octocat',
        acceptance_method: 'github',
        role: 'member',
      });
    });
  });

  it('keeps GitHub submit disabled until an invite draft exists', async () => {
    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await user.click(await screen.findByRole('button', { name: 'Invite' }));
    await user.click(screen.getByRole('tab', { name: 'GitHub username' }));
    expect(screen.getByRole('button', { name: 'Send invite' })).toBeDisabled();
    expect(createInvitationMock).not.toHaveBeenCalled();
  });

  it('moves a selected GitHub user into the invite setup when connected', async () => {
    githubInviteStatusMock.mockResolvedValue({ data: { connected: true } });
    searchGitHubUsersMock.mockResolvedValue({
      data: [{ login: 'octocat', avatar_url: 'https://example.com/a.png' }],
      meta: {},
    });

    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await user.click(await screen.findByRole('button', { name: 'Invite' }));
    await user.click(screen.getByRole('tab', { name: 'GitHub username' }));

    const commandInput = await screen.findByPlaceholderText(
      'Search GitHub users...',
    );
    await user.type(commandInput, 'octo');

    const suggestion = await screen.findByText('@octocat');
    await user.click(suggestion);

    expect(await screen.findByText('Invite setup')).toBeInTheDocument();
    expect(screen.getByText('@octocat')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Change' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Send invite to @octocat' }));

    await waitFor(() => {
      expect(createInvitationMock).toHaveBeenCalledWith({
        github_username: 'octocat',
        acceptance_method: 'github',
        role: 'member',
      });
    });
  }, 15000);

  it('shows "no users found" fallback when GitHub search returns empty', async () => {
    githubInviteStatusMock.mockResolvedValue({ data: { connected: true } });
    searchGitHubUsersMock.mockResolvedValue({ data: [], meta: {} });

    const user = userEvent.setup();
    renderWithProviders(<TeamSettingsPage />);

    await user.click(await screen.findByRole('button', { name: 'Invite' }));
    await user.click(screen.getByRole('tab', { name: 'GitHub username' }));

    const commandInput = await screen.findByPlaceholderText(
      'Search GitHub users...',
    );
    await user.type(commandInput, 'ghost');

    await waitFor(() => {
      expect(screen.getByText(/No users found\./)).toBeInTheDocument();
    });
  }, 15000);

  it('renders pending invitation using GitHub username when email is null', async () => {
    listInvitationsMock.mockResolvedValueOnce({
      data: [
        {
          id: 'inv-2',
          email: null,
          github_username: 'octocat',
          acceptance_method: 'github',
          role: 'member',
          status: 'pending',
          invited_by: { id: 'user-1', name: 'Admin User' },
          expires_at: '2026-03-01T00:00:00Z',
          created_at: '2026-02-01T00:00:00Z',
        },
      ],
      meta: {},
    });

    renderWithProviders(<TeamSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('@octocat')).toBeInTheDocument();
    });
  });
});
