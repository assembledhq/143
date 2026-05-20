import { describe, it, expect } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import { SidebarSettingsSection } from './sidebar-settings-section';

describe('SidebarSettingsSection', () => {
  it('uses the same touch target sizing as other mobile nav tabs', () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/sessions" userRole="admin" variant="mobile" />
    );

    expect(screen.getByRole('button', { name: /Settings/ })).toHaveClass('px-2.5', 'py-3', 'text-sm');
  });

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

  it('shows view-only pages for members and hides admin-only pages', () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings/account" userRole="member" />
    );

    for (const visible of [
      'Account',
      'Evals',
      'Team',
      'Integrations',
      'Coding agents',
    ]) {
      expect(screen.getByText(visible)).toBeInTheDocument();
    }

    for (const hidden of [
      'LLM',
      'Autopilot',
      'General',
      'Usage',
      'Audit log',
    ]) {
      expect(screen.queryByText(hidden)).not.toBeInTheDocument();
    }
  });

  it('shows only Account for viewers', () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings/account" userRole="viewer" />
    );

    expect(screen.getByText('Account')).toBeInTheDocument();

    for (const hidden of [
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
      expect(screen.queryByText(hidden)).not.toBeInTheDocument();
    }
  });

  it('shows only builder-safe settings entries for builders', () => {
    renderWithProviders(
      <SidebarSettingsSection pathname="/settings/account" userRole="builder" />
    );

    for (const visible of [
      'Account',
      'Coding agents',
    ]) {
      expect(screen.getByText(visible)).toBeInTheDocument();
    }

    for (const hidden of [
      'Integrations',
      'Evals',
      'Team',
      'LLM',
      'Autopilot',
      'General',
      'Usage',
      'Audit log',
    ]) {
      expect(screen.queryByText(hidden)).not.toBeInTheDocument();
    }
  });
});
