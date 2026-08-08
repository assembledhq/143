import { describe, expect, it } from 'vitest';
import { buildActivityTimelineNodes } from '@/lib/activity-timeline';
import { flattenTranscriptWindows } from '@/lib/timeline';
import type { SessionMessage, ThreadInboxEntry } from '@/lib/types';
import { appendMessageToTranscriptCache } from './session-detail-content';

function steeringMessage(): SessionMessage {
  return {
    id: 42,
    session_id: 'session-1',
    org_id: 'org-1',
    thread_id: 'thread-1',
    turn_number: 2,
    role: 'user',
    content: 'also preserve anchors',
    created_at: '2026-08-03T00:00:07Z',
  };
}

function inboxEntry(overrides: Partial<ThreadInboxEntry> = {}): ThreadInboxEntry {
  return {
    id: 'inbox-1',
    org_id: 'org-1',
    session_id: 'session-1',
    thread_id: 'thread-1',
    sequence_no: 4,
    entry_type: 'user_message',
    payload: {},
    delivery_state: 'pending',
    delivery_attempts: 0,
    accepted_at: '2026-08-03T00:00:07Z',
    created_at: '2026-08-03T00:00:07Z',
    ...overrides,
  };
}

// The optimistic patch and the eventual /transcript refetch must agree on
// whether a steering message is visible. A not-yet-applied follow-up must
// stay visible after send (the transcript is the only surface that shows it
// once the queued-message card was removed); only a failed delivery is hidden
// and surfaced by the recoverable-inbox notice.
function visibleContentAfterPatch(delivery?: {
  deliveryState?: ThreadInboxEntry['delivery_state'];
  inboxEntry?: ThreadInboxEntry;
}): string[] {
  const patched = appendMessageToTranscriptCache(undefined, steeringMessage(), 'running', delivery);
  const flattened = flattenTranscriptWindows(patched.pages[0].data);
  const nodes = buildActivityTimelineNodes(
    flattened.messages.map((data) => ({ kind: 'message' as const, data })),
    [],
  );
  return nodes.flatMap((node) => (node.kind === 'visible' && node.entry.kind === 'message' ? [node.entry.data.content] : []));
}

describe('appendMessageToTranscriptCache', () => {
  it('keeps a still-queued steering message visible exactly as the refetch will', () => {
    expect(visibleContentAfterPatch({
      deliveryState: 'pending',
      inboxEntry: inboxEntry(),
    })).toEqual(['also preserve anchors']);
  });

  it('hides a failed steering message exactly as the refetch will', () => {
    expect(visibleContentAfterPatch({
      deliveryState: 'dead_letter',
      inboxEntry: inboxEntry({ delivery_state: 'dead_letter' }),
    })).toEqual([]);
    expect(visibleContentAfterPatch({
      deliveryState: 'unknown_delivery',
      inboxEntry: inboxEntry({ delivery_state: 'unknown_delivery' }),
    })).toEqual([]);
  });

  it('carries the inbox sequence and acceptance time onto the patched entry', () => {
    const patched = appendMessageToTranscriptCache(undefined, steeringMessage(), 'running', {
      deliveryState: 'pending',
      inboxEntry: inboxEntry(),
    });
    const entry = patched.pages[0].data[0].entries[0];
    expect(entry.delivery_state).toBe('pending');
    expect(entry.inbox_sequence).toBe(4);
    expect(entry.accepted_at).toBe('2026-08-03T00:00:07Z');
  });

  it('keeps a message visible when the deployment created no inbox entry', () => {
    // '' is what the API reports when the inbox is unwired; it must not be
    // mistaken for an unapplied delivery and hide the message.
    expect(visibleContentAfterPatch({ deliveryState: '' })).toEqual(['also preserve anchors']);
    expect(visibleContentAfterPatch()).toEqual(['also preserve anchors']);
  });
});
