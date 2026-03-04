import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderWithProviders, screen, waitFor, userEvent } from '@/test/test-utils';
import AgentSettingsPage from './page';

const {
  settingsGetMock,
  settingsUpdateMock,
  agentDefaultsMock,
} = vi.hoisted(() => ({
  settingsGetMock: vi.fn().mockResolvedValue({
    data: {
      name: 'Test Org',
      settings: {
        default_agent_type: 'codex',
        agent_config: {},
      },
    },
  }),
  settingsUpdateMock: vi.fn().mockResolvedValue({ data: {} }),
  agentDefaultsMock: vi.fn().mockResolvedValue({ data: {} }),
}));

vi.mock('@/lib/api', () => ({
  api: {
    settings: {
      get: settingsGetMock,
      update: settingsUpdateMock,
      getAgentDefaults: agentDefaultsMock,
    },
  },
}));

describe('AgentSettingsPage', () => {
  beforeEach(() => {
    settingsGetMock.mockClear();
    settingsUpdateMock.mockClear();
    agentDefaultsMock.mockClear();
  });

  it('renders agent settings and saves changes', async () => {
    const user = userEvent.setup();
    renderWithProviders(<AgentSettingsPage />);

    await waitFor(() => {
      expect(screen.getByText('Advanced agent settings')).toBeInTheDocument();
    });

    expect(screen.getByRole('button', { name: 'Sign in with ChatGPT' })).toBeInTheDocument();
    expect(screen.getByText('Recommended')).toBeInTheDocument();

    await user.clear(screen.getByLabelText('Model'));
    await user.type(screen.getByLabelText('Model'), 'codex-mini');
    await user.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() => {
      expect(settingsUpdateMock).toHaveBeenCalledTimes(1);
    });
  });
});
