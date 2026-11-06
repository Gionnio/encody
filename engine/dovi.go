// Dolby Vision: bande nere (L5) dopo crop e scala.
//
// L'RPU descrive l'area attiva del frame con gli offset L5 (in pixel della sorgente). Dopo un crop o
// uno scaling vanno ricalcolati, altrimenti il TV crede che le bande nere ci siano ancora o abbiano
// un'altra misura. dovi_tool esporta gli L5 come configurazione dell'editor (preset + intervalli di
// frame): il motore la trasforma e la riapplica. Il resto dell'RPU non dipende dalla risoluzione.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type l5Preset struct {
	ID     int `json:"id"`
	Left   int `json:"left"`
	Right  int `json:"right"`
	Top    int `json:"top"`
	Bottom int `json:"bottom"`
}

type l5Config struct {
	ActiveArea struct {
		Crop    bool           `json:"crop"`
		Presets []l5Preset     `json:"presets"`
		Edits   map[string]int `json:"edits"`
	} `json:"active_area"`
}

// Offset della sorgente → offset dell'output: tolto il ritaglio, poi scalati
func transformL5(p l5Preset, srcW, srcH, cx, cy, cw, ch int, f float64) l5Preset {
	scale := func(v int) int { return int(math.Round(float64(max(0, v)) * f)) }
	return l5Preset{
		ID:     p.ID,
		Left:   scale(p.Left - cx),
		Right:  scale(p.Right - (srcW - cx - cw)),
		Top:    scale(p.Top - cy),
		Bottom: scale(p.Bottom - (srcH - cy - ch)),
	}
}

func adjustActiveArea(ctx context.Context, job Job, rpu, dir string) (string, error) {
	var srcW, srcH int
	if _, err := fmt.Sscanf(job.Resolution, "%dx%d", &srcW, &srcH); err != nil || srcW <= 0 || srcH <= 0 {
		return "", fmt.Errorf(T("risoluzione della sorgente sconosciuta (%q): impossibile correggere le bande nere del Dolby Vision"), job.Resolution)
	}
	cx, cy, cw, ch := 0, 0, srcW, srcH
	if w, h, x, y, ok := parseCrop(job.Crop); ok {
		cx, cy, cw, ch = x, y, w, h
	}
	outW, _ := outputSize(job, srcW, srcH)
	f := float64(outW) / float64(cw)

	exported := filepath.Join(dir, "l5_src.json")
	if err := runTool(ctx, "dovi_tool", Tools.DoviTool, "export", "-i", rpu, "-d", "level5="+exported); err != nil {
		return "", err
	}
	data, err := os.ReadFile(exported)
	if err != nil {
		return "", err
	}
	var cfg l5Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf(T("L5 del Dolby Vision illeggibili: %w"), err)
	}
	if len(cfg.ActiveArea.Presets) == 0 || len(cfg.ActiveArea.Edits) == 0 {
		return rpu, nil // nessun L5: niente da correggere
	}
	for i, p := range cfg.ActiveArea.Presets {
		cfg.ActiveArea.Presets[i] = transformL5(p, srcW, srcH, cx, cy, cw, ch, f)
	}
	cfg.ActiveArea.Crop = false // i valori sono già quelli finali

	edit := filepath.Join(dir, "l5_out.json")
	out, _ := json.Marshal(cfg)
	if err := os.WriteFile(edit, out, 0o644); err != nil {
		return "", err
	}
	edited := filepath.Join(dir, "rpu_l5.bin")
	if err := runTool(ctx, "dovi_tool", Tools.DoviTool, "editor", "-i", rpu, "-j", edit, "-o", edited); err != nil {
		return "", err
	}
	return edited, nil
}

// Dolby Vision profilo 7: c'è davvero il livello di miglioramento (EL)?
//
// Un file può dichiarare il profilo 7 (dvcC e RPU) senza contenere l'EL: vecchie ricodifiche che hanno
// tenuto l'RPU e scartato il secondo strato. Lettori senza decoder P7 (Fire TV, TV Sony) non li
// riproducono, e non c'è niente da conservare: l'RPU va convertito in 8.1. Si guardano i primi secondi
// del flusso: l'RPU sta nelle unità NAL di tipo 62, l'EL nelle unità di tipo 63; un EL in una traccia
// video separata (doppia traccia) conta come presente.
type dvP7 struct {
	Checked bool   // analisi riuscita
	HasEL   bool   // livello di miglioramento presente
	ELType  string // "FEL", "MEL" o "" secondo l'RPU
}

func probeDVP7(path string) dvP7 {
	var r dvP7
	if info, err := probeFile(path); err == nil {
		videos := 0
		for _, s := range info.Streams {
			if s.CodecType == "video" && s.CodecName == "hevc" {
				videos++
			}
		}
		if videos > 1 {
			r.HasEL = true // EL in una traccia a parte
		}
	}
	dir, err := os.MkdirTemp("", "enc_dvel")
	if err != nil {
		return r
	}
	defer os.RemoveAll(dir)
	hevc := filepath.Join(dir, "head.hevc")
	if exec.Command(Tools.FFmpeg, "-hide_banner", "-nostdin", "-v", "error", "-y", "-i", path, "-map", "0:v:0", "-t", "2",
		"-c:v", "copy", "-bsf:v", "hevc_mp4toannexb", "-f", "hevc", hevc).Run() != nil {
		return r
	}
	data, err := os.ReadFile(hevc)
	if err != nil {
		return r
	}
	rpu, el := nalCounts(data)
	if rpu == 0 {
		return r // nessun RPU: non è Dolby Vision nel flusso, meglio non decidere
	}
	r.Checked = true
	r.HasEL = r.HasEL || el > 0
	r.ELType = dvELType(dir, hevc)
	return r
}

// Unità NAL HEVC di tipo 62 (RPU Dolby Vision) e 63 (livello di miglioramento) in un flusso Annex B
func nalCounts(b []byte) (rpu, el int) {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			switch (b[i+3] >> 1) & 0x3f {
			case 62:
				rpu++
			case 63:
				el++
			}
			i += 2
		}
	}
	return
}

// Tipo di EL secondo l'RPU ("FEL", "MEL" o "")
func dvELType(dir, hevc string) string {
	if Tools.DoviTool == "" {
		return ""
	}
	rpu := filepath.Join(dir, "head.rpu")
	if exec.Command(Tools.DoviTool, "extract-rpu", hevc, "-o", rpu).Run() != nil {
		return ""
	}
	out, err := exec.Command(Tools.DoviTool, "info", "-i", rpu, "--summary").Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "Profile:") {
			switch {
			case strings.Contains(l, "FEL"):
				return "FEL"
			case strings.Contains(l, "MEL"):
				return "MEL"
			}
		}
	}
	return ""
}

// Video del file → HEVC grezzo con l'RPU convertito in 8.1 (stesso bitstream, EL scartato)
func convertDVTo81(ctx context.Context, src, vmap, out string, ch chan<- ProgressUpdate, dur float64) error {
	raw := out + ".src.hevc"
	defer os.Remove(raw)
	args := []string{"-y", "-i", src}
	if TestModeActive {
		args = append(args, "-t", TestDuration)
	}
	args = append(args, "-map", vmap, "-c:v", "copy", "-bsf:v", "hevc_mp4toannexb", "-f", "hevc", raw)
	if _, err := runFFmpegPiped(ctx, args, ch, T("Conversione DV 8.1"), dur); err != nil {
		return err
	}
	return runTool(ctx, "dovi_tool", Tools.DoviTool, "-m", "2", "convert", "--discard", raw, "-o", out)
}
