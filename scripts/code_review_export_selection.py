"""Independent validation of replay-owned partition assignments; no result access."""
from collections import defaultdict
from fractions import Fraction
import tomllib

from code_review_export_metadata import canonical, digest, identity, integer, keys, locator_key, need, sha, text, timestamp, validate_inventory

RULESET = "chronological-pr-stack-outcome-censor/v1"
PARTITIONS = ("development", "calibration", "review_holdout", "model_holdout")
OPEN_PARTITIONS = PARTITIONS[:2]


def protocol_rules(raw):
    protocol = tomllib.loads(raw.decode("utf-8"))
    need(type(protocol["protocol_version"]) is int and protocol["protocol_version"] == 3)
    replay, window, split = protocol["replay"], protocol["window"], protocol["split"]
    need(type(replay["decision_version"]) is int and replay["decision_version"] == 2)
    expected_replay = {"status": "approved", "scope": "development_calibration", "holdout_access": "disabled",
                       "unresolved_membership": "quarantine", "conflicting_snapshots": "quarantine", "unknown_evidence": "conservative_bounds"}
    need(all(type(replay.get(k)) is type(v) and replay[k] == v for k, v in expected_replay.items()))
    need(replay.get("overall_verdict_requires_publication_cohort") is True)
    expected_split = {"unit": "pull_request", "grouping": "stack", "order": "first_review_publication_time",
                      "model_reservation": "latest_whole_groups_first", "fraction_denominator": "remaining_prs_after_model_reservation",
                      "boundary_rounding": "whole_groups_at_or_before_target", "tie_break": "repository_then_lowest_pr",
                      "unknown_stack_membership": "quarantine", "boundary_overlap": "quarantine_publication_overlap",
                      "embargo_basis": "outcome_observation_completion", "embargo_refill": False,
                      "outcome_label_cutoff": "full_window_before_next_partition_start", "outcome_embargo_days": 14,
                      "model_holdout_fraction": 0.20, "development_fraction": 0.60,
                      "calibration_fraction": 0.20, "review_holdout_fraction": 0.20}
    need(all(type(split.get(k)) is type(v) and split[k] == v for k, v in expected_split.items()))
    need(type(window.get("outcome_observation_days")) is int and window["outcome_observation_days"] == 14)
    need(window["confirmed"] is True and window["timezone"] == "UTC")
    need(str(window["review_start"]) == "2026-07-15" and str(window["review_end"]) == "2026-10-18")
    return timestamp("2026-07-15T00:00:00Z"), timestamp("2026-10-19T00:00:00Z")


def graph_membership(graph):
    keys(graph, ("schema_version", "groups"))
    need(type(graph["schema_version"]) is int and graph["schema_version"] == 1)
    need(type(graph["groups"]) is list and len(graph["groups"]) <= 100_000)
    members, groups = {}, {}
    for group in graph["groups"]:
        keys(group, ("repository_id", "stack_id", "pull_request_ids", "complete", "provenance"))
        repository_id = identity(group["repository_id"])
        stack_id = text(group["stack_id"], 256)
        need(group["complete"] is True and (repository_id, stack_id) not in groups)
        provenance = group["provenance"]
        keys(provenance, ("availability", "kind", "effective_at", "observed_at", "source_sha256"))
        need(provenance["availability"] in ("recorded", "reconstructed"))
        need(provenance["kind"] in ("historical_graphite_export", "historical_git_graph", "historical_operator_attestation"))
        need(timestamp(provenance["effective_at"]) <= timestamp(provenance["observed_at"]))
        digest(provenance["source_sha256"])
        prs = group["pull_request_ids"]
        need(type(prs) is list and 0 < len(prs) <= 100_000 and len(prs) == len(set(prs)))
        groups[(repository_id, stack_id)] = set()
        for pr_id in prs:
            pr = (repository_id, identity(pr_id))
            need(pr not in members)
            members[pr] = (repository_id, stack_id)
            groups[(repository_id, stack_id)].add(pr)
    return members, groups


