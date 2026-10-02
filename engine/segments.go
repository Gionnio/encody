// Scelta degli spezzoni del benchmark.
//
// Invece di 45 s dal centro del film (rischio di scena scura, dissolvenza o nero) si prendono
// tre spezzoni da 15 s, uno per terzo del film (sigla e titoli di coda esclusi), scelti con:
//  1. bitrate secondo per secondo, dalle dimensioni dei pacchetti (ffprobe, senza decodifica):
//     misura quanto ogni scena è "difficile" per l'encoder;
//  2. luminosità dei keyframe a bassa risoluzione (signalstats): trova neri, dissolvenze e scene scure.
//
// In ogni terzo vince la finestra senza neri, non scura rispetto al film, con bitrate più vicino
// al 65° percentile (difficile ma non estrema). L'inizio è allineato al keyframe precedente, così
// lo spezzone si taglia senza ricodificare.
package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	SegmentCount   = 3
	SegmentSeconds = 15.0
	segIntroSkip   = 0.05 // sigla
	segCreditsSkip = 0.10 // titoli di coda
	segBlackLevel  = 0.05 // luma normalizzata sotto cui il frame è nero
	segDarkRatio   = 0.6  // finestra scura: luma media sotto il 60% della mediana del film
	segTargetPct   = 0.65 // percentile di bitrate cercato
	segAnalysisVer = 1    // da incrementare se cambia l'algoritmo (invalida la cache)
)

type BenchSegment struct {
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
	Luma     float64 `json:"luma"`    // 0–1, media dei keyframe nella finestra
	Bitrate  int64   `json:"bitrate"` // bit/s medi della finestra
	Thumb    string  `json:"thumb,omitempty"`
	Note     string  `json:"note,omitempty"` // avviso se la scelta è di ripiego
}

type SegmentAnalysis struct {
	Version    int            `json:"version"`
	Segments   []BenchSegment `json:"segments"`
	MedianLuma float64        `json:"median_luma"`
	Warnings   []string       `json:"warnings"`
}

type lumaSample struct {
	T, Y float64
}

// Segmenti di ripiego per file corti: un solo spezzone centrale di BenchSeconds
func centerSegment(info *MediaInfo) []BenchSegment {
	d := math.Min(BenchSeconds, info.Duration)
	start := 0.0
	if info.Duration > d {
		start = info.Duration/2 - d/2
	}
	return []BenchSegment{{Start: start, Duration: d}}
}

func segmentCachePath(src string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	st, err := os.Stat(src)
	if err != nil {
		return ""
	}
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", src, st.Size(), st.ModTime().Unix())))
	return filepath.Join(dir, "Encody", "segments", hex.EncodeToString(h[:8]))
}

// Analisi completa con cache (stesso file, stessa dimensione e data → risultato riusato)
func analyzeSegments(ctx context.Context, src string, info *MediaInfo, on func(ProgressUpdate)) (SegmentAnalysis, error) {
	cache := segmentCachePath(src)
	if cache != "" {
		if data, err := os.ReadFile(filepath.Join(cache, "analysis.json")); err == nil {
			var a SegmentAnalysis
			if json.Unmarshal(data, &a) == nil && a.Version == segAnalysisVer && thumbsExist(a) {
				return a, nil
			}
		}
	}

	// File corti: tre spezzoni non hanno senso
	if info.Duration < SegmentCount*SegmentSeconds*4 {
		a := SegmentAnalysis{Version: segAnalysisVer, Segments: centerSegment(info)}
		a.Warnings = append(a.Warnings, "File corto: si usa un unico spezzone centrale.")
		addThumbs(ctx, src, info, cache, a.Segments)
		return a, nil
	}

	on(ProgressUpdate{Step: "Analisi bitrate"})
	rate, keyframes, err := packetBitrate(ctx, src, info)
	if err != nil {
		return SegmentAnalysis{}, err
	}
	samples, err := keyframeLuma(ctx, src, info, on)
	if err != nil {
		return SegmentAnalysis{}, err
	}
	a := chooseSegments(info.Duration, rate, keyframes, samples)
	addThumbs(ctx, src, info, cache, a.Segments)

	if cache != "" {
		if os.MkdirAll(cache, 0o755) == nil {
			if data, err := json.Marshal(a); err == nil {
				_ = os.WriteFile(filepath.Join(cache, "analysis.json"), data, 0o644)
			}
		}
	}
	return a, nil
}

func thumbsExist(a SegmentAnalysis) bool {
	for _, s := range a.Segments {
		if s.Thumb != "" {
			if _, err := os.Stat(s.Thumb); err != nil {
				return false
			}
		}
	}
	return true
}

