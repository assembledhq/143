import { describe, it, expect, vi } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import SettingsLayout from './layout';

vi.mock('next/navigation', () => ({
  usePathname: () => '/settings',
}));

describe('SettingsLayout', () => {
  it('renders Settings header and navigation tabs', () => {
    renderWithProviders(
      <SettingsLayout>
        <div>child content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('Settings')).toBeInTheDocument();
    expect(screen.getByText('General')).toBeInTheDocument();
    expect(screen.getByText('Team')).toBeInTheDocument();
    expect(screen.getByText('child content')).toBeInTheDocument();
  });

  it('renders tab links with correct hrefs', () => {
    renderWithProviders(
      <SettingsLayout>
        <div />
      </SettingsLayout>
    );

    const generalLink = screen.getByRole('link', { name: 'General' });
    const teamLink = screen.getByRole('link', { name: 'Team' });

    expect(generalLink).toHaveAttribute('href', '/settings');
    expect(teamLink).toHaveAttribute('href', '/settings/team');
  });
});