def assignments(index, graph, protocol_raw):
    """Recompute the supported rule, rather than trusting a selected label.

    This validates a replay-owned plan and never emits or reads outcome labels.
    A complete graph component containing absent PRs is conservatively unresolved.
    """
    validate_inventory(index)
    start, until = protocol_rules(protocol_raw)
    membership, graph_groups = graph_membership(graph)
    rows = index["observations"]
    parent, reasons, by_identity, by_provider = {}, defaultdict(set), defaultdict(list), defaultdict(list)
    by_pr = defaultdict(list)
    by_public_pr = defaultdict(set)
    for row in rows:
        pr = (row["repository_id"], row["pull_request_id"])
        parent[pr] = pr
        by_pr[pr].append(row)
        if row["pr_number"] is not None:
            by_public_pr[(pr[0], row["pr_number"])].add(pr)
        by_identity[row["locator"]["source_identity"]].append(row)
        if row["provider_review_id"] is not None:
            by_provider[(pr[0], row["provider_review_id"])].append(row)
        if pr[1] is None or row["pr_number"] is None:
            reasons[pr].add("unknown_pr")
        if pr not in membership:
            reasons[pr].add("unknown_stack")
        clock = timestamp(row["publication_time"]) if row["publication_time"] else None
        if clock is None or row["provider_review_id"] is None or row["head_sha"] is None or row["publication_key"] is None:
            reasons[pr].add("unknown_publication")
        elif not start <= clock < until:
            reasons[pr].add("outside_window")

    def root(pr):
        while parent[pr] != pr:
            parent[pr] = parent[parent[pr]]
            pr = parent[pr]
        return pr

    def join(prs):
        ordered = sorted(prs, key=lambda p: (p[0], p[1] or ""))
        for pr in ordered[1:]:
            parent[root(pr)] = root(ordered[0])

    for prs in by_public_pr.values():
        if len(prs) > 1:
            join(prs)
            for pr in prs:
                reasons[pr].add("conflicting_pr_identity")
    for pr, observations in by_pr.items():
        if len({row["pr_number"] for row in observations}) != 1:
            reasons[pr].add("conflicting_pr_number")
    universe = set(parent)
    for prs in graph_groups.values():
        present = prs & universe
        if present:
            join(present)
        if present != prs:
            for pr in present:
                reasons[pr].add("incomplete_graph_component")
    for observations in by_identity.values():
        prs = {(r["repository_id"], r["pull_request_id"]) for r in observations}
        join(prs)
        # Exact original record fingerprints, not normalized result fingerprints.
        versions = {canonical({k: v for k, v in r.items() if k != "locator"}) for r in observations}
        if len(versions) != 1:
            for pr in prs:
                reasons[pr].add("conflicting_observations")
    for observations in by_provider.values():
        if len({r["locator"]["source_identity"] for r in observations}) > 1:
            prs = {(r["repository_id"], r["pull_request_id"]) for r in observations}
            join(prs)
            for pr in prs:
                reasons[pr].add("shared_provider_object")
    # Explicit alias IDs constrain grouping too; they are not extra source rows.
    aliases = defaultdict(set)
    for row in rows:
        pr = (row["repository_id"], row["pull_request_id"])
        if row["source_kind"] == "legacy":
            aliases[row["source_id"]].add(pr)
        for alias in row["legacy_alias_ids"]:
            aliases[alias].add(pr)
    for prs in aliases.values():
        join(prs)
    grouped = defaultdict(list)
    for pr in parent:
        grouped[root(pr)].append(pr)
    eligible, row_assignments = [], {locator_key(r): "quarantine" for r in rows}
    for prs in grouped.values():
        observations = [row for pr in prs for row in by_pr[pr]]
        if any(reasons[pr] for pr in prs):
            continue
        first = min(timestamp(r["publication_time"]) for r in observations)
        last = max(timestamp(r["publication_time"]) for r in observations)
        lowest = min((r["repository_id"], r["pr_number"]) for r in observations)
        eligible.append({"prs": prs, "rows": observations, "first": first, "last": last, "sort": (first, *lowest)})
    eligible.sort(key=lambda g: g["sort"])
    total = sum(len(g["prs"]) for g in eligible)
    reserved, boundary = 0, len(eligible)
    while boundary > 0 and reserved < Fraction(total, 5):
        boundary -= 1
        reserved += len(eligible[boundary]["prs"])
        eligible[boundary]["partition"] = "model_holdout"
    remaining, cumulative = total - reserved, 0
    for group in eligible[:boundary]:
        cumulative += len(group["prs"])
        group["partition"] = "development" if cumulative <= Fraction(3 * remaining, 5) else "calibration" if cumulative <= Fraction(4 * remaining, 5) else "review_holdout"
    starts = {name: min((g["first"] for g in eligible if g["partition"] == name), default=None) for name in PARTITIONS}
    for group in eligible:
        following = [starts[name] for name in PARTITIONS[PARTITIONS.index(group["partition"]) + 1:] if starts[name] is not None]
        cutoff = min(following) if following else None
        # v3 keeps reviews unless publication actually reaches the next group.
        # 14-day outcome observation is the parent's normalization concern.
        partition = "quarantine" if cutoff is not None and group["last"] >= cutoff else group["partition"]
        for row in group["rows"]:
            row_assignments[locator_key(row)] = partition
    return row_assignments


