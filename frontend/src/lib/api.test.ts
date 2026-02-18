import { describe, it, expect } from 'vitest';
import { server } from '@/test/mocks/server';
import { http, HttpResponse } from 'msw';
import { api } from './api';

describe('api client', () => {
  describe('issues', () => {
    it('fetches issues list', async () => {
      const mockData = {
        data: [
          {
            id: 'issue-1',
            org_id: 'org-1',
            external_id: 'SENTRY-123',
            source: 'sentry',
            title: 'TypeError: Cannot read properties of undefined',
            status: 'open',
            first_seen_at: '2026-02-10T10:00:00Z',
            last_seen_at: '2026-02-17T08:00:00Z',
            occurrence_count: 142,
            affected_customer_count: 23,
            severity: 'critical',
            fingerprint: 'fp-abc123',
            created_at: '2026-02-10T10:00:00Z',
            updated_at: '2026-02-17T08:00:00Z',
          },
        ],
        meta: {},
      };

      server.use(
        http.get('/api/v1/issues', () => {
          return HttpResponse.json(mockData);
        }),
      );

      const result = await api.issues.list();
      expect(result.data).toHaveLength(1);
      expect(result.data[0].id).toBe('issue-1');
      expect(result.data[0].title).toBe('TypeError: Cannot read properties of undefined');
    });

    it('fetches issues with params', async () => {
      let capturedUrl: string | undefined;

      server.use(
        http.get('/api/v1/issues', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );

      await api.issues.list({ status: 'open', severity: 'critical' });

      expect(capturedUrl).toBeDefined();
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('status')).toBe('open');
      expect(url.searchParams.get('severity')).toBe('critical');
    });

    it('fetches single issue', async () => {
      const mockIssue = {
        data: {
          id: 'id-1',
          org_id: 'org-1',
          external_id: 'SENTRY-999',
          source: 'sentry',
          title: 'A single issue',
          status: 'open',
          first_seen_at: '2026-02-10T10:00:00Z',
          last_seen_at: '2026-02-17T08:00:00Z',
          occurrence_count: 5,
          affected_customer_count: 1,
          severity: 'low',
          fingerprint: 'fp-xyz',
          created_at: '2026-02-10T10:00:00Z',
          updated_at: '2026-02-17T08:00:00Z',
        },
      };

      server.use(
        http.get('/api/v1/issues/:id', () => {
          return HttpResponse.json(mockIssue);
        }),
      );

      const result = await api.issues.get('id-1');
      expect(result.data.id).toBe('id-1');
      expect(result.data.title).toBe('A single issue');
    });
  });

  describe('runs', () => {
    it('fetches runs list', async () => {
      const mockData = {
        data: [
          {
            id: 'run-1',
            issue_id: 'issue-1',
            org_id: 'org-1',
            agent_type: 'claude_code',
            status: 'completed',
            autonomy_level: 'full',
            token_mode: 'standard',
            created_at: '2026-02-17T07:00:00Z',
          },
        ],
        meta: {},
      };

      server.use(
        http.get('/api/v1/runs', () => {
          return HttpResponse.json(mockData);
        }),
      );

      const result = await api.runs.list();
      expect(result.data).toHaveLength(1);
      expect(result.data[0].id).toBe('run-1');
      expect(result.data[0].status).toBe('completed');
    });

    it('fetches runs with status filter', async () => {
      let capturedUrl: string | undefined;

      server.use(
        http.get('/api/v1/runs', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );

      await api.runs.list({ status: 'completed' });

      expect(capturedUrl).toBeDefined();
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('status')).toBe('completed');
    });

    it('fetches single run', async () => {
      const mockRun = {
        data: {
          id: 'run-abc',
          issue_id: 'issue-1',
          org_id: 'org-1',
          agent_type: 'claude_code',
          status: 'running',
          autonomy_level: 'supervised',
          token_mode: 'standard',
          created_at: '2026-02-17T07:00:00Z',
        },
      };

      server.use(
        http.get('/api/v1/runs/:id', () => {
          return HttpResponse.json(mockRun);
        }),
      );

      const result = await api.runs.get('run-abc');
      expect(result.data.id).toBe('run-abc');
      expect(result.data.status).toBe('running');
    });
  });

  describe('repositories', () => {
    it('fetches repositories list', async () => {
      const mockData = {
        data: [
          {
            id: 'repo-1',
            org_id: 'org-1',
            integration_id: 'int-1',
            github_id: 12345,
            full_name: 'org/repo',
            default_branch: 'main',
            private: false,
            clone_url: 'https://github.com/org/repo.git',
            installation_id: 100,
            status: 'active',
            settings: {},
            created_at: '2026-01-01T00:00:00Z',
            updated_at: '2026-01-01T00:00:00Z',
          },
        ],
        meta: {},
      };

      server.use(
        http.get('/api/v1/repositories', () => {
          return HttpResponse.json(mockData);
        }),
      );

      const result = await api.repositories.list();
      expect(result.data).toHaveLength(1);
      expect(result.data[0].id).toBe('repo-1');
      expect(result.data[0].full_name).toBe('org/repo');
    });

    it('fetches single repository', async () => {
      const mockRepo = {
        data: {
          id: 'repo-2',
          org_id: 'org-1',
          integration_id: 'int-1',
          github_id: 67890,
          full_name: 'org/another-repo',
          default_branch: 'main',
          private: true,
          clone_url: 'https://github.com/org/another-repo.git',
          installation_id: 100,
          status: 'active',
          settings: {},
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      };

      server.use(
        http.get('/api/v1/repositories/:id', () => {
          return HttpResponse.json(mockRepo);
        }),
      );

      const result = await api.repositories.get('repo-2');
      expect(result.data.id).toBe('repo-2');
      expect(result.data.full_name).toBe('org/another-repo');
    });

    it('deletes repository', async () => {
      let deleteCalled = false;

      server.use(
        http.delete('/api/v1/repositories/:id', ({ params }) => {
          deleteCalled = true;
          expect(params.id).toBe('repo-1');
          return HttpResponse.json({});
        }),
      );

      await api.repositories.delete('repo-1');
      expect(deleteCalled).toBe(true);
    });
  });

  describe('settings', () => {
    it('fetches settings', async () => {
      const mockSettings = {
        data: {
          id: 'org-1',
          name: 'Test Org',
          slug: 'test-org',
          settings: { theme: 'dark' },
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      };

      server.use(
        http.get('/api/v1/settings', () => {
          return HttpResponse.json(mockSettings);
        }),
      );

      const result = await api.settings.get();
      expect(result.data.id).toBe('org-1');
      expect(result.data.name).toBe('Test Org');
    });

    it('updates settings', async () => {
      let capturedBody: unknown;

      server.use(
        http.patch('/api/v1/settings', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({
            data: {
              id: 'org-1',
              name: 'New Name',
              slug: 'test-org',
              settings: {},
              created_at: '2026-01-01T00:00:00Z',
              updated_at: '2026-01-01T00:00:00Z',
            },
          });
        }),
      );

      const result = await api.settings.update({ name: 'New Name' });
      expect(result.data.name).toBe('New Name');
      expect(capturedBody).toEqual({ name: 'New Name' });
    });
  });

  describe('auth', () => {
    it('logout calls POST', async () => {
      let logoutCalled = false;

      server.use(
        http.post('/api/v1/auth/logout', () => {
          logoutCalled = true;
          return HttpResponse.json({});
        }),
      );

      await api.auth.logout();
      expect(logoutCalled).toBe(true);
    });
  });

  describe('error handling', () => {
    it('throws ApiError on non-ok response', async () => {
      server.use(
        http.get('/api/v1/issues', () => {
          return HttpResponse.json(
            { error: { code: 'BAD_REQUEST', message: 'bad request' } },
            { status: 400 },
          );
        }),
      );

      await expect(api.issues.list()).rejects.toThrow('bad request');

      try {
        await api.issues.list();
      } catch (err: unknown) {
        const error = err as { name: string; code: string; message: string };
        expect(error.name).toBe('ApiError');
        expect(error.code).toBe('BAD_REQUEST');
        expect(error.message).toBe('bad request');
      }
    });

    it('handles non-JSON error response', async () => {
      server.use(
        http.get('/api/v1/issues', () => {
          return new HttpResponse('Internal Server Error', {
            status: 500,
            headers: { 'Content-Type': 'text/plain' },
          });
        }),
      );

      await expect(api.issues.list()).rejects.toThrow();

      try {
        await api.issues.list();
      } catch (err: unknown) {
        const error = err as { name: string; code: string };
        expect(error.name).toBe('ApiError');
        expect(error.code).toBe('UNKNOWN');
      }
    });
  });
});
