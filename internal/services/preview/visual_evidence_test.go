package preview

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestPreviewVisualDiffRegions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		bounds image.Rectangle
		points []image.Point
		color  color.RGBA
		want   *models.VisualDiff
	}{
		{name: "identical empty images", bounds: image.Rect(0, 0, 0, 0), want: &models.VisualDiff{}},
		{name: "unchanged image", bounds: image.Rect(0, 0, 64, 64), want: &models.VisualDiff{}},
		{name: "antialiasing threshold", bounds: image.Rect(0, 0, 64, 64), points: []image.Point{{1, 1}}, color: color.RGBA{R: 10, A: 255}, want: &models.VisualDiff{}},
		{name: "single changed cell", bounds: image.Rect(0, 0, 64, 64), points: []image.Point{{1, 1}}, color: color.RGBA{R: 255, A: 255}, want: &models.VisualDiff{PixelDiffPercent: .02, DiffRegions: []models.DiffRegion{{BoundingBox: models.Rect{Width: 32, Height: 32}, Severity: "major"}}}},
		{name: "adjacent changed cells merge", bounds: image.Rect(0, 0, 64, 64), points: []image.Point{{1, 1}, {33, 1}}, color: color.RGBA{G: 255, A: 255}, want: &models.VisualDiff{PixelDiffPercent: .05, DiffRegions: []models.DiffRegion{{BoundingBox: models.Rect{Width: 64, Height: 32}, Severity: "major"}}}},
		{name: "separate changed cells remain separate", bounds: image.Rect(0, 0, 128, 128), points: []image.Point{{1, 1}, {97, 97}}, color: color.RGBA{B: 255, A: 255}, want: &models.VisualDiff{PixelDiffPercent: .01, DiffRegions: []models.DiffRegion{{BoundingBox: models.Rect{Width: 32, Height: 32}, Severity: "minor"}, {BoundingBox: models.Rect{X: 96, Y: 96, Width: 32, Height: 32}, Severity: "minor"}}}},
		{name: "translated image bounds compare relative pixels", bounds: image.Rect(10, 20, 74, 84), points: []image.Point{{11, 21}}, color: color.RGBA{R: 255, A: 255}, want: &models.VisualDiff{PixelDiffPercent: .02, DiffRegions: []models.DiffRegion{{BoundingBox: models.Rect{Width: 32, Height: 32}, Severity: "major"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before, after := image.NewRGBA(tt.bounds), image.NewRGBA(tt.bounds)
			for _, point := range tt.points {
				after.SetRGBA(point.X, point.Y, tt.color)
			}
			require.Equal(t, tt.want, computePixelDiff(before, after), "visual diff must ignore antialiasing and report exact connected change regions")
		})
	}
}

func TestPreviewVisualOverlay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                      string
		beforeBounds, afterBounds image.Rectangle
		changed                   bool
		wantBounds                image.Rectangle
	}{{"changed pixel highlighted", image.Rect(0, 0, 2, 2), image.Rect(0, 0, 2, 2), true, image.Rect(0, 0, 2, 2)}, {"unchanged pixel gray", image.Rect(0, 0, 2, 2), image.Rect(0, 0, 2, 2), false, image.Rect(0, 0, 2, 2)}, {"crop to smaller after", image.Rect(0, 0, 3, 3), image.Rect(0, 0, 2, 2), true, image.Rect(0, 0, 2, 2)}, {"crop to smaller before", image.Rect(0, 0, 2, 2), image.Rect(0, 0, 3, 3), true, image.Rect(0, 0, 2, 2)}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before, after := image.NewRGBA(tt.beforeBounds), image.NewRGBA(tt.afterBounds)
			if tt.changed {
				after.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
			}
			raw, err := generateDiffOverlay(before, after)
			require.NoError(t, err, "overlay must encode cropped images")
			actual, err := png.Decode(bytes.NewReader(raw))
			require.NoError(t, err, "overlay evidence must be decodable PNG")
			require.Equal(t, tt.wantBounds, actual.Bounds(), "overlay must use common image extent")
			want := color.NRGBA{A: 100}
			if tt.changed {
				want = color.NRGBA{R: 255, A: 180}
			}
			require.Equal(t, want, color.NRGBAModel.Convert(actual.At(0, 0)), "overlay pixel must distinguish change highlight from unchanged background")
		})
	}
}

func TestPreviewAnimatedEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                       string
		corruptOnly, corruptBefore bool
		fps, frames                int
	}{{"two valid frames", false, false, 2, 2}, {"corrupt frame skipped", false, true, 4, 2}, {"no valid frames", true, false, 2, 0}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			frames := [][]byte{}
			if tt.corruptOnly || tt.corruptBefore {
				frames = append(frames, []byte("invalid PNG"))
			}
			if !tt.corruptOnly {
				for _, shade := range []color.RGBA{{R: 255, A: 255}, {B: 255, A: 255}} {
					img := image.NewRGBA(image.Rect(0, 0, 2, 2))
					img.SetRGBA(0, 0, shade)
					var encoded bytes.Buffer
					require.NoError(t, png.Encode(&encoded, img), "create independent valid frame")
					frames = append(frames, encoded.Bytes())
				}
			}
			raw, err := assembleGIF(frames, tt.fps)
			if tt.corruptOnly {
				require.ErrorContains(t, err, "no valid frames", "invalid evidence must fail instead of returning empty success")
				require.Nil(t, raw, "failed encoding must not produce an attachment")
				return
			}
			require.NoError(t, err, "valid frames must produce animated evidence")
			actual, err := gif.DecodeAll(bytes.NewReader(raw))
			require.NoError(t, err, "animated evidence must be decodable GIF")
			require.Equal(t, []int{100 / tt.fps, 100 / tt.fps}, actual.Delay, "animation timing must match requested frame rate")
			require.Len(t, actual.Image, tt.frames, "corrupt input frames must not appear in output")
			require.Equal(t, color.RGBA{R: 255, A: 255}, color.RGBAModel.Convert(actual.Image[0].At(0, 0)), "first valid frame must retain its image content")
		})
	}
}
