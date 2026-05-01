import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, renderWithProviders, screen } from '@/test/test-utils';
import { SwipeActionRow } from './swipe-action-row';

function mockMatchMedia(coarse: boolean) {
  const original = window.matchMedia;
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      matches: query.includes('coarse') ? coarse : false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  });
  return () => {
    if (original) {
      Object.defineProperty(window, 'matchMedia', {
        writable: true,
        configurable: true,
        value: original,
      });
    } else {
      // jsdom defaults to no matchMedia — restore that.
      // @ts-expect-error intentionally remove
      delete window.matchMedia;
    }
  };
}

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

  it('auto-fires the action when released past the commit threshold', () => {
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
      touches: [{ clientX: 320, clientY: 20 }],
    });
    fireEvent.touchMove(surface!, {
      touches: [{ clientX: 100, clientY: 24 }],
    });

    expect(surface!.closest('[data-swipe-state="committed"]')).not.toBeNull();

    fireEvent.touchEnd(surface!);
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

  describe('on non-touch (mouse) devices', () => {
    let restoreMatchMedia: (() => void) | undefined;

    afterEach(() => {
      restoreMatchMedia?.();
      restoreMatchMedia = undefined;
    });

    it('does not render the swipe overlay or transform the surface', () => {
      restoreMatchMedia = mockMatchMedia(false);

      renderWithProviders(
        <SwipeActionRow
          actionLabel="Archive item"
          actionText="Archive"
          onAction={() => {}}
        >
          <div>Row content</div>
        </SwipeActionRow>,
      );

      // Only the desktop hover icon button should remain.
      expect(screen.getAllByRole('button', { name: 'Archive item' })).toHaveLength(1);

      const surface = screen.getByText('Row content').closest('[data-swipe-surface="true"]') as HTMLElement;
      expect(surface).not.toBeNull();
      // No inline transform on the surface so nothing slides on a stray touch.
      expect(surface.style.transform).toBe('');
    });

    it('keeps state closed even if touch events fire', () => {
      restoreMatchMedia = mockMatchMedia(false);

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

      fireEvent.touchStart(surface!, { touches: [{ clientX: 220, clientY: 20 }] });
      fireEvent.touchMove(surface!, { touches: [{ clientX: 120, clientY: 24 }] });
      fireEvent.touchEnd(surface!);

      const action = screen.getByRole('button', { name: 'Archive item' });
      expect(action.closest('[data-swipe-state="closed"]')).not.toBeNull();
    });
  });
});
