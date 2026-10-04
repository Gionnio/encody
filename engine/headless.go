// ENCODY — modalità headless per Encody.app
//
// Comandi (flag PRIMA degli argomenti posizionali):
//   encody   caps                                   → JSON: versione, tool, preset
//   encody   probe <file>                           → JSON: info file, tracce, job precompilato
//   encody   crop <file>                            → JSON: {"crop": "crop=..."}
//   encody   plan < coda.json                       → JSON: per ogni job label video, piano audio, sub, vincoli
//   encody   run [--test] <coda.json>               → NDJSON di eventi (SIGINT = stop pulito)
//   encody   bench --presets 1,3 [--metric m] <file> → NDJSON di eventi
//   encody   quality --metric vmaf|ssim|xpsnr|cvvdp --ref A --dist B → NDJSON di eventi
//
// Per i comandi a risposta singola, in caso di errore: {"error": "..."} ed exit 1.
// Per i comandi a eventi, in caso di errore: evento {"type":"error","message":"..."} ed exit 1.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	evMu sync.Mutex
)

func newEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

func printJSON(v any) {
	evMu.Lock()
	defer evMu.Unlock()
	newEncoder(os.Stdout).Encode(v)
}

func emit(typ string, kv map[string]any) {
	if kv == nil {
		kv = map[string]any{}
	}
	kv["type"] = typ
	printJSON(kv)
}

