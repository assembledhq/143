import { describe, it, expect, vi, beforeEach } from 'vitest';
import { maybeNotifySessionCompleted } from './browser-notifications';

describe('maybeNotifySessionCompleted', () => {
  const openSession = {
    id: 'session-1',
    status: 'running' as const,
    title: 'Fix flaky tests',
  };

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it('sends a browser notification when the session transitions to completed and permission is granted', async () => {
    const notificationSpy = vi.fn();
    vi.stubGlobal('Notification', Object.assign(notificationSpy, {
      permission: 'granted',
      requestPermission: vi.fn().mockResolvedValue('granted'),
    }));

    await maybeNotifySessionCompleted({
      previousStatus: openSession.status,
      nextStatus: 'completed',
      sessionId: openSession.id,
      title: openSession.title,
      visibilityState: 'hidden',
    });

    expect(notificationSpy).toHaveBeenCalledWith('Session completed', {
      body: 'Fix flaky tests',
      tag: 'session-1',
    });
  });

  it('does not notify when the tab is visible', async () => {
    const notificationSpy = vi.fn();
    vi.stubGlobal('Notification', Object.assign(notificationSpy, {
      permission: 'granted',
      requestPermission: vi.fn().mockResolvedValue('granted'),
    }));

    await maybeNotifySessionCompleted({
      previousStatus: openSession.status,
      nextStatus: 'completed',
      sessionId: openSession.id,
      title: openSession.title,
      visibilityState: 'visible',
    });

    expect(notificationSpy).not.toHaveBeenCalled();
  });

  it('uses an attention-oriented notification when a session fails', async () => {
    const notificationSpy = vi.fn();
    vi.stubGlobal('Notification', Object.assign(notificationSpy, {
      permission: 'granted',
      requestPermission: vi.fn().mockResolvedValue('granted'),
    }));

    await maybeNotifySessionCompleted({
      previousStatus: 'running',
      nextStatus: 'failed',
      sessionId: openSession.id,
      visibilityState: 'hidden',
    });

    expect(notificationSpy).toHaveBeenCalledWith('Session failed', {
      body: 'Your session needs attention.',
      tag: 'session-1',
    });
  });

  it.each(['cancelled', 'skipped'] as const)('does not notify for routine %s transitions', async (nextStatus) => {
    const notificationSpy = vi.fn();
    vi.stubGlobal('Notification', Object.assign(notificationSpy, {
      permission: 'granted',
      requestPermission: vi.fn().mockResolvedValue('granted'),
    }));

    await maybeNotifySessionCompleted({
      previousStatus: 'running',
      nextStatus,
      sessionId: openSession.id,
      visibilityState: 'hidden',
    });

    expect(notificationSpy).not.toHaveBeenCalled();
  });
});
