import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";

import {
  renderWithProviders,
  screen,
  userEvent,
  waitFor,
} from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import type { AutomationFallbackModels } from "@/lib/types";

import {
  AutomationFallbackModelsEditor,
  AutomationFallbackModelsSummary,
  MAX_AUTOMATION_FALLBACK_MODELS,
  automationFallbackRanks,
  buildAutomationFallbackModels,
  normalizeRankReasoningEffort,
} from "./automation-fallback-models-editor";

// The primary sits on Codex and the chains below deliberately cross agents.
// A heterogeneous chain is the whole reason the three arrays are parallel
// rather than a single list of model names, so it is the case worth testing.
const PRIMARY_MODEL = "gpt-5.4";
// Claude Code accepts "max"; Codex does not. Amp has no reasoning ladder at
// all. Those three shapes cover every branch the rank normalizer can take.
const CLAUDE_MODEL = "claude-sonnet-4-6";
const OTHER_CLAUDE_MODEL = "claude-opus-5";
const AMP_MODE = "smart";

function orgCredential(agent: string, provider: string) {
  return {
    id: `cred-${agent}`,
    org_id: "org-1",
    scope: "org",
    agent,
    auth_type: "api_key",
    provider,
    label: `${agent} key`,
    status: "healthy",
    is_default: false,
    priority: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

// Makes Codex, Claude Code and Amp all selectable in the rank pickers.
// Without this the org has no credentials and the dropdown collapses to the
// default agent, which would hide the cross-agent behavior under test.
function mockSelectableAgents() {
  server.use(
    http.get("*/api/v1/settings", () =>
      HttpResponse.json({
        data: { settings: { default_agent_type: "codex", agent_config: {} } },
      }),
    ),
    http.get("*/api/v1/settings/codex-auth/status", () =>
      HttpResponse.json({ data: { status: "completed" } }),
    ),
    http.get("*/api/v1/coding-credentials*", ({ request }) => {
      const scope = new URL(request.url).searchParams.get("scope");
      if (scope !== "org") {
        return HttpResponse.json({ data: [], meta: { scope } });
      }
      return HttpResponse.json({
        data: [
          orgCredential("claude_code", "anthropic"),
          orgCredential("amp", "amp"),
        ],
        meta: {},
      });
    }),
  );
}

/**
 * The editor is fully controlled, so a bare render would show the same chain
 * after every edit and a multi-step sequence (add, then remove, then reorder)
 * could not be exercised at all. This harness feeds each emitted chain straight
 * back in, which is exactly what both call sites do through their autosave /
 * form state.
 */
function EditorHarness({
  initialValue,
  primaryReasoningEffort = "",
  onChange,
}: {
  initialValue?: AutomationFallbackModels;
  primaryReasoningEffort?: "" | "low" | "medium" | "high" | "xhigh" | "max";
  onChange: (value: AutomationFallbackModels) => void;
}) {
  const [value, setValue] = useState<AutomationFallbackModels>(
    initialValue ?? {},
  );
  return (
    <AutomationFallbackModelsEditor
      value={value}
      primaryModel={PRIMARY_MODEL}
      primaryAgentType="codex"
      primaryReasoningEffort={primaryReasoningEffort}
      onChange={(next) => {
        onChange(next);
        setValue(next);
      }}
    />
  );
}

type User = ReturnType<typeof userEvent.setup>;

async function pickModel(user: User, rank: number, optionName: string) {
  await user.click(
    screen.getByRole("combobox", { name: `Rank ${rank} fallback model` }),
  );
  await user.click(await screen.findByRole("option", { name: optionName }));
}

describe("normalizeRankReasoningEffort", () => {
  // Every editor handler runs each row through this before emitting, so a wrong
  // answer here is a guaranteed 400 on the next save rather than a soft
  // fallback — the API validates a rank's effort against that rank's own agent.
  it.each([
    {
      name: "drops anything stored for an agent with no reasoning ladder",
      agentType: "amp",
      effort: "high" as const,
      primary: "high" as const,
      expected: "",
    },
    {
      name: "keeps an explicit level the rank's own agent accepts",
      agentType: "claude_code",
      effort: "max" as const,
      primary: "high" as const,
      expected: "max",
    },
    {
      name: "clears an explicit level the rank's agent cannot run when the primary's fits",
      agentType: "codex",
      effort: "max" as const,
      primary: "high" as const,
      expected: "",
    },
    {
      // The substitution this used to make ("high") was written against older
      // backend behavior. The API now drops an inherited level the rank's agent
      // cannot run and lets the rank use its own agent's default, so pinning a
      // level here would invent a setting the user never chose — and would make
      // the inherit menu item unselectable, because picking it re-emits the
      // stored chain and the autosave's deepEqual guard skips the save.
      name: "leaves a rank inheriting when its agent cannot run the primary's level",
      agentType: "codex",
      effort: "" as const,
      primary: "max" as const,
      expected: "",
    },
    {
      name: "leaves a rank inheriting when there is nothing to inherit",
      agentType: "codex",
      effort: "" as const,
      primary: "" as const,
      expected: "",
    },
    {
      name: "leaves a rank inheriting when its agent shares the primary's level",
      agentType: "claude_code",
      effort: "" as const,
      primary: "high" as const,
      expected: "",
    },
  ])("$name", ({ agentType, effort, expected }) => {
    expect(normalizeRankReasoningEffort(agentType, effort)).toBe(expected);
  });
});

describe("buildAutomationFallbackModels", () => {
  it("returns an empty chain when no rank has a model", () => {
    // `{}` is how the API expresses "no chain"; an object with empty arrays
    // would read as a chain of length zero and churn the audit log.
    expect(buildAutomationFallbackModels([])).toEqual({});
    expect(
      buildAutomationFallbackModels([
        { model: "  ", agentType: "codex", reasoningEffort: "" },
      ]),
    ).toEqual({});
  });

  it("writes models and agent types in lockstep and omits reasoning when every rank inherits", () => {
    // `agent_types` is always explicit: the backend would otherwise re-derive
    // it from the model name and could disagree with what the row displayed.
    // `reasoning_efforts` is omitted entirely rather than sent as all-empty,
    // because that is the canonical encoding the Go side normalizes to — an
    // all-empty array would make a no-op edit look like a change.
    expect(
      buildAutomationFallbackModels([
        { model: CLAUDE_MODEL, agentType: "claude_code", reasoningEffort: "" },
        { model: AMP_MODE, agentType: "amp", reasoningEffort: "" },
      ]),
    ).toEqual({
      models: [CLAUDE_MODEL, AMP_MODE],
      agent_types: ["claude_code", "amp"],
    });
  });

  it("emits a reasoning entry for every rank as soon as one rank needs it", () => {
    // The arrays must stay the same length: a shorter `reasoning_efforts` is
    // rejected outright, so one explicit rank forces the whole array.
    expect(
      buildAutomationFallbackModels([
        { model: CLAUDE_MODEL, agentType: "claude_code", reasoningEffort: "max" },
        { model: AMP_MODE, agentType: "amp", reasoningEffort: "" },
      ]),
    ).toEqual({
      models: [CLAUDE_MODEL, AMP_MODE],
      agent_types: ["claude_code", "amp"],
      reasoning_efforts: ["max", ""],
    });
  });

  it("leaves an inheriting rank inheriting, whatever the primary runs at", () => {
    // What the primary runs at is not this function's business any more: the
    // API drops an inherited level the rank's agent cannot run instead of
    // rejecting it, so the chain stays canonical (no `reasoning_efforts` at
    // all) and a reasoning edit cannot drag an invented rank level along.
    expect(
      buildAutomationFallbackModels([
        { model: PRIMARY_MODEL, agentType: "codex", reasoningEffort: "" },
      ]),
    ).toEqual({
      models: [PRIMARY_MODEL],
      agent_types: ["codex"],
    });
  });
});

describe("automationFallbackRanks", () => {
  it("resolves each rank's agent from the override, then the model, then the primary", () => {
    const ranks = automationFallbackRanks(
      {
        models: [CLAUDE_MODEL, AMP_MODE, "some-unknown-model"],
        // Rank 0 is pinned explicitly to an agent the model name does not
        // imply, which is legal: ranks may run on a different agent entirely.
        agent_types: ["opencode", "", ""],
      },
      "codex",
    );

    expect(ranks.map((rank) => rank.agentType)).toEqual([
      "opencode",
      "amp",
      "codex",
    ]);
    expect(ranks.map((rank) => rank.reasoningEffort)).toEqual(["", "", ""]);
  });

  it("reads back an empty chain as no rows at all", () => {
    expect(automationFallbackRanks(undefined, "codex")).toEqual([]);
    expect(automationFallbackRanks({}, "codex")).toEqual([]);
  });
});

describe("AutomationFallbackModelsEditor", () => {
  it("numbers the primary first and the fallbacks after it", async () => {
    mockSelectableAgents();

    renderWithProviders(
      <EditorHarness
        initialValue={{ models: [CLAUDE_MODEL], agent_types: ["claude_code"] }}
        onChange={vi.fn()}
      />,
    );

    // The chain is one ordered list, so the primary has to carry rank 1 — a
    // fallback labelled "1." would misreport which model runs first.
    expect(screen.getByText("1. Preferred model")).toBeInTheDocument();
    expect(screen.getByText("2. Fallback model")).toBeInTheDocument();
    // The primary is displayed, not editable here; it has its own row on the
    // page and is stored in different columns.
    expect(
      screen.queryByRole("combobox", { name: "Rank 1 fallback model" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("combobox", { name: "Rank 2 fallback model" }),
    ).toBeInTheDocument();
  });

  it("opens a local draft row and only commits the chain once a model is picked", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mockSelectableAgents();

    renderWithProviders(<EditorHarness onChange={onChange} />);

    await user.click(screen.getByRole("button", { name: /Add fallback model/ }));

    // Both call sites save on every change, and a rank with no model is
    // rejected by the API — so the new row must exist locally without being
    // committed, or "Add" would fire a save that is certain to fail.
    expect(screen.getByText("2. Fallback model")).toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();

    // The draft's remove button discards it rather than saving a shorter chain.
    await user.click(screen.getByRole("button", { name: "Remove rank 2" }));
    expect(screen.queryByText("2. Fallback model")).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: /Add fallback model/ }));
    await pickModel(user, 2, CLAUDE_MODEL);

    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith({
        models: [CLAUDE_MODEL],
        agent_types: ["claude_code"],
      }),
    );
    expect(onChange).toHaveBeenCalledTimes(1);
  });

  it("removes a committed rank when its model is set back to Auto", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mockSelectableAgents();

    renderWithProviders(
      <EditorHarness
        initialValue={{
          models: [CLAUDE_MODEL, AMP_MODE],
          agent_types: ["claude_code", "amp"],
        }}
        onChange={onChange}
      />,
    );

    await pickModel(user, 2, "Auto");

    // "Auto" has no meaning for a fallback — a rank with no model is precisely
    // what removing the rank expresses, and leaving it empty would 400.
    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith({
        models: [AMP_MODE],
        agent_types: ["amp"],
      }),
    );
  });

  it("reorders the three arrays together", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mockSelectableAgents();

    renderWithProviders(
      <EditorHarness
        initialValue={{
          models: [CLAUDE_MODEL, AMP_MODE],
          agent_types: ["claude_code", "amp"],
          reasoning_efforts: ["max", ""],
        }}
        primaryReasoningEffort="high"
        onChange={onChange}
      />,
    );

    // The ends of the chain cannot move past themselves.
    expect(screen.getByRole("button", { name: "Move rank 2 up" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Move rank 3 down" }),
    ).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Move rank 3 up" }));

    // Every array moves as one. If `models` were reordered alone, rank 1 would
    // dispatch the Amp mode on Claude Code at the "max" level Amp cannot run.
    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith({
        models: [AMP_MODE, CLAUDE_MODEL],
        agent_types: ["amp", "claude_code"],
        reasoning_efforts: ["", "max"],
      }),
    );
  });

  it("keeps keyboard focus on the control the user activated after a reorder", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mockSelectableAgents();

    renderWithProviders(
      <EditorHarness
        initialValue={{
          models: [CLAUDE_MODEL, AMP_MODE, OTHER_CLAUDE_MODEL],
          agent_types: ["claude_code", "amp", "claude_code"],
        }}
        onChange={onChange}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Move rank 4 up" }));

    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith({
        models: [CLAUDE_MODEL, OTHER_CLAUDE_MODEL, AMP_MODE],
        agent_types: ["claude_code", "claude_code", "amp"],
      }),
    );

    // Rows are keyed by model, so React MOVES the row rather than unmounting
    // and remounting it. When the key mixed in the index, every key from the
    // edit point onward changed, the button the user had just pressed was
    // destroyed, and focus fell to <body> — leaving a keyboard user stranded at
    // the top of the page after a single reorder. The moved row is now rank 3,
    // so the same physical button answers to the next label up.
    expect(document.activeElement).not.toBe(document.body);
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Move rank 3 up" }),
    );
  });

  it("labels the inherit option for what the rank will actually run at", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mockSelectableAgents();

    renderWithProviders(
      <EditorHarness
        initialValue={{
          models: ["gpt-5.5"],
          agent_types: ["codex"],
          reasoning_efforts: ["high"],
        }}
        primaryReasoningEffort="max"
        onChange={onChange}
      />,
    );

    await user.click(
      screen.getByRole("combobox", { name: "Rank 2 reasoning level" }),
    );

    // The primary runs at "max" and Codex has no "max", so this rank can never
    // match the primary — calling the option "Same as preferred" would promise
    // a level the rank will not run at. It inherits nothing and uses Codex's
    // own default instead, which is what the label has to say.
    const inheritOption = await screen.findByRole("option", {
      name: "Agent default",
    });
    expect(
      screen.queryByRole("option", { name: "Same as preferred" }),
    ).not.toBeInTheDocument();

    await user.click(inheritOption);

    // And the option is selectable: clearing the level emits a chain that
    // differs from the stored one. While the normalizer substituted "high" for
    // an unrunnable inherited level, this click re-emitted the stored chain
    // byte for byte, the autosave's deepEqual guard dropped the save, and the
    // controlled select snapped back to "High" with nothing explaining why.
    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith({
        models: ["gpt-5.5"],
        agent_types: ["codex"],
      }),
    );
  });

  it("removes ranks down to an empty chain, rewriting every array as it goes", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mockSelectableAgents();

    renderWithProviders(
      <EditorHarness
        initialValue={{
          models: [CLAUDE_MODEL, AMP_MODE],
          agent_types: ["claude_code", "amp"],
          reasoning_efforts: ["max", ""],
        }}
        primaryReasoningEffort="high"
        onChange={onChange}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Remove rank 2" }));

    // Dropping the only rank that had an explicit level drops the array too,
    // instead of leaving a stale `["max"]` pointing at the surviving rank.
    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith({
        models: [AMP_MODE],
        agent_types: ["amp"],
      }),
    );

    // The last fallback is removable: an automation must be able to go back to
    // running on its primary alone.
    await user.click(screen.getByRole("button", { name: "Remove rank 2" }));
    await waitFor(() => expect(onChange).toHaveBeenLastCalledWith({}));
    expect(screen.queryByText("2. Fallback model")).not.toBeInTheDocument();
  });

  it("stops offering new ranks at the cap and offers again after a removal", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mockSelectableAgents();
    // Seeded one short of the cap so the boundary is reached in a single add
    // rather than through repeated dropdown interactions.
    const seeded = MAX_AUTOMATION_FALLBACK_MODELS - 1;
    const initialModels = [CLAUDE_MODEL, AMP_MODE, OTHER_CLAUDE_MODEL].slice(
      0,
      seeded,
    );
    const initialAgents = ["claude_code", "amp", "claude_code"].slice(0, seeded);

    renderWithProviders(
      <EditorHarness
        initialValue={{ models: initialModels, agent_types: initialAgents }}
        onChange={onChange}
      />,
    );

    const addButton = () =>
      screen.getByRole("button", { name: /Add fallback model/ });
    expect(addButton()).toBeEnabled();

    await user.click(addButton());
    // The uncommitted draft counts toward the cap, otherwise a second click
    // would open a row the chain can never hold.
    expect(addButton()).toBeDisabled();

    await pickModel(user, seeded + 2, "gpt-5.5");
    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith({
        models: [...initialModels, "gpt-5.5"],
        agent_types: [...initialAgents, "codex"],
      }),
    );
    expect(addButton()).toBeDisabled();

    await user.click(
      screen.getByRole("button", {
        name: `Remove rank ${MAX_AUTOMATION_FALLBACK_MODELS + 1}`,
      }),
    );
    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith({
        models: initialModels,
        agent_types: initialAgents,
      }),
    );
    // Room again — the cap is a live limit, not a one-way latch.
    expect(addButton()).toBeEnabled();
  });

  it("hides the primary and the sibling ranks from a rank's own picker", async () => {
    const user = userEvent.setup();
    mockSelectableAgents();

    renderWithProviders(
      <EditorHarness
        initialValue={{
          models: [CLAUDE_MODEL, AMP_MODE],
          agent_types: ["claude_code", "amp"],
        }}
        onChange={vi.fn()}
      />,
    );

    await user.click(
      screen.getByRole("combobox", { name: "Rank 2 fallback model" }),
    );

    // A duplicate rank is accepted by the API but retries a model that just
    // failed, which is the one thing a fallback chain exists to avoid.
    expect(
      screen.queryByRole("option", { name: PRIMARY_MODEL }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("option", { name: AMP_MODE }),
    ).not.toBeInTheDocument();
    // The row's own value stays listed so the trigger keeps showing it.
    expect(
      screen.getByRole("option", { name: CLAUDE_MODEL }),
    ).toBeInTheDocument();
  });

  it("only offers a reasoning level on ranks whose agent has one", async () => {
    mockSelectableAgents();

    renderWithProviders(
      <EditorHarness
        initialValue={{
          models: [AMP_MODE, CLAUDE_MODEL],
          agent_types: ["amp", "claude_code"],
        }}
        onChange={vi.fn()}
      />,
    );

    // Amp has no reasoning ladder at all, so a select there could only ever
    // offer "Same as preferred" and store a value the dispatcher ignores.
    expect(
      screen.queryByRole("combobox", { name: "Rank 2 reasoning level" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("combobox", { name: "Rank 3 reasoning level" }),
    ).toBeInTheDocument();
  });

  it("saves a per-rank reasoning level alongside the rest of the chain", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    mockSelectableAgents();

    renderWithProviders(
      <EditorHarness
        initialValue={{
          models: [CLAUDE_MODEL, AMP_MODE],
          agent_types: ["claude_code", "amp"],
        }}
        onChange={onChange}
      />,
    );

    await user.click(
      screen.getByRole("combobox", { name: "Rank 2 reasoning level" }),
    );
    await user.click(await screen.findByRole("option", { name: "Max" }));

    // Setting one rank's level fills the array for every rank, so the three
    // arrays stay the same length.
    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith({
        models: [CLAUDE_MODEL, AMP_MODE],
        agent_types: ["claude_code", "amp"],
        reasoning_efforts: ["max", ""],
      }),
    );
  });
});

