import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Table, TableBody, TableCell, TableRow } from './table';

describe('Table', () => {
  it('allows body cells to wrap long content by default', () => {
    render(
      <Table>
        <TableBody>
          <TableRow>
            <TableCell>long_session_pr_push_requested_value</TableCell>
          </TableRow>
        </TableBody>
      </Table>
    );

    expect(screen.getByText('long_session_pr_push_requested_value')).toHaveClass(
      'whitespace-normal',
      'break-words'
    );
  });

  it('allows callers to keep specific cells on one line', () => {
    render(
      <Table>
        <TableBody>
          <TableRow>
            <TableCell className="whitespace-nowrap">5m ago</TableCell>
          </TableRow>
        </TableBody>
      </Table>
    );

    expect(screen.getByText('5m ago')).toHaveClass('whitespace-nowrap');
  });
});
