import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithProviders, screen, userEvent, waitFor } from '@/test/test-utils';
import LoginPage from './page';

function createLocationMock() {
  let href = 'http://localhost/login';

  return {
    pathname: '/login',
    origin: 'http://localhost',
    protocol: 'http:',
    host: 'localhost',
    hostname: 'localhost',
    port: '',
    search: '',
    hash: '',
    get href() {
      return href;
    },
    set href(value: string) {
      href = value.startsWith('http://') || value.startsWith('https://')
        ? value
        : `http://localhost${value}`;
    },
  };
}

const loginMock = vi.hoisted(() => vi.fn());
const loginGoogleMock = vi.hoisted(() => vi.fn());
const loginEmailMock = vi.hoisted(() => vi.fn());
const registerMock = vi.hoisted(() => vi.fn());

const useAuthMock = vi.hoisted(() => vi.fn());
const useAuthProvidersMock = vi.hoisted(() => vi.fn());
const searchParamsMock = vi.hoisted(() => new URLSearchParams());

vi.mock('@/lib/api', () => ({
  api: {
    auth: {
      login: loginMock,
      loginGoogle: loginGoogleMock,
      loginEmail: loginEmailMock,
      register: registerMock,
    },
  },
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: useAuthMock,
  useAuthProviders: useAuthProvidersMock,
}));

// Mock useRouter
const pushMock = vi.hoisted(() => vi.fn());
const replaceMock = vi.hoisted(() => vi.fn());
vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: pushMock, replace: replaceMock }),
  useSearchParams: () => searchParamsMock,
}));

