import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { fireEvent, renderWithProviders, screen, waitFor } from '@/test/test-utils';
import userEvent from '@testing-library/user-event';
import { server } from '@/test/mocks/server';
import { ProjectSidebar } from './project-sidebar';
import type { Project, ListResponse } from '@/lib/types';

const { notifyError } = vi.hoisted(() => ({
  notifyError: vi.fn(),
}));

vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

let mockPathname = '/projects';
let mockSelectedSegment: string | null = null;
const mockAuthState: {
  isAuthenticated: boolean;
  user: { id: string; role: string } | null;
  isLoading: boolean;
  logout: ReturnType<typeof vi.fn>;
} = {
  isAuthenticated: true,
  user: { id: 'user-1', role: 'member' },
  isLoading: false,
  logout: vi.fn(),
};

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => mockPathname,
  useSelectedLayoutSegment: () => mockSelectedSegment,
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => mockAuthState,
}));

vi.mock('@/lib/notify', () => ({
  notify: {
    error: notifyError,
  },
}));

describe('ProjectSidebar', () => {
  beforeEach(() => {
    mockPathname = '/projects';
    mockSelectedSegment = null;
    mockAuthState.isAuthenticated = true;
    mockAuthState.user = { id: 'user-1', role: 'member' };
    mockAuthState.isLoading = false;
    mockAuthState.logout = vi.fn();
    notifyError.mockReset();
  });

  it('shows loading state initially', () => {
    renderWithProviders(<ProjectSidebar />);
    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it('renders projects returned from the API', async () => {
    renderWithProviders(<ProjectSidebar />);
    expect(await screen.findByText('Test Project')).toBeInTheDocument();
    expect(screen.getByText('Security Sweep')).toBeInTheDocument();
  });

  it('defaults the people scope to Mine', async () => {
    let capturedCreatedBy: string | null = null;
    server.use(
      http.get('*/api/v1/projects', ({ request }) => {
        capturedCreatedBy = new URL(request.url).searchParams.get('created_by_ids');
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<Project>);
      }),
    );

    renderWithProviders(<ProjectSidebar />);

    await screen.findByRole('button', { name: /Mine/ });
    expect(capturedCreatedBy).toBe('user-1');
  });

  it('switches the people scope to Everyone', async () => {
    const createdByValues: string[] = [];
    server.use(
      http.get('*/api/v1/projects', ({ request }) => {
        createdByValues.push(new URL(request.url).searchParams.get('created_by_ids') ?? '');
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<Project>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<ProjectSidebar />);

    await user.click(screen.getByRole('button', { name: /Mine/ }));
    await user.click(await screen.findByRole('button', { name: 'Everyone' }));

    expect(createdByValues).toContain('user-1');
    expect(createdByValues).toContain('');
  });

  it('can switch legacy user=all links back to Mine', async () => {
    const createdByValues: string[] = [];
    server.use(
      http.get('*/api/v1/projects', ({ request }) => {
        createdByValues.push(new URL(request.url).searchParams.get('created_by_ids') ?? '');
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<Project>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<ProjectSidebar />, { searchParams: { user: 'all' } });

    await user.click(await screen.findByRole('button', { name: /Everyone/ }));
    await user.click(await screen.findByRole('button', { name: 'Mine' }));

    expect(createdByValues).toContain('');
    expect(createdByValues).toContain('user-1');
  });

  it('shows the people filter trigger', async () => {
    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    expect(screen.getByRole('button', { name: /Mine/ })).toBeInTheDocument();
  });

  it('displays New project link at top', () => {
    renderWithProviders(<ProjectSidebar />);
    const link = screen.getByRole('link', { name: /New project/ });
    expect(link).toHaveAttribute('href', '/projects/new');
  });

  it('hides the New project entry for builders', async () => {
    mockAuthState.user = { id: 'user-1', role: 'builder' };

    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    expect(screen.queryByRole('link', { name: /New project/ })).not.toBeInTheDocument();
  });

  it('does not request the team roster for builders', async () => {
    mockAuthState.user = { id: 'user-1', role: 'builder' };
    let teamRequestCount = 0;

    server.use(
      http.get('*/api/v1/team/members', () => {
        teamRequestCount += 1;
        return HttpResponse.json({ error: { code: 'FORBIDDEN', message: 'insufficient permissions' } }, { status: 403 });
      }),
    );

    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    expect(teamRequestCount).toBe(0);
  });

  it('shows empty state when API returns no projects', async () => {
    server.use(
      http.get('*/api/v1/projects', () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<Project>);
      }),
    );

    renderWithProviders(<ProjectSidebar />, { searchParams: { people: 'all' } });
    expect(await screen.findByText('No projects yet')).toBeInTheDocument();
  });

  it('shows a dedicated empty state when the default Mine view is empty', async () => {
    server.use(
      http.get('*/api/v1/projects', ({ request }) => {
        const createdBy = new URL(request.url).searchParams.get('created_by_ids');
        if (createdBy === 'user-1') {
          return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<Project>);
        }
        return HttpResponse.json({
          data: [{
            id: 'proj-team',
            org_id: 'org-1',
            repository_id: 'repo-1',
            title: 'Teammate Project',
            goal: 'Owned by someone else',
            status: 'active',
            priority: 50,
            execution_mode: 'sequential',
            max_concurrent: 1,
            auto_merge: false,
            base_branch: 'main',
            total_tasks: 0,
            completed_tasks: 0,
            failed_tasks: 0,
            proposed_by_pm: false,
            created_at: '2026-02-17T07:00:00Z',
            updated_at: '2026-02-17T07:00:00Z',
          }],
          meta: {},
        } satisfies ListResponse<Project>);
      }),
    );

    renderWithProviders(<ProjectSidebar />);

    expect(await screen.findByText('No projects in Mine yet')).toBeInTheDocument();
    expect(screen.getByText('Switch to Everyone to browse team projects, or create a new one.')).toBeInTheDocument();
  });

  it('does not fetch projects until the Mine scope can resolve the current user', async () => {
    mockAuthState.isAuthenticated = false;
    mockAuthState.user = null;
    mockAuthState.isLoading = true;

    let requestCount = 0;
    server.use(
      http.get('*/api/v1/projects', () => {
        requestCount += 1;
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<Project>);
      }),
    );

    renderWithProviders(<ProjectSidebar />);

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(requestCount).toBe(0);
    expect(screen.getByText('Loading...')).toBeInTheDocument();
  });

  it('shows status filter tabs', async () => {
    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    expect(screen.getByRole('tab', { name: /All/ })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /Active/ })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /Draft/ })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /Done/ })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /Archived/ })).toBeInTheDocument();
  });

  it('archives a project from the swipe action', async () => {
    let archiveCalls = 0;
    server.use(
      http.post('*/api/v1/projects/proj-1/archive', () => {
        archiveCalls += 1;
        return HttpResponse.json({ status: 'archived' });
      }),
    );

    renderWithProviders(<ProjectSidebar />);
    const row = await screen.findByText('Test Project');
    const surface = row.closest('[data-swipe-surface="true"]');
    expect(surface).not.toBeNull();

    fireEvent.touchStart(surface!, { touches: [{ clientX: 220, clientY: 24 }] });
    fireEvent.touchMove(surface!, { touches: [{ clientX: 120, clientY: 26 }] });
    fireEvent.touchEnd(surface!);

    fireEvent.click(screen.getAllByRole('button', { name: 'Archive project' })[0]);

    await waitFor(() => {
      expect(archiveCalls).toBe(1);
    });
  });

  it('shows an error toast when project archive fails', async () => {
    server.use(
      http.post('*/api/v1/projects/proj-1/archive', () => {
        return HttpResponse.json(
          { error: { code: 'ARCHIVE_FAILED', message: 'archive failed' } },
          { status: 409 },
        );
      }),
    );

    renderWithProviders(<ProjectSidebar />);
    const row = await screen.findByText('Test Project');
    const surface = row.closest('[data-swipe-surface="true"]');
    expect(surface).not.toBeNull();

    fireEvent.touchStart(surface!, { touches: [{ clientX: 220, clientY: 24 }] });
    fireEvent.touchMove(surface!, { touches: [{ clientX: 120, clientY: 26 }] });
    fireEvent.touchEnd(surface!);

    fireEvent.click(screen.getAllByRole('button', { name: 'Archive project' })[0]);

    await waitFor(() => {
      expect(notifyError).toHaveBeenCalledWith('Failed to archive project');
    });
  });

  it('keeps the desktop archive action de-emphasized until hover or focus', async () => {
    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    expect(screen.getAllByRole('button', { name: 'Archive project' })[0]).toHaveClass(
      'md:opacity-0',
      'md:group-hover:opacity-100',
      'md:focus-visible:opacity-100',
    );
  });

  it('uses a left-aligned horizontal-only tab scroller', async () => {
    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    const tabList = screen.getByRole('tablist');
    expect(tabList).toHaveAttribute('data-variant', 'line');
    expect(tabList.className).toContain('pb-1');
    expect(tabList.className).toContain('justify-start');
    expect(tabList.className).toContain('overflow-x-auto');
    expect(tabList.className).toContain('overflow-y-visible');
  });

  it('has search input', () => {
    renderWithProviders(<ProjectSidebar />);
    expect(screen.getByPlaceholderText('Search projects...')).toBeInTheDocument();
  });

  it('restores search from the URL and preserves it in project detail links', async () => {
    renderWithProviders(<ProjectSidebar />, {
      searchParams: { people: 'all', search: 'Security' },
    });

    const input = await screen.findByPlaceholderText('Search projects...');
    expect(input).toHaveValue('Security');
    expect(screen.queryByText('Test Project')).not.toBeInTheDocument();
    expect((await screen.findByText('Security Sweep')).closest('a')).toHaveAttribute(
      'href',
      expect.stringMatching(/^\/projects\/[^?]+\?people=all&search=Security$/),
    );
  });

  it('shows mini progress bars for projects with tasks', async () => {
    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    // Both mock projects have tasks, so progress counts should show
    expect(screen.getByText('1/3')).toBeInTheDocument();
    expect(screen.getByText('5/5')).toBeInTheDocument();
  });

  it('shows ghost New project entry when on /projects/new', async () => {
    mockPathname = '/projects/new';
    mockSelectedSegment = 'new';

    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    const newProjectTexts = screen.getAllByText('New project');
    // At least 2: the top "+ New project" link + the ghost entry
    expect(newProjectTexts.length).toBeGreaterThanOrEqual(2);
  });

  it('hides the ghost New project entry for builders', async () => {
    mockAuthState.user = { id: 'user-1', role: 'builder' };
    mockPathname = '/projects/new';
    mockSelectedSegment = 'new';

    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    expect(screen.queryByText('New project')).not.toBeInTheDocument();
  });

  it('highlights the selected project from the active layout segment', async () => {
    mockPathname = '/projects/proj-1';
    mockSelectedSegment = 'proj-1';

    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    const selectedLink = screen.getByText('Test Project').closest('a');
    expect(selectedLink?.className).toContain('bg-background');
    expect(selectedLink?.className).toContain('shadow-sm');
    expect(selectedLink?.className).toContain('border');
  });

  it('preserves the user/status/repo filters in project detail links', async () => {
    renderWithProviders(<ProjectSidebar />, {
      searchParams: { people: 'all', status: 'active', repo: 'repo-1' },
    });

    const link = (await screen.findByText('Test Project')).closest('a');
    expect(link?.getAttribute('href')).toMatch(
      /^\/projects\/[^?]+\?people=all&status=active&repo=repo-1$/,
    );
  });

  it('preserves search alongside the existing filters in project detail links', async () => {
    renderWithProviders(<ProjectSidebar />, {
      searchParams: { people: 'all', status: 'active', repo: 'repo-1', search: 'Test' },
    });

    const link = (await screen.findByText('Test Project')).closest('a');
    expect(link?.getAttribute('href')).toMatch(
      /^\/projects\/[^?]+\?people=all&status=active&repo=repo-1&search=Test$/,
    );
  });

  it('preserves explicit people selections', async () => {
    renderWithProviders(<ProjectSidebar />, {
      searchParams: { people: 'user-2,user-3' },
    });

    const link = (await screen.findByText('Test Project')).closest('a');
    expect(link?.getAttribute('href')).toMatch(/^\/projects\/[^?]+\?people=user-2%2Cuser-3$/);
  });

  it('only serializes the filters that are actually set', async () => {
    renderWithProviders(<ProjectSidebar />, {
      searchParams: { status: 'active' },
    });

    const link = (await screen.findByText('Test Project')).closest('a');
    expect(link?.getAttribute('href')).toMatch(/^\/projects\/[^?]+\?status=active$/);
  });

  it('omits the query suffix on project detail links when no filters are active', async () => {
    renderWithProviders(<ProjectSidebar />);

    const link = (await screen.findByText('Test Project')).closest('a');
    expect(link?.getAttribute('href')).toMatch(/^\/projects\/[^?]+$/);
  });
});