def validate_selection(index, index_raw, graph, graph_raw, protocol_raw, plan, plan_raw, selection):
    keys(selection, ("schema_version", "ruleset", "catalog_sha256", "inventory_sha256", "stack_graph_sha256", "protocol_sha256", "partition_sha256", "partitions", "selected"))
    need(type(selection["schema_version"]) is int and selection["schema_version"] == 1 and selection["ruleset"] == RULESET)
    for key, expected in (("catalog_sha256", index["catalog_sha256"]), ("inventory_sha256", sha(index_raw)), ("stack_graph_sha256", sha(graph_raw)), ("protocol_sha256", sha(protocol_raw)), ("partition_sha256", sha(plan_raw))):
        need(digest(selection[key]) == expected)
    keys(plan, ("schema_version", "ruleset", "catalog_sha256", "inventory_sha256", "stack_graph_sha256", "protocol_sha256", "assignments"))
    need(type(plan["schema_version"]) is int and plan["schema_version"] == 1 and plan["ruleset"] == RULESET)
    for key in ("catalog_sha256", "inventory_sha256", "stack_graph_sha256", "protocol_sha256"):
        need(plan[key] == selection[key])
    expected = assignments(index, graph, protocol_raw)
    need(type(plan["assignments"]) is list and len(plan["assignments"]) == len(expected))
    claimed = {}
    row_map = {locator_key(r): r for r in index["observations"]}
    for entry in plan["assignments"]:
        keys(entry, ("locator", "partition"))
        key = locator_key(entry)
        need(key not in claimed and key in expected and entry["partition"] == expected[key])
        need(entry["locator"] == row_map[key]["locator"])
        claimed[key] = entry["partition"]
    partitions = selection["partitions"]
    need(type(partitions) is list and 0 < len(partitions) <= 2)
    need(partitions == [name for name in OPEN_PARTITIONS if name in partitions])
    need(type(selection["selected"]) is list and 0 < len(selection["selected"]) <= len(expected))
    selected = set()
    for entry in selection["selected"]:
        keys(entry, ("locator", "partition"))
        key = locator_key(entry)
        need(key not in selected and key in expected and expected[key] in partitions and entry["partition"] == expected[key])
        need(entry["locator"] == row_map[key]["locator"])
        selected.add(key)
    # A replay-ready selection contains the complete requested open partitions.
    need(selected == {key for key, partition in expected.items() if partition in partitions})
    return selected
