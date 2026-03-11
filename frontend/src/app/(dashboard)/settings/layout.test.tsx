import { describe, it, expect } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import SettingsLayout from './layout';

describe('SettingsLayout', () => {
  it('renders children', () => {
    renderWithProviders(
      <SettingsLayout>
        <div>child content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('child content')).toBeInTheDocument();
  });

  it('does not render tab navigation', () => {
    renderWithProviders(
      <SettingsLayout>
        <div />
      </SettingsLayout>
    );

    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });
});
