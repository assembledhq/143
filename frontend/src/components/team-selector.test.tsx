import { describe, it, expect, vi } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import { fireEvent } from '@testing-library/react';
import { TeamSelector } from './team-selector';
import type { Team } from '@/lib/types';

const makeTeam = (overrides: Partial<Team> = {}): Team => ({
  id: 't1',
  org_id: 'org1',
  name: 'Platform',
  slug: 'platform',
  member_count: 3,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  ...overrides,
});

describe('TeamSelector', () => {
  it('renders nothing when there are no teams', () => {
    const { container } = renderWithProviders(
      <TeamSelector teams={[]} selectedTeamId={undefined} onSelect={() => {}} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the placeholder "All teams" when no team is selected', () => {
    renderWithProviders(
      <TeamSelector
        teams={[makeTeam()]}
        selectedTeamId={undefined}
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText('All teams')).toBeInTheDocument();
  });

  it('calls onSelect with null when "All teams" is chosen', () => {
    const onSelect = vi.fn();
    renderWithProviders(
      <TeamSelector
        teams={[makeTeam({ id: 'a', name: 'Alpha' })]}
        selectedTeamId="a"
        onSelect={onSelect}
      />,
    );
    // Open the select
    fireEvent.click(screen.getByRole('combobox'));
    fireEvent.click(screen.getByText('All teams'));
    expect(onSelect).toHaveBeenCalledWith(null);
  });

  it('calls onSelect with team id when a specific team is chosen', () => {
    const onSelect = vi.fn();
    renderWithProviders(
      <TeamSelector
        teams={[
          makeTeam({ id: 'a', name: 'Alpha' }),
          makeTeam({ id: 'b', name: 'Beta' }),
        ]}
        selectedTeamId={undefined}
        onSelect={onSelect}
      />,
    );
    fireEvent.click(screen.getByRole('combobox'));
    fireEvent.click(screen.getByText('Beta'));
    expect(onSelect).toHaveBeenCalledWith('b');
  });
});
