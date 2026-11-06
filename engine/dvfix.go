// dvfix: Dolby Vision profilo 7 senza livello di miglioramento nella libreria.
//
// Cerca nei percorsi indicati i file MKV che dichiarano il profilo 7 ma non contengono l'EL (vedi
// probeDVP7) e, con --apply, li corregge senza ricodificare: RPU convertito in 8.1 con dovi_tool,
// poi mkvmerge con tutte le altre tracce dell'originale (audio, sottotitoli, capitoli, allegati).
// Il file nuovo viene verificato; solo allora l'originale va in una cartella nascosta
// ".encody-originali" nella radice della ricerca (stesso percorso relativo) e il nuovo prende il suo
// posto. Le cartelle nascoste vengono saltate, così gli originali non compaiono nei media server.
//
// Eventi NDJSON: dvfix_found {path, size, duration}, dvfix_scan {found, scanned}, poi con --apply
// dvfix_start {job, path}, step/progress/info con job, dvfix_done {job, path, status, error, original,
// checks}, dvfix_end {ok, fail, stopped}.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const dvfixOriginals = ".encody-originali"

type dvfixTarget struct {
	Path string
	Root string // radice della ricerca: lì va la cartella degli originali
	Info *MediaInfo
}

func cmdDVFix(ctx context.Context, paths []string, apply bool) int {
	if Tools.DoviTool == "" || Tools.MkvMerge == "" {
		return failEvent(errors.New(T("per convertire il Dolby Vision profilo 7 in 8.1 servono dovi_tool e mkvmerge")))
	}
	emit("step", map[string]any{"step": T("Ricerca dei file")})
	var targets []dvfixTarget
	scanned := 0
	for _, p := range paths {
		p = cleanPath(p)
		st, err := os.Stat(p)
		if err != nil {
			emit("info", map[string]any{"message": fmt.Sprintf(T("⚠️  %s non trovato"), p)})
			continue
		}
		root := p
		if !st.IsDir() {
			root = filepath.Dir(p)
		}
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != p && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || !strings.EqualFold(filepath.Ext(path), ".mkv") {
				return nil
			}
			scanned++
			info, err := probeFile(path)
			if err != nil || info.MetaType != "DV" || info.DVProfile != 7 {
				return nil
			}
			if p7 := probeDVP7(path); !p7.Checked || p7.HasEL {
				return nil
			}
			targets = append(targets, dvfixTarget{Path: path, Root: root, Info: info})
			emit("dvfix_found", map[string]any{"path": path, "size": fileSize(path), "duration": info.Duration})
			return nil
		})
	}
	emit("dvfix_scan", map[string]any{"found": len(targets), "scanned": scanned})
	if ctx.Err() != nil {
		return 130
	}
	if !apply {
		return 0
	}

	ok, failed, stopped := 0, 0, 0
	for i, t := range targets {
		if ctx.Err() != nil {
			stopped++
			continue
		}
		base := map[string]any{"job": i}
		emit("dvfix_start", with(base, "path", t.Path))
		var original string
		var checks []testCheck
		r := runWithProgress(ctx, func(ch chan<- ProgressUpdate) error {
			var err error
			original, checks, err = fixDVFile(ctx, t, ch)
			return err
		}, eventProgress(base))
		done := with(base, "path", t.Path)
		switch {
		case ctx.Err() != nil:
			stopped++
			emit("dvfix_done", with(done, "status", "stop"))
		case r.Err != nil:
			failed++
			emit("dvfix_done", with(done, "status", "fail", "error", r.Err.Error()))
		default:
			ok++
			emit("dvfix_done", with(done, "status", "ok", "original", original, "checks", nonNilChecks(checks)))
		}
	}
	emit("dvfix_end", map[string]any{"ok": ok, "fail": failed, "stopped": stopped})
	if ctx.Err() != nil {
		return 130
	}
	return 0
}

// Esegue f passando gli aggiornamenti alla callback, come runJob per le codifiche
func runWithProgress(ctx context.Context, f func(chan<- ProgressUpdate) error, on func(ProgressUpdate)) runResult {
	ch := make(chan ProgressUpdate, 100)
	go func() {
		defer close(ch)
		if err := f(ch); err != nil {
			ch <- ProgressUpdate{Err: err}
		}
	}()
	var r runResult
	for u := range ch {
		if u.Err != nil {
			r.Err = u.Err
			continue
		}
		on(u)
	}
	return r
}

