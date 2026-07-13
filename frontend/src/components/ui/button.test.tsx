import { describe, it, expect } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import { Button } from './button';

describe('Button', () => {
  it('uses touch-safe mobile sizing and compact desktop sizing', () => {
    renderWithProviders(
      <Button>Compact</Button>,
    );

    const button = screen.getByRole('button', { name: 'Compact' });
    expect(button).toHaveClass('h-11', 'sm:h-8');
  });

  it('shows a spinner and disables the button when loading', () => {
    const { container } = renderWithProviders(
      <Button loading>Submit</Button>,
    );

    const button = screen.getByRole('button', { name: 'Submit' });
    expect(button).toBeDisabled();
    expect(container.querySelector('[data-slot="button-spinner"]')).toBeInTheDocument();
  });

  it('keeps loading buttons at full opacity', () => {
    renderWithProviders(
      <Button loading>Submit</Button>,
    );

    const button = screen.getByRole('button', { name: 'Submit' });
    expect(button).toHaveAttribute('data-loading', 'true');
    expect(button).toHaveClass('disabled:data-[loading=true]:opacity-100');
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
