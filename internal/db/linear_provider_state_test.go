package db

import "testing"

// TestMergeLinearProviderState_PreservesStickyBools is the regression test
// for the Merge bool-clobbering bug. A partial patch (e.g. recording a skip
// reason) must NOT reset CoexistsWithGitHubIntegration once it has been
// set — without this, the suppress-on-coexistence guard is one update away
// from being lost on every state event.
func TestMergeLinearProviderState_PreservesStickyBools(t *testing.T) {
	t.Parallel()

	current := LinearProviderState{
		AttachmentID:                  "attach-1",
		CommentID:                     "comment-1",
		CoexistsWithGitHubIntegration: BoolPtr(true),
	}
	patch := LinearProviderState{LastSkippedReason: "private_session"}

	merged := MergeLinearProviderState(current, patch)

	if merged.CoexistsWithGitHubIntegration == nil || !*merged.CoexistsWithGitHubIntegration {
		t.Fatalf("Merge must preserve CoexistsWithGitHubIntegration=true after a partial patch, got %+v", merged.CoexistsWithGitHubIntegration)
	}
	if merged.LastSkippedReason != "private_session" {
		t.Fatalf("Merge must apply the patch's LastSkippedReason, got %q", merged.LastSkippedReason)
	}
	if merged.AttachmentID != "attach-1" || merged.CommentID != "comment-1" {
		t.Fatalf("Merge must preserve unrelated string fields, got %+v", merged)
	}
}

// TestMergeLinearProviderState_AllowsExplicitBoolUpdate verifies the
// pointer semantics work in the other direction too: a non-nil patch
// overwrites the stored value. Without this, the coexistence detector
// couldn't promote false → true on first observation.
func TestMergeLinearProviderState_AllowsExplicitBoolUpdate(t *testing.T) {
	t.Parallel()

	current := LinearProviderState{}
	patch := LinearProviderState{CoexistsWithGitHubIntegration: BoolPtr(true)}

	merged := MergeLinearProviderState(current, patch)

	if merged.CoexistsWithGitHubIntegration == nil || !*merged.CoexistsWithGitHubIntegration {
		t.Fatalf("explicit Merge(true) must promote the bool, got %+v", merged.CoexistsWithGitHubIntegration)
	}
}

// TestMergeLinearProviderState_AllowsExplicitFalse verifies that a patch
// can also clear a sticky bool — important for the "remove or repair"
// affordance that needs to flip IssueRepoStale back off.
func TestMergeLinearProviderState_AllowsExplicitFalse(t *testing.T) {
	t.Parallel()

	current := LinearProviderState{IssueRepoStale: BoolPtr(true)}
	patch := LinearProviderState{IssueRepoStale: BoolPtr(false)}

	merged := MergeLinearProviderState(current, patch)

	if merged.IssueRepoStale == nil {
		t.Fatalf("Merge with explicit false must set the pointer, got nil")
	}
	if *merged.IssueRepoStale {
		t.Fatalf("Merge with explicit false must clear the flag, got true")
	}
}

// TestMergeLinearProviderState_EmptyStringDoesNotClear locks the design
// invariant that empty patch strings are no-ops, not clears.
func TestMergeLinearProviderState_EmptyStringDoesNotClear(t *testing.T) {
	t.Parallel()

	current := LinearProviderState{
		AttachmentID:    "attach-1",
		CommentID:       "comment-1",
		LinkAuditReason: "linear_null_repo_carveout",
	}
	patch := LinearProviderState{LastWriteOutcome: "merged"}

	merged := MergeLinearProviderState(current, patch)

	if merged.AttachmentID != "attach-1" {
		t.Errorf("AttachmentID must remain when patch leaves it empty")
	}
	if merged.CommentID != "comment-1" {
		t.Errorf("CommentID must remain when patch leaves it empty")
	}
	if merged.LinkAuditReason != "linear_null_repo_carveout" {
		t.Errorf("LinkAuditReason must remain when patch leaves it empty")
	}
	if merged.LastWriteOutcome != "merged" {
		t.Errorf("LastWriteOutcome must apply from patch")
	}
}
