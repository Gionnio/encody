// Indice di grana del film.
//
// In 15 frame (10–85% della durata, a risoluzione piena: a bassa risoluzione la grana sparisce)
// si toglie il dettaglio grande con un passa-alto (pixel meno media 3×3) e si misura la deviazione
// standard nei blocchi 16×16. Il 10° percentile dei blocchi è quella dei blocchi più piatti
// (cielo, pareti): lì il dettaglio vero non c'è e resta solo la grana. Si considerano solo i
// mezzitoni, così bande nere, ombre profonde e luci bruciate non falsano la misura.
// Indice = mediana sui frame, in codici a 10 bit. Tarato su L'Impero colpisce ancora (BDRemux, 2,8)
// e Loki (WEB-DL x265, 0,4–0,7). Sotto i 720p non si misura.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
)

const (
	grainPoints     = 15
	grainBlock      = 16
	grainVersion    = 2 // 2: niente misura sotto i 720p
	grainMinHeight  = 720
	GrainMediumFrom = 1.0 // sotto: grana bassa
	GrainHighFrom   = 2.0
)

type GrainResult struct {
	Version int     `json:"version"`
	Index   float64 `json:"index"`
	Level   string  `json:"level"` // low | medium | high
	Points  int     `json:"points"`
	Skipped bool    `json:"skipped,omitempty"` // risoluzione troppo bassa: non misurata
}

func grainLevel(v float64) string {
	switch {
	case v >= GrainHighFrom:
		return "high"
	case v >= GrainMediumFrom:
		return "medium"
	default:
		return "low"
	}
}

func grainLabel(level string) string {
	return map[string]string{"low": "bassa", "medium": "media", "high": "alta"}[level]
}

func measureGrain(ctx context.Context, src string, info *MediaInfo) (GrainResult, error) {
	cache := segmentCachePath(src)
	if cache != "" {
		if data, err := os.ReadFile(filepath.Join(cache, "grain.json")); err == nil {
			var g GrainResult
			if json.Unmarshal(data, &g) == nil && g.Version == grainVersion {
				return g, nil
			}
		}
	}
	// Sotto i 720p la grana di pellicola non si distingue dagli artefatti di compressione
	// (Big Buck Bunny 320×180, un cartone animato, risultava "grana alta"): non si misura
	if info.Height > 0 && info.Height < grainMinHeight {
		return GrainResult{Version: grainVersion, Level: "low", Skipped: true}, nil
	}
	if info.Width <= 0 || info.Height <= 0 || info.Duration <= 0 {
		return GrainResult{}, errors.New(T("dimensioni o durata del video sconosciute"))
	}

	var values []float64
	for k := 0; k < grainPoints; k++ {
		if ctx.Err() != nil {
			return GrainResult{}, ctx.Err()
		}
		t := info.Duration * (0.10 + 0.75*float64(k)/float64(grainPoints-1))
		frame, err := grayFrame(ctx, src, info, t)
		if err != nil {
			continue
		}
		if v, ok := frameGrain(frame, info.Width, info.Height); ok {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return GrainResult{}, errors.New(T("nessun frame utile per misurare la grana"))
	}
	sort.Float64s(values)
	idx := values[len(values)/2]
	g := GrainResult{Version: grainVersion, Index: math.Round(idx*100) / 100, Level: grainLevel(idx), Points: len(values)}

	if cache != "" && os.MkdirAll(cache, 0o755) == nil {
		if data, err := json.Marshal(g); err == nil {
			_ = os.WriteFile(filepath.Join(cache, "grain.json"), data, 0o644)
		}
	}
	return g, nil
}

// Un frame in luma a 10 bit, risoluzione piena (decodifica hardware se possibile)
func grayFrame(ctx context.Context, src string, info *MediaInfo, t float64) ([]uint16, error) {
	read := func(hw bool) ([]uint16, error) {
		args := []string{"-hide_banner", "-nostdin", "-v", "error"}
		if hw {
			args = append(args, "-hwaccel", "videotoolbox")
		}
		args = append(args, "-ss", fmt.Sprintf("%.3f", t), "-i", src, "-map", fmt.Sprintf("0:%d", info.VideoIndex),
			"-frames:v", "1", "-fps_mode", "passthrough", "-vf", "format=gray10le", "-f", "rawvideo", "-")
		cmd := exec.CommandContext(ctx, Tools.FFmpeg, args...)
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		buf := make([]byte, info.Width*info.Height*2)
		_, rerr := io.ReadFull(out, buf)
		_, _ = io.Copy(io.Discard, out)
		werr := cmd.Wait()
		if rerr != nil {
			return nil, fmt.Errorf(T("frame a %.0f s: %w"), t, rerr)
		}
		if werr != nil {
			return nil, werr
		}
		px := make([]uint16, info.Width*info.Height)
		for i := range px {
			px[i] = uint16(buf[2*i]) | uint16(buf[2*i+1])<<8
		}
		return px, nil
	}
	px, err := read(runtime.GOOS == "darwin")
	if err != nil && ctx.Err() == nil && runtime.GOOS == "darwin" {
		px, err = read(false)
	}
	return px, err
}

// Deviazione del passa-alto nei blocchi più piatti dei mezzitoni; ok=false se il frame è quasi tutto
// nero o bruciato
func frameGrain(px []uint16, w, h int) (float64, bool) {
	// immagine integrale per la media 3×3 in O(1)
	iw := w + 1
	integ := make([]float64, iw*(h+1))
	for y := 0; y < h; y++ {
		var row float64
		for x := 0; x < w; x++ {
			row += float64(px[y*w+x])
			integ[(y+1)*iw+x+1] = integ[y*iw+x+1] + row
		}
	}
	box := func(x, y int) float64 {
		x0, y0 := max(x-1, 0), max(y-1, 0)
		x1, y1 := min(x+2, w), min(y+2, h)
		s := integ[y1*iw+x1] - integ[y0*iw+x1] - integ[y1*iw+x0] + integ[y0*iw+x0]
		return s / float64((x1-x0)*(y1-y0))
	}

	var stds []float64
	n := float64(grainBlock * grainBlock)
	for by := 0; by+grainBlock <= h; by += grainBlock {
		for bx := 0; bx+grainBlock <= w; bx += grainBlock {
			var sum, sumR, sumR2 float64
			for y := by; y < by+grainBlock; y++ {
				for x := bx; x < bx+grainBlock; x++ {
					v := float64(px[y*w+x])
					r := v - box(x, y)
					sum += v
					sumR += r
					sumR2 += r * r
				}
			}
			norm := (sum/n - 64) / 876
			if norm <= 0.15 || norm >= 0.65 {
				continue
			}
			mean := sumR / n
			stds = append(stds, math.Sqrt(math.Max(0, sumR2/n-mean*mean)))
		}
	}
	if len(stds) < 50 {
		return 0, false
	}
	sort.Float64s(stds)
	return stds[len(stds)/10], true
}
