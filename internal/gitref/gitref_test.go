package gitref

import "testing"

func TestIsValidRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ref  string
		want bool
	}{
		// Valid refs.
		{"main", true},
		{"feature/foo", true},
		{"release-1.2.3", true},
		{"a", true},
		{"users/john/fix_bug", true},
		{"v2.0.0", true},

		// Invalid: empty / too long.
		{"", false},

		// Invalid: leading '-' would be parsed by git as an option, not a refspec.
		{"-foo", false},
		{"--upload-pack=touch /tmp/pwned", false},
		{"-", false},

		// Invalid: shell/ref-hostile characters.
		{"foo bar", false},
		{"foo;rm -rf /", false},
		{"foo$(id)", false},
		{"foo:bar", false},
		{"foo..bar", false},
		{"foo~1", false},
		{"foo^", false},
		{"foo\\bar", false},
		{"foo\tbar", false},
		{"foo\nbar", false},
		{".hidden", false}, // must start alphanumeric
		{"/abs", false},
	}
	for _, tc := range cases {
		if got := IsValidRef(tc.ref); got != tc.want {
			t.Errorf("IsValidRef(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

func TestIsValidRef_LengthCap(t *testing.T) {
	t.Parallel()

	long := make([]byte, 256)
	for i := range long {
		long[i] = 'a'
	}
	if IsValidRef(string(long)) {
		t.Errorf("IsValidRef should reject refs longer than 255 chars")
	}
}
