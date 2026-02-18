import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { DiffViewer } from './diff-viewer';

describe('DiffViewer', () => {
  it('renders added, removed, and hunk lines', () => {
    render(<DiffViewer diff={'@@ -1,2 +1,2 @@\n-old\n+new\n context'} />);

    expect(screen.getByText('@@ -1,2 +1,2 @@')).toBeInTheDocument();
    expect(screen.getByText('-old')).toBeInTheDocument();
    expect(screen.getByText('+new')).toBeInTheDocument();
    expect(screen.getByText(/context/)).toBeInTheDocument();
  });

  it('renders an empty line as a non-breaking space placeholder', () => {
    render(<DiffViewer diff={'line-1\n\nline-3'} />);

    expect(
      screen.getByText((_, element) => element?.textContent === '\u00A0'),
    ).toBeInTheDocument();
  });
});
