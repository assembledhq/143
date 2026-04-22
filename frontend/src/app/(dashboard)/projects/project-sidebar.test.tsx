import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import userEvent from '@testing-library/user-event';
import { server } from '@/test/mocks/server';
import { ProjectSidebar } from './project-sidebar';
import type { Project, ListResponse } from '@/lib/types';

vi.mock('next/link', () => ({
  default: ({ children, href, ...props }: React.ComponentProps<'a'> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

let mockPathname = '/projects';
const mockAuthState: {
  isAuthenticated: boolean;
  user: { id: string } | null;
  isLoading: boolean;
  logout: ReturnType<typeof vi.fn>;
} = {
  isAuthenticated: true,
  user: { id: 'user-1' },
  isLoading: false,
  logout: vi.fn(),
};

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => mockPathname,
  useParams: () => ({}),
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => mockAuthState,
}));

describe('ProjectSidebar', () => {
  beforeEach(() => {
    mockPathname = '/projects';
    mockAuthState.isAuthenticated = true;
    mockAuthState.user = { id: 'user-1' };
    mockAuthState.isLoading = false;
    mockAuthState.logout = vi.fn();
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

  it('defaults the owner scope to Mine', async () => {
    let capturedCreatedBy: string | null = null;
    server.use(
      http.get('*/api/v1/projects', ({ request }) => {
        capturedCreatedBy = new URL(request.url).searchParams.get('created_by');
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<Project>);
      }),
    );

    renderWithProviders(<ProjectSidebar />);

    await screen.findByRole('radio', { name: 'Mine' });
    expect(capturedCreatedBy).toBe('user-1');
  });

  it('switches the owner scope to Everyone', async () => {
    const createdByValues: string[] = [];
    server.use(
      http.get('*/api/v1/projects', ({ request }) => {
        createdByValues.push(new URL(request.url).searchParams.get('created_by') ?? '');
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<Project>);
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<ProjectSidebar />);

    await screen.findByRole('radio', { name: 'Mine' });
    await user.click(screen.getByRole('radio', { name: 'Everyone' }));

    expect(createdByValues).toContain('user-1');
    expect(createdByValues).toContain('');
  });

  it('shows the owner scope toggle', async () => {
    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    expect(screen.getByRole('radio', { name: 'Everyone' })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: 'Mine' })).toBeInTheDocument();
  });

  it('displays New project link at top', () => {
    renderWithProviders(<ProjectSidebar />);
    const link = screen.getByRole('link', { name: /New project/ });
    expect(link).toHaveAttribute('href', '/projects/new');
  });

  it('shows empty state when API returns no projects', async () => {
    server.use(
      http.get('*/api/v1/projects', () => {
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<Project>);
      }),
    );

    renderWithProviders(<ProjectSidebar />, { searchParams: { user: 'all' } });
    expect(await screen.findByText('No projects yet')).toBeInTheDocument();
  });

  it('shows filtered empty state when Mine has no matching projects', async () => {
    server.use(
      http.get('*/api/v1/projects', ({ request }) => {
        const createdBy = new URL(request.url).searchParams.get('created_by');
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

    expect(await screen.findByText('No projects match this filter.')).toBeInTheDocument();
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
  });

  it('uses a left-aligned horizontal-only tab scroller', async () => {
    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    const tabList = screen.getByRole('tablist');
    expect(tabList.className).toContain('justify-start');
    expect(tabList.className).toContain('overflow-x-auto');
    expect(tabList.className).toContain('overflow-y-hidden');
  });

  it('has search input', () => {
    renderWithProviders(<ProjectSidebar />);
    expect(screen.getByPlaceholderText('Search projects...')).toBeInTheDocument();
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

    renderWithProviders(<ProjectSidebar />);
    await screen.findByText('Test Project');

    const newProjectTexts = screen.getAllByText('New project');
    // At least 2: the top "+ New project" link + the ghost entry
    expect(newProjectTexts.length).toBeGreaterThanOrEqual(2);
  });
});
