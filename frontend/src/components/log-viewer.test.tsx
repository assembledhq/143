import { act, render, screen, waitFor, fireEvent } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LogViewer } from './log-viewer';

// Mock the api module.
vi.mock('@/lib/api', () => ({
  api: {
    runs: {
      getLogs: vi.fn(),
    },
  },
}));

import { api } from '@/lib/api';

type EventSourceHandler = ((event: MessageEvent<string>) => void) | null;

class MockEventSource {
  static instances: MockEventSource[] = [];

  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: EventSourceHandler = null;
  closed = false;

  constructor(public url: string) {
    MockEventSource.instances.push(this);
  }

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  addEventListener(_event: string, _handler: () => void) {
    // no-op for tests
  }

  close() {
    this.closed = true;
  }
}

describe('LogViewer', () => {
  beforeEach(() => {
    vi.stubGlobal('EventSource', MockEventSource as unknown as typeof EventSource);
    (api.runs.getLogs as ReturnType<typeof vi.fn>).mockResolvedValue({ data: [] });
  });

  afterEach(() => {
    MockEventSource.instances = [];
    vi.unstubAllGlobals();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('shows loading state then "no log entries" for empty completed run', async () => {
    (api.runs.getLogs as ReturnType<typeof vi.fn>).mockResolvedValue({ data: [] });

    render(<LogViewer runId="run-1" isActive={false} />);
    expect(screen.getByText('Loading logs...')).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByText('No log entries yet.')).toBeInTheDocument();
    });
  });

  it('renders logs fetched via REST for completed runs', async () => {
    (api.runs.getLogs as ReturnType<typeof vi.fn>).mockResolvedValue({
      data: [
        {
          id: 1,
          level: 'error',
          message: 'Build failed',
          created_at: '2026-02-18T10:15:30Z',
        },
      ],
    });

    render(<LogViewer runId="run-2" isActive={false} />);

    await waitFor(() => {
      expect(screen.getByText('Build failed')).toBeInTheDocument();
    });
    expect(screen.getByText('error')).toBeInTheDocument();

    // Should not start SSE for inactive runs.
    expect(MockEventSource.instances.length).toBe(0);
  });

  it('starts SSE streaming for active runs after REST fetch', async () => {
    (api.runs.getLogs as ReturnType<typeof vi.fn>).mockResolvedValue({ data: [] });

    render(<LogViewer runId="run-3" isActive={true} />);

    await waitFor(() => {
      expect(MockEventSource.instances.length).toBe(1);
    });

    const source = MockEventSource.instances[0];
    expect(source.url).toContain('/api/v1/runs/run-3/logs/stream');

    act(() => {
      source.onopen?.();
    });

    act(() => {
      source.onmessage?.({
        data: JSON.stringify({
          id: 1,
          level: 'info',
          message: 'Starting agent',
          created_at: '2026-02-18T10:15:30Z',
        }),
      } as MessageEvent<string>);
    });

    await waitFor(() => {
      expect(screen.getByText('Starting agent')).toBeInTheDocument();
    });
  });

  it('deduplicates logs between REST fetch and SSE stream', async () => {
    (api.runs.getLogs as ReturnType<typeof vi.fn>).mockResolvedValue({
      data: [
        {
          id: 1,
          level: 'info',
          message: 'Log one',
          created_at: '2026-02-18T10:15:30Z',
        },
      ],
    });

    render(<LogViewer runId="run-4" isActive={true} />);

    await waitFor(() => {
      expect(screen.getByText('Log one')).toBeInTheDocument();
    });

    const source = MockEventSource.instances[0];
    act(() => {
      source.onopen?.();
    });

    // SSE sends the same log ID that was already fetched via REST.
    act(() => {
      source.onmessage?.({
        data: JSON.stringify({
          id: 1,
          level: 'info',
          message: 'Log one',
          created_at: '2026-02-18T10:15:30Z',
        }),
      } as MessageEvent<string>);
    });

    // Should still only have one instance of the log.
    const logEntries = screen.getAllByText('Log one');
    expect(logEntries.length).toBe(1);
  });

  it('reconnects with backoff when active and stream errors', async () => {
    vi.useFakeTimers();
    (api.runs.getLogs as ReturnType<typeof vi.fn>).mockResolvedValue({ data: [] });

    render(<LogViewer runId="run-5" isActive={true} />);

    await act(async () => {
      await vi.runAllTimersAsync();
    });

    const first = MockEventSource.instances[0];
    act(() => {
      first.onopen?.();
      first.onerror?.();
    });

    act(() => {
      vi.advanceTimersByTime(1000);
    });

    expect(MockEventSource.instances.length).toBeGreaterThan(1);
  });

  it('shows error state with retry button when REST fetch fails', async () => {
    (api.runs.getLogs as ReturnType<typeof vi.fn>).mockRejectedValue(
      new Error('Network error')
    );

    render(<LogViewer runId="run-err" isActive={false} />);

    await waitFor(() => {
      expect(screen.getByText('Network error')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument();
  });

  it('retries fetch when retry button is clicked', async () => {
    (api.runs.getLogs as ReturnType<typeof vi.fn>)
      .mockRejectedValueOnce(new Error('Network error'))
      .mockResolvedValueOnce({
        data: [
          { id: 1, level: 'info', message: 'Recovered log', created_at: '2026-02-18T10:15:30Z' },
        ],
      });

    render(<LogViewer runId="run-retry" isActive={false} />);

    await waitFor(() => {
      expect(screen.getByText('Network error')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /retry/i }));

    await waitFor(() => {
      expect(screen.getByText('Recovered log')).toBeInTheDocument();
    });
  });

  it('shows stream error after all reconnect attempts are exhausted', async () => {
    vi.useFakeTimers();
    (api.runs.getLogs as ReturnType<typeof vi.fn>).mockResolvedValue({
      data: [
        { id: 1, level: 'info', message: 'Existing log', created_at: '2026-02-18T10:15:30Z' },
      ],
    });

    render(<LogViewer runId="run-stream-err" isActive={true} />);

    await act(async () => {
      await vi.runAllTimersAsync();
    });

    // Exhaust all 5 reconnect attempts.
    for (let i = 0; i < 6; i++) {
      const source = MockEventSource.instances[MockEventSource.instances.length - 1];
      act(() => {
        source.onerror?.();
      });
      await act(async () => {
        await vi.runAllTimersAsync();
      });
    }

    expect(screen.getByText(/log stream disconnected/i)).toBeInTheDocument();
    // Existing logs should still be visible.
    expect(screen.getByText('Existing log')).toBeInTheDocument();
  });
});
