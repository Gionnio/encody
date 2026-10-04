package main

import (
	"strings"
	"testing"
)

// I preset di sistema devono generare gli stessi comandi di quando erano scritti a mano
func TestBuiltinPresetCommands(t *testing.T) {
	want := map[string]string{
		"0": "-c:v copy",
		"1": "-c:v hevc_videotoolbox -profile:v main10 -pix_fmt p010le -q:v 65 -fps_mode vfr",
		"2": "-c:v hevc_videotoolbox -profile:v main10 -pix_fmt p010le -q:v 65 -fps_mode vfr",
		"3": "-c:v libx265 -preset medium -crf 18 -profile:v main10 -pix_fmt yuv420p10le -x265-params sao=0:aq-mode=2:repeat-headers=1 -tag:v hvc1",
		"4": "-c:v hevc_videotoolbox -profile:v main10 -pix_fmt p010le -b:v 24000k -maxrate 35000k -bufsize 35000k -tag:v hvc1 -fps_mode vfr",
		"5": "-c:v libx265 -preset slow -crf 17 -profile:v main10 -pix_fmt yuv420p10le -x265-params no-sao=1:aq-mode=3:aq-strength=0.8:psy-rd=3:psy-rdoq=10:deblock=-2,-2:ipratio=1.2:pbratio=1.1:rskip=2:rskip-edge-threshold=2:strong-intra-smoothing=0:repeat-headers=1 -tag:v hvc1",
		"6": "-c:v libx265 -preset medium -crf 17 -profile:v main10 -pix_fmt yuv420p10le -x265-params no-sao=1:aq-mode=3:aq-strength=0.8:psy-rd=3:psy-rdoq=10:deblock=-2,-2:ipratio=1.2:pbratio=1.1:rskip=2:rskip-edge-threshold=2:strong-intra-smoothing=0:repeat-headers=1 -tag:v hvc1",
	}
	for id, cmd := range want {
		if got := strings.Join(Presets[id].VideoOpts, " "); got != cmd {
			t.Errorf("preset %s:\n got  %s\n want %s", id, got, cmd)
		}
	}
}

func TestValidateSpec(t *testing.T) {
	ok := PresetSpec{Name: "Test", Encoder: "libx265", Rate: PresetRate{Mode: "crf", Value: 20}, Speed: "slow",
		Params: "psy-rd=2:aq-mode=3", Extra: "-g 48 -bf 5", Audio: PresetAudio{Bitrate: "320k"}}
	if is := validateSpec(ok); hasErrors(is) {
		t.Fatalf("preset valido segnalato come errato: %v", is)
	}
	bad := []struct {
		name string
		mod  func(*PresetSpec)
	}{
		{"crf fuori intervallo", func(s *PresetSpec) { s.Rate.Value = 60 }},
		{"velocità sbagliata", func(s *PresetSpec) { s.Speed = "turbo" }},
		{"parametro senza valore", func(s *PresetSpec) { s.Params = "psy-rd" }},
		{"parametro riservato", func(s *PresetSpec) { s.Params = "master-display=G(1,2)" }},
		{"extra riservato", func(s *PresetSpec) { s.Extra = "-c:a aac" }},
		{"extra come output", func(s *PresetSpec) { s.Extra = "-g 48 out.mkv" }},
		{"apice aperto", func(s *PresetSpec) { s.Extra = "-metadata 'title=x" }},
		{"modalità non supportata", func(s *PresetSpec) { s.Rate.Mode = "quality" }},
		{"nome vuoto", func(s *PresetSpec) { s.Name = " " }},
	}
	for _, c := range bad {
		s := ok
		c.mod(&s)
		if !hasErrors(validateSpec(s)) {
			t.Errorf("%s: errore non rilevato", c.name)
		}
	}
}
