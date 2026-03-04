import { describe, it, expect, vi } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import SettingsLayout from './layout';

vi.mock('next/navigation', () => ({
  usePathname: () => '/settings',
}));

describe('SettingsLayout', () => {
  it('renders general settings header', () => {
    renderWithProviders(
      <SettingsLayout>
        <div>child content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('General Settings')).toBeInTheDocument();
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
