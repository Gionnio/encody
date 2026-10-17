package main

import "testing"

// Bande nere del Dolby Vision dopo crop e scala
func TestTransformL5(t *testing.T) {
	bars := l5Preset{Top: 276, Bottom: 276}
	cases := []struct {
		name           string
		cx, cy, cw, ch int
		f              float64
		want           l5Preset
	}{
		{"crop esatto", 0, 276, 3840, 1608, 1, l5Preset{}},
		{"solo scala", 0, 0, 3840, 2160, 0.5, l5Preset{Top: 138, Bottom: 138}},
		{"crop parziale e scala", 0, 200, 3840, 1760, 0.5, l5Preset{Top: 38, Bottom: 38}},
		{"crop più largo delle bande", 0, 300, 3840, 1560, 1, l5Preset{}},
	}
	for _, c := range cases {
		if got := transformL5(bars, 3840, 2160, c.cx, c.cy, c.cw, c.ch, c.f); got != c.want {
			t.Errorf("%s: %+v, atteso %+v", c.name, got, c.want)
		}
	}
}
