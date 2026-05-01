import { describe, expect, it, vi } from 'vitest';
import { fireEvent, renderWithProviders, screen } from '@/test/test-utils';
import { SwipeActionRow } from './swipe-action-row';

describe('SwipeActionRow', () => {
  it('keeps the hidden swipe action out of the accessibility tree while closed', () => {
    renderWithProviders(
      <SwipeActionRow
        actionLabel="Archive item"
        actionText="Archive"
        onAction={() => {}}
      >
        <div>Row content</div>
      </SwipeActionRow>,
    );

    expect(screen.getAllByRole('button', { name: 'Archive item' })).toHaveLength(1);
  });

  it('reveals the trailing action after a left swipe and invokes it', async () => {
    const onAction = vi.fn();

    renderWithProviders(
      <SwipeActionRow
        actionLabel="Archive item"
        actionText="Archive"
        onAction={onAction}
      >
        <div>Row content</div>
      </SwipeActionRow>,
    );

    const surface = screen.getByText('Row content').closest('[data-swipe-surface="true"]');
    expect(surface).not.toBeNull();

    fireEvent.touchStart(surface!, {
      touches: [{ clientX: 220, clientY: 20 }],
    });
    fireEvent.touchMove(surface!, {
      touches: [{ clientX: 120, clientY: 24 }],
    });
    fireEvent.touchEnd(surface!);

    const action = screen.getAllByRole('button', { name: 'Archive item' })[0];
    expect(action.closest('[data-swipe-state="open"]')).not.toBeNull();

    fireEvent.click(action);
    expect(onAction).toHaveBeenCalledTimes(1);
  });

  it('does not open for mostly vertical movement', () => {
    renderWithProviders(
      <SwipeActionRow
        actionLabel="Archive item"
        actionText="Archive"
        onAction={() => {}}
      >
        <div>Row content</div>
      </SwipeActionRow>,
    );

    const surface = screen.getByText('Row content').closest('[data-swipe-surface="true"]');
    expect(surface).not.toBeNull();

    fireEvent.touchStart(surface!, {
      touches: [{ clientX: 220, clientY: 20 }],
    });
    fireEvent.touchMove(surface!, {
      touches: [{ clientX: 206, clientY: 120 }],
    });
    fireEvent.touchEnd(surface!);

    const action = screen.getAllByRole('button', { name: 'Archive item' })[0];
    expect(action.closest('[data-swipe-state="closed"]')).not.toBeNull();
  });
});