func with(base map[string]any, kv ...any) map[string]any {
	m := make(map[string]any, len(base)+len(kv)/2)
	for k, v := range base {
		m[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func fail(err error) int {
	printJSON(map[string]any{"error": err.Error()})
	return 1
}

func failEvent(err error) int {
	emit("error", map[string]any{"message": err.Error()})
	return 1
}

// Callback che trasforma i ProgressUpdate in eventi (progress limitato a 5/s)
func eventProgress(extra map[string]any) func(ProgressUpdate) {
	step := ""
	var pct, fps float64
	var last time.Time
	return func(u ProgressUpdate) {
		if u.Info != "" {
			emit("info", with(extra, "message", u.Info))
			return
		}
		if u.Step != "" && u.Step != step {
			step, pct, fps = u.Step, 0, 0
			emit("step", with(extra, "step", step))
			last = time.Time{}
		}
		if u.Percent > 0 {
			pct = u.Percent
		}
		if u.FPS > 0 {
			fps = u.FPS
		}
		if time.Since(last) >= 200*time.Millisecond {
			last = time.Now()
			emit("progress", with(extra, "step", step, "percent", pct, "fps", fps))
		}
	}
}

// Ritorna (exit code, true) se args[0] è un comando headless
func headlessMain(args []string) (int, bool) {
	cmd := args[0]
	switch cmd {
	case "caps", "probe", "crop", "plan", "run", "bench", "quality", "segments", "thumb", "grain", "preset":
	case "help", "-h", "--help":
		fmt.Println("Comandi headless: caps | probe <file> | crop <file> | plan < coda.json | run [--test] <coda.json> | bench --presets 1,3 [--metric auto|vmaf|xpsnr|cvvdp] [--segments t1,t2,t3] <file> | segments <file> | thumb --at T <file> | grain <file> | preset validate|test [--sample file] < preset.json | quality --metric vmaf|ssim|xpsnr|cvvdp --ref A --dist B")
		return 0, true
	default:
		return 0, false
	}

	checkDeps(true)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer cleanupTmp()

	if cmd == "preset" {
		return cmdPreset(ctx, args[1:]), true // ha un sottocomando prima dei flag
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	test := fs.Bool("test", false, "")
	presets := fs.String("presets", "", "")
	metric := fs.String("metric", "", "") // vuoto = vmaf per quality, automatica per bench
	ref := fs.String("ref", "", "")
	dist := fs.String("dist", "", "")
	segList := fs.String("segments", "", "") // inizi in secondi, separati da virgola
	at := fs.Float64("at", -1, "")
	if err := fs.Parse(args[1:]); err != nil {
		return fail(fmt.Errorf("argomenti non validi: %w", err)), true
	}
	pos := fs.Args()
	needFile := func() (string, error) {
		if len(pos) < 1 {
			return "", errors.New("percorso file mancante")
		}
		return cleanPath(pos[0]), nil
	}

	switch cmd {
	case "caps":
		return cmdCaps(), true
	case "probe":
		f, err := needFile()
		if err != nil {
			return fail(err), true
		}
		return cmdProbe(f), true
	case "crop":
		f, err := needFile()
		if err != nil {
			return fail(err), true
		}
		return cmdCrop(f), true
	case "plan":
		return cmdPlan(os.Stdin), true
	case "run":
		f, err := needFile()
		if err != nil {
			return failEvent(err), true
		}
		TestModeActive = *test
		return cmdRun(ctx, f), true
	case "bench":
		f, err := needFile()
		if err != nil {
			return failEvent(err), true
		}
		return cmdBench(ctx, f, *presets, strings.ToLower(*metric), *segList), true
	case "segments":
		f, err := needFile()
		if err != nil {
			return failEvent(err), true
		}
		return cmdSegments(ctx, f), true
	case "grain":
		f, err := needFile()
		if err != nil {
			return fail(err), true
		}
		return cmdGrain(ctx, f), true
	case "thumb":
		f, err := needFile()
		if err != nil {
			return fail(err), true
		}
		return cmdThumb(ctx, f, *at), true
	case "quality":
		m := strings.ToLower(*metric)
		if m == "" {
			m = "vmaf"
		}
		return cmdQuality(ctx, cleanPath(*ref), cleanPath(*dist), m), true
	}
	return 0, false
}

// ---------- caps ----------

type presetOut struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Type             string     `json:"type"`
	Encoder          string     `json:"encoder"`
	Codec            string     `json:"codec"`
	Hdr              bool       `json:"hdr"`     // conserva l'HDR (10 bit)
	Dynamic          bool       `json:"dynamic"` // DV / HDR10+ reinseribili
	Builtin          bool       `json:"builtin"`
	Spec             PresetSpec `json:"spec"`
	Scale            int        `json:"scale"`
	AudioBitrate     string     `json:"audio_bitrate"`
	Passthrough      []string   `json:"passthrough"`
	PassthroughLabel string     `json:"passthrough_label"`
}

func cmdCaps() int {
	ps := []presetOut{}
	for _, k := range sortedPresetKeys(true) {
		p := Presets[k]
		e, _ := encoderCap(p.Encoder)
		ps = append(ps, presetOut{ID: p.ID, Name: p.Name, Description: p.Description, Type: p.Type, Encoder: p.Encoder,
			Codec: p.Codec, Hdr: p.TenBit, Dynamic: p.Type == "copy" || (e.Dynamic && p.Scale == 0), Builtin: p.Builtin,
			Spec: PresetSpecs[p.ID], Scale: p.Scale, AudioBitrate: p.AudioBitrate,
			Passthrough: p.Passthrough, PassthroughLabel: prettyList(p.Passthrough)})
	}
	printJSON(map[string]any{
		"version": AppName,
		"tools": map[string]string{
			"ffmpeg": Tools.FFmpeg, "ffprobe": Tools.FFprobe, "mkvmerge": Tools.MkvMerge,
			"dovi_tool": Tools.DoviTool, "hdr10plus_tool": Tools.Hdr10PlTool,
		},
		"has_zscale":     Tools.HasZscale,
		"has_vmaf":       Tools.HasVMAF,
		"encoders":       availableEncoders(),
		"ffmpeg_version": Tools.FFmpegVer,
		"preset_dir":     userPresetsDir(),
		"preset_errors":  PresetLoadErrors,
		"has_xpsnr":      Tools.HasXPSNR,
		"cvvdp":          Tools.CVVDP,
		"tonemap_algo":   ToneMapAlgo,
		"presets":        ps,
	})
	return 0
}

// ---------- probe ----------

type trackOut struct {
	Index      int      `json:"index"`
	Display    string   `json:"display"`
	Layout     string   `json:"layout"`
	BitRate    int64    `json:"bit_rate"`
	Frames     int64    `json:"frames"`
	ImageBased bool     `json:"image_based"`
	Suggested  bool     `json:"suggested"`
	Sel        TrackSel `json:"sel"`
}

type probeOut struct {
	Path     string     `json:"path"`
	Name     string     `json:"name"`
	Size     int64      `json:"size"`
	Duration float64    `json:"duration"`
	FPS      float64    `json:"fps"`
	Width    int        `json:"width"`
	Height   int        `json:"height"`
	MetaType string     `json:"meta_type"`
	DVProf   int        `json:"dv_profile"`
	Audio    []trackOut `json:"audio"`
	Subs     []trackOut `json:"subs"`
	Warnings []string   `json:"warnings"`
	Job      Job        `json:"job"` // job precompilato: la GUI imposta preset e scelte
}

func cmdProbe(path string) int {
	if !isVideo(path) {
		return fail(fmt.Errorf("estensione non supportata: %s", filepath.Ext(path)))
	}
	info, err := probeFile(path)
	if err != nil {
		return fail(fmt.Errorf("ffprobe: %w", err))
	}
	out := probeOut{
		Path: path, Name: filepath.Base(path), Size: fileSize(path), Duration: info.Duration, FPS: fpsValue(info.FPSStr),
		Width: info.Width, Height: info.Height, MetaType: info.MetaType, DVProf: info.DVProfile,
		Audio: []trackOut{}, Subs: []trackOut{}, Warnings: []string{},
	}

	anySuggested := false
	for _, s := range info.Streams {
		switch s.CodecType {
		case "audio":
			t := trackOut{Index: s.Index, Display: prettyCodec(s.CodecName, s.Profile), Layout: layoutOf(toSel(s)),
				BitRate: s.BitRate, Frames: s.Frames, Sel: toSel(s)}
			if s.Lang == "ita" || s.Lang == "it" {
				t.Suggested = true
				anySuggested = true
			}
			out.Audio = append(out.Audio, t)
		case "subtitle":
			t := trackOut{Index: s.Index, Display: prettyCodec(s.CodecName, ""), BitRate: s.BitRate, Frames: s.Frames,
				ImageBased: s.CodecName == "hdmv_pgs_subtitle" || s.CodecName == "dvd_subtitle", Sel: toSel(s)}
			t.Suggested = (s.Lang == "ita" || s.Lang == "it") && s.Forced
			out.Subs = append(out.Subs, t)
		}
	}
	if !anySuggested && len(out.Audio) > 0 {
		out.Audio[0].Suggested = true
	}
	if info.DVProfile == 5 {
		out.Warnings = append(out.Warnings, "Dolby Vision profilo 5: il base layer non è HDR10, un encode avrà colori sbagliati (verde/viola). Usa il Remux o salta il file.")
	}
	if len(out.Audio) == 0 {
		out.Warnings = append(out.Warnings, "Nessuna traccia audio nel file.")
	}

	out.Job = Job{
		InputPath: path, MetaType: info.MetaType, DVProfile: info.DVProfile,
		ColorPrim: info.ColorPrim, ColorTrc: info.ColorTrc, ColorSpace: info.ColorSpace,
		VideoMap:   fmt.Sprintf("0:%d", info.VideoIndex),
		Resolution: fmt.Sprintf("%dx%d", info.Width, info.Height),
		AudioMode:  "copy", SelAudio: []TrackSel{}, SelSubs: []TrackSel{},
		Duration: info.Duration, FPSStr: info.FPSStr,
	}
	printJSON(out)
	return 0
}

// ---------- crop ----------

func cmdCrop(path string) int {
	info, err := probeFile(path)
	if err != nil {
		return fail(fmt.Errorf("ffprobe: %w", err))
	}
	printJSON(map[string]any{"crop": detectCropQuiet(path, info.Duration)})
	return 0
}

// ---------- plan ----------

type planTrack struct {
	Index int    `json:"index"`
	Label string `json:"label"`
	Desc  string `json:"desc,omitempty"`
}

type planOut struct {
	VideoLabel    string      `json:"video_label"`
	Audio         []planTrack `json:"audio"`
	Subs          []planTrack `json:"subs"`
	CanToneMap    bool        `json:"can_tonemap"`
	MustToneMap   bool        `json:"must_tonemap"` // encoder a 8 bit su sorgente HDR: solo SDR
	CanInject     bool        `json:"can_inject"`
	InjectBlocker string      `json:"inject_blocker"`
	Warnings      []string    `json:"warnings"`
}

func cmdPlan(r io.Reader) int {
	var q []Job
	if err := json.NewDecoder(r).Decode(&q); err != nil {
		return fail(fmt.Errorf("JSON non valido: %w", err))
	}
	q = normalizeJobs(q)
	outs := make([]planOut, 0, len(q))
	for _, j := range q {
		p := planOut{VideoLabel: videoLabel(j), Audio: []planTrack{}, Subs: []planTrack{}, Warnings: []string{}}
		for _, a := range j.SelAudio {
			p.Audio = append(p.Audio, planTrack{Index: a.Index, Label: trackLabel(a), Desc: planAudio(j, a).Desc})
		}
		for _, s := range j.SelSubs {
			p.Subs = append(p.Subs, planTrack{Index: s.Index, Label: trackLabel(s), Desc: subPlanDesc(s)})
		}
		p.CanToneMap = isHDR(j.MetaType) && j.Preset.Type != "copy" && Tools.HasZscale
		p.MustToneMap = mustToneMap(j.MetaType, j.Preset)
		if len(j.Preset.VideoOpts) == 0 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("Preset %q non trovato: è stato eliminato o rinominato.", j.Preset.ID))
		}
		if p.MustToneMap && !Tools.HasZscale {
			p.Warnings = append(p.Warnings, "Encoder a 8 bit su sorgente HDR: serve zscale per convertire in SDR, ma FFmpeg non lo ha.")
		}
		if j.MetaType == "DV" || j.MetaType == "HDR10+" {
			p.InjectBlocker = injectBlocker(j.MetaType, j.DVProfile, j.Preset)
			// indipendente dalla scelta SDR: la GUI deve poter offrire tutte le opzioni insieme
			if j.Preset.Type == "copy" {
				p.InjectBlocker = "il remux mantiene già i metadati originali"
			}
			p.CanInject = p.InjectBlocker == ""
		} else {
			p.InjectBlocker = "la sorgente non ha metadati dinamici"
		}
		if len(j.SelAudio) == 0 {
			p.Warnings = append(p.Warnings, "Nessuna traccia audio selezionata: il file sarà muto.")
		}
		if j.DVProfile == 5 && j.Preset.Type != "copy" {
			p.Warnings = append(p.Warnings, "Dolby Vision profilo 5: i colori usciranno sbagliati.")
		}
		if j.DoInject && !j.ToneMap && !p.CanInject {
			p.Warnings = append(p.Warnings, "Inject impossibile: "+p.InjectBlocker+".")
		}
		if j.OutputPath != "" && j.OutputPath == j.InputPath {
			p.Warnings = append(p.Warnings, "L'output coincide con il file di origine.")
		}
		outs = append(outs, p)
	}
	printJSON(outs)
	return 0
}

// ---------- run ----------

func cmdRun(ctx context.Context, path string) int {
	jobs, warns, err := loadQueueQuiet(path)
	if err != nil {
		return failEvent(err)
	}
	for _, w := range warns {
		emit("info", map[string]any{"message": w})
	}

	emit("queue_start", map[string]any{"total": len(jobs), "test": TestModeActive})
	queueStart := time.Now()
	var totIn, totOut int64
	okCount, failCount, stopCount := 0, 0, 0

	for i, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		base := map[string]any{"job": i}
		emit("job_start", with(base, "name", filepath.Base(j.InputPath), "input", j.InputPath, "output", j.OutputPath))

		start := time.Now()
		r := runJob(ctx, j, eventProgress(base))
		elapsed := time.Since(start)
		done := with(base, "elapsed", elapsed.Seconds(), "output", j.OutputPath)

		switch {
		case ctx.Err() != nil:
			stopCount++
			emit("job_done", with(done, "status", "stop"))
		case r.Err != nil:
			failCount++
			emit("job_done", with(done, "status", "fail", "error", r.Err.Error()))
		case r.Done:
			okCount++
			rc := computeRecap(j, elapsed)
			totIn += rc.In
			totOut += rc.Out
			emit("job_done", with(done, "status", "ok", "in_bytes", rc.In, "out_bytes", rc.Out,
				"estimated", rc.Estimated, "speed", rc.Speed, "avg_fps", rc.AvgFPS))
		default:
			failCount++
			emit("job_done", with(done, "status", "fail", "error", "terminato senza conferma di completamento"))
		}
	}

	emit("queue_done", map[string]any{
		"ok": okCount, "fail": failCount, "stopped": stopCount, "total": len(jobs),
		"total_in": totIn, "total_out": totOut, "elapsed": time.Since(queueStart).Seconds(), "test": TestModeActive,
	})
	if ctx.Err() != nil {
		return 130
	}
	return 0
}

