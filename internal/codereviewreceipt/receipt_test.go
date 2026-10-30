package codereviewreceipt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchesRetainsTaggedStructSemantics(t *testing.T) {
	t.Parallel()
	const id = "11111111-1111-4111-8111-111111111111"
	const identity = `"assessment_id":"` + id + `","input_digest":"synthetic-digest","submitted_commit_sha":"synthetic-head"`
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "canonical", raw: `{` + identity + `,"github_review_id":731}`, want: true},
		{name: "folded aliases", raw: `{"ASSESSMENT_ID":"` + id + `","INPUT_DIGEST":"synthetic-digest","SUBMITTED_COMMIT_SHA":"synthetic-head","GITHUB_REVIEW_ID":731}`, want: true},
		{name: "unicode long s aliases", raw: `{"aſſeſſment_id":"` + id + `","input_digeſt":"synthetic-digest","ſubmitted_commit_ſha":"synthetic-head","github_review_id":731}`, want: true},
		{name: "wrong typed alias remains error before correct member", raw: `{"ASSESSMENT_ID":17,` + identity + `,"github_review_id":731}`},
		{name: "wrong typed alias remains error after correct member", raw: `{` + identity + `,"ASSESSMENT_ID":17,"github_review_id":731}`},
		{name: "digest alias type error is sticky", raw: `{` + identity + `,"INPUT_DIGEST":false,"github_review_id":731}`},
		{name: "head alias type error is sticky", raw: `{` + identity + `,"SUBMITTED_COMMIT_SHA":17,"github_review_id":731}`},
		{name: "review alias type error is sticky", raw: `{` + identity + `,"GITHUB_REVIEW_ID":"731","github_review_id":731}`},
		{name: "later string alias changes value", raw: `{` + identity + `,"ASSESSMENT_ID":"different","github_review_id":731}`},
		{name: "later canonical string repairs typed value", raw: `{"ASSESSMENT_ID":"different",` + identity + `,"github_review_id":731}`, want: true},
		{name: "string null retains prior assignment", raw: `{` + identity + `,"ASSESSMENT_ID":null,"github_review_id":731}`, want: true},
		{name: "pointer null clears prior assignment", raw: `{` + identity + `,"github_review_id":731,"GITHUB_REVIEW_ID":null}`},
		{name: "later pointer value replaces null", raw: `{` + identity + `,"GITHUB_REVIEW_ID":null,"github_review_id":731}`, want: true},
		{name: "decimal review id rejected", raw: `{` + identity + `,"github_review_id":731.0}`},
		{name: "exponent review id rejected", raw: `{` + identity + `,"github_review_id":731e0}`},
		{name: "overflow review id rejected", raw: `{` + identity + `,"github_review_id":9223372036854775808}`},
		{name: "unrelated fields remain ignored", raw: `{` + identity + `,"github_review_id":731,"unrelated":{"nested":[null,17]}}`, want: true},
		{name: "wrong id", raw: `{"assessment_id":"different","input_digest":"synthetic-digest","submitted_commit_sha":"synthetic-head","github_review_id":731}`},
		{name: "wrong digest", raw: `{"assessment_id":"` + id + `","input_digest":"different","submitted_commit_sha":"synthetic-head","github_review_id":731}`},
		{name: "wrong head", raw: `{"assessment_id":"` + id + `","input_digest":"synthetic-digest","submitted_commit_sha":"different","github_review_id":731}`},
		{name: "wrong review id", raw: `{` + identity + `,"github_review_id":732}`},
		{name: "missing review id", raw: `{` + identity + `}`},
		{name: "array rejected", raw: `[]`},
		{name: "null rejected", raw: `null`},
		{name: "malformed rejected", raw: `{"assessment_id":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Matches([]byte(tt.raw), id, "synthetic-digest", "synthetic-head", 731), "receipt comparison must retain the original tagged-struct decoder's exact semantics")
		})
	}
}