// Corregge un file: ritorna dove è finito l'originale e l'esito della verifica
func fixDVFile(ctx context.Context, t dvfixTarget, ch chan<- ProgressUpdate) (string, []testCheck, error) {
	src, info := t.Path, t.Info
	dir := filepath.Dir(src)
	size := fileSize(src)
	if free, ok := freeSpace(dir); ok && free < size+size/50 {
		return "", nil, fmt.Errorf(T("spazio insufficiente accanto al file: servono circa %s, liberi %s"), humanSize(size), humanSize(free))
	}
	// temporanei (fino a 2× il video): cartella di sistema se c'è posto, altrimenti accanto al file
	tmpParent := ""
	if free, _ := freeSpace(os.TempDir()); free < 2*size {
		tmpParent = dir
	}
	workDir, err := os.MkdirTemp(tmpParent, map[bool]string{true: ".encody-tmp-", false: "enc_dvfix"}[tmpParent != ""])
	if err != nil {
		return "", nil, err
	}
	trackTmp(workDir)
	defer releaseTmp(workDir)

	vmap := fmt.Sprintf("0:%d", info.VideoIndex)
	vid := filepath.Join(workDir, "video81.hevc")
	ch <- ProgressUpdate{Step: T("Conversione DV 8.1")}
	if err := convertDVTo81(ctx, src, vmap, vid, ch, info.Duration); err != nil {
		return "", nil, err
	}

	// file nuovo accanto all'originale, nascosto finché non è verificato
	tmpOut := filepath.Join(dir, "."+strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))+".encody-dvfix.mkv")
	trackTmp(tmpOut)
	defer releaseTmp(tmpOut)
	ch <- ProgressUpdate{Step: "Muxing"}
	job := Job{InputPath: src, OutputPath: tmpOut, FPSStr: info.FPSStr, ColorTrc: info.ColorTrc, VideoMap: vmap,
		MetaType: info.MetaType, DVProfile: info.DVProfile, Preset: Presets["0"], DVTo81: true}
	args := append([]string{"-o", tmpOut, "--default-duration", "0:" + info.FPSStr + "p"}, hdrMuxArgs(job, 0)...)
	for _, s := range info.Streams {
		if s.Index == info.VideoIndex {
			if s.Lang != "" {
				args = append(args, "--language", "0:"+s.Lang)
			}
			if s.Title != "" {
				args = append(args, "--track-name", "0:"+s.Title)
			}
		}
	}
	// -D: dall'originale tutto tranne il video (audio, sottotitoli, capitoli, allegati, tag)
	args = append(args, vid, "-D", src)
	warn, err := runMkvmerge(ctx, args...)
	if err != nil {
		return "", nil, err
	}
	if warn != "" {
		ch <- ProgressUpdate{Info: T("⚠️  mkvmerge ha emesso dei warning (file comunque valido):\n") + warn}
	}

	ch <- ProgressUpdate{Step: T("Verifica finale")}
	for _, s := range info.Streams {
		switch s.CodecType {
		case "audio":
			job.SelAudio = append(job.SelAudio, toSel(s))
		case "subtitle":
			job.SelSubs = append(job.SelSubs, toSel(s))
		}
	}
	checks, err := verifyOutput(ctx, job, 0)
	if err != nil {
		return "", nil, fmt.Errorf(T("verifica finale: %w"), err)
	}
	for _, c := range checks {
		if c.Level != "ok" {
			return "", checks, fmt.Errorf(T("verifica finale: %s %s; originale lasciato al suo posto"), c.Title, c.Detail)
		}
	}

	// originale da parte, poi il nuovo al suo posto
	rel, err := filepath.Rel(t.Root, src)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(src)
	}
	keep := filepath.Join(t.Root, dvfixOriginals, rel)
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		return "", nil, err
	}
	if _, err := os.Stat(keep); err == nil {
		return "", nil, fmt.Errorf(T("esiste già una copia dell'originale in %s"), keep)
	}
	if err := os.Rename(src, keep); err != nil {
		return "", nil, fmt.Errorf(T("impossibile spostare l'originale: %w"), err)
	}
	if err := os.Rename(tmpOut, src); err != nil {
		os.Rename(keep, src) // rimette l'originale
		return "", nil, fmt.Errorf(T("impossibile mettere il file nuovo al posto dell'originale: %w"), err)
	}
	return keep, checks, nil
}
