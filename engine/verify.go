// Verifica del file finito.
//
// Dopo ogni codifica il motore rilegge l'output prima di dichiararla riuscita. Gli errori indicano un
// file rotto (illeggibile, troncato, che non si decodifica): il lavoro fallisce e il file parziale
// viene rimosso come per ogni altro errore. Gli avvisi indicano un file valido a cui manca qualcosa
// (metadati HDR, Dolby Vision, tracce): il lavoro riesce e l'app li mostra accanto al risultato.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
)

// Secondi decodificati all'inizio e alla fine dell'output
const verifyDecodeSeconds = 3.0

func verifyOutput(ctx context.Context, job Job, expectDur float64) ([]testCheck, error) {
	var c []testCheck
	add := func(level, title, detail string) { c = append(c, testCheck{level, title, detail}) }

	out, err := probeFile(job.OutputPath)
	if err != nil || out.Duration <= 0 {
		return nil, errors.New(T("il file prodotto non è leggibile"))
	}
	src, err := probeFile(job.InputPath)
	if err != nil {
		return nil, fmt.Errorf(T("sorgente illeggibile per la verifica: %w"), err)
	}

	// 1. durata: un file troncato è l'errore più comune di un encode interrotto male
	if expectDur > 0 {
		tol := math.Max(2, expectDur*0.01)
		if diff := math.Abs(out.Duration - expectDur); diff > tol {
			return nil, fmt.Errorf(T("durata %s invece di %s: il file sembra incompleto"),
				fmtDur(secDur(out.Duration)), fmtDur(secDur(expectDur)))
		}
		add("ok", fmt.Sprintf(T("Durata %s"), fmtDur(secDur(out.Duration))), "")
	}

	// 2. decodifica dell'inizio e della fine: la durata nell'intestazione può essere giusta anche
	// in un file troncato, i frame alla fine no
	for _, at := range []float64{0, math.Max(0, out.Duration-verifyDecodeSeconds-1)} {
		frames, msg, err := decodeWindow(ctx, job.OutputPath, at)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			return nil, fmt.Errorf(T("il file non si decodifica a %s: %s"), fmtDur(secDur(at)), msg)
		case strings.Contains(msg, "ended prematurely") || strings.Contains(msg, "Truncat"):
			return nil, errors.New(T("il file termina prima del previsto: sembra incompleto"))
		case frames == 0:
			return nil, fmt.Errorf(T("nessun frame decodificato a %s: il file sembra incompleto"), fmtDur(secDur(at)))
		case msg != "":
			add("warning", fmt.Sprintf(T("Avvisi del decoder a %s"), fmtDur(secDur(at))), msg)
		}
	}

	// 3. codec e risoluzione
	codec, _, pixFmt := streamProps(job.OutputPath)
	wantCodec := job.Preset.Codec
	if job.Preset.Type == "copy" && src.VideoIndex < len(src.Streams) {
		wantCodec = src.Streams[src.VideoIndex].CodecName
	}
	if codec != wantCodec {
		add("warning", T("Codec inatteso"), fmt.Sprintf(T("atteso %s, ottenuto %s"), wantCodec, codec))
	}
	if w, h := outputSize(job, src.Width, src.Height); w > 0 && (out.Width != w || out.Height != h) {
		add("warning", T("Risoluzione inattesa"), fmt.Sprintf("%d×%d ≠ %d×%d", out.Width, out.Height, w, h))
	} else {
		add("ok", fmt.Sprintf("%s %d×%d", strings.ToUpper(codec), out.Width, out.Height), pixFmt)
	}

	// 4. HDR: tag colore, metadati statici, metadati dinamici
	if isHDR(src.MetaType) {
		switch {
		case job.ToneMap:
			if out.ColorTrc == "bt709" {
				add("ok", T("Convertito in SDR (BT.709)"), "")
			} else {
				add("warning", T("Conversione in SDR non riuscita"), T("trasferimento ")+out.ColorTrc)
			}
		case out.ColorTrc != src.ColorTrc || out.ColorPrim != src.ColorPrim:
			add("warning", T("Tag colore HDR persi"), fmt.Sprintf(T("%s · %s invece di %s · %s"), out.ColorPrim, out.ColorTrc, src.ColorPrim, src.ColorTrc))
		default:
			srcSide, outSide := frameSideData(job.InputPath), frameSideData(job.OutputPath)
			switch {
			case srcSide.Mastering && !outSide.Mastering:
				add("warning", T("Metadati HDR10 persi"), T("mastering display assente nell'output"))
			case srcSide.CLL && !outSide.CLL:
				add("warning", T("MaxCLL/MaxFALL assenti"), T("alcuni TV regolano peggio la luminosità"))
			default:
				add("ok", T("HDR conservato"), out.ColorPrim+" · "+out.ColorTrc)
			}
		}
		if (src.MetaType == "DV" || src.MetaType == "HDR10+") && !job.ToneMap && (job.DoInject || job.Preset.Type == "copy") {
			switch {
			case out.MetaType != src.MetaType:
				add("warning", fmt.Sprintf(T("%s perso"), src.MetaType), fmt.Sprintf(T("l'output risulta %s"), out.MetaType))
			case src.MetaType == "DV":
				add("ok", fmt.Sprintf(T("Dolby Vision profilo %d"), out.DVProfile), "")
			default:
				add("ok", T("HDR10+ conservato"), "")
			}
		}
	}

	// 5. tracce audio e sottotitoli
	var na, ns int
	for _, s := range out.Streams {
		switch s.CodecType {
		case "audio":
			na++
		case "subtitle":
			ns++
		}
	}
	if na != len(job.SelAudio) || ns != len(job.SelSubs) {
		add("warning", T("Tracce diverse da quelle scelte"), fmt.Sprintf(T("audio %d su %d, sottotitoli %d su %d"), na, len(job.SelAudio), ns, len(job.SelSubs)))
	} else {
		add("ok", fmt.Sprintf(T("Tracce: %d audio, %d sottotitoli"), na, ns), "")
	}
	return c, nil
}

