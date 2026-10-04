// ENCODY (ex MediaEnc) — motore Go
//
// Encody 1.0 (prima versione con GUI; la storia precedente è di MediaEnc):
//  - Rinominato Encody; metriche XPSNR e ColorVideoVDP (HDR) per benchmark e quality check
//
// Changelog v15.0:
//  - Modalità headless per la GUI (caps, probe, crop, plan, run, bench, quality) con output JSON/NDJSON
//  - Quality check con progress e verdetto; SSIM medio da stats_file
//  - Refactor: runJob con callback, computeRecap, benchOne/makeBenchRef, measureQuality condivisi tra CLI e GUI
//
// Changelog v14.3:
//  - HDR→SDR vero: linearizzazione + tonemap (hable) + conversione BT.709 via zscale, scelta per file
//  - Audio: downmix automatico 7.1→5.1 per AC3/E-AC3, AC3 multicanale a 640k, piano audio mostrato per traccia
//  - Dynamic metadata: encode con -fps_mode passthrough (niente frame duplicati/scartati), verifica
//    conteggio frame video vs RPU/HDR10+ prima dell'inject, test mode su bitstream tagliato comune
//  - Info tracce estese (formato, layout, bitrate, titolo, default/forced, n. righe sub), riepilogo coda dettagliato
//  - Recap spazio a fine file e a fine coda (originale → nuovo, risparmio, tempo, velocità)
//  - Test mode attivabile dal menu
//
// Changelog v14.2:
//  - Pipe engine senza race, errori propagati, mkvmerge exit 1 = warning, HDR10+/HLG/DV profile detection,
//    blocco DV P5, tag colore dalla sorgente, stdin unico, Ctrl+C pulito, benchmark/VMAF corretti

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ==========================================
// 1. CONFIG & TYPES
// ==========================================

const (
	AppName      = "ENCODY v1.0"
	TestDuration = "300"
	TestSeconds  = 300.0
	BenchSeconds = 45.0
	ToneMapAlgo  = "hable"
)

var (
	TestModeActive = false
	Tools          ToolPaths
	C              = struct {
		Reset, Red, Green, Yellow, Blue, Purple, Cyan, White, Bold, Dim string
	}{
		"\033[0m", "\033[31m", "\033[32m", "\033[33m", "\033[34m", "\033[35m", "\033[36m", "\033[37m", "\033[1m", "\033[2m",
	}

	stdin = bufio.NewReader(os.Stdin)

	busy     atomic.Bool // true mentre un encode gestisce da sé la pulizia
	tmpMu    sync.Mutex
	tmpPaths = map[string]struct{}{}

	framesRe = regexp.MustCompile(`Frames:\s*(\d+)`)
)

type ToolPaths struct {
	FFmpeg      string
	FFprobe     string
	MkvMerge    string
	DoviTool    string
	Hdr10PlTool string
	HasZscale   bool
	HasVMAF     bool
	HasXPSNR    bool
	CVVDP       string // ColorVideoVDP (Python/PyTorch), facoltativo
	Encoders    map[string]bool
}

// Preset pronto per l'uso, compilato da una PresetSpec (presets.go)
type Preset struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Type         string   `json:"type"`    // copy | gpu | cpu
	Encoder      string   `json:"encoder"` // copy, libx265, hevc_videotoolbox…
	Codec        string   `json:"codec"`   // copy | hevc | h264 | av1
	TenBit       bool     `json:"ten_bit"` // conserva l'HDR
	Builtin      bool     `json:"builtin,omitempty"`
	VideoOpts    []string `json:"video_opts"`
	FilterFormat string   `json:"filter_format,omitempty"` // formato pixel da preparare nei filtri (VideoToolbox)
	AudioBitrate string   `json:"audio_bitrate"`           // AC3 stereo; il multicanale va sempre a 640k
	Passthrough  []string `json:"passthrough"`
	Scale        int      `json:"scale,omitempty"` // larghezza target, 0 = nessuno scaling
}

// Sorgente HDR con un encoder a 8 bit: l'unica uscita possibile è l'SDR
func mustToneMap(meta string, p Preset) bool {
	return isHDR(meta) && p.Type != "copy" && !p.TenBit
}

type Job struct {
	InputPath  string     `json:"input_path"`
	OutputPath string     `json:"output_path"`
	Preset     Preset     `json:"preset"`
	MetaType   string     `json:"meta_type"`
	DVProfile  int        `json:"dv_profile,omitempty"`
	ColorPrim  string     `json:"color_primaries,omitempty"`
	ColorTrc   string     `json:"color_transfer,omitempty"`
	ColorSpace string     `json:"color_space,omitempty"`
	VideoMap   string     `json:"video_map,omitempty"` // es. "0:0"; vuoto = "0:v:0"
	Resolution string     `json:"resolution,omitempty"`
	ToneMap    bool       `json:"tonemap,omitempty"` // HDR → SDR
	DoInject   bool       `json:"do_inject"`
	Crop       string     `json:"crop"`
	AudioMode  string     `json:"audio_mode"`
	SelAudio   []TrackSel `json:"sel_audio"`
	SelSubs    []TrackSel `json:"sel_subs"`
	Duration   float64    `json:"duration"`
	FPSStr     string     `json:"fps_str"`
}

