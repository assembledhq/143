import { describe, it, expect, vi } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import SettingsLayout from './layout';

vi.mock('next/navigation', () => ({
  usePathname: () => '/settings',
}));

describe('SettingsLayout', () => {
  it('renders organization settings header without team tab', () => {
    renderWithProviders(
      <SettingsLayout>
        <div>child content</div>
      </SettingsLayout>
    );

    expect(screen.getByText('Organization Settings')).toBeInTheDocument();
    expect(screen.getByText('General')).toBeInTheDocument();
    expect(screen.queryByText('Team')).not.toBeInTheDocument();
    expect(screen.getByText('child content')).toBeInTheDocument();
  });

  it('renders only the general tab link', () => {
    renderWithProviders(
      <SettingsLayout>
        <div />
      </SettingsLayout>
    );

    const generalLink = screen.getByRole('link', { name: 'General' });

    expect(generalLink).toHaveAttribute('href', '/settings');
    expect(screen.queryByRole('link', { name: 'Team' })).not.toBeInTheDocument();
  });
});