// ---------- bench ----------

func cmdBench(ctx context.Context, src, presetList, metric, segList string) int {
	info, err := probeFile(src)
	if err != nil {
		return failEvent(fmt.Errorf("ffprobe: %w", err))
	}
	// Spezzoni indicati dall'app (scelta manuale o analisi già fatta); altrimenti analisi qui
	var segs []BenchSegment
	if segList != "" {
		if segs, err = parseSegmentStarts(segList, info); err != nil {
			return failEvent(err)
		}
	}
	if metric == "" || metric == "auto" {
		metric = defaultMetric(info)
	}
	if metric == "ssim" {
		return failEvent(errors.New("il benchmark usa vmaf, xpsnr o cvvdp"))
	}
	if err := metricAvailable(metric); err != nil {
		return failEvent(fmt.Errorf("benchmark non disponibile: %w", err))
	}
	ids := []string{}
	for _, s := range strings.Split(presetList, ",") {
		if s = strings.TrimSpace(s); s != "" {
			if p, ok := Presets[s]; ok && p.Type != "copy" {
				ids = append(ids, s)
			}
		}
	}
	if len(ids) == 0 {
		return failEvent(errors.New("nessun preset valido selezionato"))
	}

	emit("bench_start", map[string]any{"name": filepath.Base(src), "meta_type": info.MetaType, "duration": info.Duration,
		"metric": metric, "warnings": benchWarnings(info, metric), "total": len(ids)})

	dir, err := os.MkdirTemp("", "enc_bench")
	if err != nil {
		return failEvent(err)
	}
	trackTmp(dir)
	defer releaseTmp(dir)

	if segs == nil {
		a, err := analyzeSegments(ctx, src, info, eventProgress(nil))
		if err != nil {
			if ctx.Err() != nil {
				return 130
			}
			emit("info", map[string]any{"message": "Analisi spezzoni non riuscita, si usa quello centrale: " + err.Error()})
		}
		segs = a.Segments
	}
	emit("step", map[string]any{"step": "Reference"})
	ref, err := makeBenchRef(ctx, src, info, dir, segs)
	if err != nil {
		if ctx.Err() != nil {
			return 130
		}
		return failEvent(err)
	}

	results := []BenchResult{}
	for n, id := range ids {
		if ctx.Err() != nil {
			break
		}
		p := Presets[id]
		base := map[string]any{"id": id}
		emit("bench_preset_start", with(base, "name", p.Name, "index", n, "total", len(ids)))
		res, err := benchOne(ctx, info, ref, dir, p, metric, eventProgress(base))
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			emit("bench_error", with(base, "name", p.Name, "error", err.Error()))
			continue
		}
		results = append(results, res)
		emit("bench_result", with(base, "result", res))
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	emit("bench_done", map[string]any{"results": results, "stopped": ctx.Err() != nil})
	if ctx.Err() != nil {
		return 130
	}
	return 0
}