type TrackSel struct {
	Index    int    `json:"index"`
	Lang     string `json:"lang"`
	Codec    string `json:"codec"`
	Profile  string `json:"profile,omitempty"`
	Channels int    `json:"channels"`
	Layout   string `json:"layout,omitempty"`
	Title    string `json:"title"`
	Forced   bool   `json:"forced,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

type ProgressUpdate struct {
	Percent float64
	FPS     float64
	Step    string
	Info    string // messaggio da stampare su riga propria
	Err     error
	Done    bool
}

// ==========================================
// 2. HELPERS
// ==========================================

func checkDeps(quiet bool) {
	look := func(n string) string { p, _ := exec.LookPath(n); return p }
	Tools.FFmpeg = look("ffmpeg")
	Tools.FFprobe = look("ffprobe")
	Tools.DoviTool = look("dovi_tool")
	Tools.Hdr10PlTool = look("hdr10plus_tool")

	if p := look("mkvmerge"); p != "" {
		Tools.MkvMerge = p
	} else if _, err := os.Stat("/Applications/MKVToolNix.app/Contents/MacOS/mkvmerge"); err == nil {
		Tools.MkvMerge = "/Applications/MKVToolNix.app/Contents/MacOS/mkvmerge"
	} else if _, err := os.Stat("/opt/homebrew/bin/mkvmerge"); err == nil {
		Tools.MkvMerge = "/opt/homebrew/bin/mkvmerge"
	}

	if Tools.FFmpeg == "" || Tools.FFprobe == "" {
		if quiet {
			msg := "ffmpeg o ffprobe non trovati nel PATH"
			printJSON(map[string]any{"error": msg, "type": "error", "message": msg}) // valido sia per comandi JSON che a eventi
			os.Exit(1)
		}
		fmt.Printf("%s❌ Errore Critico: Manca FFmpeg o FFprobe.%s\n", C.Red, C.Reset)
		os.Exit(1)
	}

	if out, err := exec.Command(Tools.FFmpeg, "-hide_banner", "-filters").Output(); err == nil {
		s := string(out)
		Tools.HasZscale = strings.Contains(s, " zscale ")
		Tools.HasVMAF = strings.Contains(s, " libvmaf ")
		Tools.HasXPSNR = strings.Contains(s, " xpsnr ")
	}
	if out, err := exec.Command(Tools.FFmpeg, "-hide_banner", "-encoders").Output(); err == nil {
		Tools.Encoders = map[string]bool{}
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Fields(l); len(f) >= 2 && strings.HasPrefix(f[0], "V") {
				Tools.Encoders[f[1]] = true
			}
		}
	}
	loadUserPresets()
	Tools.CVVDP = findCVVDP()

	if quiet {
		return
	}
	missing := []string{}
	if Tools.MkvMerge == "" {
		missing = append(missing, "mkvmerge")
	}
	if Tools.DoviTool == "" {
		missing = append(missing, "dovi_tool")
	}
	if Tools.Hdr10PlTool == "" {
		missing = append(missing, "hdr10plus_tool")
	}
	if !Tools.HasZscale {
		missing = append(missing, "zscale (tonemap HDR→SDR)")
	}
	if !Tools.HasVMAF {
		missing = append(missing, "libvmaf")
	}
	if !Tools.HasXPSNR {
		missing = append(missing, "xpsnr (FFmpeg 7.1+)")
	}
	if len(missing) > 0 {
		fmt.Printf("%sℹ️  Non disponibili: %s (le funzioni relative saranno disattivate)%s\n", C.Yellow, strings.Join(missing, ", "), C.Reset)
	}
}

func cleanPath(path string) string {
	p := strings.TrimSpace(path)
	p = strings.Trim(p, "'")
	p = strings.Trim(p, "\"")
	if runtime.GOOS != "windows" {
		p = strings.ReplaceAll(p, "\\", "")
	}
	return p
}

func drawBar(pct float64) string {
	total := 20
	fill := int((pct / 100) * float64(total))
	if fill > total {
		fill = total
	}
	if fill < 0 {
		fill = 0
	}
	return fmt.Sprintf("[%s%s]", strings.Repeat("█", fill), strings.Repeat("░", total-fill))
}

func tail(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func known(v string) string {
	if v == "" || v == "unknown" || v == "reserved" {
		return ""
	}
	return v
}

func isHDR(meta string) bool { return meta != "" && meta != "SDR" }

func isPQ(job Job) bool { return isHDR(job.MetaType) && job.ColorTrc != "arib-std-b67" }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Dimensioni in base 1000, come le mostra il Finder
func humanSize(b int64) string {
	f := float64(b)
	switch {
	case f >= 1e9:
		return fmt.Sprintf("%.2f GB", f/1e9)
	case f >= 1e6:
		return fmt.Sprintf("%.0f MB", f/1e6)
	default:
		return fmt.Sprintf("%.0f KB", f/1e3)
	}
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func secDur(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }

func fpsValue(s string) float64 {
	if n, d, ok := strings.Cut(s, "/"); ok {
		nf, _ := strconv.ParseFloat(n, 64)
		df, _ := strconv.ParseFloat(d, 64)
		if df > 0 {
			return nf / df
		}
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func fileSize(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.Size()
	}
	return 0
}

func homeShort(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// --- file temporanei tracciati, rimossi anche in caso di Ctrl+C ---

func trackTmp(p string) {
	tmpMu.Lock()
	tmpPaths[p] = struct{}{}
	tmpMu.Unlock()
}

func releaseTmp(p string) {
	tmpMu.Lock()
	delete(tmpPaths, p)
	tmpMu.Unlock()
	os.RemoveAll(p)
}

func cleanupTmp() {
	tmpMu.Lock()
	defer tmpMu.Unlock()
	for p := range tmpPaths {
		os.RemoveAll(p)
	}
	tmpPaths = map[string]struct{}{}
}

func exitClean(code int) {
	cleanupTmp()
	os.Exit(code)
}

func sortedPresetKeys(includeCopy bool) []string {
	keys := []string{}
	for k, p := range Presets {
		if !includeCopy && p.Type == "copy" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ==========================================
// 3. PROBING
// ==========================================

type MediaInfo struct {
	Duration   float64
	FPSStr     string
	Width      int
	Height     int
	MetaType   string // SDR | HDR10 | HDR10+ | HLG | DV
	DVProfile  int
	ColorPrim  string
	ColorTrc   string
	ColorSpace string
	VideoIndex int
	Streams    []FFStream
}

type FFStream struct {
	Index     int
	CodecType string
	CodecName string
	Profile   string
	Channels  int
	Layout    string
	BitRate   int64
	Frames    int64 // NUMBER_OF_FRAMES (per i sub = numero di righe)
	Lang      string
	Title     string
	Default   bool
	Forced    bool
}

func tagAny(tags map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := tags[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

func probeFile(path string) (*MediaInfo, error) {
	out, err := exec.Command(Tools.FFprobe, "-v", "quiet", "-print_format", "json", "-show_streams", "-show_entries", "format=duration", path).Output()
	if err != nil {
		return nil, err
	}

	var raw struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Index         int               `json:"index"`
			CodecType     string            `json:"codec_type"`
			CodecName     string            `json:"codec_name"`
			Profile       string            `json:"profile"`
			Width         int               `json:"width"`
			Height        int               `json:"height"`
			AvgFPS        string            `json:"avg_frame_rate"`
			Channels      int               `json:"channels"`
			ChannelLayout string            `json:"channel_layout"`
			BitRate       string            `json:"bit_rate"`
			ColorPrim     string            `json:"color_primaries"`
			ColorTrc      string            `json:"color_transfer"`
			ColorSpace    string            `json:"color_space"`
			SideData      []map[string]any  `json:"side_data_list"`
			Tags          map[string]string `json:"tags"`
			Disposition   map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}

	info := &MediaInfo{FPSStr: "24000/1001", MetaType: "SDR", VideoIndex: -1}
	if d, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil {
		info.Duration = d
	}

	for _, s := range raw.Streams {
		fs := FFStream{
			Index: s.Index, CodecType: s.CodecType, CodecName: s.CodecName, Profile: s.Profile,
			Channels: s.Channels, Layout: strings.TrimSuffix(strings.TrimSuffix(s.ChannelLayout, "(side)"), "(back)"),
			Lang: strings.ToLower(s.Tags["language"]), Title: s.Tags["title"],
			Default: s.Disposition["default"] == 1, Forced: s.Disposition["forced"] == 1,
		}
		if br, err := strconv.ParseInt(s.BitRate, 10, 64); err == nil {
			fs.BitRate = br
		} else if br, err := strconv.ParseInt(tagAny(s.Tags, "BPS", "BPS-eng"), 10, 64); err == nil {
			fs.BitRate = br
		}
		if n, err := strconv.ParseInt(tagAny(s.Tags, "NUMBER_OF_FRAMES", "NUMBER_OF_FRAMES-eng"), 10, 64); err == nil {
			fs.Frames = n
		}
		lt := strings.ToLower(fs.Title)
		if strings.Contains(lt, "forced") || strings.Contains(lt, "forzat") {
			fs.Forced = true
		}
		info.Streams = append(info.Streams, fs)

		// Solo il primo stream video vero: le cover (attached_pic) vengono ignorate
		if s.CodecType != "video" || info.VideoIndex >= 0 || s.Disposition["attached_pic"] == 1 {
			continue
		}
		info.VideoIndex = s.Index
		info.Width, info.Height = s.Width, s.Height
		if s.AvgFPS != "" && s.AvgFPS != "0/0" {
			info.FPSStr = s.AvgFPS
		}
		info.ColorPrim = strings.ToLower(s.ColorPrim)
		info.ColorTrc = strings.ToLower(s.ColorTrc)
		info.ColorSpace = strings.ToLower(s.ColorSpace)

		dv := false
		for _, sd := range s.SideData {
			t, _ := sd["side_data_type"].(string)
			if strings.Contains(t, "DOVI") {
				dv = true
				if p, ok := sd["dv_profile"].(float64); ok {
					info.DVProfile = int(p)
				}
			}
		}

		switch {
		case dv:
			info.MetaType = "DV"
		case info.ColorTrc == "smpte2084":
			info.MetaType = "HDR10"
			if hasHDR10Plus(path, s.Index) {
				info.MetaType = "HDR10+"
			}
		case info.ColorTrc == "arib-std-b67":
			info.MetaType = "HLG"
		case info.ColorPrim == "bt2020" && known(info.ColorTrc) == "":
			info.MetaType = "HDR10"
		}
	}
	if info.VideoIndex < 0 {
		info.VideoIndex = 0
	}
	return info, nil
}

// HDR10+ è un SEI per-frame: va letto dal primo frame, non dallo stream
func hasHDR10Plus(path string, streamIdx int) bool {
	out, err := exec.Command(Tools.FFprobe, "-v", "quiet", "-select_streams", strconv.Itoa(streamIdx),
		"-read_intervals", "%+#1", "-show_frames", "-print_format", "json", path).Output()
	if err != nil {
		return false
	}
	var r struct {
		Frames []struct {
			SideData []map[string]any `json:"side_data_list"`
		} `json:"frames"`
	}
	if json.Unmarshal(out, &r) != nil {
		return false
	}
	for _, f := range r.Frames {
		for _, sd := range f.SideData {
			t, _ := sd["side_data_type"].(string)
			if strings.Contains(t, "2094-40") || strings.Contains(t, "HDR10+") {
				return true
			}
		}
	}
	return false
}

func detectCrop(path string, duration float64) string {
	fmt.Printf("%s⏳ Analisi crop...%s ", C.Blue, C.Reset)
	c := detectCropQuiet(path, duration)
	fmt.Println(c)
	return c
}

// Crop = UNIONE dei rettangoli rilevati in 9 punti del film: una scena scura può suggerire un crop
// troppo stretto, ma non può "allargarne" un altro. Così non si taglia mai contenuto reale.
func detectCropQuiet(path string, duration float64) string {
	x1, y1, x2, y2 := 1<<30, 1<<30, 0, 0
	found := 0
	for _, f := range []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9} {
		cmd := exec.Command(Tools.FFmpeg, "-hide_banner", "-nostdin", "-y", "-ss", fmt.Sprintf("%f", duration*f),
			"-i", path, "-frames:v", "24", "-vf", "cropdetect=0.1:2:0", "-f", "null", "-")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		_ = cmd.Run()

		// cropdetect converge: vale l'ultima riga del campione
		last := ""
		for _, l := range strings.Split(stderr.String(), "\n") {
			if idx := strings.Index(l, "crop="); idx != -1 {
				if parts := strings.Fields(l[idx:]); len(parts) > 0 {
					last = parts[0]
				}
			}
		}
		var w, h, x, y int
		if _, err := fmt.Sscanf(strings.TrimPrefix(last, "crop="), "%d:%d:%d:%d", &w, &h, &x, &y); err != nil || w <= 0 || h <= 0 {
			continue // frame nero o campione illeggibile
		}
		found++
		x1, y1 = min(x1, x), min(y1, y)
		x2, y2 = max(x2, x+w), max(y2, y+h)
	}
	if found == 0 {
		return ""
	}
	return fmt.Sprintf("crop=%d:%d:%d:%d", x2-x1, y2-y1, x1, y1)
}

// ==========================================
// 4. DESCRIZIONI TRACCE & PIANO AUDIO
// ==========================================

var codecNames = map[string]string{
	"truehd": "TrueHD", "dts": "DTS", "eac3": "E-AC3", "ac3": "AC3", "aac": "AAC", "flac": "FLAC",
	"opus": "Opus", "mp3": "MP3", "vorbis": "Vorbis", "alac": "ALAC",
	"hdmv_pgs_subtitle": "PGS", "subrip": "SRT", "ass": "ASS", "ssa": "SSA", "dvd_subtitle": "VobSub",
	"mov_text": "mov_text", "webvtt": "WebVTT",
}

func prettyCodec(codec, profile string) string {
	n, ok := codecNames[codec]
	if !ok {
		if strings.HasPrefix(codec, "pcm_") {
			n = "PCM"
		} else {
			n = codec
		}
	}
	switch {
	case codec == "dts" && profile != "" && profile != "DTS":
		return profile // DTS-HD MA, DTS-HD HRA, DTS-ES...
	case strings.Contains(profile, "Atmos"):
		return n + " Atmos"
	}
	return n
}

func prettyList(codecs []string) string {
	out := make([]string, 0, len(codecs))
	for _, c := range codecs {
		out = append(out, prettyCodec(c, ""))
	}
	return strings.Join(out, "/")
}

func layoutOf(t TrackSel) string {
	if t.Layout != "" {
		return t.Layout
	}
	switch t.Channels {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	}
	return fmt.Sprintf("%dch", t.Channels)
}

func fmtBitrate(b int64) string {
	switch {
	case b <= 0:
		return ""
	case b >= 1e6:
		return fmt.Sprintf("%.1f Mb/s", float64(b)/1e6)
	default:
		return fmt.Sprintf("%d kb/s", b/1000)
	}
}

func isLossless(t TrackSel) bool {
	return t.Codec == "truehd" || t.Codec == "flac" || t.Codec == "alac" || strings.HasPrefix(t.Codec, "pcm_") ||
		(t.Codec == "dts" && strings.Contains(t.Profile, "MA"))
}

func toSel(s FFStream) TrackSel {
	return TrackSel{Index: s.Index, Lang: s.Lang, Codec: s.CodecName, Profile: s.Profile, Channels: s.Channels,
		Layout: s.Layout, Title: s.Title, Forced: s.Forced, Default: s.Default}
}

func trackLabel(t TrackSel) string {
	lang := t.Lang
	if lang == "" {
		lang = "und"
	}
	if t.Channels > 0 {
		return fmt.Sprintf("%s %s %s", lang, prettyCodec(t.Codec, t.Profile), layoutOf(t))
	}
	s := fmt.Sprintf("%s %s", lang, prettyCodec(t.Codec, t.Profile))
	if t.Forced {
		s += " forced"
	}
	return s
}

func flagsOf(def, forced bool) string {
	f := ""
	if def {
		f += " [default]"
	}
	if forced {
		f += " [forced]"
	}
	return f
}

type AudioPlan struct {
	Codec    string // "copy" o encoder
	Bitrate  string
	Channels int // 0 = invariati
	Desc     string
}

func multichPlan(codec, name, br string, t TrackSel) AudioPlan {
	p := AudioPlan{Codec: codec, Bitrate: br}
	if t.Channels > 6 {
		// AC3 ed E-AC3 in ffmpeg arrivano al massimo a 5.1
		p.Channels = 6
		p.Desc = fmt.Sprintf("%s 5.1 %s (downmix %s→5.1)", name, br, layoutOf(t))
	} else {
		p.Desc = fmt.Sprintf("%s %s %s", name, layoutOf(t), br)
	}
	return p
}

func withLossNotes(p AudioPlan, t TrackSel) AudioPlan {
	notes := []string{}
	if isLossless(t) {
		notes = append(notes, "lossless→lossy")
	}
	if strings.Contains(t.Profile, "Atmos") || strings.Contains(t.Profile, "DTS:X") {
		notes = append(notes, "oggetti Atmos/DTS:X persi")
	}
	if len(notes) > 0 {
		p.Desc += "  ⚠️ " + strings.Join(notes, ", ")
	}
	return p
}

func planAudio(job Job, t TrackSel) AudioPlan {
	switch job.AudioMode {
	case "aac":
		p := AudioPlan{Codec: "aac", Bitrate: "256k"}
		if t.Channels > 2 {
			p.Channels = 2
			p.Desc = fmt.Sprintf("AAC stereo 256k (downmix %s→2.0)", layoutOf(t))
		} else {
			p.Desc = fmt.Sprintf("AAC %s 256k", layoutOf(t))
		}
		return withLossNotes(p, t)
	case "eac3":
		if t.Codec == "ac3" || t.Codec == "eac3" {
			return AudioPlan{Codec: "copy", Desc: "copia (già Dolby Digital)"}
		}
		if t.Channels <= 2 {
			return AudioPlan{Codec: "copy", Desc: "copia (stereo)"}
		}
		return withLossNotes(multichPlan("eac3", "E-AC3", "640k", t), t)
	}
	if job.Preset.Type == "copy" || contains(job.Preset.Passthrough, t.Codec) {
		return AudioPlan{Codec: "copy", Desc: "copia (passthrough)"}
	}
	if t.Channels > 2 {
		return withLossNotes(multichPlan("ac3", "AC3", "640k", t), t)
	}
	br := job.Preset.AudioBitrate
	return withLossNotes(AudioPlan{Codec: "ac3", Bitrate: br, Desc: fmt.Sprintf("AC3 %s %s", layoutOf(t), br)}, t)
}

func buildAudioArgs(job Job) []string {
	args := []string{}
	for idx, t := range job.SelAudio {
		p := planAudio(job, t)
		args = append(args, "-map", fmt.Sprintf("0:%d", t.Index), fmt.Sprintf("-c:a:%d", idx), p.Codec)
		if p.Bitrate != "" {
			args = append(args, fmt.Sprintf("-b:a:%d", idx), p.Bitrate)
		}
		if p.Channels > 0 {
			args = append(args, fmt.Sprintf("-ac:a:%d", idx), strconv.Itoa(p.Channels))
		}
	}
	for n, t := range job.SelSubs {
		args = append(args, "-map", fmt.Sprintf("0:%d", t.Index), fmt.Sprintf("-c:s:%d", n), subCodec(t))
	}
	return args
}

// MKV non accetta mov_text (sottotitoli MP4): si convertono in SRT, il resto si copia
func subCodec(t TrackSel) string {
	if t.Codec == "mov_text" {
		return "srt"
	}
	return "copy"
}

func subPlanDesc(t TrackSel) string {
	if subCodec(t) == "srt" {
		return "convertito in SRT (MKV non supporta mov_text)"
	}
	return "copia"
}

func videoLabel(j Job) string {
	if j.Preset.Type == "copy" {
		return "copia (" + j.MetaType + ")"
	}
	parts := []string{}
	switch {
	case j.ToneMap:
		parts = append(parts, fmt.Sprintf("%s → SDR (tonemap %s)", j.MetaType, ToneMapAlgo))
	case isHDR(j.MetaType):
		parts = append(parts, j.MetaType+" mantenuto")
	default:
		parts = append(parts, "SDR")
	}
	if !j.ToneMap && (j.MetaType == "DV" || j.MetaType == "HDR10+") {
		if j.DoInject {
			parts = append(parts, "metadati dinamici iniettati")
		} else {
			parts = append(parts, "metadati dinamici scartati (fallback HDR10)")
		}
	}
	if j.Crop != "" {
		parts = append(parts, strings.Replace(j.Crop, "crop=", "crop ", 1))
	}
	if j.Preset.Scale > 0 {
		parts = append(parts, fmt.Sprintf("scala a %dpx", j.Preset.Scale))
	}
	return strings.Join(parts, " | ")
}

// ==========================================
// 5. ENGINE CORE
// ==========================================

func colorArgs(job Job) []string {
	args := []string{"-color_range", "tv"}
	switch {
	case job.ToneMap:
		return append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
	case isHDR(job.MetaType):
		trc := "smpte2084"
		if job.ColorTrc == "arib-std-b67" {
			trc = "arib-std-b67"
		}
		return append(args, "-color_primaries", "bt2020", "-color_trc", trc, "-colorspace", "bt2020nc")
	}
	if v := known(job.ColorPrim); v != "" {
		args = append(args, "-color_primaries", v)
	}
	if v := known(job.ColorTrc); v != "" {
		args = append(args, "-color_trc", v)
	}
	if v := known(job.ColorSpace); v != "" {
		args = append(args, "-colorspace", v)
	}
	return args
}

// HDR (PQ o HLG) → lineare → tonemap → BT.709. Parametri di ingresso espliciti per non dipendere dai tag.
func tonemapChain(job Job) string {
	tin := "smpte2084"
	if job.ColorTrc == "arib-std-b67" {
		tin = "arib-std-b67"
	}
	return fmt.Sprintf("zscale=tin=%s:min=2020_ncl:pin=2020:rin=tv:t=linear:npl=100,format=gbrpf32le,"+
		"zscale=p=709,tonemap=tonemap=%s:desat=0,zscale=t=709:m=709:r=tv,format=p010le", tin, ToneMapAlgo)
}

func videoArgs(job Job, dropFPSMode bool) []string {
	opts := job.Preset.VideoOpts
	out := make([]string, 0, len(opts)+2)
	for i := 0; i < len(opts); i++ {
		o := opts[i]
		switch {
		case dropFPSMode && o == "-fps_mode" && i+1 < len(opts):
			i++
		case o == "-x265-params" && i+1 < len(opts):
			v := opts[i+1]
			if isPQ(job) && !job.ToneMap && !strings.Contains(v, "hdr10") {
				v += ":hdr10-opt=1"
			}
			out = append(out, o, v)
			i++
		default:
			out = append(out, o)
		}
	}
	return out
}

func videoMap(job Job) string {
	if job.VideoMap != "" {
		return job.VideoMap
	}
	return "0:v:0"
}

// Pipe engine: stdout letto fino a EOF prima di Wait. Ritorna i frame prodotti (da -progress).
func runFFmpegPiped(ctx context.Context, args []string, ch chan<- ProgressUpdate, stepName string, duration float64) (int64, error) {
	allArgs := append([]string{"-hide_banner", "-nostdin", "-progress", "pipe:1", "-nostats", "-v", "error"}, args...)
	cmd := exec.CommandContext(ctx, Tools.FFmpeg, allArgs...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	var frames int64
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			k, v, ok := strings.Cut(scanner.Text(), "=")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch k {
			case "out_time_us":
				us, err := strconv.ParseFloat(v, 64)
				if err != nil {
					continue
				}
				pct := 0.0
				if duration > 0 {
					pct = (us / 1e6 / duration) * 100
				}
				if pct > 100 {
					pct = 100
				}
				ch <- ProgressUpdate{Percent: pct, Step: stepName}
			case "fps":
				if fps, err := strconv.ParseFloat(v, 64); err == nil {
					ch <- ProgressUpdate{FPS: fps, Step: stepName}
				}
			case "frame":
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					frames = n
				}
			}
		}
		io.Copy(io.Discard, stdout)
	}()

	<-scanDone
	err = cmd.Wait()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err != nil {
		if msg := tail(stderr.Bytes(), 15); msg != "" {
			return 0, fmt.Errorf("%s: %w\n%s", stepName, err, msg)
		}
		return 0, fmt.Errorf("%s: %w", stepName, err)
	}
	return frames, nil
}

func runTool(ctx context.Context, name, bin string, args ...string) error {
	if bin == "" {
		return fmt.Errorf("%s non trovato", name)
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, tail(out, 15))
	}
	return nil
}

// mkvmerge: exit 0 = ok, 1 = warning (file valido), 2 = errore
func runMkvmerge(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, Tools.MkvMerge, args...).CombinedOutput()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return tail(out, 5), nil
		}
		return "", fmt.Errorf("mkvmerge: %w\n%s", err, tail(out, 15))
	}
	return "", nil
}

// Numero di frame descritti dai metadati dinamici estratti
func metaFrameCount(ctx context.Context, meta, file string) (int64, error) {
	if meta == "DV" {
		out, err := exec.CommandContext(ctx, Tools.DoviTool, "info", "-i", file, "--summary").CombinedOutput()
		if err != nil {
			return 0, fmt.Errorf("dovi_tool info: %w", err)
		}
		m := framesRe.FindSubmatch(out)
		if m == nil {
			return 0, errors.New("conteggio frame non trovato nel summary")
		}
		return strconv.ParseInt(string(m[1]), 10, 64)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return 0, err
	}
	var j struct {
		SceneInfo []json.RawMessage `json:"SceneInfo"`
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return 0, err
	}
	if len(j.SceneInfo) == 0 {
		return 0, errors.New("SceneInfo vuoto")
	}
	return int64(len(j.SceneInfo)), nil
}

func injectBlocker(meta string, dvProfile int, p Preset) string {
	switch {
	case p.Type != "copy" && p.Codec != "hevc":
		return "l'encoder del preset non è HEVC: dovi_tool e hdr10plus_tool reinseriscono i metadati solo in HEVC"
	case p.Scale > 0:
		return "inject non supportato con i preset scalati"
	case meta == "DV" && dvProfile == 5:
		return "il profilo 5 non è convertibile in 8.1"
	case Tools.MkvMerge == "":
		return "mkvmerge non trovato"
	case meta == "DV" && Tools.DoviTool == "":
		return "dovi_tool non trovato"
	case meta == "HDR10+" && Tools.Hdr10PlTool == "":
		return "hdr10plus_tool non trovato"
	}
	return ""
}

func effectiveDuration(job Job) float64 {
	if TestModeActive && job.Duration > TestSeconds {
		return TestSeconds
	}
	return job.Duration
}

func runEncoder(ctx context.Context, job Job, ch chan<- ProgressUpdate) (err error) {
	defer func() {
		if err != nil {
			os.Remove(job.OutputPath)
		}
	}()
	if len(job.Preset.VideoOpts) == 0 {
		return fmt.Errorf("preset %q non trovato", job.Preset.ID)
	}
	if mustToneMap(job.MetaType, job.Preset) {
		if !Tools.HasZscale {
			return errors.New("encoder a 8 bit su sorgente HDR: serve zscale per convertire in SDR")
		}
		job.ToneMap = true
		job.DoInject = false
	}

	workDir, err := os.MkdirTemp("", "enc_job")
	if err != nil {
		return err
	}
	trackTmp(workDir)
	defer releaseTmp(workDir)

	dur := effectiveDuration(job)
	report := func(step string) { ch <- ProgressUpdate{Step: step} }
	info := func(msg string) { ch <- ProgressUpdate{Info: msg} }

	if job.ToneMap && !Tools.HasZscale {
		return errors.New("tonemap richiesto ma FFmpeg non ha zscale")
	}

	if !job.DoInject {
		// 1. FAST PATH
		report("Encoding")
		cmd := []string{"-y", "-i", job.InputPath}
		if TestModeActive {
			cmd = append(cmd, "-t", TestDuration)
		}

		cmd = append(cmd, encodeVideoArgs(job)...)
		cmd = append(cmd, buildAudioArgs(job)...)
		cmd = append(cmd, job.OutputPath)

		if _, err := runFFmpegPiped(ctx, cmd, ch, "Encoding", dur); err != nil {
			return err
		}
	} else {
		// 2. SAFE PATH (inject metadati dinamici)
		if why := injectBlocker(job.MetaType, job.DVProfile, job.Preset); why != "" {
			return fmt.Errorf("inject impossibile: %s", why)
		}

		// In test mode metadati ed encode leggono lo STESSO bitstream tagliato: frame allineati per costruzione
		src, srcMap := job.InputPath, videoMap(job)
		var srcIn []string
		if TestModeActive {
			report("Test Trim")
			trimmed := filepath.Join(workDir, "test_src.hevc")
			if _, err := runFFmpegPiped(ctx, []string{"-y", "-i", job.InputPath, "-t", TestDuration, "-map", videoMap(job),
				"-c:v", "copy", "-bsf:v", "hevc_mp4toannexb", "-f", "hevc", trimmed}, ch, "Test Trim", dur); err != nil {
				return err
			}
			src, srcMap = trimmed, "0:v:0"
			srcIn = []string{"-r", job.FPSStr} // l'HEVC raw non ha timestamp
		}

		report("Audio Extract")
		tmpAudio := filepath.Join(workDir, "audio.mkv")
		cmdAud := []string{"-y", "-i", job.InputPath, "-vn"}
		if TestModeActive {
			cmdAud = append(cmdAud, "-t", TestDuration)
		}
		cmdAud = append(cmdAud, buildAudioArgs(job)...)
		cmdAud = append(cmdAud, tmpAudio)
		if _, err := runFFmpegPiped(ctx, cmdAud, ch, "Audio Extract", dur); err != nil {
			return err
		}

		report("Meta Extract")
		var metaFile string
		if job.MetaType == "DV" {
			metaFile = filepath.Join(workDir, "rpu.bin")
			err = runTool(ctx, "dovi_tool", Tools.DoviTool, "-m", "2", "extract-rpu", src, "-o", metaFile)
		} else {
			metaFile = filepath.Join(workDir, "hdr10plus.json")
			err = runTool(ctx, "hdr10plus_tool", Tools.Hdr10PlTool, "extract", src, "-o", metaFile)
		}
		if err != nil {
			return err
		}

		report("Video Encode")
		rawVid := filepath.Join(workDir, "video.hevc")
		cmdVid := append([]string{"-y"}, srcIn...)
		cmdVid = append(cmdVid, "-i", src, "-map", srcMap)
		if job.Crop != "" {
			cmdVid = append(cmdVid, "-vf", job.Crop)
		}
		cmdVid = append(cmdVid, videoArgs(job, true)...)
		cmdVid = append(cmdVid, colorArgs(job)...)
		// passthrough: un frame in uscita per ogni frame decodificato, niente duplicati/scarti (niente più -r)
		cmdVid = append(cmdVid, "-fps_mode", "passthrough", "-bsf:v", "hevc_mp4toannexb", "-f", "hevc", rawVid)
		frames, err := runFFmpegPiped(ctx, cmdVid, ch, "Video Encode", dur)
		if err != nil {
			return err
		}

		report("Verifica Frame")
		if frames <= 0 {
			info("⚠️  Conteggio frame video non disponibile: verifica di allineamento saltata.")
		} else if n, err := metaFrameCount(ctx, job.MetaType, metaFile); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			info(fmt.Sprintf("⚠️  Impossibile contare i frame dei metadati (%v): verifica saltata.", err))
		} else if n != frames {
			return fmt.Errorf("frame non allineati: video %d, metadati %s %d. L'inject desincronizzerebbe i metadati", frames, job.MetaType, n)
		} else {
			info(fmt.Sprintf("✓ Frame allineati: %d video = %d metadati", frames, n))
		}

		report("Meta Inject")
		injVid := filepath.Join(workDir, "injected.hevc")
		if job.MetaType == "DV" {
			err = runTool(ctx, "dovi_tool", Tools.DoviTool, "inject-rpu", "-i", rawVid, "--rpu-in", metaFile, "-o", injVid)
		} else {
			err = runTool(ctx, "hdr10plus_tool", Tools.Hdr10PlTool, "inject", "-i", rawVid, "-j", metaFile, "-o", injVid)
		}
		if err != nil {
			return err
		}
		os.Remove(rawVid) // libera spazio prima del mux

		report("Muxing")
		warn, err := runMkvmerge(ctx, "--default-duration", "0:"+job.FPSStr+"p", "-o", job.OutputPath, injVid, tmpAudio)
		if err != nil {
			return err
		}
		if warn != "" {
			info("⚠️  mkvmerge ha emesso dei warning (file comunque valido):\n" + warn)
		}
	}

	ch <- ProgressUpdate{Percent: 100, Step: "Done", Done: true}
	return nil
}

type runResult struct {
	FPS  float64
	Err  error
	Done bool
}

// Esegue un job e passa ogni update (progress, step, info) alla callback. Usato da CLI e GUI.
// Filtri, mappa e argomenti video del percorso diretto (senza inject): usati dalla codifica,
// dall'anteprima dei preset e dalla loro prova
func encodeVideoArgs(job Job) []string {
	var cmd []string
	if job.Preset.Type != "copy" {
		vf := []string{}
		if job.Crop != "" {
			vf = append(vf, job.Crop)
		}
		if job.Preset.Scale > 0 {
			// Scaling prima del tonemap: la parte float a 32 bit lavora su meno pixel
			vf = append(vf, fmt.Sprintf("scale=%d:-2:flags=lanczos", job.Preset.Scale))
		}
		if job.ToneMap {
			vf = append(vf, tonemapChain(job)) // termina con format=p010le (gli encoder a 8 bit convertono da sé)
		} else if job.Preset.FilterFormat != "" {
			vf = append(vf, "format="+job.Preset.FilterFormat)
		} else if job.Preset.Scale > 0 && job.Preset.TenBit {
			vf = append(vf, "format=p010le")
		}
		if len(vf) > 0 {
			cmd = append(cmd, "-vf", strings.Join(vf, ","))
		}
	}
	cmd = append(cmd, "-map", videoMap(job))
	cmd = append(cmd, videoArgs(job, false)...)
	if job.Preset.Type != "copy" {
		cmd = append(cmd, colorArgs(job)...)
	}
	return cmd
}

func runJob(ctx context.Context, job Job, on func(ProgressUpdate)) runResult {
	ch := make(chan ProgressUpdate, 100)
	go func() {
		defer close(ch)
		if err := runEncoder(ctx, job, ch); err != nil {
			ch <- ProgressUpdate{Err: err}
		}
	}()
	var r runResult
	for u := range ch {
		switch {
		case u.Err != nil:
			r.Err = u.Err
		case u.Done:
			r.Done = true
		default:
			if u.FPS > 0 {
				r.FPS = u.FPS
			}
			on(u)
		}
	}
	return r
}

// Printer CLI: barra su una riga, info su righe proprie
func cliProgress() (func(ProgressUpdate), func()) {
	var pct, fps float64
	step := ""
	on := func(u ProgressUpdate) {
		if u.Info != "" {
			fmt.Printf("\r\033[K%s\n", u.Info)
			return
		}
		if u.Step != "" && u.Step != step {
			step, pct, fps = u.Step, 0, 0
		}
		if u.Percent > 0 {
			pct = u.Percent
		}
		if u.FPS > 0 {
			fps = u.FPS
		}
		fmt.Printf("\r\033[K%-14s %s %5.1f%% | FPS: %.1f", step, drawBar(pct), pct, fps)
	}
	return on, func() { fmt.Println() }
}

func encodeWithProgress(ctx context.Context, job Job) runResult {
	on, end := cliProgress()
	r := runJob(ctx, job, on)
	end()
	return r
}

// ==========================================
// 6. BENCHMARK & QUALITY
// ==========================================

// Metriche: vmaf (modello SDR), ssim, xpsnr (filtro FFmpeg 7.1+, ok anche su 10 bit/HDR),
// cvvdp (ColorVideoVDP, Python/PyTorch esterno: modello della visione con display HDR)

// Secondi analizzati da ColorVideoVDP (~1 frame/s in 4K su GPU Apple)
const CVVDPSeconds = 8.0

type QualityScore struct {
	Value  float64
	Detail string
}

func metricLabel(m string) string {
	switch m {
	case "cvvdp":
		return "ColorVideoVDP"
	default:
		return strings.ToUpper(m)
	}
}

func formatScore(m string, v float64) string {
	switch m {
	case "ssim":
		return fmt.Sprintf("%.4f", v)
	case "xpsnr":
		return fmt.Sprintf("%.2f dB", v)
	case "cvvdp":
		return fmt.Sprintf("%.2f JOD", v)
	default:
		return fmt.Sprintf("%.1f", v)
	}
}

// Errore se la metrica non è utilizzabile con gli strumenti trovati
func metricAvailable(m string) error {
	switch m {
	case "vmaf":
		if !Tools.HasVMAF {
			return errors.New("FFmpeg compilato senza libvmaf")
		}
	case "xpsnr":
		if !Tools.HasXPSNR {
			return errors.New("FFmpeg senza filtro xpsnr (serve FFmpeg 7.1 o successivo)")
		}
	case "cvvdp":
		if Tools.CVVDP == "" {
			return errors.New("ColorVideoVDP non installato (pip install cvvdp, oppure Impostazioni → Installa)")
		}
	case "ssim":
	default:
		return fmt.Errorf("metrica sconosciuta: %s", m)
	}
	return nil
}

// VMAF è un modello SDR: sull'HDR si usa XPSNR quando disponibile
func defaultMetric(info *MediaInfo) string {
	if info != nil && isHDR(info.MetaType) && Tools.HasXPSNR {
		return "xpsnr"
	}
	if Tools.HasVMAF {
		return "vmaf"
	}
	if Tools.HasXPSNR {
		return "xpsnr"
	}
	return "ssim"
}

func findCVVDP() string {
	if p := os.Getenv("ENCODY_CVVDP"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, "Library/Application Support/Encody/cvvdp/bin/cvvdp"), // installato dall'app
		filepath.Join(home, ".local/bin/cvvdp"),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	p, _ := exec.LookPath("cvvdp")
	return p
}

func vmafFilter(w, h int, logPath string) string {
	f := scaledPair(w, h) + "libvmaf=model=version=vmaf_v0.6.1:n_subsample=10"
	if logPath != "" {
		f += ":log_fmt=json:log_path=" + logPath
	}
	return f
}

// [0:v] = distorto (riportato alla risoluzione del reference), [1:v] = reference
func scaledPair(w, h int) string {
	dist := "[0:v]setpts=PTS-STARTPTS[d]"
	if w > 0 && h > 0 {
		dist = fmt.Sprintf("[0:v]scale=%d:%d:flags=bicubic,setpts=PTS-STARTPTS[d]", w, h)
	}
	return dist + ";[1:v]setpts=PTS-STARTPTS[r];[d][r]"
}

// Soglie (indistinguibile, ottimo, buono); l'app usa le stesse
func qualityVerdict(metric string, v float64) string {
	t := map[string][3]float64{
		"vmaf":  {95, 90, 80},
		"ssim":  {0.99, 0.97, 0.95},
		"xpsnr": {45, 40, 35},
		"cvvdp": {9.5, 9, 8},
	}[metric]
	switch {
	case v >= t[0]:
		return "praticamente indistinguibile"
	case v >= t[1]:
		return "ottimo"
	case v >= t[2]:
		return "buono"
	default:
		return "artefatti visibili"
	}
}

// Misura la qualità del distorto rispetto al reference, con progress
// cvvdpWindows (facoltative): tratti da confrontare con ColorVideoVDP; senza, gli 8 s centrali.
func measureQuality(ctx context.Context, ref, dist, metric string, on func(ProgressUpdate), cvvdpWindows ...[2]float64) (QualityScore, error) {
	if err := metricAvailable(metric); err != nil {
		return QualityScore{}, err
	}
	refInfo, err := probeFile(ref)
	if err != nil {
		return QualityScore{}, fmt.Errorf("reference illeggibile: %w", err)
	}
	distInfo, err := probeFile(dist)
	if err != nil {
		return QualityScore{}, fmt.Errorf("file encodato illeggibile: %w", err)
	}
	w, h := refInfo.Width, refInfo.Height

	dir, err := os.MkdirTemp("", "enc_quality") // path senza ':' o apici per il filtergraph
	if err != nil {
		return QualityScore{}, err
	}
	trackTmp(dir)
	defer releaseTmp(dir)

	if metric == "cvvdp" {
		return measureCVVDP(ctx, ref, dist, refInfo, distInfo, dir, cvvdpWindows, on)
	}

	logPath := filepath.Join(dir, "log")
	var fc string
	switch metric {
	case "ssim":
		fc = scaledPair(w, h) + "ssim=stats_file=" + logPath
	case "xpsnr":
		fc = scaledPair(w, h) + "xpsnr=stats_file=" + logPath
	default:
		fc = vmafFilter(w, h, logPath)
	}

	ch := make(chan ProgressUpdate, 100)
	var runErr error
	go func() {
		defer close(ch)
		_, runErr = runFFmpegPiped(ctx, []string{"-i", dist, "-i", ref, "-filter_complex", fc, "-f", "null", "-"}, ch, "Analisi "+metricLabel(metric), distInfo.Duration)
	}()
	for u := range ch {
		on(u)
	}
	if runErr != nil {
		return QualityScore{}, runErr
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		return QualityScore{}, err
	}
	switch metric {
	case "ssim":
		var sum float64
		n := 0
		for _, l := range strings.Split(string(data), "\n") {
			i := strings.Index(l, "All:")
			if i < 0 {
				continue
			}
			f := strings.Fields(l[i+4:])
			if len(f) == 0 {
				continue
			}
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				sum += v
				n++
			}
		}
		if n == 0 {
			return QualityScore{}, errors.New("nessun valore SSIM nel log")
		}
		return QualityScore{Value: sum / float64(n)}, nil
	case "xpsnr":
		return parseXPSNR(string(data))
	}
	var vLog struct {
		Pooled struct {
			Vmaf struct {
				Mean float64 `json:"mean"`
			} `json:"vmaf"`
		} `json:"pooled_metrics"`
	}
	if err := json.Unmarshal(data, &vLog); err != nil {
		return QualityScore{}, err
	}
	return QualityScore{Value: vLog.Pooled.Vmaf.Mean}, nil
}

var xpsnrAvgRe = regexp.MustCompile(`XPSNR average.*?y:\s*([\d.]+|inf)\s+u:\s*([\d.]+|inf)\s+v:\s*([\d.]+|inf)`)

// Ultima riga dello stats_file: "XPSNR average, N frames  y: 40.1  u: 43.2  v: 43.8".
// Il valore unico è la media pesata Y:U:V 6:1:1 (convenzione dei test JVET).
func parseXPSNR(log string) (QualityScore, error) {
	m := xpsnrAvgRe.FindStringSubmatch(log)
	if m == nil {
		return QualityScore{}, errors.New("nessun valore XPSNR nel log")
	}
	val := func(s string) float64 {
		if s == "inf" {
			return 100 // frame identici
		}
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	y, u, v := val(m[1]), val(m[2]), val(m[3])
	return QualityScore{
		Value:  (6*y + u + v) / 8,
		Detail: fmt.Sprintf("Y %.2f · U %.2f · V %.2f dB", y, u, v),
	}, nil
}

// Display di ColorVideoVDP adatto al tipo di sorgente
func cvvdpDisplay(info *MediaInfo) (string, string) {
	switch {
	case info.MetaType == "HLG":
		return "standard_hdr_hlg", "display HDR HLG"
	case isHDR(info.MetaType):
		return "standard_hdr_pq", "display HDR PQ 1500 nit"
	case info.Width >= 2560:
		return "standard_4k", "display SDR 4K"
	default:
		return "standard_fhd", "display SDR Full HD"
	}
}

var tqdmRe = regexp.MustCompile(`(\d+)/(\d+) \[`)

// ColorVideoVDP: estrae lo stesso spezzone dai due file (lossless, seek preciso al frame),
// porta il distorto alla risoluzione del reference e lo confronta su GPU (MPS) se disponibile.
func measureCVVDP(ctx context.Context, ref, dist string, refInfo, distInfo *MediaInfo, dir string, windows [][2]float64, on func(ProgressUpdate)) (QualityScore, error) {
	dur := math.Min(refInfo.Duration, distInfo.Duration)
	seconds := math.Min(CVVDPSeconds, dur)
	start := 0.0
	if dur > CVVDPSeconds*2 {
		start = dur/2 - CVVDPSeconds/2
	}
	// Più finestre (spezzoni del benchmark): si decodifica tutto e si tengono solo quei tratti,
	// con la stessa selezione per reference e distorto, quindi i frame restano allineati
	selectExpr := ""
	if len(windows) > 0 {
		var terms []string
		seconds = 0
		for _, w := range windows {
			terms = append(terms, fmt.Sprintf("between(t,%.3f,%.3f)", w[0], w[1]))
			seconds += w[1] - w[0]
		}
		fps := refInfo.FPSStr
		if fps == "" {
			fps = "24"
		}
		selectExpr = fmt.Sprintf(",select='%s',setpts=N/(%s)/TB", strings.Join(terms, "+"), fps)
	}
	extract := func(src string, info *MediaInfo, out, step string, scale bool) error {
		vf := "setpts=PTS-STARTPTS" + selectExpr
		if scale && (info.Width != refInfo.Width || info.Height != refInfo.Height) {
			vf = fmt.Sprintf("scale=%d:%d:flags=bicubic,", refInfo.Width, refInfo.Height) + vf
		}
		args := []string{"-y", "-ss", fmt.Sprintf("%.3f", start), "-i", src, "-t", fmt.Sprintf("%.3f", seconds)}
		if selectExpr != "" {
			args = []string{"-y", "-i", src, "-fps_mode", "passthrough"}
		}
		args = append(args, "-map", fmt.Sprintf("0:%d", info.VideoIndex), "-an", "-sn", "-dn", "-vf", vf,
			"-c:v", "ffv1", "-level", "3", "-slices", "16", "-pix_fmt", "yuv420p10le")
		// tag colore del reference su entrambi: cvvdp legge da lì la curva di trasferimento
		for k, v := range map[string]string{"-color_primaries": refInfo.ColorPrim, "-color_trc": refInfo.ColorTrc, "-colorspace": refInfo.ColorSpace} {
			if v != "" && v != "unknown" {
				args = append(args, k, v)
			}
		}
		ch := make(chan ProgressUpdate, 100)
		var err error
		go func() {
			defer close(ch)
			_, err = runFFmpegPiped(ctx, append(args, out), ch, step, seconds)
		}()
		for u := range ch {
			on(u)
		}
		return err
	}
	refClip := filepath.Join(dir, "ref.mkv")
	distClip := filepath.Join(dir, "dist.mkv")
	if err := extract(ref, refInfo, refClip, "Estrazione reference", false); err != nil {
		return QualityScore{}, err
	}
	if err := extract(dist, distInfo, distClip, "Estrazione encodato", true); err != nil {
		return QualityScore{}, err
	}

	display, displayLabel := cvvdpDisplay(refInfo)
	step := "Analisi ColorVideoVDP"
	on(ProgressUpdate{Step: step})
	args := []string{"--device", "mps", "-d", display, "-t", distClip, "-r", refClip}
	if runtime.GOOS != "darwin" {
		args = args[2:] // cvvdp sceglie da sé CUDA o CPU
	}
	cmd := exec.CommandContext(ctx, Tools.CVVDP, args...)
	cmd.Env = append(os.Environ(), "PYTORCH_ENABLE_MPS_FALLBACK=1")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return QualityScore{}, err
	}
	if err := cmd.Start(); err != nil {
		return QualityScore{}, err
	}
	// tqdm aggiorna la riga con \r: si spezza su \r e \n
	var errTail []string
	sc := bufio.NewScanner(stderr)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	startT := time.Now()
	for sc.Scan() {
		line := sc.Text()
		if m := tqdmRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.ParseFloat(m[1], 64)
			tot, _ := strconv.ParseFloat(m[2], 64)
			if tot > 0 {
				on(ProgressUpdate{Step: step, Percent: n / tot * 100, FPS: n / math.Max(time.Since(startT).Seconds(), 0.001)})
			}
			continue
		}
		if strings.TrimSpace(line) != "" {
			errTail = append(errTail, line)
			if len(errTail) > 15 {
				errTail = errTail[1:]
			}
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return QualityScore{}, ctx.Err()
	}
	if err != nil {
		return QualityScore{}, fmt.Errorf("ColorVideoVDP: %w\n%s", err, strings.Join(errTail, "\n"))
	}
	m := regexp.MustCompile(`cvvdp=([\d.]+)`).FindStringSubmatch(stdout.String())
	if m == nil {
		return QualityScore{}, fmt.Errorf("ColorVideoVDP: risultato non trovato\n%s", tail(stdout.Bytes(), 10))
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	what := fmt.Sprintf("%.0f s analizzati", seconds)
	if len(windows) > 1 {
		what = fmt.Sprintf("%.0f s analizzati in %d tratti", seconds, len(windows))
	}
	return QualityScore{Value: v, Detail: what + " · " + displayLabel}, nil
}

// Reference di benchmark: gli spezzoni scelti, solo video, stream copy, uniti in un unico file
type BenchRef struct {
	File     string
	Duration float64
	Windows  [][2]float64 // finestre per ColorVideoVDP, nel tempo della reference
}

func makeBenchRef(ctx context.Context, src string, info *MediaInfo, dir string, segs []BenchSegment) (BenchRef, error) {
	if len(segs) == 0 {
		segs = centerSegment(info)
	}
	refFile := filepath.Join(dir, "ref.mkv")
	ffmpeg := func(args ...string) error {
		out, err := exec.CommandContext(ctx, Tools.FFmpeg, append([]string{"-hide_banner", "-nostdin", "-v", "error", "-y"}, args...)...).CombinedOutput()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return fmt.Errorf("generazione reference: %w\n%s", err, tail(out, 10))
		}
		return nil
	}
	parts := []string{}
	durs := []float64{}
	for i, sg := range segs {
		part := refFile
		if len(segs) > 1 {
			part = filepath.Join(dir, fmt.Sprintf("seg%d.mkv", i))
		}
		if err := ffmpeg("-ss", fmt.Sprintf("%f", sg.Start), "-i", src, "-t", fmt.Sprintf("%f", sg.Duration),
			"-map", fmt.Sprintf("0:%d", info.VideoIndex), "-c", "copy", part); err != nil {
			return BenchRef{}, err
		}
		d := sg.Duration
		if pi, err := probeFile(part); err == nil && pi.Duration > 0 {
			d = pi.Duration // il taglio cade sui keyframe: durata reale
		}
		parts = append(parts, part)
		durs = append(durs, d)
	}
	if len(parts) > 1 {
		list := filepath.Join(dir, "concat.txt")
		var b strings.Builder
		for _, p := range parts {
			fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(p, "'", `'\''`))
		}
		if err := os.WriteFile(list, []byte(b.String()), 0o644); err != nil {
			return BenchRef{}, err
		}
		if err := ffmpeg("-f", "concat", "-safe", "0", "-i", list, "-map", "0", "-c", "copy", refFile); err != nil {
			return BenchRef{}, err
		}
		for _, p := range parts {
			os.Remove(p)
		}
	}
	ref := BenchRef{File: refFile}
	// ColorVideoVDP: CVVDPSeconds divisi tra gli spezzoni, presi al centro di ciascuno
	w := CVVDPSeconds / float64(len(durs))
	for _, d := range durs {
		c := ref.Duration + d/2
		ref.Windows = append(ref.Windows, [2]float64{math.Max(ref.Duration, c-w/2), math.Min(ref.Duration+d, c+w/2)})
		ref.Duration += d
	}
	return ref, nil
}

type BenchResult struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Metric string  `json:"metric"`
	Score  float64 `json:"score"`
	Detail string  `json:"detail,omitempty"`
	Size   int64   `json:"size"` // stima sul film intero (solo video)
	FPS    float64 `json:"fps"`
}

func benchOne(ctx context.Context, info *MediaInfo, ref BenchRef, dir string, p Preset, metric string, on func(ProgressUpdate)) (BenchResult, error) {
	refFile, benchDur := ref.File, ref.Duration
	outFile := filepath.Join(dir, "bench_"+p.ID+".mkv")
	defer os.Remove(outFile)
	job := Job{
		InputPath: refFile, OutputPath: outFile, Preset: p,
		MetaType: info.MetaType, DVProfile: info.DVProfile,
		ColorPrim: info.ColorPrim, ColorTrc: info.ColorTrc, ColorSpace: info.ColorSpace,
		Duration: benchDur, FPSStr: info.FPSStr, AudioMode: "copy",
	}
	r := runJob(ctx, job, on)
	if r.Err != nil {
		return BenchResult{}, r.Err
	}
	q, err := measureQuality(ctx, refFile, outFile, metric, on, ref.Windows...)
	if err != nil {
		return BenchResult{}, err
	}
	var est int64
	if benchDur > 0 {
		est = int64(float64(fileSize(outFile)) / benchDur * info.Duration)
	}
	return BenchResult{ID: p.ID, Name: p.Name, Metric: metric, Score: q.Value, Detail: q.Detail, Size: est, FPS: r.FPS}, nil
}

func modeBenchmark(ctx context.Context) {
	fmt.Printf("\n%s--- MODALITÀ BENCHMARK ---%s\n", C.Purple, C.Reset)
	if !Tools.HasVMAF && !Tools.HasXPSNR {
		fmt.Printf("%s❌ FFmpeg senza libvmaf né xpsnr: benchmark non disponibile.%s\n", C.Red, C.Reset)
		return
	}

	for {
		cleanP := cleanPath(prompt("Trascina File Video: "))
		info, err := probeFile(cleanP)
		if err != nil || info == nil {
			fmt.Printf("%s❌ Errore file!%s\n", C.Red, C.Reset)
			return
		}
		metric := defaultMetric(info)
		fmt.Printf("Metrica: %s%s%s\n", C.Bold, metricLabel(metric), C.Reset)
		for _, w := range benchWarnings(info, metric) {
			fmt.Printf("%s⚠️  %s%s\n", C.Yellow, w, C.Reset)
		}

		action, winner := benchFile(ctx, cleanP, info, metric)
		switch action {
		case "new":
			continue
		case "encode":
			win := Presets[winner]
			reviewQueue(ctx, buildJobs(cleanP, &win))
		}
		return
	}
}

func benchWarnings(info *MediaInfo, metric string) []string {
	w := []string{}
	if info.DVProfile == 5 {
		w = append(w, "Dolby Vision profilo 5: gli encode avranno colori sbagliati, il punteggio è poco significativo.")
	}
	if isHDR(info.MetaType) && metric == "vmaf" {
		w = append(w, fmt.Sprintf("Sorgente %s: vmaf_v0.6.1 è un modello SDR, i valori sono solo indicativi. Per l'HDR usa XPSNR o ColorVideoVDP.", info.MetaType))
	}
	if metric == "cvvdp" {
		w = append(w, fmt.Sprintf("ColorVideoVDP analizza %.0f s per preset ed è lento (qualche minuto per preset su un 4K).", CVVDPSeconds))
	}
	return w
}

func benchFile(ctx context.Context, src string, info *MediaInfo, metric string) (string, string) {
	benchDir, err := os.MkdirTemp("", "enc_bench")
	if err != nil {
		fmt.Printf("%s❌ %v%s\n", C.Red, err, C.Reset)
		return "", ""
	}
	trackTmp(benchDir)
	defer releaseTmp(benchDir)

	fmt.Printf("\n%sScelta degli spezzoni...%s\n", C.Blue, C.Reset)
	on, end := cliProgress()
	analysis, err := analyzeSegments(ctx, src, info, on)
	end()
	if err != nil {
		if ctx.Err() != nil {
			return "", ""
		}
		fmt.Printf("%s⚠️  %v: si usa lo spezzone centrale.%s\n", C.Yellow, err, C.Reset)
		analysis.Segments = centerSegment(info)
	}
	for _, sg := range analysis.Segments {
		fmt.Printf(" • %s (%.0f s, luminosità %.0f%%, %.1f Mb/s)\n", formatTimestamp(sg.Start), sg.Duration, sg.Luma*100, float64(sg.Bitrate)/1e6)
	}
	for _, w := range analysis.Warnings {
		fmt.Printf("%s⚠️  %s%s\n", C.Yellow, w, C.Reset)
	}

	fmt.Printf("\n%sGenerazione Reference...%s\n", C.Blue, C.Reset)
	ref, err := makeBenchRef(ctx, src, info, benchDir, analysis.Segments)
	if err != nil {
		if ctx.Err() == nil {
			fmt.Printf("%s❌ %v%s\n", C.Red, err, C.Reset)
		}
		return "", ""
	}

	for {
		keys := sortedPresetKeys(false)
		fmt.Println("\nPreset Disponibili:")
		for _, k := range keys {
			fmt.Printf(" [%s] %s\n", k, Presets[k].Name)
		}

		pids := []string{}
		for _, s := range strings.Split(prompt("\nSeleziona ID (virgola sep, es. 1,3): "), ",") {
			if s = strings.TrimSpace(s); s != "" {
				pids = append(pids, s)
			}
		}
		if len(pids) == 0 {
			return "", ""
		}

		results := []BenchResult{}
		func() {
			busy.Store(true)
			defer busy.Store(false)
			for _, pid := range pids {
				if ctx.Err() != nil {
					return
				}
				p, ok := Presets[pid]
				if !ok || p.Type == "copy" {
					fmt.Printf("Preset %q ignorato.\n", pid)
					continue
				}
				fmt.Printf("\nTesting: %s%s%s\n", C.Bold, p.Name, C.Reset)
				on, end := cliProgress()
				res, err := benchOne(ctx, info, ref, benchDir, p, metric, on)
				end()
				if err != nil {
					if ctx.Err() == nil {
						fmt.Printf("%s❌ Errore:%s %v\n", C.Red, C.Reset, err)
					}
					continue
				}
				results = append(results, res)
				fmt.Printf("%s %s\n", metricLabel(metric), formatScore(metric, res.Score))
			}
		}()

		if ctx.Err() != nil {
			return "", ""
		}
		if len(results) == 0 {
			fmt.Printf("%sNessun risultato valido.%s\n", C.Yellow, C.Reset)
			continue
		}

		sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
		fmt.Printf("\n%s--- RISULTATI (video, stima sul film intero) ---%s\n", C.Cyan, C.Reset)
		for _, r := range results {
			fmt.Printf("%s: %s | Size: %s | FPS: %.0f | %s\n", metricLabel(metric), formatScore(metric, r.Score), humanSize(r.Size), r.FPS, r.Name)
		}

		fmt.Println("\nAzioni:\n [1] Nuovo Bench (Altro File)\n [2] Stesso File (Altri Preset)\n [3] ENCODE con Vincente (Top 1)\n [q] Esci")
		switch prompt("> ") {
		case "1":
			return "new", ""
		case "2":
			continue
		case "3":
			return "encode", results[0].ID
		default:
			return "", ""
		}
	}
}

func runQualityCheck(ctx context.Context) {
	fmt.Printf("\n%s--- QUALITY CHECK ---%s\n", C.Purple, C.Reset)
	ref := cleanPath(prompt("File Originale (Reference): "))
	dist := cleanPath(prompt("File Encodato: "))
	fmt.Println("1. VMAF (SDR)")
	fmt.Println("2. SSIM")
	fmt.Println("3. XPSNR (anche HDR)")
	fmt.Println("4. ColorVideoVDP (HDR, lento)")
	metric := map[string]string{"1": "vmaf", "2": "ssim", "3": "xpsnr", "4": "cvvdp"}[prompt("> ")]
	if metric == "" {
		metric = "ssim"
	}
	on, end := cliProgress()
	q, err := measureQuality(ctx, ref, dist, metric, on)
	end()
	if err != nil {
		if ctx.Err() == nil {
			fmt.Printf("%s❌ %v%s\n", C.Red, err, C.Reset)
		}
		return
	}
	fmt.Printf("%s%s: %s%s  →  %s\n", C.Bold, metricLabel(metric), formatScore(metric, q.Value), C.Reset, qualityVerdict(metric, q.Value))
	if q.Detail != "" {
		fmt.Printf("%s%s%s\n", C.Dim, q.Detail, C.Reset)
	}
}

// ==========================================
// 7. UI
// ==========================================

func parseIndices(sel string) []int {
	out := []int{}
	for _, p := range strings.Split(sel, ",") {
		if idx, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, idx)
		}
	}
	return out
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ",")
}

func buildJobs(autoFile string, autoPreset *Preset) []Job {
	fmt.Printf("\n%s--- CONFIGURAZIONE ---%s\n", C.Green, C.Reset)

	var preset Preset
	if autoPreset != nil {
		preset = *autoPreset
		fmt.Printf("Preset: %s%s%s\n", C.Bold, preset.Name, C.Reset)
	} else {
		fmt.Println("Preset:")
		for _, k := range sortedPresetKeys(true) {
			fmt.Printf("[%s] %s\n", k, Presets[k].Name)
		}
		p, ok := Presets[prompt("ID: ")]
		if !ok {
			return nil
		}
		preset = p
	}

	pathClean := ""
	if autoFile != "" {
		pathClean = autoFile
		fmt.Printf("File: %s\n", filepath.Base(pathClean))
	} else {
		pathClean = cleanPath(prompt("Trascina File/Cartella: "))
	}

	files := []string{}
	if s, err := os.Stat(pathClean); err == nil && s.IsDir() {
		filepath.WalkDir(pathClean, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && isVideo(p) {
				files = append(files, p)
			}
			return nil
		})
	} else if isVideo(pathClean) {
		files = append(files, pathClean)
	}
	if len(files) == 0 {
		fmt.Println("Nessun file.")
		return nil
	}

	queue := []Job{}
	home, _ := os.UserHomeDir()
	outDir := filepath.Join(home, "Movies")

	for _, f := range files {
		info, err := probeFile(f)
		if err != nil {
			fmt.Printf("Skip %s\n", filepath.Base(f))
			continue
		}

		label := info.MetaType
		if info.MetaType == "DV" && info.DVProfile > 0 {
			label = fmt.Sprintf("DV P%d", info.DVProfile)
		}
		fmt.Printf("\n--> %s%s%s\n", C.Bold, filepath.Base(f), C.Reset)
		fmt.Printf("    %s | %s | %dx%d | %.3f fps | %s\n",
			humanSize(fileSize(f)), fmtDur(secDur(info.Duration)), info.Width, info.Height, fpsValue(info.FPSStr), label)

		if info.DVProfile == 5 && preset.Type != "copy" {
			fmt.Printf("%s⚠️  Dolby Vision profilo 5: il base layer non è HDR10, i colori usciranno sbagliati (verde/viola).%s\n", C.Red, C.Reset)
			if prompt("Saltare il file? [S/n]: ") != "n" {
				continue
			}
		}

		// --- HDR / SDR ---
		toneMap := false
		if isHDR(info.MetaType) && preset.Type != "copy" {
			if !Tools.HasZscale {
				fmt.Printf("%sℹ️  zscale non disponibile: l'output resta %s.%s\n", C.Yellow, info.MetaType, C.Reset)
			} else {
				def := "1"
				if preset.Scale > 0 {
					def = "2" // i preset ridotti sono pensati per compatibilità
				}
				fmt.Println("Gamma dinamica:")
				fmt.Printf(" [1] Mantieni %s  (serve una TV/player HDR)\n", info.MetaType)
				fmt.Printf(" [2] Converti in SDR  (tonemap %s → BT.709, guardabile ovunque; più lento, calcolo in float)\n", ToneMapAlgo)
				c := prompt(fmt.Sprintf("> (Invio=%s): ", def))
				if c == "" {
					c = def
				}
				toneMap = c == "2"
			}
		}

		// --- Metadati dinamici ---
		doInject := false
		if (info.MetaType == "DV" || info.MetaType == "HDR10+") && preset.Type != "copy" && !toneMap {
			if why := injectBlocker(info.MetaType, info.DVProfile, preset); why != "" {
				fmt.Printf("%sℹ️  Metadati dinamici ignorati: %s. L'output sarà HDR10 statico.%s\n", C.Yellow, why, C.Reset)
			} else {
				fmt.Println("Metadati dinamici:")
				fmt.Println(" [1] Ignora        HDR10 statico, veloce, crop possibile")
				fmt.Printf(" [2] Inject (Safe) mantiene %s; estrazione + verifica frame + mux (niente crop)\n", info.MetaType)
				if prompt("> ") == "2" {
					doInject = true
				}
			}
		}

		crop := ""
		if preset.Type != "copy" && !doInject {
			crop = detectCrop(f, info.Duration)
			if crop != "" {
				fmt.Printf("Crop: %s. Usare? [s/n] (Invio=Si): ", crop)
				if prompt("") == "n" {
					crop = ""
				}
			}
		}

		// --- Audio ---
		fmt.Println("Modalità audio:")
		if preset.Type == "copy" {
			fmt.Println(" [1] Pass        copia tutte le tracce così come sono")
		} else {
			fmt.Printf(" [1] Pass        copia %s; il resto → AC3 (multicanale 640k, stereo %s)\n", prettyList(preset.Passthrough), preset.AudioBitrate)
		}
		fmt.Println(" [2] EAC3 Smart  AC3/E-AC3 e stereo copiati; TrueHD/DTS/FLAC → E-AC3 640k (max 5.1)")
		fmt.Println(" [3] Stereo AAC  tutto AAC 256k stereo: massima compatibilità, qualità minore")
		audioMode := "copy"
		switch prompt("> ") {
		case "2":
			audioMode = "eac3"
		case "3":
			audioMode = "aac"
		}

		audioByIdx := map[int]FFStream{}
		suggested := []int{}
		fmt.Println("Tracce audio:")
		for _, s := range info.Streams {
			if s.CodecType != "audio" {
				continue
			}
			audioByIdx[s.Index] = s
			t := toSel(s)
			marker := ""
			if s.Lang == "ita" || s.Lang == "it" {
				marker = " ★"
				suggested = append(suggested, s.Index)
			}
			title := ""
			if s.Title != "" {
				title = fmt.Sprintf(" %q", s.Title)
			}
			br := ""
			if b := fmtBitrate(s.BitRate); b != "" {
				br = " ~" + b
			}
			fmt.Printf(" [%d] %-4s %-18s %-6s%s%s%s%s%s%s\n", s.Index, s.Lang, prettyCodec(s.CodecName, s.Profile), layoutOf(t),
				br, C.Dim, title, flagsOf(s.Default, s.Forced), C.Reset, marker)
		}
		if len(suggested) == 0 {
			for _, s := range info.Streams {
				if s.CodecType == "audio" {
					suggested = append(suggested, s.Index)
					break
				}
			}
		}
		sel := prompt(fmt.Sprintf("Indici (Invio=%s, a=tutte): ", joinInts(suggested)))
		var finalIdx []int
		switch sel {
		case "":
			finalIdx = suggested
		case "a":
			for _, s := range info.Streams {
				if s.CodecType == "audio" {
					finalIdx = append(finalIdx, s.Index)
				}
			}
		default:
			finalIdx = parseIndices(sel)
		}
		selAudio := []TrackSel{}
		for _, idx := range finalIdx {
			if s, ok := audioByIdx[idx]; ok {
				selAudio = append(selAudio, toSel(s))
			} else {
				fmt.Printf("%sIndice audio %d inesistente, ignorato.%s\n", C.Yellow, idx, C.Reset)
			}
		}

		tmp := Job{Preset: preset, AudioMode: audioMode}
		if len(selAudio) == 0 {
			fmt.Printf("%s⚠️  Nessuna traccia audio selezionata: il file sarà muto.%s\n", C.Yellow, C.Reset)
		} else {
			fmt.Println("Piano audio:")
			for _, t := range selAudio {
				fmt.Printf("  [%d] %s → %s\n", t.Index, trackLabel(t), planAudio(tmp, t).Desc)
			}
		}

		// --- Sottotitoli ---
		subByIdx := map[int]FFStream{}
		subSuggested := []int{}
		for _, s := range info.Streams {
			if s.CodecType != "subtitle" {
				continue
			}
			if len(subByIdx) == 0 {
				fmt.Println("Tracce sottotitoli:")
			}
			subByIdx[s.Index] = s
			marker := ""
			if (s.Lang == "ita" || s.Lang == "it") && s.Forced {
				marker = " ★"
				subSuggested = append(subSuggested, s.Index)
			}
			kind := "testo"
			if s.CodecName == "hdmv_pgs_subtitle" || s.CodecName == "dvd_subtitle" {
				kind = "immagine"
			}
			extra := kind
			if s.Frames > 0 {
				extra += fmt.Sprintf(", %d righe", s.Frames)
			}
			title := ""
			if s.Title != "" {
				title = fmt.Sprintf(" %q", s.Title)
			}
			fmt.Printf(" [%d] %-4s %-8s (%s)%s%s%s%s%s\n", s.Index, s.Lang, prettyCodec(s.CodecName, ""), extra,
				C.Dim, title, flagsOf(s.Default, s.Forced), C.Reset, marker)
		}
		selSubs := []TrackSel{}
		if len(subByIdx) > 0 {
			def := "Nessuno"
			if len(subSuggested) > 0 {
				def = joinInts(subSuggested)
			}
			selSubStr := prompt(fmt.Sprintf("Indici (Invio=%s, n=nessuno, a=tutti): ", def))
			var subIdx []int
			switch selSubStr {
			case "":
				subIdx = subSuggested
			case "n":
			case "a":
				for _, s := range info.Streams {
					if s.CodecType == "subtitle" {
						subIdx = append(subIdx, s.Index)
					}
				}
			default:
				subIdx = parseIndices(selSubStr)
			}
			for _, idx := range subIdx {
				if s, ok := subByIdx[idx]; ok {
					selSubs = append(selSubs, toSel(s))
				} else {
					fmt.Printf("%sIndice sub %d inesistente, ignorato.%s\n", C.Yellow, idx, C.Reset)
				}
			}
		}

		queue = append(queue, Job{
			InputPath:  f,
			OutputPath: filepath.Join(outDir, strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))+"_enc.mkv"),
			Preset:     preset, MetaType: info.MetaType, DVProfile: info.DVProfile,
			ColorPrim: info.ColorPrim, ColorTrc: info.ColorTrc, ColorSpace: info.ColorSpace,
			VideoMap:   fmt.Sprintf("0:%d", info.VideoIndex),
			Resolution: fmt.Sprintf("%dx%d", info.Width, info.Height),
			ToneMap:    toneMap, DoInject: doInject, Crop: crop, AudioMode: audioMode, SelAudio: selAudio, SelSubs: selSubs,
			Duration: info.Duration, FPSStr: info.FPSStr,
		})
	}
	return queue
}

func loadQueue(path string) ([]Job, error) {
	q, warns, err := loadQueueQuiet(path)
	for _, w := range warns {
		fmt.Printf("%sℹ️  %s%s\n", C.Yellow, w, C.Reset)
	}
	return q, err
}

func loadQueueQuiet(path string) ([]Job, []string, error) {
	data, err := os.ReadFile(cleanPath(path))
	if err != nil {
		return nil, nil, err
	}
	var q []Job
	if err := json.Unmarshal(data, &q); err != nil {
		return nil, nil, fmt.Errorf("JSON non valido: %w", err)
	}
	warns := jobWarningsLoad(q) // prima di normalizzare, che spegne il tonemap
	return normalizeJobs(q), warns, nil
}

// Preset riletti per ID dalla versione corrente; tonemap obbligatorio con gli encoder a 8 bit,
// disattivato se manca zscale
func normalizeJobs(q []Job) []Job {
	for i := range q {
		if p, ok := Presets[q[i].Preset.ID]; ok {
			q[i].Preset = p
		}
		if mustToneMap(q[i].MetaType, q[i].Preset) {
			q[i].ToneMap = true
		}
		if q[i].ToneMap && !Tools.HasZscale {
			q[i].ToneMap = false
		}
	}
	return q
}

func jobWarningsLoad(q []Job) []string {
	w := []string{}
	if !Tools.HasZscale {
		for _, j := range q {
			if j.ToneMap {
				w = append(w, filepath.Base(j.InputPath)+": tonemap disattivato (zscale assente), resta HDR.")
			}
		}
	}
	return w
}

func printJob(i int, j Job) {
	name := filepath.Base(j.InputPath)
	meta := []string{}
	if sz := fileSize(j.InputPath); sz > 0 {
		meta = append(meta, humanSize(sz))
	}
	if j.Duration > 0 {
		meta = append(meta, fmtDur(secDur(j.Duration)))
	}
	if j.Resolution != "" {
		meta = append(meta, j.Resolution)
	}
	fmt.Printf(" %s%d. %s%s  %s(%s)%s\n", C.Bold, i+1, name, C.Reset, C.Dim, strings.Join(meta, ", "), C.Reset)
	fmt.Printf("    Preset: %s\n", j.Preset.Name)
	fmt.Printf("    Video:  %s\n", videoLabel(j))
	if len(j.SelAudio) == 0 {
		fmt.Println("    Audio:  nessuna traccia")
	}
	for k, a := range j.SelAudio {
		prefix := "    Audio:  "
		if k > 0 {
			prefix = "            "
		}
		fmt.Printf("%s[%d] %s → %s\n", prefix, a.Index, trackLabel(a), planAudio(j, a).Desc)
	}
	if len(j.SelSubs) == 0 {
		fmt.Println("    Subs:   nessuno")
	} else {
		subs := []string{}
		for _, s := range j.SelSubs {
			subs = append(subs, fmt.Sprintf("[%d] %s", s.Index, trackLabel(s)))
		}
		fmt.Printf("    Subs:   %s\n", strings.Join(subs, ", "))
	}
	fmt.Printf("    Output: %s\n\n", homeShort(j.OutputPath))
}

func reviewQueue(ctx context.Context, jobs []Job) {
	if len(jobs) == 0 {
		return
	}
	for {
		fmt.Printf("\n%s--- RIEPILOGO CODA (%d) ---%s\n", C.Yellow, len(jobs), C.Reset)
		if TestModeActive {
			fmt.Printf("%s🧪 TEST MODE: verranno codificati solo i primi 5 minuti.%s\n", C.Purple, C.Reset)
		}
		for i, j := range jobs {
			printJob(i, j)
		}

		fmt.Println("[s] START  [a] APPEND FILE  [i] IMPORT  [e] EXPORT  [q] MENU")
		switch prompt("> ") {
		case "s":
			runQueue(ctx, jobs)
			return
		case "a":
			if newJobs := buildJobs("", nil); newJobs != nil {
				jobs = append(jobs, newJobs...)
			}
		case "e":
			p := prompt("Salva nome (es. coda): ")
			if !strings.HasSuffix(p, ".json") {
				p += ".json"
			}
			home, _ := os.UserHomeDir()
			fullPath := filepath.Join(home, "Desktop", p)
			b, _ := json.MarshalIndent(jobs, "", "  ")
			if err := os.WriteFile(fullPath, b, 0644); err != nil {
				fmt.Printf("%s❌ Errore salvataggio: %v%s\n", C.Red, err, C.Reset)
			} else {
				fmt.Printf("Salvato in: %s%s%s\n", C.Green, fullPath, C.Reset)
			}
		case "i":
			q, err := loadQueue(prompt("File JSON: "))
			if err != nil {
				fmt.Printf("%s❌ Errore load: %v%s\n", C.Red, err, C.Reset)
			} else {
				jobs = append(jobs, q...)
			}
		case "q":
			return
		}
	}
}

// ==========================================
// 8. RUN & RECAP SPAZIO
// ==========================================

type jobOutcome struct {
	Name    string
	Status  string // ok | fail | stop
	In, Out int64
	Elapsed time.Duration
}

func pctChange(in, out int64) float64 {
	if in <= 0 {
		return 0
	}
	return (float64(out) - float64(in)) / float64(in) * 100
}

func savingLine(in, out int64) string {
	diff := in - out
	pct := pctChange(in, out)
	if diff >= 0 {
		return fmt.Sprintf("%s%.1f%% (risparmiati %s)%s", C.Green, pct, humanSize(diff), C.Reset)
	}
	return fmt.Sprintf("%s+%.1f%% (aumentato di %s)%s", C.Yellow, pct, humanSize(-diff), C.Reset)
}

type fileRecap struct {
	In        int64         `json:"in_bytes"`
	Out       int64         `json:"out_bytes"`
	Estimated bool          `json:"estimated"`
	Elapsed   time.Duration `json:"-"`
	Speed     float64       `json:"speed"`   // x realtime
	AvgFPS    float64       `json:"avg_fps"` // frame/s medi
}

func computeRecap(j Job, elapsed time.Duration) fileRecap {
	r := fileRecap{In: fileSize(j.InputPath), Out: fileSize(j.OutputPath), Elapsed: elapsed}
	if TestModeActive && j.Duration > 0 {
		// confronto con la porzione equivalente dell'originale
		r.In = int64(float64(r.In) * effectiveDuration(j) / j.Duration)
		r.Estimated = true
	}
	if s := elapsed.Seconds(); s > 0 {
		r.Speed = effectiveDuration(j) / s
		r.AvgFPS = r.Speed * fpsValue(j.FPSStr)
	}
	return r
}

func printFileRecap(j Job, elapsed time.Duration) (int64, int64) {
	r := computeRecap(j, elapsed)
	note := ""
	if r.Estimated {
		note = C.Dim + " (test: stima sui primi 5 min dell'originale)" + C.Reset
	}
	fmt.Printf("   Originale: %s → Nuovo: %s  |  %s%s\n", humanSize(r.In), humanSize(r.Out), savingLine(r.In, r.Out), note)
	speed := ""
	if r.Speed > 0 {
		speed = fmt.Sprintf(" (%.2fx realtime", r.Speed)
		if r.AvgFPS > 0 {
			speed += fmt.Sprintf(", %.1f fps medi", r.AvgFPS)
		}
		speed += ")"
	}
	fmt.Printf("   Tempo: %s%s\n", fmtDur(elapsed), speed)
	return r.In, r.Out
}

func runQueue(ctx context.Context, jobs []Job) {
	busy.Store(true)
	defer busy.Store(false)

	fmt.Printf("\n%s--- AVVIO ---%s\n", C.Green, C.Reset)
	queueStart := time.Now()
	outcomes := []jobOutcome{}

	for i, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		fmt.Printf("\n%sJob %d/%d:%s %s\n", C.Bold, i+1, len(jobs), C.Reset, filepath.Base(j.InputPath))
		start := time.Now()
		r := encodeWithProgress(ctx, j)
		elapsed := time.Since(start)
		o := jobOutcome{Name: filepath.Base(j.InputPath), Elapsed: elapsed}

		switch {
		case ctx.Err() != nil:
			o.Status = "stop"
			fmt.Printf("%s⏹  Interrotto.%s\n", C.Yellow, C.Reset)
		case r.Err != nil:
			o.Status = "fail"
			fmt.Printf("%s❌ Errore:%s %v\n", C.Red, C.Reset, r.Err)
		case r.Done:
			o.Status = "ok"
			fmt.Printf("%s✅ Completato →%s %s\n", C.Green, C.Reset, homeShort(j.OutputPath))
			o.In, o.Out = printFileRecap(j, elapsed)
		default:
			o.Status = "fail"
			fmt.Printf("%s❌ Terminato senza conferma di completamento.%s\n", C.Red, C.Reset)
		}
		outcomes = append(outcomes, o)
	}

	printQueueRecap(outcomes, len(jobs), time.Since(queueStart))
}

func printQueueRecap(outcomes []jobOutcome, total int, elapsed time.Duration) {
	fmt.Printf("\n%s--- RECAP CODA ---%s\n", C.Cyan, C.Reset)
	var totIn, totOut int64
	okCount, failCount := 0, 0
	for _, o := range outcomes {
		switch o.Status {
		case "ok":
			okCount++
			totIn += o.In
			totOut += o.Out
			fmt.Printf(" ✅ %-40s %9s → %9s  %+6.1f%%  %s\n", truncate(o.Name, 40), humanSize(o.In), humanSize(o.Out), pctChange(o.In, o.Out), fmtDur(o.Elapsed))
		case "fail":
			failCount++
			fmt.Printf(" ❌ %-40s errore\n", truncate(o.Name, 40))
		case "stop":
			fmt.Printf(" ⏹  %-40s interrotto\n", truncate(o.Name, 40))
		}
	}
	if skipped := total - len(outcomes); skipped > 0 {
		fmt.Printf(" ⏭  %d job non avviati\n", skipped)
	}
	fmt.Printf("\n %d ok, %d falliti su %d  |  tempo totale %s\n", okCount, failCount, total, fmtDur(elapsed))
	if okCount > 0 {
		fmt.Printf(" %sTotale: %s → %s  |  %s%s\n", C.Bold, humanSize(totIn), humanSize(totOut), savingLine(totIn, totOut), C.Reset)
		if TestModeActive {
			fmt.Printf(" %s(test mode: originali stimati sulla porzione codificata)%s\n", C.Dim, C.Reset)
		}
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func main() {
	if len(os.Args) > 1 {
		if code, ok := headlessMain(os.Args[1:]); ok {
			os.Exit(code)
		}
	}

	fmt.Printf("%s%s%s\n", C.Bold, AppName, C.Reset)
	checkDeps(false)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
		fmt.Printf("\n%s⏹  Interruzione richiesta...%s\n", C.Yellow, C.Reset)
		if !busy.Load() {
			exitClean(130)
		}
	}()

	for {
		testState := "OFF"
		if TestModeActive {
			testState = C.Purple + "ON" + C.Reset
		}
		fmt.Println("\n1. Nuova Coda")
		fmt.Println("2. Benchmark")
		fmt.Println("3. Check Qualità")
		fmt.Println("4. Importa Coda")
		fmt.Printf("t. Test Mode (primi 5 min): %s\n", testState)
		fmt.Println("q. Esci")
		switch prompt("> ") {
		case "1":
			reviewQueue(ctx, buildJobs("", nil))
		case "2":
			modeBenchmark(ctx)
		case "3":
			runQualityCheck(ctx)
		case "4":
			q, err := loadQueue(prompt("JSON: "))
			if err != nil {
				fmt.Printf("%s❌ Errore load: %v%s\n", C.Red, err, C.Reset)
			} else {
				reviewQueue(ctx, q)
			}
		case "t":
			TestModeActive = !TestModeActive
		case "q":
			cleanupTmp()
			return
		}
		if ctx.Err() != nil {
			exitClean(130)
		}
	}
}

func prompt(label string) string {
	fmt.Print(label)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
		exitClean(0)
	}
	return strings.TrimSpace(line)
}

func isVideo(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".mkv", ".mp4", ".mov", ".avi", ".m2ts", ".ts", ".m4v":
		return true
	}
	return false
}
