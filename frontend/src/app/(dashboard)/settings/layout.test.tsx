import { describe, it, expect, vi, beforeEach } from 'vitest';
import { useEffect } from 'react';
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
  function EffectChild({ onMount }: { onMount: () => void }) {
    useEffect(() => {
      onMount();
    }, [onMount]);

    return <div>child content</div>;
  }

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

  it('redirects viewers from /settings/evals', async () => {
    useAuthMock.mockReturnValue({ user: { role: 'viewer' }, isLoading: false });
    pathnameMock.value = '/settings/evals';

    renderWithProviders(
      <SettingsLayout>
        <div>evals content</div>
      </SettingsLayout>
    );

    await waitFor(() => expect(replaceMock).toHaveBeenCalledWith('/settings/account'));
  });

  it('lets members view /settings/evals', () => {
    useAuthMock.mockReturnValue({ user: { role: 'member' }, isLoading: false });
    pathnameMock.value = '/settings/evals';

    renderWithProviders(
      <SettingsLayout>
        <div>evals content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('evals content')).toBeInTheDocument();
    expect(replaceMock).not.toHaveBeenCalled();
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

  it('redirects builders away from /settings/team', async () => {
    useAuthMock.mockReturnValue({ user: { role: 'builder' }, isLoading: false });
    pathnameMock.value = '/settings/team';

    renderWithProviders(
      <SettingsLayout>
        <div>team roster</div>
      </SettingsLayout>
    );

    await waitFor(() => expect(replaceMock).toHaveBeenCalledWith('/settings/account'));
  });

  it('lets builders through to /settings/agent', () => {
    useAuthMock.mockReturnValue({ user: { role: 'builder' }, isLoading: false });
    pathnameMock.value = '/settings/agent';

    renderWithProviders(
      <SettingsLayout>
        <div>agent settings</div>
      </SettingsLayout>
    );

    expect(screen.getByText('agent settings')).toBeInTheDocument();
    expect(replaceMock).not.toHaveBeenCalled();
  });

  it('does not mount children for access-controlled pages while auth is still loading', () => {
    useAuthMock.mockReturnValue({ user: null, isLoading: true });
    pathnameMock.value = '/settings/team';
    const onMount = vi.fn();

    renderWithProviders(
      <SettingsLayout>
        <EffectChild onMount={onMount} />
      </SettingsLayout>
    );

    expect(replaceMock).not.toHaveBeenCalled();
    expect(onMount).not.toHaveBeenCalled();
    expect(screen.queryByText('child content')).not.toBeInTheDocument();
  });

  it('still renders unrestricted pages while auth is still loading', () => {
    useAuthMock.mockReturnValue({ user: null, isLoading: true });
    pathnameMock.value = '/settings/account';

    renderWithProviders(
      <SettingsLayout>
        <div>child content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('child content')).toBeInTheDocument();
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

  it('adds shared bottom scroll padding for settings page containers', () => {
    useAuthMock.mockReturnValue({ user: { role: 'admin' }, isLoading: false });

    const { container } = renderWithProviders(
      <SettingsLayout>
        <div data-slot="page-container">settings content</div>
      </SettingsLayout>
    );

    const paddingScope = container.querySelector('[data-slot="settings-layout-padding-scope"]');
    expect(paddingScope).not.toBeNull();
    expect(paddingScope).toHaveClass('[&_[data-slot=page-container]]:pb-24');
    expect(paddingScope).toHaveClass('md:[&_[data-slot=page-container]]:pb-20');
  });
});
