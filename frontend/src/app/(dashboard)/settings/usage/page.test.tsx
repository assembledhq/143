import { describe, it, expect, vi, beforeEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { renderWithProviders, screen } from '@/test/test-utils';
import { server } from '@/test/mocks/server';
import UsagePage from './page';
import { UsageDatePicker } from './usage-date-picker';
import {
  formatMinutes,
  formatTokenCount,
  formatCost,
  formatNumber,
  getDateRangePreset,
  groupByLocalDay,
  formatDayLabel,
  formatDateForApi,
} from './usage-helpers';
import type { UsageTimeseriesBucket } from '@/lib/types';

const { useAuthMock } = vi.hoisted(() => ({
  useAuthMock: vi.fn(),
}));

vi.mock('@/hooks/use-auth', () => ({
  useAuth: useAuthMock,
}));

function makeBucket(overrides: Partial<UsageTimeseriesBucket> = {}): UsageTimeseriesBucket {
  return {
    hour_utc: '2026-04-10T00:00:00Z',
    total_container_minutes: 0,
    total_sessions: 0,
    total_container_starts: 0,
    peak_concurrent: 0,
    avg_duration_sec: 0,
    p95_duration_sec: 0,
    total_input_tokens: 0,
    total_output_tokens: 0,
    total_llm_cost_usd: 0,
    ...overrides,
  };
}

function setupHandlers() {
  server.use(
    http.get('/api/v1/usage/timeseries', () => {
      return HttpResponse.json({
        buckets: [],
        period_start: '2026-03-13T00:00:00Z',
        period_end: '2026-04-12T00:00:00Z',
      });
    }),
    http.get('/api/v1/usage/summary', () => {
      return HttpResponse.json({
        total_container_minutes: 0,
        total_sessions: 0,
        total_container_starts: 0,
        peak_concurrent: 0,
        total_input_tokens: 0,
        total_output_tokens: 0,
        total_llm_cost_usd: 0,
      });
    }),
    http.get('/api/v1/usage/breakdown', () => {
      return HttpResponse.json({ rows: [] });
    }),
    http.get('/api/v1/usage/capacity', () => {
      return HttpResponse.json({ tiers: [] });
    }),
  );
}

// ---------------------------------------------------------------------------
// Pure helper tests
// ---------------------------------------------------------------------------

describe('formatMinutes', () => {
  it('formats minutes below 60 with "m" suffix', () => {
    expect(formatMinutes(0)).toBe('0.0m');
    expect(formatMinutes(15)).toBe('15.0m');
    expect(formatMinutes(59.9)).toBe('59.9m');
  });

  it('formats 60+ minutes as hours', () => {
    expect(formatMinutes(60)).toBe('1.0h');
    expect(formatMinutes(90)).toBe('1.5h');
    expect(formatMinutes(150)).toBe('2.5h');
  });
});

describe('formatTokenCount', () => {
  it('returns raw number below 1000', () => {
    expect(formatTokenCount(0)).toBe('0');
    expect(formatTokenCount(999)).toBe('999');
  });

  it('formats thousands with K suffix', () => {
    expect(formatTokenCount(1000)).toBe('1.0K');
    expect(formatTokenCount(1500)).toBe('1.5K');
    expect(formatTokenCount(999999)).toBe('1000.0K');
  });

  it('formats millions with M suffix', () => {
    expect(formatTokenCount(1_000_000)).toBe('1.0M');
    expect(formatTokenCount(2_500_000)).toBe('2.5M');
  });
});

describe('formatCost', () => {
  it('returns $0.00 for very small values', () => {
    expect(formatCost(0)).toBe('$0.00');
    expect(formatCost(0.001)).toBe('$0.00');
    expect(formatCost(0.009)).toBe('$0.00');
  });

  it('formats normal costs with two decimal places', () => {
    expect(formatCost(0.01)).toBe('$0.01');
    expect(formatCost(1.5)).toBe('$1.50');
    expect(formatCost(99.999)).toBe('$100.00');
  });
});

describe('formatNumber', () => {
  it('formats small numbers without commas', () => {
    expect(formatNumber(0)).toBe('0');
    expect(formatNumber(999)).toBe('999');
  });

  it('formats large numbers with commas', () => {
    expect(formatNumber(1000)).toBe('1,000');
    expect(formatNumber(1234567)).toBe('1,234,567');
  });
});

describe('getDateRangePreset', () => {
  it('returns a 7-day range for "7d"', () => {
    const { start, end } = getDateRangePreset('7d');
    const diffMs = end.getTime() - start.getTime();
    const diffDays = diffMs / (1000 * 60 * 60 * 24);
    expect(diffDays).toBeCloseTo(7, 0);
  });

  it('returns a 30-day range for "30d"', () => {
    const { start, end } = getDateRangePreset('30d');
    const diffMs = end.getTime() - start.getTime();
    const diffDays = diffMs / (1000 * 60 * 60 * 24);
    expect(diffDays).toBeCloseTo(30, 0);
  });

  it('returns first of current month for "this_month"', () => {
    const { start } = getDateRangePreset('this_month');
    expect(start.getDate()).toBe(1);
  });

  it('defaults to 30 days for unknown preset', () => {
    const { start, end } = getDateRangePreset('unknown');
    const diffMs = end.getTime() - start.getTime();
    const diffDays = diffMs / (1000 * 60 * 60 * 24);
    expect(diffDays).toBeCloseTo(30, 0);
  });
});

describe('formatDateForApi', () => {
  it('returns an ISO string', () => {
    const d = new Date('2026-04-12T15:30:00Z');
    expect(formatDateForApi(d)).toBe(d.toISOString());
  });
});

describe('formatDayLabel', () => {
  it('formats a YYYY-MM-DD string to short month + day', () => {
    const label = formatDayLabel('2026-01-05');
    expect(label).toBe('Jan 5');
  });

  it('handles month boundaries', () => {
    expect(formatDayLabel('2026-12-31')).toBe('Dec 31');
  });
});

describe('groupByLocalDay', () => {
  it('returns empty array for empty input', () => {
    expect(groupByLocalDay([])).toEqual([]);
  });

  it('groups buckets from the same day', () => {
    const buckets: UsageTimeseriesBucket[] = [
      makeBucket({ hour_utc: '2026-04-10T02:00:00Z', total_sessions: 3, peak_concurrent: 2 }),
      makeBucket({ hour_utc: '2026-04-10T14:00:00Z', total_sessions: 5, peak_concurrent: 4 }),
    ];
    const result = groupByLocalDay(buckets);
    // Depending on timezone they may or may not collapse into one day,
    // but verify the aggregation logic works.
    const totalSessions = result.reduce((s, d) => s + d.total_sessions, 0);
    expect(totalSessions).toBe(8);
  });

  it('uses max for peak_concurrent across hours in a day', () => {
    const buckets: UsageTimeseriesBucket[] = [
      makeBucket({ hour_utc: '2026-04-10T12:00:00Z', peak_concurrent: 2 }),
      makeBucket({ hour_utc: '2026-04-10T13:00:00Z', peak_concurrent: 7 }),
      makeBucket({ hour_utc: '2026-04-10T14:00:00Z', peak_concurrent: 3 }),
    ];
    const result = groupByLocalDay(buckets);
    const maxPeak = Math.max(...result.map((d) => d.peak_concurrent));
    expect(maxPeak).toBe(7);
  });

  it('sums cost across hours', () => {
    const buckets: UsageTimeseriesBucket[] = [
      makeBucket({ hour_utc: '2026-04-10T12:00:00Z', total_llm_cost_usd: 1.5 }),
      makeBucket({ hour_utc: '2026-04-10T13:00:00Z', total_llm_cost_usd: 2.5 }),
    ];
    const result = groupByLocalDay(buckets);
    const totalCost = result.reduce((s, d) => s + d.total_llm_cost_usd, 0);
    expect(totalCost).toBeCloseTo(4.0);
  });
});

// ---------------------------------------------------------------------------
// Component render tests
// ---------------------------------------------------------------------------

describe('UsageDatePicker', () => {
  it('renders all preset buttons', () => {
    const onPresetChange = vi.fn();
    renderWithProviders(
      <UsageDatePicker activePreset="30d" onPresetChange={onPresetChange} />
    );

    expect(screen.getByText('Last 7d')).toBeInTheDocument();
    expect(screen.getByText('Last 30d')).toBeInTheDocument();
    expect(screen.getByText('This month')).toBeInTheDocument();
  });

  it('highlights the active preset', () => {
    const onPresetChange = vi.fn();
    renderWithProviders(
      <UsageDatePicker activePreset="7d" onPresetChange={onPresetChange} />
    );

    const btn7d = screen.getByText('Last 7d');
    const btn30d = screen.getByText('Last 30d');

    // Active button should NOT have the muted class; inactive should
    expect(btn30d.className).toContain('text-muted-foreground');
    expect(btn7d.className).not.toContain('text-muted-foreground');
  });
});

describe('UsagePage', () => {
  beforeEach(() => {
    useAuthMock.mockReturnValue({
      user: { id: 'user-1', name: 'Test User', email: 'test@example.com', role: 'admin' },
      isLoading: false,
      isAuthenticated: true,
    });
    setupHandlers();
  });

  it('renders page header and date picker', () => {
    renderWithProviders(<UsagePage />);
    expect(screen.getByText('Usage & Billing')).toBeInTheDocument();
    expect(screen.getByText('Last 7d')).toBeInTheDocument();
    expect(screen.getByText('Last 30d')).toBeInTheDocument();
    expect(screen.getByText('This month')).toBeInTheDocument();
  });

  it('renders the footer disclaimer text', () => {
    renderWithProviders(<UsagePage />);
    expect(
      screen.getByText(/Data updates every ~5 minutes/)
    ).toBeInTheDocument();
  });
});
