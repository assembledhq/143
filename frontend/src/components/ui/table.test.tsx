import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Table, TableBody, TableCell, TableHeader, TableRow } from './table';

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

  it('uses semantic surfaces for sticky headers and selected rows', () => {
    render(
      <Table>
        <TableHeader data-testid="table-header" />
        <TableBody>
          <TableRow data-state="selected" data-testid="selected-row">
            <TableCell>Selected session</TableCell>
          </TableRow>
        </TableBody>
      </Table>
    );

    expect(screen.getByTestId('table-header')).toHaveClass('bg-surface-raised');
    expect(screen.getByTestId('selected-row')).toHaveClass('data-[state=selected]:bg-surface-selected');
  });
});
