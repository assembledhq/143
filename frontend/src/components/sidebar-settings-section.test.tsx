import { describe, it, expect } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import { SidebarSettingsSection } from './sidebar-settings-section';

describe('SidebarSettingsSection', () => {
  it('shows admin-only entries when role is admin', () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings" userRole="admin" />
    );

    for (const label of [
      'Account',
      'Integrations',
      'Coding agents',
      'LLM',
      'Autopilot',
      'Evals',
      'General',
      'Team',
      'Usage',
      'Audit log',
    ]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
  });

  it('hides admin-only entries for members', () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings/account" userRole="member" />
    );

    expect(screen.getByText('Account')).toBeInTheDocument();
    expect(screen.getByText('Evals')).toBeInTheDocument();
    expect(screen.getByText('Team')).toBeInTheDocument();

    for (const hidden of [
      'Integrations',
      'Coding agents',
      'LLM',
      'Autopilot',
      'General',
      'Usage',
      'Audit log',
    ]) {
      expect(screen.queryByText(hidden)).not.toBeInTheDocument();
    }
  });

  it('hides admin-only entries and Team for viewers (Team is admin+member-only on the backend)', () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings/account" userRole="viewer" />
    );

    expect(screen.getByText('Account')).toBeInTheDocument();
    expect(screen.getByText('Evals')).toBeInTheDocument();

    for (const hidden of [
      'Integrations',
      'Coding agents',
      'LLM',
      'Autopilot',
      'General',
      'Team',
      'Usage',
      'Audit log',
    ]) {
      expect(screen.queryByText(hidden)).not.toBeInTheDocument();
    }
  });
});
