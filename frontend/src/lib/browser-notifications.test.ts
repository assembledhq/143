import { describe, it, expect, vi, beforeEach } from 'vitest';
import { maybeNotifySessionCompleted } from './browser-notifications';

describe('maybeNotifySessionCompleted', () => {
  const openSession = {
    id: 'session-1',
    status: 'running',
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
      enabled: true,
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
      enabled: true,
      previousStatus: openSession.status,
      nextStatus: 'completed',
      sessionId: openSession.id,
      title: openSession.title,
      visibilityState: 'visible',
    });

    expect(notificationSpy).not.toHaveBeenCalled();
  });

  it('does not notify on initial load when no previous status was observed', async () => {
    const notificationSpy = vi.fn();
    vi.stubGlobal('Notification', Object.assign(notificationSpy, {
      permission: 'granted',
      requestPermission: vi.fn().mockResolvedValue('granted'),
    }));

    await maybeNotifySessionCompleted({
      enabled: true,
      previousStatus: undefined,
      nextStatus: 'completed',
      sessionId: openSession.id,
      title: openSession.title,
      visibilityState: 'hidden',
    });

    expect(notificationSpy).not.toHaveBeenCalled();
  });

  it('does not notify when the feature is disabled in settings', async () => {
    const notificationSpy = vi.fn();
    vi.stubGlobal('Notification', Object.assign(notificationSpy, {
      permission: 'granted',
      requestPermission: vi.fn().mockResolvedValue('granted'),
    }));

    await maybeNotifySessionCompleted({
      enabled: false,
      previousStatus: openSession.status,
      nextStatus: 'completed',
      sessionId: openSession.id,
      title: openSession.title,
      visibilityState: 'hidden',
    });

    expect(notificationSpy).not.toHaveBeenCalled();
  });
});
