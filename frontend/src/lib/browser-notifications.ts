import type { SessionStatus } from './types';

const settledSessionStatuses = new Set<SessionStatus>([
  'completed', 'pr_created', 'failed', 'cancelled', 'skipped',
]);

type VisibilityState = 'visible' | 'hidden' | 'prerender';

interface NotifySessionCompletedOptions {
  previousStatus?: SessionStatus;
  nextStatus?: SessionStatus;
  sessionId: string;
  title?: string;
  visibilityState: VisibilityState;
}

export async function maybeNotifySessionCompleted(options: NotifySessionCompletedOptions): Promise<void> {
  const {
    previousStatus,
    nextStatus,
    sessionId,
    title,
    visibilityState,
  } = options;

  if (!nextStatus || !settledSessionStatuses.has(nextStatus)) {
    return;
  }

  if (previousStatus && settledSessionStatuses.has(previousStatus)) {
    return;
  }

  // Cancelled and skipped are settled but routine; the favicon simply
  // returns to normal without promoting those transitions to notifications.
  if (nextStatus === 'cancelled' || nextStatus === 'skipped') {
    return;
  }

  if (visibilityState === 'visible') {
    return;
  }

  if (typeof window === 'undefined' || typeof Notification === 'undefined') {
    return;
  }

  let permission = Notification.permission;
  if (permission === 'default') {
    permission = await Notification.requestPermission();
  }

  if (permission !== 'granted') {
    return;
  }

  const failed = nextStatus === 'failed';
  new Notification(failed ? 'Session failed' : 'Session completed', {
    body: title || (failed ? 'Your session needs attention.' : 'Your session has finished running.'),
    tag: sessionId,
  });
}
