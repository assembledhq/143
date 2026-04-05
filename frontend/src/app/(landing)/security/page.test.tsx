import { describe, it, expect, beforeAll } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import SecurityPage from './page';

describe('SecurityPage', () => {
  beforeAll(() => {
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: () => ({
        matches: false,
        media: '',
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: () => {},
        removeEventListener: () => {},
        dispatchEvent: () => false,
      }),
    });
  });

  it('links repository security policy and supported versions guidance', () => {
    renderWithProviders(<SecurityPage />);

    expect(screen.getAllByText(/security\.md/i).length).toBeGreaterThan(0);
    expect(screen.getByText('Supported versions')).toBeInTheDocument();
  });

  it('displays vulnerability response timelines', () => {
    renderWithProviders(<SecurityPage />);

    expect(screen.getByText(/within 14 calendar days/i)).toBeInTheDocument();
    expect(screen.getByText(/within 30 calendar days/i)).toBeInTheDocument();
    expect(screen.getByText(/within 60 calendar days/i)).toBeInTheDocument();
  });
});
