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

    expect(screen.getByText(/security\.md/i)).toBeInTheDocument();
    expect(screen.getByText('Supported versions')).toBeInTheDocument();
  });

  it('displays vulnerability response timelines', () => {
    renderWithProviders(<SecurityPage />);

    expect(screen.getByText(/within 5 business days/i)).toBeInTheDocument();
    expect(screen.getByText(/within 60 days/i)).toBeInTheDocument();
    expect(screen.getByText(/within 180 days/i)).toBeInTheDocument();
  });
});
