import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { ChangesetSummary } from '@/lib/types';
import { PullRequestList } from './session-detail-content';

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
    await userEvent.click(screen.getByRole('button', { name: /API integration/ }));
    expect(onSelect).toHaveBeenCalledWith('changeset-2');
  });
});
