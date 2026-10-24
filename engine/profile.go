// Profilo di conformità: i preset e le regole di Encody in un JSON versionato.
//
// Un media server lo legge per dare un voto ai file della libreria (quanto somigliano al preset più
// vicino) e per riconoscere quelli firmati da Encody (signature.go). Le regole sono le stesse che
// Encody usa quando prepara un job (lingue suggerite, sottotitoli forzati, conservazione dell'HDR),
// così cambiando Encody cambia anche il controllo, senza duplicare regole altrove.
//
// encody profile export   stampa il profilo
package main

import (
	"errors"
	"time"
)

const ProfileSchema = 1

// Lingua preferita per audio e sottotitoli forzati: la usa anche probe per suggerire le tracce
var preferredLangs = []string{"ita", "it"}

func isPreferredLang(l string) bool {
	for _, p := range preferredLangs {
		if l == p {
			return true
		}
	}
	return false
}

type profilePreset struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Builtin  bool            `json:"builtin"`
	Encoder  string          `json:"encoder"`
	Codec    string          `json:"codec"`
	TenBit   bool            `json:"ten_bit"`
	Hardware bool            `json:"hardware"`
	Rate     PresetRate      `json:"rate"`
	Speed    string          `json:"speed,omitempty"`
	Scale    int             `json:"scale,omitempty"`
	Audio    PresetAudio     `json:"audio"`
	Hash     string          `json:"hash"`
	Verified *PresetVerified `json:"verified,omitempty"`
}

type profileRules struct {
	Container string `json:"container"`
	Video     struct {
		KeepSourceHDR      bool  `json:"keep_source_hdr"`
		DVTargetProfile    int   `json:"dv_target_profile"`
		DVConvertProfiles  []int `json:"dv_convert_profiles"`
		DVRemuxOnlyProfile []int `json:"dv_remux_only_profiles"`
	} `json:"video"`
	Audio struct {
		PreferredLanguages []string `json:"preferred_languages"`
		OriginalLanguage   bool     `json:"original_language"`
		DefaultPreferred   bool     `json:"default_preferred"`
		MultichannelKbps   int      `json:"multichannel_kbps"`
	} `json:"audio"`
	Subtitles struct {
		ForcedLanguages    []string `json:"forced_languages"`
		ForcedWhenForeign  bool     `json:"forced_when_foreign_audio"`
		FullTrackDefault   bool     `json:"full_track_default"`
		ImageBasedAllowed  bool     `json:"image_based_allowed"`
		TextFormatsAllowed []string `json:"text_formats_allowed"`
	} `json:"subtitles"`
}

type profileOut struct {
	Schema     int             `json:"schema"`
	Generator  string          `json:"generator"`
	Version    string          `json:"version"`
	ExportedAt string          `json:"exported_at"`
	Signature  []string        `json:"signature_tags"`
	Skipped    []string        `json:"skipped_presets"` // preset dell'utente non caricati, con il motivo
	Presets    []profilePreset `json:"presets"`
	Rules      profileRules    `json:"rules"`
}

func buildProfile() profileOut {
	out := profileOut{Schema: ProfileSchema, Generator: "encody", Version: Version,
		ExportedAt: time.Now().UTC().Format(time.RFC3339), Signature: SignatureTags, Skipped: PresetLoadErrors, Presets: []profilePreset{}}
	for _, k := range sortedPresetKeys(true) {
		s, p := PresetSpecs[k], Presets[k]
		e, _ := encoderCap(s.Encoder)
		out.Presets = append(out.Presets, profilePreset{ID: s.ID, Name: s.Name, Builtin: s.Builtin, Encoder: s.Encoder,
			Codec: p.Codec, TenBit: p.TenBit, Hardware: e.Hardware, Rate: s.Rate, Speed: s.Speed, Scale: s.Scale,
			Audio: PresetAudio{Bitrate: p.AudioBitrate, Passthrough: p.Passthrough}, Hash: presetFingerprint(s), Verified: s.Verified})
	}

	r := &out.Rules
	r.Container = "matroska"
	r.Video.KeepSourceHDR = true
	r.Video.DVTargetProfile = 8           // inject in 8.1
	r.Video.DVConvertProfiles = []int{7}  // convertibile in 8.1 (con avviso se FEL)
	r.Video.DVRemuxOnlyProfile = []int{5} // il base layer non è HDR10: solo remux
	r.Audio.PreferredLanguages = preferredLangs
	r.Audio.OriginalLanguage = true
	r.Audio.DefaultPreferred = true
	r.Audio.MultichannelKbps = 640 // il multicanale ricodificato va sempre a 640k
	r.Subtitles.ForcedLanguages = preferredLangs
	r.Subtitles.ForcedWhenForeign = true
	r.Subtitles.FullTrackDefault = false
	r.Subtitles.ImageBasedAllowed = true
	r.Subtitles.TextFormatsAllowed = []string{"subrip", "ass", "ssa"}
	return out
}

func cmdProfile(args []string) int {
	if len(args) < 1 || args[0] != "export" {
		return fail(errors.New(T("uso: encody profile export")))
	}
	printJSON(buildProfile())
	return 0
}
