import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import Loading from './loading';

describe('SessionDetail route loading shell', () => {
  it('renders the session detail skeleton', () => {
    render(<Loading />);

    expect(screen.getByTestId('session-detail-loading-skeleton')).toBeInTheDocument();
  });
});
