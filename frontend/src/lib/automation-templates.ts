import type React from "react";
import { FlaskConical, Shield, Wrench, ClipboardList, FileText } from "lucide-react";

export interface AutomationTemplate {
  id: string;
  name: string;
  icon: React.ComponentType<{ className?: string }>;
  goal: string;
  defaultInterval: number;
  defaultUnit: 'hours' | 'days' | 'weeks';
}

export const automationTemplates: AutomationTemplate[] = [
  {
    id: 'flaky-tests',
    name: 'Find flaky tests',
    icon: FlaskConical,
    goal: 'Identify flaky tests from recent failures, reproduce nondeterminism, propose deterministic fixes.',
    defaultInterval: 1,
    defaultUnit: 'days',
  },
  {
    id: 'security-sweep',
    name: 'Security sweep',
    icon: Shield,
    goal: 'Review recent changes for concrete security vulnerabilities, propose remediations.',
    defaultInterval: 7,
    defaultUnit: 'days',
  },
  {
    id: 'codebase-maintenance',
    name: 'Codebase maintenance',
    icon: Wrench,
    goal: 'Identify high-leverage maintenance opportunities that reduce operational risk.',
    defaultInterval: 3,
    defaultUnit: 'days',
  },
  {
    id: 'backlog-triage',
    name: 'Backlog triage',
    icon: ClipboardList,
    goal: 'Analyze current issues, prioritize by impact/urgency, cluster related items.',
    defaultInterval: 1,
    defaultUnit: 'days',
  },
  {
    id: 'doc-freshness',
    name: 'Documentation freshness',
    icon: FileText,
    goal: 'Find stale or missing docs for recently changed code, update or flag them.',
    defaultInterval: 7,
    defaultUnit: 'days',
  },
];
