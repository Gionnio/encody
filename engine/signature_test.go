package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// L'impronta dipende solo da ciò che cambia la codifica
func TestPresetFingerprint(t *testing.T) {
	base := PresetSpecs["3"]
	h := presetFingerprint(base)
	if len(h) != 16 {
		t.Fatalf("impronta di %d cifre, attese 16", len(h))
	}
	same := base
	same.Name, same.Description, same.Verified = "Altro nome", "altra descrizione", &PresetVerified{At: "x"}
	if presetFingerprint(same) != h {
		t.Error("nome, descrizione e verifica non devono cambiare l'impronta")
	}
	diff := base
	diff.Params += ":rd=4"
	if presetFingerprint(diff) == h {
		t.Error("parametri diversi devono cambiare l'impronta")
	}
	diff = base
	diff.Rate.Value = 19
	if presetFingerprint(diff) == h {
		t.Error("qualità diversa deve cambiare l'impronta")
	}
	// tutte diverse tra i preset di sistema
	seen := map[string]string{}
	for id, s := range PresetSpecs {
		if !s.Builtin {
			continue
		}
		f := presetFingerprint(s)
		if other, dup := seen[f]; dup {
			t.Errorf("preset %s e %s con la stessa impronta", id, other)
		}
		seen[f] = id
	}
}

func TestSignatureTags(t *testing.T) {
	job := Job{Preset: Presets["1"]}
	tags := signatureTags(job)
	for _, k := range SignatureTags {
		if tags[k] == "" {
			t.Errorf("tag %s vuoto", k)
		}
	}
	if tags[TagPreset] != "1" || tags[TagPresetHash] != presetFingerprint(PresetSpecs["1"]) {
		t.Errorf("tag del preset sbagliati: %v", tags)
	}
	args := signatureFFmpegArgs(tags)
	if len(args) != 2*len(SignatureTags) || args[0] != "-metadata" || !strings.HasPrefix(args[1], TagVersion+"=") {
		t.Errorf("argomenti FFmpeg inattesi: %v", args)
	}

	TestModeActive = true
	if signatureTags(job) != nil {
		t.Error("la modalità di prova non deve firmare")
	}
	TestModeActive = false

	os.Setenv("ENCODY_SIGNATURE", "0")
	if signatureTags(job) != nil {
		t.Error("ENCODY_SIGNATURE=0 deve spegnere la firma")
	}
	os.Unsetenv("ENCODY_SIGNATURE")

	if signatureTags(Job{Preset: Preset{ID: "inesistente"}}) != nil {
		t.Error("preset sconosciuto: niente firma")
	}
}

func TestSignatureXML(t *testing.T) {
	path, err := writeSignatureXML(t.TempDir(), map[string]string{TagVersion: "1<2&3", TagPreset: "1"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, "<Name>ENCODY_VERSION</Name><String>1&lt;2&amp;3</String>") {
		t.Errorf("XML non escapato: %s", s)
	}
	if strings.Index(s, TagVersion) > strings.Index(s, TagPreset) {
		t.Error("ordine dei tag diverso da SignatureTags")
	}
}

// Il profilo esporta tutti i preset con l'impronta e le regole usate da probe
func TestProfileExport(t *testing.T) {
	p := buildProfile()
	if p.Schema != ProfileSchema || len(p.Presets) != len(Presets) {
		t.Fatalf("profilo incompleto: schema %d, %d preset su %d", p.Schema, len(p.Presets), len(Presets))
	}
	for _, pp := range p.Presets {
		if pp.Hash != presetFingerprint(PresetSpecs[pp.ID]) {
			t.Errorf("preset %s: impronta diversa da quella della firma", pp.ID)
		}
	}
	if !isPreferredLang(p.Rules.Audio.PreferredLanguages[0]) {
		t.Error("lingua preferita del profilo diversa da quella di probe")
	}
	if _, err := json.Marshal(p); err != nil {
		t.Fatal(err)
	}
}
