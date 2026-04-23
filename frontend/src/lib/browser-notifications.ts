const terminalSessionStatuses = new Set([
  'completed',
  'pr_created',
  'failed',
  'cancelled',
  'skipped',
]);

type VisibilityState = 'visible' | 'hidden' | 'prerender';

interface NotifySessionCompletedOptions {
  enabled: boolean;
  previousStatus?: string;
  nextStatus?: string;
  sessionId: string;
  title?: string;
  visibilityState: VisibilityState;
}

export async function maybeNotifySessionCompleted(options: NotifySessionCompletedOptions): Promise<void> {
  const {
    enabled,
    previousStatus,
    nextStatus,
    sessionId,
    title,
    visibilityState,
  } = options;

  if (!enabled) {
    return;
  }

  if (!nextStatus || !terminalSessionStatuses.has(nextStatus)) {
    return;
  }

  if (!previousStatus) {
    return;
  }

  if (terminalSessionStatuses.has(previousStatus)) {
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

  new Notification('Session completed', {
    body: title || 'Your session has finished running.',
    tag: sessionId,
  });
}
