// Comandi dei preset per l'app:
//
//	encody preset validate [--spec f.json] < spec.json            → JSON: problemi, anteprima del comando
//	encody preset test [--sample file] [--spec f.json] < spec.json → NDJSON: step, preset_test
//
// Il preset arriva da stdin oppure da --spec (l'app lancia i comandi a eventi senza stdin).
//
// La prova codifica 3 s (da un file dell'utente o da un clip 4K HDR10 generato) e controlla che
// FFmpeg accetti tutti i parametri (x265 e SVT-AV1 ignorano in silenzio quelli sbagliati, quindi si
// leggono i loro avvisi), che codec, profondità e tag colore siano quelli attesi, che i metadati
// HDR10 statici arrivino all'output e che il file si decodifichi.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const presetTestSeconds = 3.0

func cmdPreset(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return fail(errors.New(T("uso: preset validate|test [--sample file] < preset.json")))
	}
	fs := flag.NewFlagSet("preset", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sample := fs.String("sample", "", "")
	specFile := fs.String("spec", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return fail(fmt.Errorf(T("argomenti non validi: %w"), err))
	}
	var in io.Reader = os.Stdin
	if *specFile != "" {
		f, err := os.Open(cleanPath(*specFile))
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		in = f
	}
	var spec PresetSpec
	if err := json.NewDecoder(in).Decode(&spec); err != nil {
		return fail(fmt.Errorf(T("preset JSON non valido: %w"), err))
	}
	if spec.Schema == 0 {
		spec.Schema = PresetSchema
	}
	switch args[0] {
	case "validate":
		issues := validateSpec(spec)
		if issues == nil {
			issues = []PresetIssue{} // sempre una lista: l'app non accetta null
		}
		out := map[string]any{"ok": !hasErrors(issues), "issues": issues}
		if !hasErrors(issues) {
			if p, err := compileSpec(spec); err == nil {
				out["command"] = previewCommand(p)
			}
		}
		printJSON(out)
		return 0
	case "test":
		return presetTest(ctx, spec, cleanPath(*sample))
	}
	return fail(fmt.Errorf(T("sottocomando sconosciuto: %s"), args[0]))
}