// Bit per secondo (indice = secondo) e tempi dei keyframe, letti dai pacchetti senza decodificare
func packetBitrate(ctx context.Context, src string, info *MediaInfo) ([]float64, []float64, error) {
	cmd := exec.CommandContext(ctx, Tools.FFprobe, "-v", "error", "-select_streams", strconv.Itoa(info.VideoIndex),
		"-show_entries", "packet=pts_time,dts_time,size,flags", "-of", "csv=p=0", src)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	n := int(math.Ceil(info.Duration)) + 1
	rate := make([]float64, n)
	var keys []float64
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		// pts_time,dts_time,size,flags (l'ordine segue quello di ffprobe: pts, dts, size, flags)
		f := strings.Split(sc.Text(), ",")
		if len(f) < 4 {
			continue
		}
		t, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			if t, err = strconv.ParseFloat(f[1], 64); err != nil {
				continue
			}
		}
		size, _ := strconv.ParseFloat(f[2], 64)
		if i := int(t); i >= 0 && i < n {
			rate[i] += size * 8
		}
		if strings.HasPrefix(f[3], "K") {
			keys = append(keys, t)
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("analisi pacchetti: %w", err)
	}
	sort.Float64s(keys)
	return rate, keys, nil
}

// Luma media normalizzata (0 = nero, 1 = bianco nominale) di ogni keyframe, a 320 px.
// Il range limitato a 10 bit va da 64 a 940; in PQ i valori restano codificati, ma le soglie
// sono relative alla mediana del film, quindi funzionano per SDR e HDR.
func keyframeLuma(ctx context.Context, src string, info *MediaInfo, on func(ProgressUpdate)) ([]lumaSample, error) {
	dir, err := os.MkdirTemp("", "enc_segments")
	if err != nil {
		return nil, err
	}
	trackTmp(dir)
	defer releaseTmp(dir)
	logPath := filepath.Join(dir, "luma.txt")

	run := func(hw bool) error {
		args := []string{"-skip_frame", "nokey"}
		if hw {
			args = append(args, "-hwaccel", "videotoolbox") // ~6× più veloce sui keyframe 4K HEVC
		}
		args = append(args, "-i", src, "-map", fmt.Sprintf("0:%d", info.VideoIndex),
			"-an", "-sn", "-dn", "-fps_mode", "passthrough",
			"-vf", "scale=320:-2,format=yuv420p10le,signalstats,metadata=mode=print:key=lavfi.signalstats.YAVG:file="+logPath,
			"-f", "null", "-")
		ch := make(chan ProgressUpdate, 100)
		var err error
		go func() {
			defer close(ch)
			_, err = runFFmpegPiped(ctx, args, ch, "Analisi luminosità", info.Duration)
		}()
		for u := range ch {
			on(u)
		}
		return err
	}
	runErr := run(runtime.GOOS == "darwin")
	if runErr != nil && ctx.Err() == nil && runtime.GOOS == "darwin" {
		runErr = run(false) // decodifica hardware non disponibile per questo file
	}
	if runErr != nil {
		return nil, runErr
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}
	var out []lumaSample
	t := -1.0
	for _, l := range strings.Split(string(data), "\n") {
		if i := strings.Index(l, "pts_time:"); i >= 0 {
			if v, err := strconv.ParseFloat(strings.Fields(l[i+9:])[0], 64); err == nil {
				t = v
			}
			continue
		}
		if v, ok := strings.CutPrefix(l, "lavfi.signalstats.YAVG="); ok && t >= 0 {
			if y, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				out = append(out, lumaSample{T: t, Y: math.Max(0, (y-64)/876)})
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("analisi luminosità: nessun keyframe letto")
	}
	return out, nil
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[int(math.Min(float64(len(s)-1), p*float64(len(s))))]
}

func chooseSegments(duration float64, rate, keys []float64, samples []lumaSample) SegmentAnalysis {
	a := SegmentAnalysis{Version: segAnalysisVer}
	from := duration * segIntroSkip
	to := duration * (1 - segCreditsSkip)

	var lumas []float64
	for _, s := range samples {
		if s.T >= from && s.T <= to {
			lumas = append(lumas, s.Y)
		}
	}
	a.MedianLuma = percentile(lumas, 0.5)
	var rates []float64
	for i := int(from); i < int(to) && i < len(rate); i++ {
		rates = append(rates, rate[i])
	}
	target := math.Max(percentile(rates, segTargetPct), 1)

	type window struct {
		start, luma, bitrate float64
		black                bool
	}
	measure := func(s float64) window {
		w := window{start: s, luma: -1}
		var sum float64
		n := 0
		for _, x := range samples {
			if x.T >= s && x.T < s+SegmentSeconds {
				sum += x.Y
				n++
				if x.Y < segBlackLevel {
					w.black = true
				}
			}
		}
		if n > 0 {
			w.luma = sum / float64(n)
		} else {
			// nessun keyframe nella finestra: vale il più vicino
			best := math.Inf(1)
			for _, x := range samples {
				if d := math.Abs(x.T - (s + SegmentSeconds/2)); d < best {
					best, w.luma = d, x.Y
				}
			}
			w.black = w.luma < segBlackLevel
		}
		var bits float64
		for i := int(s); i < int(s+SegmentSeconds) && i < len(rate); i++ {
			bits += rate[i]
		}
		w.bitrate = bits / SegmentSeconds
		return w
	}

	zone := (to - from) / SegmentCount
	for z := 0; z < SegmentCount; z++ {
		zs, ze := from+float64(z)*zone, from+float64(z+1)*zone
		var cands []window
		for s := zs; s+SegmentSeconds <= ze; s++ {
			cands = append(cands, measure(s))
		}
		if len(cands) == 0 {
			continue
		}
		score := func(w window) float64 { return math.Abs(math.Log(math.Max(w.bitrate, 1) / target)) }
		pick := func(ok func(window) bool) (window, bool) {
			var best window
			found := false
			for _, w := range cands {
				if ok(w) && (!found || score(w) < score(best)) {
					best, found = w, true
				}
			}
			return best, found
		}
		note := ""
		w, ok := pick(func(w window) bool { return !w.black && w.luma >= a.MedianLuma*segDarkRatio })
		if !ok {
			// film o zona molto scuri: basta che non ci siano neri
			w, ok = pick(func(w window) bool { return !w.black })
			note = "Zona scura: scelta la scena migliore disponibile."
		}
		if !ok {
			// solo nero o dissolvenze: la finestra meno scura
			w = cands[0]
			for _, c := range cands {
				if c.luma > w.luma {
					w = c
				}
			}
			note = "Zona quasi tutta nera: risultato poco affidabile."
		}
		start := snapToKeyframe(w.start, keys)
		seg := BenchSegment{Start: start, Duration: SegmentSeconds, Luma: w.luma, Bitrate: int64(w.bitrate), Note: note}
		a.Segments = append(a.Segments, seg)
		if note != "" {
			a.Warnings = append(a.Warnings, fmt.Sprintf("Spezzone %d: %s", z+1, note))
		}
	}
	return a
}

// Keyframe più vicino a t senza superarlo (tagliando con -c copy l'inizio cade lì comunque)
func snapToKeyframe(t float64, keys []float64) float64 {
	i := sort.SearchFloat64s(keys, t+0.001)
	if i == 0 {
		return t
	}
	return keys[i-1]
}

// Miniature JPEG (tonemap per l'HDR, altrimenti i colori PQ sembrano slavati)
func addThumbs(ctx context.Context, src string, info *MediaInfo, cache string, segs []BenchSegment) {
	if cache == "" {
		return
	}
	for i := range segs {
		if p, err := makeThumb(ctx, src, info, cache, segs[i].Start+segs[i].Duration/2); err == nil {
			segs[i].Thumb = p
		}
	}
}

func makeThumb(ctx context.Context, src string, info *MediaInfo, dir string, at float64) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, fmt.Sprintf("thumb_%d.jpg", int(at*1000)))
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	vf := "scale=480:-2"
	if isHDR(info.MetaType) && Tools.HasZscale {
		tin := "smpte2084"
		if info.ColorTrc == "arib-std-b67" {
			tin = "arib-std-b67"
		}
		vf = fmt.Sprintf("scale=480:-2,zscale=tin=%s:min=2020_ncl:pin=2020:rin=tv:t=linear:npl=100,format=gbrpf32le,"+
			"zscale=p=709,tonemap=tonemap=%s:desat=0,zscale=t=709:m=709:r=tv,format=yuvj420p", tin, ToneMapAlgo)
	}
	cmd := exec.CommandContext(ctx, Tools.FFmpeg, "-hide_banner", "-nostdin", "-v", "error", "-y",
		"-ss", fmt.Sprintf("%.3f", at), "-i", src, "-map", fmt.Sprintf("0:%d", info.VideoIndex),
		"-frames:v", "1", "-vf", vf, "-q:v", "4", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("miniatura: %w\n%s", err, tail(msg, 5))
	}
	return out, nil
}

// "754.2,1450,2465.5" → spezzoni da SegmentSeconds (scelta manuale dall'app)
func parseSegmentStarts(list string, info *MediaInfo) ([]BenchSegment, error) {
	var segs []BenchSegment
	for _, f := range strings.Split(list, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		t, err := strconv.ParseFloat(f, 64)
		if err != nil || t < 0 {
			return nil, fmt.Errorf("inizio spezzone non valido: %q", f)
		}
		if info.Duration > 0 && t >= info.Duration {
			return nil, fmt.Errorf("inizio spezzone oltre la fine del file: %s", f)
		}
		d := math.Min(SegmentSeconds, info.Duration-t)
		segs = append(segs, BenchSegment{Start: t, Duration: d})
	}
	if len(segs) == 0 {
		return nil, errors.New("nessuno spezzone indicato")
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	return segs, nil
}

func formatTimestamp(t float64) string {
	s := int(t)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
