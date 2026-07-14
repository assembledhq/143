import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ChangesetSummary } from '@/lib/types';
import {
  CHANGESET_SPLIT_MIN_ADDITIONS,
  ChangesetSplitPlanner,
  PullRequestList,
  shouldOfferChangesetSplit,
} from './session-detail-content';

function changeset(overrides: Partial<ChangesetSummary> = {}): ChangesetSummary {
  return {
    id: 'changeset-1',
    is_primary: true,
    order_index: 0,
    title: 'Foundation',
    summary: 'Shared types',
    status: 'planned',
    target_branch: 'main',
    base_branch: 'main',
    created_at: '2026-07-11T00:00:00Z',
    updated_at: '2026-07-11T00:00:00Z',
    ...overrides,
  };
}

describe('PullRequestList', () => {
  it('stays hidden for the compatible one-PR path', () => {
    const { queryByTestId } = render(
      <PullRequestList changesets={[changeset()]} selectedID="changeset-1" onSelect={vi.fn()} />,
    );

    expect(queryByTestId('pull-request-list')).not.toBeInTheDocument();
  });

  it('renders multiple pull requests and changes selection', async () => {
    const onSelect = vi.fn();
    render(
      <PullRequestList
        changesets={[
          changeset(),
          changeset({
            id: 'changeset-2',
            is_primary: false,
            order_index: 1,
            title: 'API integration',
            active_lease_holder_type: 'agent_turn',
            active_lease_holder_label: 'Tab 2',
            pull_request: {
              id: 'pr-2', session_id: 'session-1', org_id: 'org-1', changeset_id: 'changeset-2',
              github_pr_number: 102, github_pr_url: 'https://github.test/pull/102', github_repo: 'acme/repo',
              title: 'API integration', body: '', status: 'open', branch_name: '143/api', review_status: null,
              ci_status: 'success', merged_at: null, closed_at: null, created_at: '2026-07-11T00:00:00Z', updated_at: '2026-07-11T00:00:00Z',
            },
          }),
        ]}
        selectedID="changeset-1"
        onSelect={onSelect}
      />,
    );

    expect(screen.getByTestId('pull-request-list')).toBeInTheDocument();
    expect(screen.getByText('#102 · open')).toBeInTheDocument();
    expect(screen.getByText('Being edited in Tab 2')).toHaveClass('text-info');
    await userEvent.click(screen.getByRole('button', { name: /API integration/ }));
    expect(onSelect).toHaveBeenCalledWith('changeset-2');
  });

  it('uses the warning token for unpushed changes', () => {
    render(
      <PullRequestList
        changesets={[
          changeset(),
          changeset({
            id: 'changeset-2',
            is_primary: false,
            order_index: 1,
            title: 'Unpushed work',
            has_unpushed_changes: true,
          }),
        ]}
        selectedID="changeset-1"
        onSelect={vi.fn()}
      />,
    );

    expect(screen.getByText('Unpushed changes')).toHaveClass('text-warning');
  });
});

describe('ChangesetSplitPlanner', () => {
  it.each([
    { additions: undefined, expected: false },
    { additions: CHANGESET_SPLIT_MIN_ADDITIONS - 1, expected: false },
    { additions: CHANGESET_SPLIT_MIN_ADDITIONS, expected: true },
    { additions: CHANGESET_SPLIT_MIN_ADDITIONS + 1, expected: true },
  ])('offers splitting for $additions additions: $expected', ({ additions, expected }) => {
    expect(shouldOfferChangesetSplit(additions)).toBe(expected);
  });

  it('shows verified split progress and enables acceptance only when complete', () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    queryClient.setQueryData(['session', 'session-1', 'changeset-split'], {
      data: {
        status: 'draft', source_diff_snapshot_id: 'snapshot-1', source_paths: ['api.go'],
        assignments: [{ changeset_id: 'changeset-2', paths: ['api.go'] }], unassigned_paths: [],
        duplicates: [], conflicts: [], omissions: [], unexpected_paths: [], verification: 'verified', complete: true,
      },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <ChangesetSplitPlanner sessionID="session-1" changesets={[
          changeset(),
          changeset({ id: 'changeset-2', is_primary: false, order_index: 1, title: 'API', worktree_path: '/work/api' }),
        ]} />
      </QueryClientProvider>,
    );
    expect(screen.getByTestId('changeset-split-planner')).toBeInTheDocument();
    expect(screen.getByText('1 of 1 files accounted for')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Accept split' })).toBeEnabled();
  });
});
