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

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => mockPathname,
  useParams: () => ({}),
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: () => ({ isAuthenticated: true, user: { id: 'user-1' }, isLoading: false, logout: vi.fn() }),
}));

describe('ProjectSidebar', () => {
  beforeEach(() => {
    mockPathname = '/projects';
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

    renderWithProviders(<ProjectSidebar />);
    expect(await screen.findByText('No projects yet')).toBeInTheDocument();
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
