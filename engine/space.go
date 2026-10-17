// Spazio su disco prima di ogni lavoro.
//
// Il percorso con inject scrive nella cartella temporanea il video codificato e la sua copia con i
// metadati: fino a circa il doppio del video, che per un film 4K sono decine di GB sul disco di
// sistema. Prima di partire il motore sceglie dove metterli (cartella temporanea di sistema o, se
// lì non c'è posto, una cartella nascosta accanto all'output) e controlla lo spazio dell'output.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func freeSpace(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

type spaceCheck struct {
	TmpParent string // "" = cartella temporanea di sistema
	Warning   string
}

// Stima prudente: l'output di una codifica non supera la sorgente, quello di un remux la eguaglia
func checkSpace(job Job, inject bool) (spaceCheck, error) {
	var sc spaceCheck
	src := fileSize(job.InputPath)
	if TestModeActive && job.Duration > TestSeconds {
		src = int64(float64(src) * TestSeconds / job.Duration)
	}
	if src <= 0 {
		return sc, nil
	}
	outDir := filepath.Dir(job.OutputPath)
	outFree, ok := freeSpace(outDir)
	if !ok {
		return sc, nil
	}
	switch {
	case job.Preset.Type == "copy" && outFree < src+src/50:
		return sc, fmt.Errorf(T("spazio insufficiente per il remux: servono circa %s, liberi %s"), humanSize(src), humanSize(outFree))
	case job.Preset.Type != "copy" && outFree < src/4:
		sc.Warning = fmt.Sprintf(T("⚠️  Poco spazio sul disco di destinazione (%s liberi): il file potrebbe non starci."), humanSize(outFree))
	}
	if !inject {
		return sc, nil
	}

	need := 2 * src
	tmpFree, _ := freeSpace(os.TempDir())
	switch {
	case tmpFree >= need:
	case outFree >= need+src:
		sc.TmpParent = outDir
	default:
		if outFree > tmpFree {
			sc.TmpParent = outDir
		}
		sc.Warning = fmt.Sprintf(T("⚠️  Poco spazio per i file temporanei dell'inject (servono circa %s): il lavoro potrebbe fallire."), humanSize(need))
	}
	return sc, nil
}
