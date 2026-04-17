export interface AutomationTemplate {
  id: string;
  name: string;
  icon: string;
  goal: string;
  defaultInterval: number;
  defaultUnit: 'hours' | 'days' | 'weeks';
}

export const automationTemplates: AutomationTemplate[] = [
  {
    id: 'flaky-tests',
    name: 'Find flaky tests',
    icon: '🧪',
    goal: 'Identify flaky tests from recent failures, reproduce nondeterminism, propose deterministic fixes.',
    defaultInterval: 1,
    defaultUnit: 'days',
  },
  {
    id: 'security-sweep',
    name: 'Security sweep',
    icon: '🛡',
    goal: 'Review recent changes for concrete security vulnerabilities, propose remediations.',
    defaultInterval: 7,
    defaultUnit: 'days',
  },
  {
    id: 'codebase-maintenance',
    name: 'Codebase maintenance',
    icon: '🔧',
    goal: 'Identify high-leverage maintenance opportunities that reduce operational risk.',
    defaultInterval: 3,
    defaultUnit: 'days',
  },
  {
    id: 'backlog-triage',
    name: 'Backlog triage',
    icon: '📋',
    goal: 'Analyze current issues, prioritize by impact/urgency, cluster related items.',
    defaultInterval: 1,
    defaultUnit: 'days',
  },
  {
    id: 'doc-freshness',
    name: 'Documentation freshness',
    icon: '📝',
    goal: 'Find stale or missing docs for recently changed code, update or flag them.',
    defaultInterval: 7,
    defaultUnit: 'days',
  },
];