// ---------- quality ----------

func cmdQuality(ctx context.Context, ref, dist, metric string) int {
	if ref == "" || dist == "" {
		return failEvent(errors.New("servono --ref e --dist"))
	}
	if err := metricAvailable(metric); err != nil {
		return failEvent(err)
	}
	q, err := measureQuality(ctx, ref, dist, metric, eventProgress(nil))
	if err != nil {
		if ctx.Err() != nil {
			return 130
		}
		return failEvent(err)
	}
	emit("quality_result", map[string]any{"metric": metric, "value": q.Value, "detail": q.Detail, "verdict": qualityVerdict(metric, q.Value)})
	return 0
}

// ---------- segments / thumb ----------

// NDJSON: step, progress, segments (con miniature). Risultato in cache per lo stesso file.
func cmdSegments(ctx context.Context, src string) int {
	info, err := probeFile(src)
	if err != nil {
		return failEvent(fmt.Errorf("ffprobe: %w", err))
	}
	a, err := analyzeSegments(ctx, src, info, eventProgress(nil))
	if err != nil {
		if ctx.Err() != nil {
			return 130
		}
		return failEvent(err)
	}
	emit("segments", map[string]any{"segments": a.Segments, "warnings": a.Warnings,
		"median_luma": a.MedianLuma, "segment_seconds": SegmentSeconds, "duration": info.Duration})
	return 0
}

