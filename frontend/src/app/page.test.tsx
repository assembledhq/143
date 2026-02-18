import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, userEvent } from '@/test/test-utils';
import Overview from './page';

const { loginMock, sentryLoginMock } = vi.hoisted(() => ({
  loginMock: vi.fn(),
  sentryLoginMock: vi.fn(),
}));

vi.mock('@/lib/api', () => ({
  api: {
    auth: {
      login: loginMock,
      loginSentry: sentryLoginMock,
    },
  },
}));

describe('OverviewPage', () => {
  beforeEach(() => {
    loginMock.mockReset();
    sentryLoginMock.mockReset();
  });

  it('starts GitHub onboarding directly from the dashboard', async () => {
    const user = userEvent.setup();

    renderWithProviders(<Overview />);

    await user.click(screen.getByRole('button', { name: 'Connect GitHub' }));

    expect(loginMock).toHaveBeenCalledTimes(1);
  });

  it('starts Sentry onboarding directly from the dashboard', async () => {
    const user = userEvent.setup();

    renderWithProviders(<Overview />);

    await user.click(screen.getByRole('button', { name: 'Connect Sentry' }));

    expect(sentryLoginMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('heading', { name: 'Integrations' })).not.toBeInTheDocument();
  });

  it('shows Linear integration on the dashboard', () => {
    renderWithProviders(<Overview />);

    expect(screen.getByText('Connect Linear')).toBeInTheDocument();
    expect(screen.getByText('Coming soon')).toBeInTheDocument();
  });
});
