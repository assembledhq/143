import { describe, it, expect } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import { Button } from './button';

describe('Button', () => {
  it('shows a spinner and disables the button when loading', () => {
    const { container } = renderWithProviders(
      <Button loading>Submit</Button>,
    );

    const button = screen.getByRole('button', { name: 'Submit' });
    expect(button).toBeDisabled();
    expect(container.querySelector('[data-slot="button-spinner"]')).toBeInTheDocument();
  });

  it('does not show a spinner when not loading', () => {
    const { container } = renderWithProviders(
      <Button>Submit</Button>,
    );

    const button = screen.getByRole('button', { name: 'Submit' });
    expect(button).toBeEnabled();
    expect(container.querySelector('[data-slot="button-spinner"]')).not.toBeInTheDocument();
  });
});