// JSON: {"thumb": "/percorso.jpg"} per la scelta manuale di uno spezzone
func cmdThumb(ctx context.Context, src string, at float64) int {
	info, err := probeFile(src)
	if err != nil {
		return fail(fmt.Errorf("ffprobe: %w", err))
	}
	if at < 0 || at >= info.Duration {
		return fail(errors.New("--at fuori dalla durata del file"))
	}
	dir := segmentCachePath(src)
	if dir == "" {
		return fail(errors.New("cartella cache non disponibile"))
	}
	p, err := makeThumb(ctx, src, info, dir, math.Min(at+SegmentSeconds/2, info.Duration-0.5))
	if err != nil {
		return fail(err)
	}
	printJSON(map[string]any{"thumb": p})
	return 0
}

// JSON: {"index": 2.82, "level": "high", "points": 15}. Risultato in cache per lo stesso file.
func cmdGrain(ctx context.Context, src string) int {
	info, err := probeFile(src)
	if err != nil {
		return fail(fmt.Errorf("ffprobe: %w", err))
	}
	g, err := measureGrain(ctx, src, info)
	if err != nil {
		if ctx.Err() != nil {
			return 130
		}
		return fail(err)
	}
	printJSON(g)
	return 0
}

// Encoder dei preset presenti nel FFmpeg installato
func availableEncoders() []EncoderCap {
	out := []EncoderCap{}
	for _, e := range Encoders {
		if len(Tools.Encoders) == 0 || Tools.Encoders[e.ID] {
			out = append(out, e)
		}
	}
	return out
}
