package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaultScreenshotOpts(t *testing.T) {
	t.Parallel()

	opts := DefaultScreenshotOpts()
	require.Equal(t, "/", opts.Path)
	require.Equal(t, 1280, opts.ViewportW)
	require.Equal(t, 720, opts.ViewportH)
	require.False(t, opts.FullPage)
	require.Equal(t, time.Second, opts.Delay)
}

func TestPreviewConfigPrimaryServiceSupportsHMR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  PreviewConfig
		want bool
	}{
		{
			name: "primary declares hmr",
			cfg: PreviewConfig{
				Primary:  "web",
				Services: map[string]ServiceConfig{"web": {HMR: true}, "api": {HMR: false}},
			},
			want: true,
		},
		{
			name: "primary does not declare hmr",
			cfg: PreviewConfig{
				Primary:  "web",
				Services: map[string]ServiceConfig{"web": {HMR: false}},
			},
			want: false,
		},
		{
			name: "only a support service declares hmr",
			cfg: PreviewConfig{
				Primary:  "web",
				Services: map[string]ServiceConfig{"web": {HMR: false}, "api": {HMR: true}},
			},
			want: false,
		},
		{
			name: "primary service missing",
			cfg: PreviewConfig{
				Primary:  "web",
				Services: map[string]ServiceConfig{"api": {HMR: true}},
			},
			want: false,
		},
		{
			name: "no services",
			cfg:  PreviewConfig{Primary: "web"},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.cfg.PrimaryServiceSupportsHMR())
		})
	}
}

func TestDefaultViewports(t *testing.T) {
	t.Parallel()

	vps := DefaultViewports()
	require.Len(t, vps, 3)

	require.Equal(t, "mobile", vps[0].Name)
	require.Equal(t, 375, vps[0].Width)
	require.Equal(t, 812, vps[0].Height)

	require.Equal(t, "tablet", vps[1].Name)
	require.Equal(t, 768, vps[1].Width)
	require.Equal(t, 1024, vps[1].Height)

	require.Equal(t, "desktop", vps[2].Name)
	require.Equal(t, 1280, vps[2].Width)
	require.Equal(t, 720, vps[2].Height)
}
