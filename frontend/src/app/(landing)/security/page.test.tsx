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

  it('commits to faster vulnerability acknowledgement windows', () => {
    renderWithProviders(<SecurityPage />);

    expect(screen.getByText(/within 3 business days/i)).toBeInTheDocument();
    expect(screen.getByText(/within 14 business days/i)).toBeInTheDocument();
  });
});
