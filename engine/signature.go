// Firma di Encody nei file prodotti.
//
// Ogni file finito porta tag globali Matroska ENCODY_*: versione del motore, preset e impronta della
// sua definizione, data. Così un media server (es. Jellyfin) può riconoscere i file fatti da Encody e
// sapere con quale preset, anche dopo una rinomina. L'impronta cambia solo se cambia qualcosa che
// tocca la codifica (encoder, qualità, velocità, scala, parametri, audio), non nome o descrizione.
//
// La modalità di prova non firma: uno spezzone di prova non è un file della libreria.
// L'app spegne la firma dalle Impostazioni con ENCODY_SIGNATURE=0.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	TagVersion    = "ENCODY_VERSION"
	TagPreset     = "ENCODY_PRESET"
	TagPresetHash = "ENCODY_PRESET_HASH"
	TagDate       = "ENCODY_DATE"
)

// SignatureTags: nomi dei tag di firma, nell'ordine in cui si scrivono
var SignatureTags = []string{TagVersion, TagPreset, TagPresetHash, TagDate}

// Impronta di un preset: SHA-256 (16 cifre esadecimali) dei soli campi che cambiano la codifica
func presetFingerprint(s PresetSpec) string {
	core := struct {
		Encoder string      `json:"encoder"`
		Rate    PresetRate  `json:"rate"`
		Speed   string      `json:"speed,omitempty"`
		Scale   int         `json:"scale,omitempty"`
		Params  string      `json:"params,omitempty"`
		Extra   string      `json:"extra,omitempty"`
		Audio   PresetAudio `json:"audio"`
	}{s.Encoder, s.Rate, s.Speed, s.Scale, s.Params, s.Extra, s.Audio}
	data, _ := json.Marshal(core)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// Tag di firma per un job; nil in modalità di prova o se il preset non è noto
func signatureTags(job Job) map[string]string {
	if TestModeActive || os.Getenv("ENCODY_SIGNATURE") == "0" {
		return nil
	}
	spec, ok := PresetSpecs[job.Preset.ID]
	if !ok {
		return nil
	}
	return map[string]string{
		TagVersion:    Version,
		TagPreset:     spec.ID,
		TagPresetHash: presetFingerprint(spec),
		TagDate:       time.Now().UTC().Format(time.RFC3339),
	}
}

// Argomenti FFmpeg per scrivere la firma come metadati globali (il muxer Matroska li scrive come tag)
func signatureFFmpegArgs(tags map[string]string) []string {
	var args []string
	for _, k := range sortedKeys(tags) {
		args = append(args, "-metadata", k+"="+tags[k])
	}
	return args
}

// File XML dei tag globali per mkvmerge (--global-tags)
func writeSignatureXML(dir string, tags map[string]string) (string, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<Tags>\n  <Tag>\n    <Targets/>\n")
	for _, k := range sortedKeys(tags) {
		b.WriteString("    <Simple><Name>")
		xml.EscapeText(&b, []byte(k))
		b.WriteString("</Name><String>")
		xml.EscapeText(&b, []byte(tags[k]))
		b.WriteString("</String></Simple>\n")
	}
	b.WriteString("  </Tag>\n</Tags>\n")
	path := filepath.Join(dir, "encody-tags.xml")
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// ordine fisso: quello di SignatureTags, poi eventuali altri in ordine alfabetico
	rank := map[string]int{}
	for i, k := range SignatureTags {
		rank[k] = i + 1
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := rank[keys[i]], rank[keys[j]]
		if ri != rj {
			if ri == 0 {
				return false
			}
			if rj == 0 {
				return true
			}
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	return keys
}
