import { act, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { LogViewer } from './log-viewer';

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

  close() {
    this.closed = true;
  }
}

describe('LogViewer', () => {
  afterEach(() => {
    MockEventSource.instances = [];
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('shows connection placeholder before stream opens', () => {
    vi.stubGlobal('EventSource', MockEventSource as unknown as typeof EventSource);

    render(<LogViewer runId="run-1" isActive={true} />);
    expect(screen.getByText('Connecting to log stream...')).toBeInTheDocument();
  });

  it('renders logs after receiving SSE messages', async () => {
    vi.stubGlobal('EventSource', MockEventSource as unknown as typeof EventSource);

    render(<LogViewer runId="run-2" isActive={true} />);

    const source = MockEventSource.instances[0];
    act(() => {
      source.onopen?.();
    });

    act(() => {
      source.onmessage?.({
        data: JSON.stringify({
          id: 'log-1',
          level: 'error',
          message: 'Build failed',
          created_at: '2026-02-18T10:15:30Z',
        }),
      } as MessageEvent<string>);
    });

    await waitFor(() => {
      expect(screen.getByText('Build failed')).toBeInTheDocument();
    });
    expect(screen.getByText('error')).toBeInTheDocument();
  });

  it('reconnects with backoff when active and stream errors', async () => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', MockEventSource as unknown as typeof EventSource);

    render(<LogViewer runId="run-3" isActive={true} />);

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
});
