import { act } from 'react';
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

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
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

  it('can de-emphasize the desktop action button until hover or focus', () => {
    renderWithProviders(
      <SwipeActionRow
        actionLabel="Archive item"
        actionText="Archive"
        desktopActionVisibility="hover"
        onAction={() => {}}
      >
        <div>Row content</div>
      </SwipeActionRow>,
    );

    expect(screen.getByRole('button', { name: 'Archive item' })).toHaveClass(
      'md:opacity-0',
      'md:group-hover:opacity-100',
      'md:focus-visible:opacity-100',
    );
  });

  it('gives the desktop action button a roomier inset from the top edge', () => {
    const restore = mockMatchMedia(false);

    try {
      renderWithProviders(
        <SwipeActionRow
          actionLabel="Archive item"
          actionText="Archive"
          actionIcon={<span>Archive icon</span>}
          onAction={() => {}}
        >
          <div>Row content</div>
        </SwipeActionRow>,
      );

      expect(screen.getByRole('button', { name: 'Archive item' })).toHaveClass('top-3', 'right-3');
    } finally {
      restore();
    }
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

  it('continues from the revealed position on a follow-up swipe and auto-fires', () => {
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
    const container = surface!.parentElement;
    expect(container).not.toBeNull();
    Object.defineProperty(container!, 'offsetWidth', {
      configurable: true,
      value: 390,
    });

    fireEvent.touchStart(surface!, {
      touches: [{ clientX: 320, clientY: 20 }],
    });
    fireEvent.touchMove(surface!, {
      touches: [{ clientX: 240, clientY: 24 }],
    });
    fireEvent.touchEnd(surface!);

    expect(container).toHaveAttribute('data-swipe-state', 'open');
    expect(onAction).not.toHaveBeenCalled();

    fireEvent.touchStart(surface!, {
      touches: [{ clientX: 240, clientY: 24 }],
    });
    fireEvent.touchMove(surface!, {
      touches: [{ clientX: 170, clientY: 28 }],
    });
    fireEvent.touchEnd(surface!);

    expect(onAction).toHaveBeenCalledTimes(1);
  });

  it('closes an open row on a tap without needing a synthetic click', () => {
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
    const container = surface!.parentElement;
    expect(container).not.toBeNull();

    fireEvent.touchStart(surface!, {
      touches: [{ clientX: 220, clientY: 20 }],
    });
    fireEvent.touchMove(surface!, {
      touches: [{ clientX: 120, clientY: 24 }],
    });
    fireEvent.touchEnd(surface!);

    expect(container).toHaveAttribute('data-swipe-state', 'open');

    fireEvent.touchStart(surface!, {
      touches: [{ clientX: 120, clientY: 24 }],
    });
    fireEvent.touchEnd(surface!);

    expect(container).toHaveAttribute('data-swipe-state', 'closed');
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

    const container = surface!.parentElement;
    expect(container).toHaveAttribute('data-swipe-state', 'committed');

    fireEvent.touchEnd(surface!);
    expect(onAction).toHaveBeenCalledTimes(1);
  });

  it('shows progressive feedback before and after the auto-archive threshold', () => {
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
    const container = surface!.parentElement;
    expect(container).not.toBeNull();
    Object.defineProperty(container!, 'offsetWidth', {
      configurable: true,
      value: 390,
    });

    fireEvent.touchStart(surface!, {
      touches: [{ clientX: 320, clientY: 20 }],
    });
    fireEvent.touchMove(surface!, {
      touches: [{ clientX: 240, clientY: 24 }],
    });

    expect(screen.getByText('Keep swiping')).toBeInTheDocument();
    expect(screen.queryByText('Release to archive')).not.toBeInTheDocument();

    fireEvent.touchMove(surface!, {
      touches: [{ clientX: 170, clientY: 24 }],
    });

    expect(screen.getByText('Release to archive')).toBeInTheDocument();
    expect(container).toHaveAttribute('data-swipe-state', 'committed');
  });

  it('keeps the trailing archive affordance to two text lines while swiping', () => {
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
      touches: [{ clientX: 120, clientY: 24 }],
    });

    expect(screen.getByText('Archive')).toBeInTheDocument();
    expect(screen.getByText('Keep swiping')).toBeInTheDocument();
    expect(screen.queryByTestId('swipe-action-icon')).not.toBeInTheDocument();
  });

  it('keeps the row slid fully left until the archive action settles', async () => {
    vi.useFakeTimers();
    const action = deferred<void>();
    const onAction = vi.fn(() => action.promise);

    try {
      renderWithProviders(
        <SwipeActionRow
          actionLabel="Archive item"
          actionText="Archive"
          onAction={onAction}
        >
          <div>Row content</div>
        </SwipeActionRow>,
      );

      const surface = screen.getByText('Row content').closest('[data-swipe-surface="true"]') as HTMLElement | null;
      expect(surface).not.toBeNull();
      const container = surface!.parentElement;
      expect(container).not.toBeNull();
      Object.defineProperty(container!, 'offsetWidth', {
        configurable: true,
        value: 390,
      });

      fireEvent.touchStart(surface!, {
        touches: [{ clientX: 320, clientY: 20 }],
      });
      fireEvent.touchMove(surface!, {
        touches: [{ clientX: 170, clientY: 24 }],
      });
      fireEvent.touchEnd(surface!);

      expect(onAction).toHaveBeenCalledTimes(1);
      expect(surface!.style.transform).toBe('translateX(-390px)');

      await act(async () => {
        action.resolve();
        await action.promise;
      });

      expect(surface!.style.transform).toBe('translateX(-390px)');

      await act(async () => {
        vi.advanceTimersByTime(200);
      });

      expect(surface!.style.transform).toBe('translateX(-0px)');
    } finally {
      vi.useRealTimers();
    }
  });

  it('swallows rejected promises from the revealed action button path', async () => {
    const action = deferred<void>();
    const onUnhandledRejection = vi.fn();
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    window.addEventListener('unhandledrejection', onUnhandledRejection);

    try {
      renderWithProviders(
        <SwipeActionRow
          actionLabel="Archive item"
          actionText="Archive"
          onAction={() => action.promise}
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

      fireEvent.click(screen.getAllByRole('button', { name: 'Archive item' })[0]);

      await act(async () => {
        action.reject(new Error('archive failed'));
        try {
          await action.promise;
        } catch {
          // Expected rejection for this test.
        }
      });

      expect(onUnhandledRejection).not.toHaveBeenCalled();
    } finally {
      consoleError.mockRestore();
      window.removeEventListener('unhandledrejection', onUnhandledRejection);
    }
  });

  it('swallows rejected promises from the desktop action button path', async () => {
    const action = deferred<void>();
    const onUnhandledRejection = vi.fn();
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    window.addEventListener('unhandledrejection', onUnhandledRejection);

    try {
      renderWithProviders(
        <SwipeActionRow
          actionLabel="Archive item"
          actionText="Archive"
          actionIcon={<span>Archive icon</span>}
          onAction={() => action.promise}
        >
          <div>Row content</div>
        </SwipeActionRow>,
      );

      fireEvent.click(screen.getByRole('button', { name: 'Archive item' }));

      await act(async () => {
        action.reject(new Error('archive failed'));
        try {
          await action.promise;
        } catch {
          // Expected rejection for this test.
        }
      });

      expect(onUnhandledRejection).not.toHaveBeenCalled();
    } finally {
      consoleError.mockRestore();
      window.removeEventListener('unhandledrejection', onUnhandledRejection);
    }
  });

  it('keeps the moving row surface opaque while the action is revealed', () => {
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
      touches: [{ clientX: 120, clientY: 24 }],
    });

    expect(surface).toHaveClass('bg-background');
  });

  it('keeps the touch surface opaque before any swipe begins', () => {
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

    expect(surface).toHaveClass('bg-background');
  });

  it('keeps the trailing action tray collapsed while closed', () => {
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
    const container = surface!.parentElement;
    expect(container).not.toBeNull();

    const actionTray = container!.querySelector('[aria-hidden="true"]')?.parentElement as HTMLElement | null;
    expect(actionTray).not.toBeNull();
    expect(actionTray?.style.width).toBe('0px');
  });

  it('auto-fires on a deliberate mobile-width swipe before half-row travel', () => {
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
    const container = surface!.parentElement;
    expect(container).not.toBeNull();
    Object.defineProperty(container!, 'offsetWidth', {
      configurable: true,
      value: 390,
    });

    fireEvent.touchStart(surface!, {
      touches: [{ clientX: 320, clientY: 20 }],
    });
    fireEvent.touchMove(surface!, {
      touches: [{ clientX: 170, clientY: 24 }],
    });
    fireEvent.touchEnd(surface!);

    expect(onAction).toHaveBeenCalledTimes(1);
  });

  it('auto-fires when touchend lands before React flushes the latest drag offset', () => {
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
    const container = surface!.parentElement;
    expect(container).not.toBeNull();
    Object.defineProperty(container!, 'offsetWidth', {
      configurable: true,
      value: 390,
    });

    act(() => {
      fireEvent.touchStart(surface!, {
        touches: [{ clientX: 320, clientY: 20 }],
      });
      fireEvent.touchMove(surface!, {
        touches: [{ clientX: 170, clientY: 24 }],
      });
      fireEvent.touchEnd(surface!);
    });

    expect(onAction).toHaveBeenCalledTimes(1);
  });

  it('still completes the action when the haptic request fails', () => {
    const onAction = vi.fn();
    const originalVibrate = navigator.vibrate;
    const originalConsoleError = console.error;
    const consoleError = vi.fn();
    Object.defineProperty(navigator, 'vibrate', {
      configurable: true,
      value: vi.fn(() => {
        throw new Error('blocked');
      }),
    });
    console.error = consoleError;

    try {
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
      fireEvent.touchEnd(surface!);

      expect(onAction).toHaveBeenCalledTimes(1);
      expect(consoleError).toHaveBeenCalledWith(
        'Failed to trigger swipe haptic feedback',
        expect.any(Error),
      );
    } finally {
      console.error = originalConsoleError;
      if (originalVibrate) {
        Object.defineProperty(navigator, 'vibrate', {
          configurable: true,
          value: originalVibrate,
        });
      } else {
        Reflect.deleteProperty(navigator, 'vibrate');
      }
    }
  });

  it('only emits the ready haptic once per gesture even if the swipe crosses the threshold multiple times', () => {
    const onAction = vi.fn();
    const originalVibrate = navigator.vibrate;
    const vibrate = vi.fn();
    Object.defineProperty(navigator, 'vibrate', {
      configurable: true,
      value: vibrate,
    });

    try {
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
      const container = surface!.parentElement;
      expect(container).not.toBeNull();
      Object.defineProperty(container!, 'offsetWidth', {
        configurable: true,
        value: 390,
      });

      fireEvent.touchStart(surface!, {
        touches: [{ clientX: 320, clientY: 20 }],
      });
      fireEvent.touchMove(surface!, {
        touches: [{ clientX: 170, clientY: 24 }],
      });
      fireEvent.touchMove(surface!, {
        touches: [{ clientX: 230, clientY: 24 }],
      });
      fireEvent.touchMove(surface!, {
        touches: [{ clientX: 150, clientY: 24 }],
      });

      expect(vibrate).toHaveBeenCalledTimes(1);
      expect(vibrate).toHaveBeenNthCalledWith(1, 10);
      expect(onAction).not.toHaveBeenCalled();
    } finally {
      if (originalVibrate) {
        Object.defineProperty(navigator, 'vibrate', {
          configurable: true,
          value: originalVibrate,
        });
      } else {
        Reflect.deleteProperty(navigator, 'vibrate');
      }
    }
  });

  it('uses a distinct confirmation haptic when the archive commits on release', () => {
    const onAction = vi.fn();
    const originalVibrate = navigator.vibrate;
    const vibrate = vi.fn();
    Object.defineProperty(navigator, 'vibrate', {
      configurable: true,
      value: vibrate,
    });

    try {
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
      const container = surface!.parentElement;
      expect(container).not.toBeNull();
      Object.defineProperty(container!, 'offsetWidth', {
        configurable: true,
        value: 390,
      });

      fireEvent.touchStart(surface!, {
        touches: [{ clientX: 320, clientY: 20 }],
      });
      fireEvent.touchMove(surface!, {
        touches: [{ clientX: 170, clientY: 24 }],
      });
      fireEvent.touchEnd(surface!);

      expect(onAction).toHaveBeenCalledTimes(1);
      expect(vibrate).toHaveBeenCalledTimes(2);
      expect(vibrate).toHaveBeenNthCalledWith(1, 10);
      expect(vibrate).toHaveBeenNthCalledWith(2, [16, 24, 40]);
    } finally {
      if (originalVibrate) {
        Object.defineProperty(navigator, 'vibrate', {
          configurable: true,
          value: originalVibrate,
        });
      } else {
        Reflect.deleteProperty(navigator, 'vibrate');
      }
    }
  });

  it('does not auto-fire the action when a committed swipe is cancelled', () => {
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
    fireEvent.touchCancel(surface!);

    expect(onAction).not.toHaveBeenCalled();
    const container = surface!.parentElement;
    expect(container).toHaveAttribute('data-swipe-state', 'closed');
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
