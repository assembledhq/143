import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
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

    expect(screen.getByRole('button', { name: /All/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Active/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Draft/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Done/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Paused/ })).toBeInTheDocument();
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
