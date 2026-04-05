import { describe, it, expect, beforeAll } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import TermsPage from './page';

describe('TermsPage', () => {
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

  it('includes AI output and human review responsibilities', () => {
    renderWithProviders(<TermsPage />);

    expect(screen.getByText('AI-generated output')).toBeInTheDocument();
    expect(screen.getByText(/you are responsible for reviewing/i)).toBeInTheDocument();
  });

  it('points contribution licensing to repository docs instead of only hosted terms', () => {
    renderWithProviders(<TermsPage />);

    expect(screen.getByText(/contributing\.md/i)).toBeInTheDocument();
    expect(screen.getByText(/inbound=outbound/i)).toBeInTheDocument();
  });
});
