package jobctx_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
)

func TestIsFinalAttempt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ctx  func() context.Context
		want bool
	}{
		{
			name: "missing metadata returns false",
			ctx:  context.Background,
			want: false,
		},
		{
			name: "first attempt of three is not final",
			ctx:  func() context.Context { return jobctx.WithAttempt(context.Background(), 1, 3) },
			want: false,
		},
		{
			name: "mid attempt is not final",
			ctx:  func() context.Context { return jobctx.WithAttempt(context.Background(), 2, 3) },
			want: false,
		},
		{
			name: "last attempt equals ceiling and is final",
			ctx:  func() context.Context { return jobctx.WithAttempt(context.Background(), 3, 3) },
			want: true,
		},
		{
			name: "current above ceiling is also final",
			ctx:  func() context.Context { return jobctx.WithAttempt(context.Background(), 5, 3) },
			want: true,
		},
		{
			name: "single-attempt job is final on first try",
			ctx:  func() context.Context { return jobctx.WithAttempt(context.Background(), 1, 1) },
			want: true,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, c.want, jobctx.IsFinalAttempt(c.ctx()))
		})
	}
}
