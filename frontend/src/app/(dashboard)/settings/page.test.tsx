import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, waitFor } from '@/test/test-utils';
import SettingsPage from './page';

const {
  settingsGetMock,
} = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: 'Test Org',
      settings: {},
    },
  }),
}));

vi.mock('@/lib/api', () => ({
  api: {
    settings: {
      get: settingsGetMock,
    },
  },
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
});