// Dimensioni dell'output: sorgente, ritagliata, poi scalata in larghezza
func outputSize(job Job, w, h int) (int, int) {
	if job.Preset.Type == "copy" {
		return w, h
	}
	if cw, ch, _, _, ok := parseCrop(job.Crop); ok {
		w, h = cw, ch
	}
	if job.Preset.Scale > 0 && w > 0 {
		// scale=W:-2: altezza proporzionale arrotondata al pari
		h = int(math.Round(float64(h)*float64(job.Preset.Scale)/float64(w)/2)) * 2
		w = job.Preset.Scale
	}
	return w, h
}

// "crop=w:h:x:y"
func parseCrop(crop string) (w, h, x, y int, ok bool) {
	if crop == "" {
		return
	}
	_, err := fmt.Sscanf(strings.TrimPrefix(crop, "crop="), "%d:%d:%d:%d", &w, &h, &x, &y)
	return w, h, x, y, err == nil && w > 0 && h > 0
}

// Decodifica pochi secondi da "at": frame decodificati, avvisi del decoder, errore se FFmpeg fallisce
func decodeWindow(ctx context.Context, path string, at float64) (int64, string, error) {
	cmd := exec.CommandContext(ctx, Tools.FFmpeg, "-hide_banner", "-nostdin", "-v", "error", "-progress", "pipe:1", "-nostats",
		"-ss", fmt.Sprintf("%.3f", at), "-i", path, "-t", fmt.Sprintf("%g", verifyDecodeSeconds),
		"-map", "0:v:0", "-f", "null", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var frames int64
	for _, l := range strings.Split(stdout.String(), "\n") {
		if v, ok := strings.CutPrefix(l, "frame="); ok {
			fmt.Sscanf(v, "%d", &frames)
		}
	}
	msg := strings.TrimSpace(tail(stderr.Bytes(), 3))
	if err != nil && msg == "" {
		msg = err.Error()
	}
	return frames, msg, err
}
