import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, userEvent } from '@/test/test-utils';
import LoginPage from './page';

const loginMock = vi.hoisted(() => vi.fn());
const loginGoogleMock = vi.hoisted(() => vi.fn());
const loginEmailMock = vi.hoisted(() => vi.fn());
const registerMock = vi.hoisted(() => vi.fn());

const useAuthMock = vi.hoisted(() => vi.fn());
const useAuthProvidersMock = vi.hoisted(() => vi.fn());

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
}));

describe('LoginPage', () => {
  beforeEach(() => {
    loginMock.mockReset();
    loginGoogleMock.mockReset();
    loginEmailMock.mockReset();
    registerMock.mockReset();
    pushMock.mockReset();
    replaceMock.mockReset();

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
  });

  it('renders Sign In and Sign Up tabs', () => {
    renderWithProviders(<LoginPage />);

    expect(screen.getByRole('tab', { name: 'Sign In' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Sign Up' })).toBeInTheDocument();
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
    await user.click(screen.getByRole('button', { name: 'Sign In' }));

    expect(loginEmailMock).toHaveBeenCalledWith('test@example.com', 'password123');
  });

  it('submits sign-up form', async () => {
    registerMock.mockResolvedValue({ data: { id: '1' } });
    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    // Switch to Sign Up tab
    await user.click(screen.getByRole('tab', { name: 'Sign Up' }));

    await user.type(screen.getByLabelText('Name'), 'New User');
    // There are now two email fields; get the one in the sign-up tab
    const emailInputs = screen.getAllByLabelText('Email');
    await user.type(emailInputs[emailInputs.length - 1], 'new@example.com');
    const passwordInputs = screen.getAllByLabelText('Password');
    await user.type(passwordInputs[passwordInputs.length - 1], 'newpass123');
    await user.click(screen.getByRole('button', { name: 'Create Account' }));

    expect(registerMock).toHaveBeenCalledWith('new@example.com', 'newpass123', 'New User');
  });

  it('shows error on failed sign-in', async () => {
    loginEmailMock.mockRejectedValue(new Error('invalid email or password'));
    const user = userEvent.setup();
    renderWithProviders(<LoginPage />);

    await user.type(screen.getByLabelText('Email', { exact: false }), 'bad@example.com');
    await user.type(screen.getByLabelText('Password', { exact: false }), 'wrongpass');
    await user.click(screen.getByRole('button', { name: 'Sign In' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('invalid email or password');
  });
});