describe("AutomationFallbackModelsSummary", () => {
  it("lists the whole chain in dispatch order for a viewer who cannot edit it", () => {
    renderWithProviders(
      <AutomationFallbackModelsSummary
        value={{
          models: [CLAUDE_MODEL, AMP_MODE],
          agent_types: ["claude_code", "amp"],
          reasoning_efforts: ["max", ""],
        }}
        primaryModel={PRIMARY_MODEL}
        primaryAgentType="codex"
      />,
    );

    // Same numbering as the editor: a reader comparing the two views should
    // not have to work out whether "1." means the primary in one and not the
    // other.
    expect(screen.getByText(/^1\./)).toHaveTextContent(PRIMARY_MODEL);
    expect(screen.getByText(/^2\./)).toHaveTextContent(CLAUDE_MODEL);
    expect(screen.getByText(/^3\./)).toHaveTextContent(AMP_MODE);
    // Read-only: no control a non-manager could fire a rejected save from.
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("says so explicitly when the automation has no fallbacks", () => {
    renderWithProviders(
      <AutomationFallbackModelsSummary
        primaryModel={PRIMARY_MODEL}
        primaryAgentType="codex"
      />,
    );

    // An empty area would read as "this feature is missing" rather than "this
    // automation has one model".
    expect(screen.getByText("No fallback models.")).toBeInTheDocument();
    expect(screen.getByText(/^1\./)).toHaveTextContent(PRIMARY_MODEL);
  });
});
