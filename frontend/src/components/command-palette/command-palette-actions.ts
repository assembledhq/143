import {
  Zap,
  Play,
  FolderKanban,
  RefreshCw,
  Settings,
  CircleUser,
  Users,
  Plug,
  Bot,
  Sparkles,
  Target,
  FlaskConical,
  ScrollText,
  MonitorPlay,
  Plus,
  LogOut,
  type LucideIcon,
} from "lucide-react";

export interface PaletteAction {
  id: string;
  label: string;
  icon: LucideIcon;
  href?: string;
  /** If true, preserve the current ?repo= param when navigating. */
  preserveRepo?: boolean;
  /** If set, only users with this role see the action. */
  requiredRole?: string;
  /** If set, users with any of these roles do not see the action. */
  hiddenRoles?: string[];
  group: "navigation" | "settings" | "quick-actions";
  /** If set, show this keyboard shortcut hint on the right. */
  shortcut?: string;
}

export const staticActions: PaletteAction[] = [
  // Navigation
  { id: "nav-sessions", label: "Sessions", icon: Play, href: "/sessions", preserveRepo: true, group: "navigation" },
  { id: "nav-automations", label: "Automations", icon: RefreshCw, href: "/automations", group: "navigation" },
  { id: "nav-projects", label: "Projects", icon: FolderKanban, href: "/projects", preserveRepo: true, group: "navigation" },
  { id: "nav-autopilot", label: "Autopilot", icon: Zap, href: "/autopilot", group: "navigation" },
  { id: "nav-previews", label: "Previews", icon: MonitorPlay, href: "/previews", preserveRepo: true, group: "navigation" },

  // Settings & admin
  { id: "settings-account", label: "Account", icon: CircleUser, href: "/settings/account", group: "settings" },
  { id: "settings-general", label: "General", icon: Settings, href: "/settings", requiredRole: "admin", group: "settings" },
  { id: "settings-integrations", label: "Integrations", icon: Plug, href: "/settings/integrations", hiddenRoles: ["viewer", "builder"], group: "settings" },
  { id: "settings-agents", label: "Coding agents", icon: Bot, href: "/settings/agent", hiddenRoles: ["viewer"], group: "settings" },
  { id: "settings-llm", label: "LLM", icon: Sparkles, href: "/settings/llm", requiredRole: "admin", group: "settings" },
  { id: "settings-autopilot", label: "Autopilot", icon: Target, href: "/settings/autopilot", requiredRole: "admin", group: "settings" },
  { id: "settings-evals", label: "Evals", icon: FlaskConical, href: "/settings/evals", hiddenRoles: ["viewer", "builder"], group: "settings" },
  { id: "settings-team", label: "Team", icon: Users, href: "/settings/team", hiddenRoles: ["viewer", "builder"], group: "settings" },
  { id: "settings-audit-log", label: "Audit log", icon: ScrollText, href: "/settings/audit-log", requiredRole: "admin", group: "settings" },

  // Quick actions
  { id: "action-new-session", label: "New session", icon: Plus, href: "/sessions/new", preserveRepo: true, group: "quick-actions" },
  { id: "action-create-preview", label: "Create preview", icon: MonitorPlay, href: "/previews/new", preserveRepo: true, hiddenRoles: ["viewer"], group: "quick-actions" },
  { id: "action-new-project", label: "New project", icon: Plus, href: "/projects/new", preserveRepo: true, hiddenRoles: ["builder"], group: "quick-actions" },
  { id: "action-new-eval", label: "Create eval task", icon: Plus, href: "/settings/evals/new", hiddenRoles: ["viewer", "builder"], group: "quick-actions" },
  { id: "action-logout", label: "Log out", icon: LogOut, group: "quick-actions" },
];

export function getFilteredActions(userRole: string): PaletteAction[] {
  return staticActions.filter(
    (action) =>
      (!action.requiredRole || action.requiredRole === userRole) &&
      !action.hiddenRoles?.includes(userRole)
  );
}
