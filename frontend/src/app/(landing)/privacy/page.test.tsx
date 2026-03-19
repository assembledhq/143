import { describe, it, expect, beforeAll } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import PrivacyPage from './page';

describe('PrivacyPage', () => {
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

  it('discloses customer content and AI provider processing', () => {
    renderWithProviders(<PrivacyPage />);

    expect(screen.getByText('Customer content')).toBeInTheDocument();
    expect(screen.getByText(/source code, issue descriptions, pull request content/i)).toBeInTheDocument();
    expect(screen.getByText('AI providers')).toBeInTheDocument();
    expect(screen.getAllByText(/Anthropic, OpenAI, OpenRouter, or Google Gemini/i).length).toBeGreaterThan(0);
  });

  it('discloses broader cookie usage and retention detail', () => {
    renderWithProviders(<PrivacyPage />);

    expect(screen.getByText(/session, csrf, and short-lived oauth flow cookies/i)).toBeInTheDocument();
    expect(screen.getByText(/session messages, logs, and snapshots/i)).toBeInTheDocument();
  });
});
