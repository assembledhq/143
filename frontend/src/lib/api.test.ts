import { describe, it, expect, afterEach } from 'vitest';
import { server } from '@/test/mocks/server';
import { http, HttpResponse } from 'msw';
import { api } from './api';
import { setActiveOrgId } from './active-org';

describe('api client', () => {
  afterEach(() => {
    setActiveOrgId(null);
  });

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

  describe('sessions', () => {
    it('fetches sessions list', async () => {
      const mockData = {
        data: [
          {
            id: 'session-1',
            primary_issue_id: 'issue-1',
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
        http.get('/api/v1/sessions', () => {
          return HttpResponse.json(mockData);
        }),
      );

      const result = await api.sessions.list();
      expect(result.data).toHaveLength(1);
      expect(result.data[0].id).toBe('session-1');
      expect(result.data[0].primary_issue_id).toBe('issue-1');
      expect(result.data[0].status).toBe('completed');
    });

    it('fetches sessions with status filter', async () => {
      let capturedUrl: string | undefined;

      server.use(
        http.get('/api/v1/sessions', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );

      await api.sessions.list({ status: 'completed' });

      expect(capturedUrl).toBeDefined();
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('status')).toBe('completed');
    });

    it('serializes multi-person session filters', async () => {
      let capturedUrl: string | undefined;

      server.use(
        http.get('/api/v1/sessions', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );

      await api.sessions.list({ triggered_by_user_ids: ['user-1', 'user-2'] });

      expect(capturedUrl).toBeDefined();
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('triggered_by_user_ids')).toBe('user-1,user-2');
    });

    it('normalizes timeline payloads at the API boundary', async () => {
      server.use(
        http.get('/api/v1/sessions/:id/timeline', () => {
          return HttpResponse.json({
            data: [
              {
                kind: 'message',
                created_at: '2026-01-01T00:00:01Z',
                message: {
                  id: 1,
                  session_id: 'session-1',
                  org_id: 'org-1',
                  turn_number: 1,
                  role: 'user',
                  content: 'hello',
                },
              },
              {
                kind: 'log',
                created_at: '2026-01-01T00:00:02Z',
                log: {
                  id: 2,
                  session_id: 'session-1',
                  level: 'info',
                  message: 'working',
                  metadata: null,
                  turn_number: 1,
                },
              },
            ],
            meta: {},
          });
        }),
      );

      const result = await api.sessions.getTimeline('session-1');

      expect(result.data[0].message?.created_at).toBe('2026-01-01T00:00:01Z');
      expect(result.data[1].log?.created_at).toBe('2026-01-01T00:00:02Z');
    });

    it('fetches single session', async () => {
      const mockSession = {
        data: {
          id: 'session-abc',
          primary_issue_id: 'issue-1',
          org_id: 'org-1',
          agent_type: 'claude_code',
          status: 'running',
          autonomy_level: 'supervised',
          token_mode: 'standard',
          created_at: '2026-02-17T07:00:00Z',
        },
      };

      server.use(
        http.get('/api/v1/sessions/:id', () => {
          return HttpResponse.json(mockSession);
        }),
      );

      const result = await api.sessions.get('session-abc');
      expect(result.data.id).toBe('session-abc');
      expect(result.data.status).toBe('running');
    });

    it('fetches a thread message window with cursor params', async () => {
      let capturedUrl: string | undefined;
      const mockWindow = {
        data: [{ id: 21, role: 'assistant', content: 'latest' }],
        meta: {
          next_older_cursor: '21',
          has_older: true,
          latest_assistant_message_id: 21,
          live_edge_message_id: 21,
          thread_status: 'idle',
        },
      };

      server.use(
        http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json(mockWindow);
        }),
      );

      const result = await api.sessions.getThreadMessageWindow('session-abc', 'thread-1', {
        before: '30',
        limit: 25,
      });

      expect(result).toEqual(mockWindow);
      expect(capturedUrl).toBeDefined();
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('before')).toBe('30');
      expect(url.searchParams.get('limit')).toBe('25');
    });

    it('fetches thread message windows around anchors and after cursors', async () => {
      const capturedUrls: string[] = [];
      const mockWindow = {
        data: [],
        meta: {
          has_older: false,
          has_newer: false,
          thread_status: 'idle',
          window_position: 'around',
        },
      };

      server.use(
        http.get('/api/v1/sessions/:id/threads/:threadId/messages', ({ request }) => {
          capturedUrls.push(request.url);
          return HttpResponse.json(mockWindow);
        }),
      );

      await api.sessions.getThreadMessageWindow('session-abc', 'thread-1', {
        position: 'around',
        anchorMessageId: 456,
        limit: 80,
      });
      await api.sessions.getThreadMessageWindow('session-abc', 'thread-1', {
        after: '789',
        limit: 40,
      });

      const aroundUrl = new URL(capturedUrls[0]!);
      expect(aroundUrl.searchParams.get('position')).toBe('around');
      expect(aroundUrl.searchParams.get('anchor_message_id')).toBe('456');
      expect(aroundUrl.searchParams.get('limit')).toBe('80');

      const afterUrl = new URL(capturedUrls[1]!);
      expect(afterUrl.searchParams.get('after')).toBe('789');
      expect(afterUrl.searchParams.get('limit')).toBe('40');
    });

    it('fetches thread logs only for loaded message turns', async () => {
      let capturedUrl: string | undefined;
      const mockLogs = {
        data: [{ id: 101, level: 'output', message: 'latest turn', turn_number: 7 }],
        meta: {},
      };

      server.use(
        http.get('/api/v1/sessions/:id/threads/:threadId/logs', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json(mockLogs);
        }),
      );

      const result = await api.sessions.getThreadLogs('session-abc', 'thread-1', {
        turnNumbers: [7, 6, 7, 5],
      });

      expect(result).toEqual(mockLogs);
      expect(capturedUrl).toBeDefined();
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('turn_numbers')).toBe('5,6,7');
    });

    it('fetches recoverable thread inbox entries', async () => {
      const mockEntries = {
        data: [
          {
            id: 'entry-1',
            org_id: 'org-1',
            session_id: 'session-abc',
            thread_id: 'thread-1',
            sequence_no: 12,
            message_id: 99,
            entry_type: 'user_message',
            payload: { content: 'Please continue' },
            delivery_state: 'dead_letter',
            delivery_attempts: 2,
            last_error: 'payload serialization failed',
            accepted_at: '2026-05-26T10:00:00Z',
            created_at: '2026-05-26T10:00:00Z',
          },
        ],
        meta: {},
      };

      server.use(
        http.get('/api/v1/sessions/:id/threads/:threadId/inbox/recoverable', () => {
          return HttpResponse.json(mockEntries);
        }),
      );

      const result = await api.sessions.listRecoverableThreadInboxEntries('session-abc', 'thread-1');

      expect(result).toEqual(mockEntries);
    });

    it('retries a recoverable thread inbox entry', async () => {
      let capturedBody: unknown;
      const mockEntry = {
        data: {
          id: 'entry-1',
          org_id: 'org-1',
          session_id: 'session-abc',
          thread_id: 'thread-1',
          sequence_no: 12,
          message_id: 99,
          entry_type: 'user_message',
          payload: { content: 'Please continue' },
          delivery_state: 'pending',
          delivery_attempts: 0,
          accepted_at: '2026-05-26T10:00:00Z',
          created_at: '2026-05-26T10:00:00Z',
        },
      };

      server.use(
        http.post('/api/v1/sessions/:id/threads/:threadId/inbox/:entryId/retry', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json(mockEntry);
        }),
      );

      const result = await api.sessions.retryThreadInboxEntry('session-abc', 'thread-1', 'entry-1', { replayUnknownDelivery: true });

      expect(result).toEqual(mockEntry);
      expect(capturedBody).toEqual({ replay_unknown_delivery: true });
    });

    it('answers question with backend contract field', async () => {
      let capturedBody: unknown;

      server.use(
        http.post('/api/v1/sessions/:id/questions/:qid/answer', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { id: 'q-1' } });
        }),
      );

      await api.sessions.answerQuestion('session-1', 'q-1', 'Try option B');
      expect(capturedBody).toEqual({ answer: 'Try option B' });
    });
  });

  describe('projects', () => {
    it('serializes multi-person project filters', async () => {
      let capturedUrl: string | undefined;

      server.use(
        http.get('/api/v1/projects', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );

      await api.projects.list({ created_by_ids: ['user-1', 'user-2'] });

      expect(capturedUrl).toBeDefined();
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('created_by_ids')).toBe('user-1,user-2');
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

  describe('team', () => {
    it('removes member when backend returns 204 no content', async () => {
      let deleteCalled = false;

      server.use(
        http.delete('/api/v1/team/members/:id', ({ params }) => {
          deleteCalled = true;
          expect(params.id).toBe('member-1');
          return new HttpResponse(null, { status: 204 });
        }),
      );

      await expect(api.team.removeMember('member-1')).resolves.toBeUndefined();
      expect(deleteCalled).toBe(true);
    });

    it('revokes invitation when backend returns 204 no content', async () => {
      let revokeCalled = false;

      server.use(
        http.delete('/api/v1/team/invitations/:id', ({ params }) => {
          revokeCalled = true;
          expect(params.id).toBe('inv-1');
          return new HttpResponse(null, { status: 204 });
        }),
      );

      await expect(api.team.revokeInvitation('inv-1')).resolves.toBeUndefined();
      expect(revokeCalled).toBe(true);
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

  describe('priority', () => {
    it('fetches priority score for an issue', async () => {
      const mockPriority = {
        data: {
          id: 'ps-1',
          issue_id: 'issue-1',
          org_id: 'org-1',
          score: 85.5,
          customer_impact_score: 90,
          severity_score: 80,
          recency_score: 75,
          revenue_risk_score: 60,
          direction_alignment: 0.9,
          eligible_for_agent: true,
          computed_at: '2026-02-17T08:00:00Z',
        },
      };

      server.use(
        http.get('/api/v1/issues/:issueId/priority', ({ params }) => {
          expect(params.issueId).toBe('issue-1');
          return HttpResponse.json(mockPriority);
        }),
      );

      const result = await api.priority.getForIssue('issue-1');
      expect(result.data.id).toBe('ps-1');
      expect(result.data.score).toBe(85.5);
      expect(result.data.eligible_for_agent).toBe(true);
    });

    it('fetches priority scores list', async () => {
      const mockData = {
        data: [
          {
            id: 'ps-1',
            issue_id: 'issue-1',
            org_id: 'org-1',
            score: 85.5,
            customer_impact_score: 90,
            severity_score: 80,
            recency_score: 75,
            revenue_risk_score: 60,
            direction_alignment: 0.9,
            eligible_for_agent: true,
            computed_at: '2026-02-17T08:00:00Z',
          },
        ],
        meta: {},
      };

      server.use(
        http.get('/api/v1/priority-scores', () => {
          return HttpResponse.json(mockData);
        }),
      );

      const result = await api.priority.list();
      expect(result.data).toHaveLength(1);
      expect(result.data[0].id).toBe('ps-1');
      expect(result.data[0].score).toBe(85.5);
    });

    it('fetches priority scores with params', async () => {
      let capturedUrl: string | undefined;

      server.use(
        http.get('/api/v1/priority-scores', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );

      await api.priority.list({ eligible_only: true, limit: 10 });

      expect(capturedUrl).toBeDefined();
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('eligible_only')).toBe('true');
      expect(url.searchParams.get('limit')).toBe('10');
    });
  });

  describe('integrations', () => {
    it('fetches integrations list', async () => {
      const mockData = {
        data: [
          {
            id: 'int-1',
            org_id: 'org-1',
            provider: 'github',
            status: 'active',
            last_synced_at: '2026-02-17T08:00:00Z',
            created_at: '2026-01-01T00:00:00Z',
          },
          {
            id: 'int-2',
            org_id: 'org-1',
            provider: 'sentry',
            status: 'active',
            created_at: '2026-01-15T00:00:00Z',
          },
        ],
        meta: {},
      };

      server.use(
        http.get('/api/v1/integrations', () => {
          return HttpResponse.json(mockData);
        }),
      );

      const result = await api.integrations.list();
      expect(result.data).toHaveLength(2);
      expect(result.data[0].id).toBe('int-1');
      expect(result.data[0].provider).toBe('github');
      expect(result.data[1].provider).toBe('sentry');
    });
  });

  describe('memories', () => {
    it('fetches memories by repo', async () => {
      const mockData = {
        data: [
          {
            id: 'rp-1',
            org_id: 'org-1',
            repo: 'org/repo',
            rule: 'Always use error boundaries',
            category: 'error-handling',
            source_comment_ids: ['comment-1', 'comment-2'],
            occurrence_count: 5,
            status: 'active',
            manually_curated: false,
            active: true,
            scope: 'repo',
            source: 'review',
            times_reinforced: 5,
            created_at: '2026-02-01T00:00:00Z',
          },
        ],
        meta: {},
      };

      server.use(
        http.get('/api/v1/memories/:owner/:repo', ({ params }) => {
          expect(params.owner).toBe('org');
          expect(params.repo).toBe('repo');
          return HttpResponse.json(mockData);
        }),
      );

      const result = await api.memories.listByRepo('org/repo');
      expect(result.data).toHaveLength(1);
      expect(result.data[0].id).toBe('rp-1');
      expect(result.data[0].rule).toBe('Always use error boundaries');
    });

    it('fetches memories with params', async () => {
      let capturedUrl: string | undefined;

      server.use(
        http.get('/api/v1/memories/:owner/:repo', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );

      await api.memories.listByRepo('org/repo', { status: 'active', cursor: 'abc' });

      expect(capturedUrl).toBeDefined();
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('status')).toBe('active');
      expect(url.searchParams.get('cursor')).toBe('abc');
    });
  });

  describe('auth - email login and register', () => {
    it('loginEmail sends credentials', async () => {
      let capturedBody: unknown;

      server.use(
        http.post('/api/v1/auth/login', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { id: '1', email: 'a@b.com' } });
        }),
      );

      await api.auth.loginEmail('a@b.com', 'pass123');
      expect(capturedBody).toEqual({ email: 'a@b.com', password: 'pass123' });
    });

    it('register sends user details', async () => {
      let capturedBody: unknown;

      server.use(
        http.post('/api/v1/auth/register', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { id: '1', email: 'a@b.com' } });
        }),
      );

      await api.auth.register('a@b.com', 'pass', 'Test User');
      expect(capturedBody).toEqual({ email: 'a@b.com', password: 'pass', name: 'Test User' });
    });

    it('register includes invitation when provided', async () => {
      let capturedBody: unknown;

      server.use(
        http.post('/api/v1/auth/register', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { id: '1' } });
        }),
      );

      await api.auth.register('a@b.com', 'pass', 'Test', 'inv-1');
      expect(capturedBody).toEqual({ email: 'a@b.com', password: 'pass', name: 'Test', invitation: 'inv-1' });
    });

    it('claimInvitation posts the invitation token', async () => {
      let capturedBody: unknown;

      server.use(
        http.post('/api/v1/invitations/claim', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { org_id: 'org-2', role: 'member' } });
        }),
      );

      const result = await api.auth.claimInvitation('invite-123');

      expect(capturedBody).toEqual({ token: 'invite-123' });
      expect(result.data).toEqual({ org_id: 'org-2', role: 'member' });
    });

    it('auth providers fetches provider info', async () => {
      server.use(
        http.get('/api/v1/auth/providers', () => {
          return HttpResponse.json({ data: { github: true, google: false, email: true } });
        }),
      );

      const result = await api.auth.providers();
      expect(result.data.github).toBe(true);
    });
  });

  describe('pm', () => {
    it('triggers analysis', async () => {
      let called = false;

      server.use(
        http.post('/api/v1/pm/analyze', () => {
          called = true;
          return HttpResponse.json({ data: { job_id: 'job-1' } });
        }),
      );

      const result = await api.pm.analyze();
      expect(called).toBe(true);
      expect(result.data.job_id).toBe('job-1');
    });

    it('lists plans', async () => {
      server.use(
        http.get('/api/v1/pm/plans', () => {
          return HttpResponse.json({ data: [{ id: 'plan-1' }], meta: {} });
        }),
      );

      const result = await api.pm.list();
      expect(result.data).toHaveLength(1);
    });

    it('lists plans with params', async () => {
      let capturedUrl: string | undefined;

      server.use(
        http.get('/api/v1/pm/plans', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );

      await api.pm.list({ cursor: 'c1', limit: 5 });
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('cursor')).toBe('c1');
      expect(url.searchParams.get('limit')).toBe('5');
    });

    it('fetches latest plan', async () => {
      server.use(
        http.get('/api/v1/pm/plans/latest', () => {
          return HttpResponse.json({ data: { id: 'plan-latest' } });
        }),
      );

      const result = await api.pm.latest();
      expect(result.data?.id).toBe('plan-latest');
    });

    it('fetches single plan', async () => {
      server.use(
        http.get('/api/v1/pm/plans/:id', () => {
          return HttpResponse.json({ data: { id: 'plan-42' } });
        }),
      );

      const result = await api.pm.get('plan-42');
      expect(result.data.id).toBe('plan-42');
    });
  });

  describe('codexAuth', () => {
    it('initiates device auth', async () => {
      server.use(
        http.post('/api/v1/settings/codex-auth/initiate', () => {
          return HttpResponse.json({
            data: { user_code: 'CODE', verification_uri: 'https://example.com', expires_in: 600 },
          });
        }),
      );

      const result = await api.codexAuth.initiate();
      expect(result.data.user_code).toBe('CODE');
    });

    it('fetches auth status', async () => {
      server.use(
        http.get('/api/v1/settings/codex-auth/status', () => {
          return HttpResponse.json({ data: { status: 'completed' } });
        }),
      );

      const result = await api.codexAuth.status();
      expect(result.data.status).toBe('completed');
    });

    it('disconnects codex auth', async () => {
      let called = false;

      server.use(
        http.post('/api/v1/settings/codex-auth/disconnect', () => {
          called = true;
          return HttpResponse.json({});
        }),
      );

      await api.codexAuth.disconnectAll();
      expect(called).toBe(true);
    });
  });

  describe('sessions - createPR', () => {
    it('creates PR and returns queued status', async () => {
      let capturedUrl: string | undefined;
      let capturedBody: unknown;

      server.use(
        http.post('/api/v1/sessions/:id/pr', async ({ request }) => {
          capturedUrl = request.url;
          capturedBody = await request.json();
          return HttpResponse.json({ status: 'queued' }, { status: 202 });
        }),
      );

      const result = await api.sessions.createPR('session-abc', { draft: true, authorMode: 'user', resumeToken: 'resume-123' });
      expect(result.status).toBe('queued');
      expect(capturedUrl).toContain('/api/v1/sessions/session-abc/pr');
      expect(capturedBody).toEqual({ draft: true, author_mode: 'user', resume_token: 'resume-123' });
    });

    it('throws on conflict when PR already exists', async () => {
      server.use(
        http.post('/api/v1/sessions/:id/pr', () => {
          return HttpResponse.json(
            { error: { code: 'PR_EXISTS', message: 'a pull request already exists for this session' } },
            { status: 409 },
          );
        }),
      );

      await expect(api.sessions.createPR('session-abc')).rejects.toThrow('a pull request already exists for this session');
    });

    it('surfaces auth intercept details', async () => {
      server.use(
        http.post('/api/v1/sessions/:id/pr', () => {
          return HttpResponse.json(
            {
              error: {
                code: 'GITHUB_PR_AUTHORSHIP_REQUIRED',
                message: 'Authorize GitHub to create this pull request as you.',
                details: {
                  connect_url: '/api/v1/users/me/github/connect?flow=pr_authorship',
                  resume_token: 'resume-123',
                  can_fallback_to_app: true,
                },
              },
            },
            { status: 409 },
          );
        }),
      );

      await expect(api.sessions.createPR('session-abc')).rejects.toMatchObject({
        code: 'GITHUB_PR_AUTHORSHIP_REQUIRED',
        details: expect.objectContaining({
          connect_url: '/api/v1/users/me/github/connect?flow=pr_authorship',
          resume_token: 'resume-123',
        }),
      });
    });
  });

  describe('sessions - createBranch', () => {
    it('creates a branch and returns queued status', async () => {
      let capturedUrl: string | undefined;
      let capturedBody: unknown;

      server.use(
        http.post('/api/v1/sessions/:id/branch', async ({ request }) => {
          capturedUrl = request.url;
          capturedBody = await request.json();
          return HttpResponse.json({ status: 'queued' }, { status: 202 });
        }),
      );

      const result = await api.sessions.createBranch('session-abc', { authorMode: 'user', resumeToken: 'resume-123' });
      expect(result.status).toBe('queued');
      expect(capturedUrl).toContain('/api/v1/sessions/session-abc/branch');
      expect(capturedBody).toEqual({ author_mode: 'user', resume_token: 'resume-123' });
    });
  });

  describe('issues - triggerFix', () => {
    it('triggers fix for an issue', async () => {
      let capturedBody: unknown;

      server.use(
        http.post('/api/v1/issues/:id/fix', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { id: 'run-1' } });
        }),
      );

      await api.issues.triggerFix('issue-1', { agent_type: 'codex', autonomy_level: 'full' });
      expect(capturedBody).toEqual({ agent_type: 'codex', autonomy_level: 'full' });
    });
  });

  describe('team - additional methods', () => {
    it('lists team members', async () => {
      server.use(
        http.get('/api/v1/team/members', () => {
          return HttpResponse.json({ data: [{ id: 'u-1', name: 'Alice' }], meta: {} });
        }),
      );

      const result = await api.team.listMembers();
      expect(result.data).toHaveLength(1);
      expect(result.data[0].name).toBe('Alice');
    });

    it('changes member role', async () => {
      let capturedBody: unknown;

      server.use(
        http.patch('/api/v1/team/members/:id/role', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { id: 'u-1', role: 'admin' } });
        }),
      );

      await api.team.changeRole('u-1', 'admin');
      expect(capturedBody).toEqual({ role: 'admin' });
    });

    it('lists invitations', async () => {
      server.use(
        http.get('/api/v1/team/invitations', () => {
          return HttpResponse.json({ data: [{ id: 'inv-1', email: 'bob@test.com' }], meta: {} });
        }),
      );

      const result = await api.team.listInvitations();
      expect(result.data).toHaveLength(1);
    });

    it('creates invitation', async () => {
      let capturedBody: unknown;

      server.use(
        http.post('/api/v1/team/invitations', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { id: 'inv-new', email: 'bob@test.com' } });
        }),
      );

      await api.team.createInvitation({ email: 'bob@test.com', role: 'member' });
      expect(capturedBody).toEqual({ email: 'bob@test.com', role: 'member' });
    });
  });

  describe('memories - updateStatus and updateRule', () => {
    it('updates memory status', async () => {
      let capturedBody: unknown;

      server.use(
        http.patch('/api/v1/memories/:id', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { id: 'rp-1', status: 'dismissed' } });
        }),
      );

      await api.memories.updateStatus('rp-1', 'dismissed');
      expect(capturedBody).toEqual({ status: 'dismissed' });
    });

    it('updates memory rule with PUT', async () => {
      let capturedBody: unknown;
      let capturedMethod: string | undefined;

      server.use(
        http.put('/api/v1/memories/:id', async ({ request }) => {
          capturedBody = await request.json();
          capturedMethod = request.method;
          return HttpResponse.json({ data: { id: 'rp-1', rule: 'new rule' } });
        }),
      );

      await api.memories.updateRule('rp-1', 'new rule');
      expect(capturedBody).toEqual({ rule: 'new rule' });
      expect(capturedMethod).toBe('PUT');
    });
  });

  describe('reviewComments', () => {
    it('lists review comments', async () => {
      server.use(
        http.get('/api/v1/review-comments', () => {
          return HttpResponse.json({ data: [{ id: 'rc-1' }], meta: {} });
        }),
      );

      const result = await api.reviewComments.list();
      expect(result.data).toHaveLength(1);
    });

    it('lists review comments with params', async () => {
      let capturedUrl: string | undefined;

      server.use(
        http.get('/api/v1/review-comments', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );

      await api.reviewComments.list({ pull_request_id: 'pr-1', filter_status: 'pending', cursor: 'c1' });

      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('pull_request_id')).toBe('pr-1');
      expect(url.searchParams.get('filter_status')).toBe('pending');
      expect(url.searchParams.get('cursor')).toBe('c1');
    });
  });

  describe('priority - additional methods', () => {
    it('fetches complexity estimate for an issue', async () => {
      server.use(
        http.get('/api/v1/issues/:issueId/complexity', () => {
          return HttpResponse.json({ data: { issue_id: 'issue-1', estimate: 'medium' } });
        }),
      );

      const result = await api.priority.getComplexity('issue-1');
      expect(result.data.issue_id).toBe('issue-1');
    });

    it('reprioritizes an issue', async () => {
      let called = false;

      server.use(
        http.post('/api/v1/issues/:issueId/reprioritize', () => {
          called = true;
          return HttpResponse.json({});
        }),
      );

      await api.priority.reprioritize('issue-1');
      expect(called).toBe(true);
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
        const error = err as { name: string; code: string; message: string; status: number };
        expect(error.name).toBe('ApiError');
        expect(error.code).toBe('BAD_REQUEST');
        expect(error.message).toBe('bad request');
        expect(error.status).toBe(400);
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

  describe('userCredentials', () => {
    it('lists personal credentials', async () => {
      server.use(
        http.get('/api/v1/settings/credentials/personal', () => {
          return HttpResponse.json({
            data: [{ provider: 'anthropic', configured: true, masked_key: 'sk-ant-...abc' }],
            meta: {},
          });
        }),
      );

      const result = await api.userCredentials.listPersonal();
      expect(result.data).toHaveLength(1);
      expect(result.data[0].provider).toBe('anthropic');
      expect(result.data[0].configured).toBe(true);
    });

    it('upserts personal credential', async () => {
      let capturedBody: unknown;
      let capturedUrl: string | undefined;

      server.use(
        http.put('/api/v1/settings/credentials/personal/:provider', async ({ request, params }) => {
          capturedBody = await request.json();
          capturedUrl = params.provider as string;
          return HttpResponse.json({ data: { provider: 'anthropic', configured: true } });
        }),
      );

      await api.userCredentials.upsertPersonal('anthropic', { api_key: 'sk-ant-test' });
      expect(capturedUrl).toBe('anthropic');
      expect(capturedBody).toEqual({ config: { api_key: 'sk-ant-test' }, is_team_default: false });
    });

    it('upserts personal credential with team default flag', async () => {
      let capturedBody: unknown;

      server.use(
        http.put('/api/v1/settings/credentials/personal/:provider', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { provider: 'openai', configured: true } });
        }),
      );

      await api.userCredentials.upsertPersonal('openai', { api_key: 'sk-test' }, true);
      expect(capturedBody).toEqual({ config: { api_key: 'sk-test' }, is_team_default: true });
    });

    it('deletes personal credential', async () => {
      let deleteCalled = false;

      server.use(
        http.delete('/api/v1/settings/credentials/personal/:provider', ({ params }) => {
          deleteCalled = true;
          expect(params.provider).toBe('anthropic');
          return new HttpResponse(null, { status: 204 });
        }),
      );

      await api.userCredentials.deletePersonal('anthropic');
      expect(deleteCalled).toBe(true);
    });

    it('lists team defaults', async () => {
      server.use(
        http.get('/api/v1/settings/credentials/team', () => {
          return HttpResponse.json({
            data: [{ provider: 'anthropic', configured: true, is_team_default: true, set_by_user_name: 'Alice' }],
            meta: {},
          });
        }),
      );

      const result = await api.userCredentials.listTeamDefaults();
      expect(result.data).toHaveLength(1);
      expect(result.data[0].is_team_default).toBe(true);
    });

    it('sets team default', async () => {
      let capturedBody: unknown;

      server.use(
        http.put('/api/v1/settings/credentials/team/:provider', async ({ request, params }) => {
          capturedBody = await request.json();
          expect(params.provider).toBe('anthropic');
          return HttpResponse.json({});
        }),
      );

      await api.userCredentials.setTeamDefault('anthropic', 'user-1');
      expect(capturedBody).toEqual({ user_id: 'user-1' });
    });

    it('removes team default', async () => {
      let deleteCalled = false;

      server.use(
        http.delete('/api/v1/settings/credentials/team/:provider', ({ params }) => {
          deleteCalled = true;
          expect(params.provider).toBe('openai');
          return new HttpResponse(null, { status: 204 });
        }),
      );

      await api.userCredentials.removeTeamDefault('openai');
      expect(deleteCalled).toBe(true);
    });

    it('lists resolved credentials', async () => {
      server.use(
        http.get('/api/v1/settings/credentials/resolved', () => {
          return HttpResponse.json({
            data: [
              { provider: 'anthropic', source: 'personal', masked_key: 'sk-ant-...abc' },
              { provider: 'openai', source: 'team_default', masked_key: 'sk-...def' },
              { provider: 'gemini', source: 'none' },
            ],
            meta: {},
          });
        }),
      );

      const result = await api.userCredentials.listResolved();
      expect(result.data).toHaveLength(3);
      expect(result.data[0].source).toBe('personal');
      expect(result.data[1].source).toBe('team_default');
      expect(result.data[2].source).toBe('none');
    });
  });

  describe('uploads', () => {
    it('sends the active org header on uploads', async () => {
      setActiveOrgId('org-switched');

      let capturedHeader: string | null = null;

      server.use(
        http.post('/api/v1/uploads', ({ request }) => {
          capturedHeader = request.headers.get('X-Active-Org-ID');
          return HttpResponse.json({
            url: '/api/v1/uploads/files/org-switched/2026-04/test.txt',
            file_name: 'test.txt',
            content_type: 'text/plain',
          });
        }),
      );

      const file = new File(['hello'], 'test.txt', { type: 'text/plain' });
      await api.uploads.upload(file);

      expect(capturedHeader).toBe('org-switched');
    });
  });

  // These tests must be last because they modify window.location
  describe('auth - browser redirects', () => {
    const originalLocation = window.location;

    afterEach(() => {
      Object.defineProperty(window, 'location', {
        value: originalLocation,
        writable: true,
        configurable: true,
      });
    });

    it('login redirects to GitHub OAuth', () => {
      const loc = { href: '', pathname: '/overview' };
      Object.defineProperty(window, 'location', {
        value: loc,
        writable: true,
        configurable: true,
      });

      api.auth.login();
      expect(loc.href).toBe('/api/v1/auth/github/login?return_to=%2Foverview');
    });

    it('login passes invitation param', () => {
      const loc = { href: '', pathname: '/integrations' };
      Object.defineProperty(window, 'location', {
        value: loc,
        writable: true,
        configurable: true,
      });

      api.auth.login('invite-123');
      expect(loc.href).toBe('/api/v1/auth/github/login?invitation=invite-123&return_to=%2Fintegrations');
    });

    it('loginGoogle redirects to Google OAuth', () => {
      const loc = { href: '', pathname: '/overview' };
      Object.defineProperty(window, 'location', {
        value: loc,
        writable: true,
        configurable: true,
      });

      api.auth.loginGoogle();
      expect(loc.href).toBe('/api/v1/auth/google/login?return_to=%2Foverview');
    });

    it('loginGoogle passes invitation param', () => {
      const loc = { href: '', pathname: '/integrations' };
      Object.defineProperty(window, 'location', {
        value: loc,
        writable: true,
        configurable: true,
      });

      api.auth.loginGoogle('inv-456');
      expect(loc.href).toBe('/api/v1/auth/google/login?invitation=inv-456&return_to=%2Fintegrations');
    });

    it('loginSentry redirects to backend Sentry OAuth start', () => {
      const loc = { href: '' };
      Object.defineProperty(window, 'location', {
        value: loc,
        writable: true,
        configurable: true,
      });

      api.auth.loginSentry();
      expect(loc.href).toBe('/api/v1/integrations/sentry/login');
    });

    it('integration loginSentry redirects to backend Sentry OAuth start', () => {
      const loc = { href: '' };
      Object.defineProperty(window, 'location', {
        value: loc,
        writable: true,
        configurable: true,
      });

      api.integrations.loginSentry();
      expect(loc.href).toBe('/api/v1/integrations/sentry/login');
    });

    it('loginLinear redirects to backend Linear OAuth start', () => {
      const loc = { href: '' };
      Object.defineProperty(window, 'location', {
        value: loc,
        writable: true,
        configurable: true,
      });

      api.integrations.loginLinear();
      expect(loc.href).toBe('/api/v1/integrations/linear/login');
    });
  });

  describe('evals', () => {
    it('lists tasks without params', async () => {
      server.use(
        http.get('/api/v1/evals/tasks', () => {
          return HttpResponse.json({ data: [{ id: 'task-1', name: 'Test task' }], meta: {} });
        }),
      );
      const result = await api.evals.listTasks();
      expect(result.data).toHaveLength(1);
      expect(result.data[0].id).toBe('task-1');
    });

    it('lists tasks with filter params', async () => {
      let capturedUrl: string | undefined;
      server.use(
        http.get('/api/v1/evals/tasks', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );
      await api.evals.listTasks({ source: 'manual', complexity: 'simple', tags: 'unit,e2e' });
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('source')).toBe('manual');
      expect(url.searchParams.get('complexity')).toBe('simple');
      expect(url.searchParams.get('tags')).toBe('unit,e2e');
    });

    it('gets a single task', async () => {
      server.use(
        http.get('/api/v1/evals/tasks/:id', ({ params }) => {
          return HttpResponse.json({ data: { id: params.id, name: 'Task A' } });
        }),
      );
      const result = await api.evals.getTask('task-42');
      expect(result.data.id).toBe('task-42');
    });

    it('creates a task', async () => {
      let capturedBody: unknown;
      server.use(
        http.post('/api/v1/evals/tasks', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { id: 'new-task', name: 'Created' } });
        }),
      );
      const body = {
        repo_id: 'r-1',
        name: 'Created',
        description: 'desc',
        base_commit_sha: 'abc123',
        issue_description: 'Fix bug',
        scoring_criteria: [],
        pass_threshold: 0.7,
        complexity: 'moderate',
      };
      const result = await api.evals.createTask(body);
      expect(result.data.id).toBe('new-task');
      expect(capturedBody).toMatchObject({ name: 'Created', repo_id: 'r-1' });
    });

    it('updates a task', async () => {
      server.use(
        http.patch('/api/v1/evals/tasks/:id', () => {
          return HttpResponse.json({ data: { id: 'task-1', name: 'Updated' } });
        }),
      );
      const result = await api.evals.updateTask('task-1', { name: 'Updated' });
      expect(result.data.name).toBe('Updated');
    });

    it('archives a task', async () => {
      let deleteCalled = false;
      server.use(
        http.delete('/api/v1/evals/tasks/:id', () => {
          deleteCalled = true;
          return new HttpResponse(null, { status: 204 });
        }),
      );
      await api.evals.archiveTask('task-1');
      expect(deleteCalled).toBe(true);
    });

    it('starts a run', async () => {
      server.use(
        http.post('/api/v1/evals/tasks/:taskId/runs', () => {
          return HttpResponse.json({ data: { id: 'run-1', status: 'pending' } });
        }),
      );
      const result = await api.evals.startRun('task-1', { model: 'claude-sonnet-4-6' });
      expect(result.data.id).toBe('run-1');
    });

    it('lists runs without params', async () => {
      server.use(
        http.get('/api/v1/evals/tasks/:taskId/runs', () => {
          return HttpResponse.json({ data: [{ id: 'run-1' }], meta: {} });
        }),
      );
      const result = await api.evals.listRuns('task-1');
      expect(result.data).toHaveLength(1);
    });

    it('lists runs with params', async () => {
      let capturedUrl: string | undefined;
      server.use(
        http.get('/api/v1/evals/tasks/:taskId/runs', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );
      await api.evals.listRuns('task-1', { limit: 10, cursor: 'abc' });
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('limit')).toBe('10');
      expect(url.searchParams.get('cursor')).toBe('abc');
    });

    it('gets a single run', async () => {
      server.use(
        http.get('/api/v1/evals/runs/:id', () => {
          return HttpResponse.json({ data: { id: 'run-1', status: 'completed' } });
        }),
      );
      const result = await api.evals.getRun('run-1');
      expect(result.data.id).toBe('run-1');
    });

    it('lists batches without params', async () => {
      server.use(
        http.get('/api/v1/evals/batch', () => {
          return HttpResponse.json({ data: [{ id: 'b-1' }], meta: {} });
        }),
      );
      const result = await api.evals.listBatches();
      expect(result.data).toHaveLength(1);
    });

    it('lists batches with params', async () => {
      let capturedUrl: string | undefined;
      server.use(
        http.get('/api/v1/evals/batch', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: [], meta: {} });
        }),
      );
      await api.evals.listBatches({ limit: 5 });
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('limit')).toBe('5');
    });

    it('starts a batch', async () => {
      server.use(
        http.post('/api/v1/evals/batch', () => {
          return HttpResponse.json({ data: { id: 'b-1', status: 'pending' } });
        }),
      );
      const result = await api.evals.startBatch({
        name: 'Batch 1',
        task_ids: ['t-1'],
        configs: [{ model: 'claude-sonnet-4-6' }],
      });
      expect(result.data.id).toBe('b-1');
    });

    it('gets a batch', async () => {
      server.use(
        http.get('/api/v1/evals/batch/:id', () => {
          return HttpResponse.json({ data: { id: 'b-1', runs: [] } });
        }),
      );
      const result = await api.evals.getBatch('b-1');
      expect(result.data.id).toBe('b-1');
    });

    it('triggers bootstrap', async () => {
      server.use(
        http.post('/api/v1/evals/bootstrap', () => {
          return HttpResponse.json({ data: { id: 'bs-1', status: 'pending' } });
        }),
      );
      const result = await api.evals.bootstrap({ repo_id: 'r-1' });
      expect(result.data.id).toBe('bs-1');
    });

    it('gets bootstrap candidates without params', async () => {
      server.use(
        http.get('/api/v1/evals/bootstrap/candidates', () => {
          return HttpResponse.json({ data: { id: 'bs-1', candidates: [] } });
        }),
      );
      const result = await api.evals.getBootstrapCandidates();
      expect(result.data.id).toBe('bs-1');
    });

    it('gets bootstrap candidates with params', async () => {
      let capturedUrl: string | undefined;
      server.use(
        http.get('/api/v1/evals/bootstrap/candidates', ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ data: { id: 'bs-1', candidates: [] } });
        }),
      );
      await api.evals.getBootstrapCandidates({ repo_id: 'r-1', bootstrap_run_id: 'bs-1' });
      const url = new URL(capturedUrl!);
      expect(url.searchParams.get('repo_id')).toBe('r-1');
      expect(url.searchParams.get('bootstrap_run_id')).toBe('bs-1');
    });

    it('accepts bootstrap candidates', async () => {
      server.use(
        http.post('/api/v1/evals/bootstrap/accept', () => {
          return HttpResponse.json({ data: [{ id: 'task-1' }] });
        }),
      );
      const result = await api.evals.acceptBootstrapCandidates({
        bootstrap_run_id: 'bs-1',
        candidate_indices: [0, 2],
      });
      expect(result.data).toHaveLength(1);
    });

    it('reviews a bootstrap candidate', async () => {
      let capturedBody: unknown;
      server.use(
        http.patch('/api/v1/evals/bootstrap/candidates/cand-1', async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ data: { candidate_id: 'cand-1', status: 'needs_revision' } });
        }),
      );
      const result = await api.evals.reviewBootstrapCandidate('cand-1', {
        status: 'needs_revision',
        rejection_reason: 'Needs deterministic scoring.',
      });
      expect(capturedBody).toEqual({
        status: 'needs_revision',
        rejection_reason: 'Needs deterministic scoring.',
      });
      expect(result.data.status).toBe('needs_revision');
    });
  });

  describe('repository preview secret bundles', () => {
    it('lists preview secret bundles for a repository', async () => {
      server.use(
        http.get('/api/v1/repositories/repo-1/preview-secret-bundles', () => {
          return HttpResponse.json({
            data: [{
              id: 'bundle-1',
              repository_id: 'repo-1',
              name: 'staging',
              source_type: 'managed',
              exposure_policy: 'preview_runtime',
              outputs: [{ type: 'env', env: ['API_TOKEN'] }],
              created_by_user_id: 'user-1',
              created_at: '2026-01-01T00:00:00Z',
            }],
          });
        }),
      );

      const result = await api.repositories.previewSecretBundles.list('repo-1');

      expect(result.data[0].name).toBe('staging');
      expect(result.data[0].outputs[0].env).toEqual(['API_TOKEN']);
    });

    it('upserts preview secret bundles for a repository', async () => {
      let capturedBody: unknown;
      let capturedUrl: string | undefined;
      server.use(
        http.post('/api/v1/repositories/repo-1/preview-secret-bundles', async ({ request }) => {
          capturedUrl = request.url;
          capturedBody = await request.json();
          return HttpResponse.json({
            data: {
              id: 'bundle-1',
              repository_id: 'repo-1',
              name: 'staging-api',
              source_type: 'managed',
              exposure_policy: 'preview_runtime',
              outputs: [{ type: 'env', env: ['API_TOKEN'] }],
              created_by_user_id: 'user-1',
              created_at: '2026-01-01T00:00:00Z',
            },
          });
        }),
      );

      await api.repositories.previewSecretBundles.upsert('repo-1', {
        name: 'staging-api',
        source: { type: 'managed', values: { API_TOKEN: 'secret' } },
        outputs: [{ type: 'env', values: { API_TOKEN: 'secret:API_TOKEN' } }],
        exposure_policy: 'preview_runtime',
      });

      expect(capturedUrl).toContain('/api/v1/repositories/repo-1/preview-secret-bundles');
      expect(capturedBody).toEqual({
        name: 'staging-api',
        source: { type: 'managed', values: { API_TOKEN: 'secret' } },
        outputs: [{ type: 'env', values: { API_TOKEN: 'secret:API_TOKEN' } }],
        exposure_policy: 'preview_runtime',
      });
    });

    it('tests a preview secret bundle by ID', async () => {
      let capturedUrl: string | undefined;
      server.use(
        http.post('/api/v1/preview-secret-bundles/:bundleId/test', ({ params }) => {
          capturedUrl = `/api/v1/preview-secret-bundles/${params.bundleId as string}/test`;
          return HttpResponse.json({
            data: {
              status: 'ready',
              bundle: {
                id: 'bundle-1',
                repository_id: 'repo-1',
                name: 'staging',
                source_type: 'managed',
                exposure_policy: 'preview_runtime',
                outputs: [{ type: 'env', env: ['API_TOKEN'] }],
                created_by_user_id: 'user-1',
                created_at: '2026-01-01T00:00:00Z',
              },
            },
          });
        }),
      );

      const result = await api.repositories.previewSecretBundles.test('bundle-1');

      expect(capturedUrl).toBe('/api/v1/preview-secret-bundles/bundle-1/test');
      expect(result.data.status).toBe('ready');
      expect(result.data.bundle.name).toBe('staging');
    });

    it('reveals preview secret bundle source by ID', async () => {
      let capturedUrl: string | undefined;
      server.use(
        http.post('/api/v1/preview-secret-bundles/:bundleId/reveal', ({ params }) => {
          capturedUrl = `/api/v1/preview-secret-bundles/${params.bundleId as string}/reveal`;
          return HttpResponse.json({
            data: {
              bundle: {
                id: 'bundle-1',
                repository_id: 'repo-1',
                name: 'staging',
                source_type: 'managed',
                exposure_policy: 'preview_runtime',
                outputs: [{ type: 'file', path: 'development.conf.json', format: 'json' }],
                created_by_user_id: 'user-1',
                created_at: '2026-01-01T00:00:00Z',
              },
              source: { type: 'managed', values: { SECRET_FILE_CONTENT: '{"token":"super-secret"}' } },
              outputs: [{ type: 'file', path: 'development.conf.json', format: 'json', value: 'secret:SECRET_FILE_CONTENT' }],
            },
          });
        }),
      );

      const result = await api.repositories.previewSecretBundles.reveal('bundle-1');

      expect(capturedUrl).toBe('/api/v1/preview-secret-bundles/bundle-1/reveal');
      expect(result.data.source.values.SECRET_FILE_CONTENT).toBe('{"token":"super-secret"}');
    });

    it('patches a preview secret bundle by ID', async () => {
      let capturedBody: unknown;
      let capturedUrl: string | undefined;
      server.use(
        http.patch('/api/v1/preview-secret-bundles/:bundleId', async ({ request, params }) => {
          capturedUrl = `/api/v1/preview-secret-bundles/${params.bundleId as string}`;
          capturedBody = await request.json();
          return HttpResponse.json({
            data: {
              id: 'bundle-1',
              repository_id: 'repo-1',
              name: 'staging-renamed',
              source_type: 'managed',
              exposure_policy: 'preview_runtime',
              outputs: [{ type: 'env', env: ['NEW_TOKEN'] }],
              created_by_user_id: 'user-1',
              created_at: '2026-01-01T00:00:00Z',
            },
          });
        }),
      );

      await api.repositories.previewSecretBundles.patch('bundle-1', {
        name: 'staging-renamed',
        source: { type: 'managed', values: { NEW_TOKEN: 'secret' } },
        outputs: [{ type: 'env', values: { NEW_TOKEN: 'secret:NEW_TOKEN' } }],
      });

      expect(capturedUrl).toBe('/api/v1/preview-secret-bundles/bundle-1');
      expect(capturedBody).toEqual({
        name: 'staging-renamed',
        source: { type: 'managed', values: { NEW_TOKEN: 'secret' } },
        outputs: [{ type: 'env', values: { NEW_TOKEN: 'secret:NEW_TOKEN' } }],
      });
    });

    it('deletes preview secret bundles for a repository', async () => {
      let capturedUrl: string | undefined;
      server.use(
        http.delete('/api/v1/repositories/repo-1/preview-secret-bundles/:name', ({ request }) => {
          capturedUrl = request.url;
          return new HttpResponse(null, { status: 204 });
        }),
      );

      await expect(api.repositories.previewSecretBundles.delete('repo-1', 'staging')).resolves.toBeUndefined();
      expect(capturedUrl).toContain('/api/v1/repositories/repo-1/preview-secret-bundles/staging');
    });
  });
});
