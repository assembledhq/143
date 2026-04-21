package handlers

import (
	"encoding/json"
	"reflect"
	"sort"
)

// settingsAuditDiff reports every leaf key whose value differs between the
// previous and the merged settings JSON blobs. Nested objects are walked
// recursively and reported via dotted paths ("context_limits.max_open_issues",
// "agent_config.codex.OPENAI_MODEL") so the audit viewer's ChangesBlock can
// render one row per real change instead of one giant JSON diff.
//
// The returned map has the shape {"field.path": {"before": X, "after": Y}}
// and uses nil on the absent side for keys that were added or removed, so a
// value→null transition is distinguishable from a no-op in the audit UI.
// Unchanged keys are omitted; if nothing changed the map is empty and the
// caller should skip emitting the audit row.
//
// We diff raw JSON maps (not the strongly-typed OrgSettings struct) on
// purpose: ParseOrgSettings applies size-aware defaults that are not
// persisted to the DB, so parsing both sides would surface a flood of
// "changes" on the first write to a previously-empty settings blob even
// though the user only touched one field.
func settingsAuditDiff(old, new_ json.RawMessage) map[string]any {
	changes := map[string]any{}
	diffJSONMaps("", rawSettingsToMap(old), rawSettingsToMap(new_), changes)
	return changes
}

// rawSettingsToMap unmarshals a settings JSON blob into a generic map.
// Empty or malformed input yields an empty map — malformed DB state is rare
// and the handler validates incoming JSON before calling this, so we swallow
// the unmarshal error rather than propagate it and block audit emission.
func rawSettingsToMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

// diffJSONMaps walks two JSON objects in lockstep and appends every leaf
// difference to changes. When both sides hold an object at the same key we
// recurse so nested edits report as dotted paths; otherwise we compare with
// reflect.DeepEqual (correct for scalars, arrays, and mixed type/null cases
// that == cannot handle).
//
// Keys are walked in sorted order so the emitted diff is deterministic —
// this keeps audit entries byte-stable across replays and makes the
// side-by-side UI render in a predictable order.
func diffJSONMaps(prefix string, old, new_ map[string]any, changes map[string]any) {
	keys := make(map[string]struct{}, len(old)+len(new_))
	for k := range old {
		keys[k] = struct{}{}
	}
	for k := range new_ {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, k := range sorted {
		oldV, hasOld := old[k]
		newV, hasNew := new_[k]
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}

		oldSub, oldIsObj := oldV.(map[string]any)
		newSub, newIsObj := newV.(map[string]any)
		if hasOld && hasNew && oldIsObj && newIsObj {
			diffJSONMaps(path, oldSub, newSub, changes)
			continue
		}

		if reflect.DeepEqual(oldV, newV) {
			continue
		}

		var before, after any
		if hasOld {
			before = oldV
		}
		if hasNew {
			after = newV
		}
		changes[path] = map[string]any{"before": before, "after": after}
	}
}
