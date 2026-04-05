import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, waitFor } from '@/test/test-utils';
import SettingsPage from './page';

const {
  settingsGetMock,
  githubStatusGetMock,
} = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: 'Test Org',
      settings: {},
    },
  }),
  githubStatusGetMock: vi.fn().mockResolvedValue({
    connected: false,
    has_repo_scope: false,
    pr_authorship_mode: 'user_preferred',
  }),
}));

vi.mock('@/lib/api', () => ({
  api: {
    settings: {
      get: settingsGetMock,
    },
    githubStatus: {
      get: githubStatusGetMock,
      connect: vi.fn(),
      disconnect: vi.fn(),
    },
  },
}));

vi.mock('next-themes', () => ({
  useTheme: () => ({ theme: 'system', setTheme: vi.fn() }),
}));

describe('SettingsPage', () => {
  beforeEach(() => {
    settingsGetMock.mockClear();
    settingsGetMock.mockResolvedValue({
      data: {
        name: 'Test Org',
        settings: {},
      },
    });
  });

  it('renders the General section with organization name', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('General')).toBeInTheDocument();
    });

    expect(screen.getByLabelText('Organization name')).toBeInTheDocument();
  });

  it('displays the organization name from server', async () => {
    settingsGetMock.mockResolvedValue({
      data: {
        name: 'My Org',
        settings: {},
      },
    });

    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByLabelText('Organization name')).toHaveValue('My Org');
    });
  });

  it('has the organization name field disabled', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByLabelText('Organization name')).toBeDisabled();
    });
  });

  it('renders the Appearance section with theme selector', async () => {
    renderWithProviders(<SettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Appearance')).toBeInTheDocument();
    });

    expect(screen.getByText('Theme')).toBeInTheDocument();
    expect(screen.getByText('Select your preferred color scheme')).toBeInTheDocument();
  });
});