describe('LoginPage', () => {
  const originalLocation = window.location;

  beforeEach(() => {
    loginMock.mockReset();
    loginGoogleMock.mockReset();
    loginEmailMock.mockReset();
    registerMock.mockReset();
    pushMock.mockReset();
    replaceMock.mockReset();
    Array.from(searchParamsMock.keys()).forEach((key) => {
      searchParamsMock.delete(key);
    });

    useAuthMock.mockReturnValue({
      user: null,
      isLoading: false,
      isAuthenticated: false,
      logout: vi.fn(),
    });
    useAuthProvidersMock.mockReturnValue({
      providers: { github: true, google: true, email: true },
      isLoading: false,
    });

    Object.defineProperty(window, 'location', {
      value: createLocationMock(),
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    Object.defineProperty(window, 'location', {
      value: originalLocation,
      writable: true,
      configurable: true,
    });
  });

  it('renders Sign In and Sign Up tabs', () => {
    renderWithProviders(<LoginPage />);

    expect(screen.getByRole('tab', { name: 'Sign in' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Sign up' })).toBeInTheDocument();
  });

  it('keeps the login form visible while auth status is still loading', () => {
    useAuthMock.mockReturnValue({
      user: null,
      isLoading: true,
      isAuthenticated: false,
      logout: vi.fn(),
    });

    renderWithProviders(<LoginPage />);

    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('shows GitHub button', () => {
    renderWithProviders(<LoginPage />);

    expect(screen.getByRole('button', { name: 'Continue with GitHub' })).toBeInTheDocument();
  });

  it('shows Google button when provider is configured', () => {
    renderWithProviders(<LoginPage />);

    expect(screen.getByRole('button', { name: 'Continue with Google' })).toBeInTheDocument();
  });

  it('hides Google button when provider is not configured', () => {
    useAuthProvidersMock.mockReturnValue({
      providers: { github: true, google: false, email: true },
      isLoading: false,
    });

    renderWithProviders(<LoginPage />);

    expect(screen.queryByRole('button', { name: 'Continue with Google' })).not.toBeInTheDocument();
  });

  it('shows demo banner with credentials from /providers when demo mode is on', () => {
    useAuthProvidersMock.mockReturnValue({
      providers: {
        github: false,
        google: false,
        email: true,
        demo: true,
        demo_email: 'dogfood@143.dev',
        demo_password: 'preview-dogfood',
      },
      isLoading: false,
    });

    renderWithProviders(<LoginPage />);

    const banner = screen.getByTestId('demo-banner');
    expect(banner).toHaveTextContent('Demo environment');
    expect(banner).toHaveTextContent('dogfood@143.dev');
    expect(banner).toHaveTextContent('preview-dogfood');
  });

  it('renders banner text returned by /providers verbatim (server is source of truth)', () => {
    useAuthProvidersMock.mockReturnValue({
      providers: {
        github: false,
        google: false,
        email: true,
        demo: true,
        demo_email: 'override@example.com',
        demo_password: 'override-pw',
      },
      isLoading: false,
    });

    renderWithProviders(<LoginPage />);

    const banner = screen.getByTestId('demo-banner');
    expect(banner).toHaveTextContent('override@example.com');
    expect(banner).toHaveTextContent('override-pw');
    expect(banner).not.toHaveTextContent('dogfood@143.dev');
  });

  it('hides demo banner when demo mode is off', () => {
    renderWithProviders(<LoginPage />);

    expect(screen.queryByTestId('demo-banner')).not.toBeInTheDocument();
  });

  it('hides demo banner when demo is on but credentials are missing', () => {
    useAuthProvidersMock.mockReturnValue({
      providers: { github: false, google: false, email: true, demo: true },
      isLoading: false,
    });

    renderWithProviders(<LoginPage />);

    expect(screen.queryByTestId('demo-banner')).not.toBeInTheDocument();
  });

  it('calls login on GitHub button click', async () => {
    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    await user.click(screen.getByRole('button', { name: 'Continue with GitHub' }));
    expect(loginMock).toHaveBeenCalledTimes(1);
  });

  it('calls loginGoogle on Google button click', async () => {
    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    await user.click(screen.getByRole('button', { name: 'Continue with Google' }));
    expect(loginGoogleMock).toHaveBeenCalledTimes(1);
  });

  it('submits email sign-in form', async () => {
    loginEmailMock.mockResolvedValue({ data: { id: '1' } });
    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    await user.type(screen.getByLabelText('Email', { exact: false }), 'test@example.com');
    await user.type(screen.getByLabelText('Password', { exact: false }), 'password123');
    const locationMock = window.location as unknown as ReturnType<typeof createLocationMock>;
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(loginEmailMock).toHaveBeenCalledWith('test@example.com', 'password123');
    expect(new URL(locationMock.href).pathname).toBe('/sessions');
  });

  it('returns invited email sign-in flows to /invite/accept so the session can claim the invite', async () => {
    loginEmailMock.mockResolvedValue({ data: { id: '1' } });
    searchParamsMock.set('invitation', 'invite-123');
    searchParamsMock.set('email', 'invitee@example.com');
    searchParamsMock.set('org', 'Acme');
    searchParamsMock.set('switch_account', '1');

    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    await user.type(screen.getByLabelText('Password', { exact: false }), 'password123');
    const locationMock = window.location as unknown as ReturnType<typeof createLocationMock>;
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(loginEmailMock).toHaveBeenCalledWith('invitee@example.com', 'password123');
    expect(
      `${new URL(locationMock.href).pathname}${new URL(locationMock.href).search}`
    ).toBe('/invite/accept?token=invite-123');
  });

  it('submits sign-up form', async () => {
    registerMock.mockResolvedValue({ data: { id: '1' } });
    searchParamsMock.set('tab', 'signup');
    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);
    const nameInput = document.getElementById('signup-name');
    const emailInput = document.getElementById('signup-email');
    const passwordInput = document.getElementById('signup-password');

    expect(nameInput).not.toBeNull();
    expect(emailInput).not.toBeNull();
    expect(passwordInput).not.toBeNull();

    await user.type(nameInput as HTMLElement, 'New User');
    await user.type(emailInput as HTMLElement, 'new@example.com');
    await user.type(passwordInput as HTMLElement, 'newpass123');
    await user.click(screen.getByRole('button', { name: 'Create account' }));

    await waitFor(() => {
      expect(registerMock).toHaveBeenCalledWith('new@example.com', 'newpass123', 'New User', undefined);
    });
  });

  it('shows error on failed sign-in', async () => {
    loginEmailMock.mockRejectedValue(new Error('invalid email or password'));
    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    await user.type(screen.getByLabelText('Email', { exact: false }), 'bad@example.com');
    await user.type(screen.getByLabelText('Password', { exact: false }), 'wrongpass');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('invalid email or password');
  });

  it('disables sign-in button while request is pending', async () => {
    let resolveLogin: ((value?: unknown) => void) | undefined;
    loginEmailMock.mockReturnValue(
      new Promise((resolve) => {
        resolveLogin = resolve;
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    await user.type(screen.getByLabelText('Email', { exact: false }), 'test@example.com');
    await user.type(screen.getByLabelText('Password', { exact: false }), 'password123');

    const signInButton = screen.getByRole('button', { name: 'Sign in' });
    await user.click(signInButton);

    expect(signInButton).toBeDisabled();
    expect(signInButton.querySelector('[data-slot="button-spinner"]')).toBeInTheDocument();
    await user.click(signInButton);
    expect(loginEmailMock).toHaveBeenCalledTimes(1);

    resolveLogin?.();
    await waitFor(() => {
      expect(signInButton).toBeEnabled();
    });
  });

  it('prefills invited email and keeps it read-only on sign up', async () => {
    registerMock.mockResolvedValue({ data: { id: '1' } });
    searchParamsMock.set('invitation', 'invite-123');
    searchParamsMock.set('email', 'invitee@example.com');
    searchParamsMock.set('org', 'Acme');
    searchParamsMock.set('tab', 'signup');

    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    expect(screen.getByText(/invitee@example.com/i)).toBeInTheDocument();
    expect(screen.getByText('Join Acme')).toBeInTheDocument();

    const signupEmail = screen.getByLabelText('Email');
    expect(signupEmail).toHaveValue('invitee@example.com');
    expect(signupEmail).toHaveAttribute('readonly');

    await user.type(screen.getByLabelText('Name'), 'Invited User');
    await user.type(screen.getByLabelText('Password'), 'invitepass123');
    await user.click(screen.getByRole('button', { name: 'Create account' }));

    expect(registerMock).toHaveBeenCalledWith('invitee@example.com', 'invitepass123', 'Invited User', 'invite-123');
  });

  it('shows an explicit invitation banner when arriving from an invite', () => {
    searchParamsMock.set('invitation', 'invite-123');
    searchParamsMock.set('email', 'invitee@example.com');
    searchParamsMock.set('org', 'Acme');

    renderWithProviders(<LoginPage />);

    expect(screen.getByText('Invitation pending')).toBeInTheDocument();
    expect(screen.getByText('Join Acme')).toBeInTheDocument();
    expect(screen.getByText(/invitee@example.com/)).toBeInTheDocument();
  });

  it('shows invitation context for GitHub-only invites too', () => {
    searchParamsMock.set('invitation', 'invite-gh');
    searchParamsMock.set('github_username', 'megan-assembled');
    searchParamsMock.set('org', 'Acme');

    renderWithProviders(<LoginPage />);

    expect(screen.getByText('Invitation pending')).toBeInTheDocument();
    expect(screen.getByText('Join Acme')).toBeInTheDocument();
    expect(screen.getByText(/@megan-assembled/)).toBeInTheDocument();
  });

  it('treats GitHub-locked invites with notification email as GitHub identity invites', async () => {
    searchParamsMock.set('invitation', 'invite-gh');
    searchParamsMock.set('email', 'notify@example.com');
    searchParamsMock.set('github_username', 'megan-assembled');
    searchParamsMock.set('acceptance_method', 'github');
    searchParamsMock.set('org', 'Acme');

    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    expect(screen.getByText('Invitation pending')).toBeInTheDocument();
    expect(screen.getByText(/@megan-assembled/)).toBeInTheDocument();
    expect(screen.queryByText(/as notify@example\.com/)).not.toBeInTheDocument();

    const signinEmail = document.getElementById('signin-email');
    expect(signinEmail).toHaveValue('');
    expect(signinEmail).not.toHaveAttribute('readonly');

    await user.click(screen.getByRole('tab', { name: 'Sign up' }));
    const signupEmail = document.getElementById('signup-email');
    expect(signupEmail).toHaveValue('');
    expect(signupEmail).not.toHaveAttribute('readonly');
  });

  it('renders the login form instead of redirecting when switch_account is requested', () => {
    searchParamsMock.set('invitation', 'invite-123');
    searchParamsMock.set('email', 'invitee@example.com');
    searchParamsMock.set('org', 'Acme');
    searchParamsMock.set('switch_account', '1');
    useAuthMock.mockReturnValue({
      user: {
        id: 'user-1',
        email: 'wrong-user@example.com',
      },
      isLoading: false,
      isAuthenticated: true,
      logout: vi.fn(),
    });

    renderWithProviders(<LoginPage />);

    expect(replaceMock).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument();
    expect(screen.getByDisplayValue('invitee@example.com')).toBeInTheDocument();
  });
});
