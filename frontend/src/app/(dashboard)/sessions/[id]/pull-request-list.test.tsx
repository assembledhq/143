import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { ChangesetSummary } from '@/lib/types';
import {
  CHANGESET_SPLIT_MIN_ADDITIONS,
  ChangesetSplitPrompt,
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

describe('ChangesetSplitPrompt', () => {
  it.each([
    { additions: undefined, expected: false },
    { additions: CHANGESET_SPLIT_MIN_ADDITIONS - 1, expected: false },
    { additions: CHANGESET_SPLIT_MIN_ADDITIONS, expected: true },
    { additions: CHANGESET_SPLIT_MIN_ADDITIONS + 1, expected: true },
  ])('offers splitting for $additions additions: $expected', ({ additions, expected }) => {
    expect(shouldOfferChangesetSplit(additions)).toBe(expected);
  });

  it('asks the coding agent to split the diff instead of initializing a manual split', async () => {
    const onRequestSplit = vi.fn();

    render(
      <ChangesetSplitPrompt
        additions={CHANGESET_SPLIT_MIN_ADDITIONS}
        filesChanged={1}
        onRequestSplit={onRequestSplit}
      />,
    );

    await userEvent.click(await screen.findByRole('button', { name: 'Split into PRs' }));

    expect(screen.getByText('Large change · 750 additions · 1 file')).toBeInTheDocument();
    expect(screen.getByText('Split this diff into smaller, reviewable pull requests.')).toBeInTheDocument();
    expect(document.querySelector('[data-slot="overview-suggestion-icon"] .lucide-git-branch')).toBeInTheDocument();
    expect(onRequestSplit).toHaveBeenCalledOnce();
  });

  it('renders as a quiet suggestion row instead of another card', () => {
    render(
      <ChangesetSplitPrompt
        additions={CHANGESET_SPLIT_MIN_ADDITIONS}
        onRequestSplit={vi.fn()}
      />,
    );

    const suggestion = screen.getByRole('region', { name: 'Pull request size suggestion' });
    expect(suggestion).toHaveAttribute('data-slot', 'overview-suggestion');
    expect(suggestion).toHaveClass('flex', 'items-center');
    expect(suggestion.closest('[data-slot="card"]')).toBeNull();
  });

  it('uses a compact ghost action that stays aligned on narrow panels', () => {
    render(
      <ChangesetSplitPrompt
        additions={CHANGESET_SPLIT_MIN_ADDITIONS}
        onRequestSplit={vi.fn()}
      />,
    );

    const button = screen.getByRole('button', { name: 'Split into PRs' });
    expect(button).toHaveAttribute('data-size', 'xs');
    expect(button).toHaveAttribute('data-variant', 'ghost');
    expect(button).toHaveClass('shrink-0');
  });
});