type testCheck struct {
	Level  string `json:"level"` // ok | warning | error
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// Avvisi con cui gli encoder segnalano parametri ignorati o rifiutati
var encoderComplaintRe = regexp.MustCompile(`(?i)(unknown option|invalid value|error parsing option|cannot open|unrecognized option|option not found|\[error\]|error while|invalid argument)`)

func presetTest(ctx context.Context, spec PresetSpec, sample string) int {
	if issues := validateSpec(spec); hasErrors(issues) {
		return failEvent(errors.New(T("il preset ha errori: ") + firstError(issues)))
	}
	p, err := compileSpec(spec)
	if err != nil {
		return failEvent(err)
	}
	dir, err := os.MkdirTemp("", "enc_presettest")
	if err != nil {
		return failEvent(err)
	}
	trackTmp(dir)
	defer releaseTmp(dir)

	// 1. sorgente di prova
	emit("step", map[string]any{"step": T("Preparazione del clip di prova")})
	var src string
	if sample != "" {
		info, err := probeFile(sample)
		if err != nil {
			return failEvent(fmt.Errorf(T("file di prova illeggibile: %w"), err))
		}
		ref, err := makeBenchRef(ctx, sample, info, dir, []BenchSegment{{Start: info.Duration / 2, Duration: presetTestSeconds}})
		if err != nil {
			return failEvent(err)
		}
		src = ref.File
	} else if src, err = makeHDRTestClip(ctx, dir); err != nil {
		return failEvent(err)
	}
	info, err := probeFile(src)
	if err != nil {
		return failEvent(fmt.Errorf(T("clip di prova illeggibile: %w"), err))
	}
	srcSide := frameSideData(src)

	// 2. codifica con il comando reale del motore, con gli avvisi visibili
	job := Job{InputPath: src, OutputPath: filepath.Join(dir, "out.mkv"), Preset: p, MetaType: info.MetaType,
		DVProfile: info.DVProfile, ColorPrim: info.ColorPrim, ColorTrc: info.ColorTrc, ColorSpace: info.ColorSpace,
		Duration: info.Duration, FPSStr: info.FPSStr, AudioMode: "copy"}
	if mustToneMap(job.MetaType, p) {
		job.ToneMap = true
	}
	emit("step", map[string]any{"step": T("Codifica di prova")})
	args := append([]string{"-hide_banner", "-nostdin", "-v", "warning", "-y", "-i", src}, encodeVideoArgs(job)...)
	args = append(args, job.OutputPath)
	start := time.Now()
	cmd := exec.CommandContext(ctx, Tools.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	elapsed := time.Since(start).Seconds()
	if ctx.Err() != nil {
		return 130
	}

	var checks []testCheck
	add := func(level, title, detail string) { checks = append(checks, testCheck{level, title, detail}) }

	var complaints []string
	for _, l := range strings.Split(stderr.String(), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && encoderComplaintRe.MatchString(l) {
			complaints = append(complaints, l)
		}
	}
	switch {
	case runErr != nil:
		add("error", "FFmpeg ha rifiutato il preset", strings.TrimSpace(tail(stderr.Bytes(), 8)))
	case len(complaints) > 0:
		add("error", "Parametri ignorati o rifiutati dall'encoder", strings.Join(complaints, "\n"))
	default:
		add("ok", T("Parametri accettati"), "")
	}

	result := map[string]any{"command": "ffmpeg " + strings.Join(args, " ")}
	if runErr == nil {
		out, err := probeFile(job.OutputPath)
		if err != nil {
			add("error", T("Output illeggibile"), err.Error())
		} else {
			checks = append(checks, checkOutput(job, info, out, srcSide)...)
			frames := info.Duration * fpsValue(info.FPSStr)
			if elapsed > 0 && frames > 0 {
				result["fps"] = frames / elapsed
			}
			if out.Duration > 0 {
				result["bitrate_kbps"] = float64(fileSize(job.OutputPath)) * 8 / out.Duration / 1000
			}
		}
		emit("step", map[string]any{"step": T("Verifica della decodifica")})
		dec := exec.CommandContext(ctx, Tools.FFmpeg, "-hide_banner", "-nostdin", "-v", "error", "-i", job.OutputPath, "-f", "null", "-")
		var decErr bytes.Buffer
		dec.Stderr = &decErr
		if err := dec.Run(); err != nil || strings.TrimSpace(decErr.String()) != "" {
			add("error", "Il file non si decodifica correttamente", strings.TrimSpace(tail(decErr.Bytes(), 5)))
		} else {
			add("ok", T("Decodifica senza errori"), "")
		}
	}

	ok := true
	for _, c := range checks {
		ok = ok && c.Level != "error"
	}
	result["passed"] = ok
	result["checks"] = checks
	result["sample"] = map[bool]string{true: filepath.Base(sample), false: "clip 4K HDR10 generato"}[sample != ""]
	emit("preset_test", result)
	return 0
}

// Clip 4K HDR10 di 3 s con metadati statici (mastering display e MaxCLL), per provare i preset
func makeHDRTestClip(ctx context.Context, dir string) (string, error) {
	out := filepath.Join(dir, "sample_hdr10.mkv")
	xp := "master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,50):max-cll=1000,400:" +
		"hdr10=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:repeat-headers=1:log-level=error"
	cmd := exec.CommandContext(ctx, Tools.FFmpeg, "-hide_banner", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=s=3840x2160:r=24000/1001:d=%g", presetTestSeconds),
		// i frame di testsrc nascono senza colore: i tag vanno impostati sui frame, non solo sull'encoder
		"-vf", "format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv",
		"-c:v", "libx265", "-preset", "ultrafast", "-crf", "12", "-x265-params", xp,
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf(T("clip di prova: %w\n%s"), err, tail(msg, 5))
	}
	return out, nil
}

type sideData struct{ Mastering, CLL bool }

// Metadati HDR10 statici del primo frame
func frameSideData(path string) sideData {
	out, err := exec.Command(Tools.FFprobe, "-v", "error", "-select_streams", "v:0", "-read_intervals", "%+#1",
		"-show_frames", "-show_entries", "frame=side_data_list", "-of", "json", path).Output()
	if err != nil {
		return sideData{}
	}
	s := string(out)
	return sideData{Mastering: strings.Contains(s, "Mastering display metadata"), CLL: strings.Contains(s, "Content light level metadata")}
}

func streamProps(path string) (codec, profile, pixFmt string) {
	out, err := exec.Command(Tools.FFprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,profile,pix_fmt", "-of", "json", path).Output()
	if err != nil {
		return
	}
	var r struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			Profile   string `json:"profile"`
			PixFmt    string `json:"pix_fmt"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &r) == nil && len(r.Streams) > 0 {
		return r.Streams[0].CodecName, r.Streams[0].Profile, r.Streams[0].PixFmt
	}
	return
}

func checkOutput(job Job, in, out *MediaInfo, srcSide sideData) []testCheck {
	var c []testCheck
	add := func(level, title, detail string) { c = append(c, testCheck{level, title, detail}) }
	p := job.Preset

	codec, profile, pixFmt := streamProps(job.OutputPath)
	if codec == p.Codec {
		add("ok", "Codec "+strings.ToUpper(codec), profile)
	} else {
		add("error", T("Codec inatteso"), fmt.Sprintf(T("atteso %s, ottenuto %s"), p.Codec, codec))
	}
	tenBit := strings.Contains(pixFmt, "10")
	wantTen := p.TenBit && !job.ToneMap
	switch {
	case wantTen && tenBit, !wantTen && !tenBit:
		add("ok", map[bool]string{true: T("10 bit"), false: T("8 bit")}[tenBit], pixFmt)
	case wantTen:
		add("error", T("Profondità a 8 bit"), T("l'HDR richiede 10 bit: ")+pixFmt)
	default:
		add("warning", T("Profondità inattesa"), pixFmt)
	}
	if p.Scale > 0 && out.Width != p.Scale {
		add("error", T("Risoluzione inattesa"), fmt.Sprintf(T("larghezza %d invece di %d"), out.Width, p.Scale))
	} else {
		add("ok", fmt.Sprintf(T("Risoluzione %d×%d"), out.Width, out.Height), "")
	}

	if isHDR(in.MetaType) {
		if job.ToneMap {
			if out.ColorTrc == "bt709" {
				add("ok", T("Convertito in SDR (BT.709)"), T("encoder a 8 bit: l'HDR non si può conservare"))
			} else {
				add("error", T("Conversione in SDR non riuscita"), T("trasferimento ")+out.ColorTrc)
			}
		} else {
			if out.ColorTrc == in.ColorTrc && out.ColorPrim == in.ColorPrim {
				add("ok", T("Tag colore HDR conservati"), in.ColorPrim+" · "+in.ColorTrc)
			} else {
				add("error", T("Tag colore HDR persi"), fmt.Sprintf(T("%s · %s invece di %s · %s"), out.ColorPrim, out.ColorTrc, in.ColorPrim, in.ColorTrc))
			}
			side := frameSideData(job.OutputPath)
			switch {
			case srcSide.Mastering && !side.Mastering:
				add("error", T("Metadati HDR10 persi"), T("mastering display assente nell'output"))
			case srcSide.CLL && !side.CLL:
				add("warning", T("MaxCLL/MaxFALL assenti"), T("alcuni TV regolano peggio la luminosità"))
			case srcSide.Mastering:
				add("ok", T("Metadati HDR10 conservati"), T("mastering display e MaxCLL"))
			}
		}
	}
	if !p.TenBit && p.Type != "copy" {
		add("warning", T("Solo SDR"), T("con sorgenti HDR il preset converte sempre in SDR"))
	}
	if p.Codec != "hevc" {
		add("warning", T("Niente Dolby Vision / HDR10+"), T("i metadati dinamici si reinseriscono solo in HEVC"))
	}
	return c
}
