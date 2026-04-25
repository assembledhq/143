import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, waitFor } from '@/test/test-utils';
import SettingsLayout from './layout';

const useAuthMock = vi.hoisted(() => vi.fn());
vi.mock('@/hooks/use-auth', () => ({
  useAuth: useAuthMock,
}));

const replaceMock = vi.hoisted(() => vi.fn());
const pathnameMock = vi.hoisted(() => ({ value: '/settings/account' }));
vi.mock('next/navigation', () => ({
  useRouter: () => ({ replace: replaceMock, push: vi.fn() }),
  usePathname: () => pathnameMock.value,
}));

beforeEach(() => {
  replaceMock.mockReset();
  useAuthMock.mockReset();
  pathnameMock.value = '/settings/account';
});

describe('SettingsLayout', () => {
  it('renders children for admins on any settings page', () => {
    useAuthMock.mockReturnValue({ user: { role: 'admin' }, isLoading: false });
    pathnameMock.value = '/settings/integrations';

    renderWithProviders(
      <SettingsLayout>
        <div>child content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('child content')).toBeInTheDocument();
    expect(replaceMock).not.toHaveBeenCalled();
  });

  it('renders children for members on member-allowed pages', () => {
    useAuthMock.mockReturnValue({ user: { role: 'member' }, isLoading: false });
    pathnameMock.value = '/settings/team';

    renderWithProviders(
      <SettingsLayout>
        <div>child content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('child content')).toBeInTheDocument();
    expect(replaceMock).not.toHaveBeenCalled();
  });

  it('redirects members away from admin-only pages (LLM, General, Usage, etc.)', async () => {
    useAuthMock.mockReturnValue({ user: { role: 'member' }, isLoading: false });
    pathnameMock.value = '/settings/llm';

    renderWithProviders(
      <SettingsLayout>
        <div>secret admin content</div>
      </SettingsLayout>
    );

    await waitFor(() => expect(replaceMock).toHaveBeenCalledWith('/settings/account'));
    expect(screen.queryByText('secret admin content')).not.toBeInTheDocument();
  });

  it('lets members view /settings/integrations (read-only access)', () => {
    useAuthMock.mockReturnValue({ user: { role: 'member' }, isLoading: false });
    pathnameMock.value = '/settings/integrations';

    renderWithProviders(
      <SettingsLayout>
        <div>integrations content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('integrations content')).toBeInTheDocument();
    expect(replaceMock).not.toHaveBeenCalled();
  });

  it('lets members view /settings/agent (read-only access)', () => {
    useAuthMock.mockReturnValue({ user: { role: 'member' }, isLoading: false });
    pathnameMock.value = '/settings/agent';

    renderWithProviders(
      <SettingsLayout>
        <div>agent content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('agent content')).toBeInTheDocument();
    expect(replaceMock).not.toHaveBeenCalled();
  });

  it('redirects viewers from /settings/integrations and /settings/agent', async () => {
    useAuthMock.mockReturnValue({ user: { role: 'viewer' }, isLoading: false });
    pathnameMock.value = '/settings/integrations';

    renderWithProviders(
      <SettingsLayout>
        <div>integrations content</div>
      </SettingsLayout>
    );

    await waitFor(() => expect(replaceMock).toHaveBeenCalledWith('/settings/account'));
  });

  it('redirects non-admins from /settings (General) root path', async () => {
    useAuthMock.mockReturnValue({ user: { role: 'member' }, isLoading: false });
    pathnameMock.value = '/settings';

    renderWithProviders(
      <SettingsLayout>
        <div>general settings</div>
      </SettingsLayout>
    );

    await waitFor(() => expect(replaceMock).toHaveBeenCalledWith('/settings/account'));
  });

  it('redirects viewers away from /settings/team', async () => {
    useAuthMock.mockReturnValue({ user: { role: 'viewer' }, isLoading: false });
    pathnameMock.value = '/settings/team';

    renderWithProviders(
      <SettingsLayout>
        <div>team roster</div>
      </SettingsLayout>
    );

    await waitFor(() => expect(replaceMock).toHaveBeenCalledWith('/settings/account'));
  });

  it('lets members through to /settings/team', () => {
    useAuthMock.mockReturnValue({ user: { role: 'member' }, isLoading: false });
    pathnameMock.value = '/settings/team';

    renderWithProviders(
      <SettingsLayout>
        <div>team roster</div>
      </SettingsLayout>
    );

    expect(screen.getByText('team roster')).toBeInTheDocument();
    expect(replaceMock).not.toHaveBeenCalled();
  });

  it('does not redirect while auth is still loading', () => {
    useAuthMock.mockReturnValue({ user: null, isLoading: true });
    pathnameMock.value = '/settings/integrations';

    renderWithProviders(
      <SettingsLayout>
        <div>child content</div>
      </SettingsLayout>
    );

    expect(replaceMock).not.toHaveBeenCalled();
  });

  it('does not render tab navigation', () => {
    useAuthMock.mockReturnValue({ user: { role: 'admin' }, isLoading: false });

    renderWithProviders(
      <SettingsLayout>
        <div />
      </SettingsLayout>
    );

    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });
});
