// Preset come dati.
//
// Ogni preset (di sistema o dell'utente) è una PresetSpec in JSON; compileSpec la traduce negli
// argomenti FFmpeg e nelle proprietà che il resto del motore usa (Preset). È l'unico punto in cui
// nascono i comandi video, così tag colore, HDR10 e inject restano sotto il controllo del motore
// anche per i preset personalizzati: i parametri extra dell'utente si possono solo aggiungere.
//
// Preset dell'utente: ~/Library/Application Support/Encody/presets/*.json (o ENCODY_PRESETS_DIR).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const PresetSchema = 1

// ---------- capacità degli encoder ----------

type EncoderCap struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Codec        string   `json:"codec"` // hevc | h264 | av1 | copy
	Hardware     bool     `json:"hardware"`
	TenBit       bool     `json:"ten_bit"`       // conserva l'HDR
	Dynamic      bool     `json:"dynamic"`       // DV / HDR10+ reinseribili (solo HEVC)
	RateModes    []string `json:"rate_modes"`    // crf | quality | bitrate
	QualityLabel string   `json:"quality_label"` // "CRF" o "Qualità"
	QualityMin   float64  `json:"quality_min"`
	QualityMax   float64  `json:"quality_max"`
	QualityDef   float64  `json:"quality_default"`
	LowerBetter  bool     `json:"lower_is_better"` // CRF: più basso = qualità più alta
	Speeds       []string `json:"speeds"`          // vuoto = nessuna scelta di velocità
	SpeedDef     string   `json:"speed_default"`
	ParamsFlag   string   `json:"params_flag"` // -x265-params ecc., vuoto = niente parametri encoder
	ParamsHelp   string   `json:"params_help"`
}

var x26xSpeeds = []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}

var Encoders = []EncoderCap{
	{ID: "libx265", Label: "x265 (HEVC, CPU)", Codec: "hevc", TenBit: true, Dynamic: true,
		RateModes: []string{"crf", "bitrate"}, QualityLabel: "CRF", QualityMin: 0, QualityMax: 51, QualityDef: 18, LowerBetter: true,
		Speeds: x26xSpeeds, SpeedDef: "medium", ParamsFlag: "-x265-params",
		ParamsHelp: "chiave=valore separati da \":\", es. psy-rd=2:aq-mode=3"},
	{ID: "hevc_videotoolbox", Label: "VideoToolbox HEVC (GPU Apple)", Codec: "hevc", Hardware: true, TenBit: true, Dynamic: true,
		RateModes: []string{"quality", "bitrate"}, QualityLabel: "Qualità", QualityMin: 1, QualityMax: 100, QualityDef: 65},
	{ID: "libsvtav1", Label: "SVT-AV1 (AV1, CPU)", Codec: "av1", TenBit: true,
		RateModes: []string{"crf", "bitrate"}, QualityLabel: "CRF", QualityMin: 0, QualityMax: 63, QualityDef: 28, LowerBetter: true,
		Speeds: []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13"}, SpeedDef: "6",
		ParamsFlag: "-svtav1-params", ParamsHelp: "chiave=valore separati da \":\", es. tune=0:film-grain=8"},
	{ID: "libx264", Label: "x264 (H.264, CPU, solo SDR)", Codec: "h264",
		RateModes: []string{"crf", "bitrate"}, QualityLabel: "CRF", QualityMin: 0, QualityMax: 51, QualityDef: 18, LowerBetter: true,
		Speeds: x26xSpeeds, SpeedDef: "medium", ParamsFlag: "-x264-params",
		ParamsHelp: "chiave=valore separati da \":\", es. aq-mode=3:psy-rd=1.0,0.15"},
	{ID: "h264_videotoolbox", Label: "VideoToolbox H.264 (GPU Apple, solo SDR)", Codec: "h264", Hardware: true,
		RateModes: []string{"quality", "bitrate"}, QualityLabel: "Qualità", QualityMin: 1, QualityMax: 100, QualityDef: 65},
}

func encoderCap(id string) (EncoderCap, bool) {
	for _, e := range Encoders {
		if e.ID == id {
			return e, true
		}
	}
	return EncoderCap{}, false
}

// ---------- formato dei preset ----------

type PresetRate struct {
	Mode    string  `json:"mode"`              // crf | quality | bitrate
	Value   float64 `json:"value,omitempty"`   // CRF o qualità
	Bitrate int     `json:"bitrate,omitempty"` // kbit/s
	Maxrate int     `json:"maxrate,omitempty"` // kbit/s, 0 = nessun tetto
}

type PresetAudio struct {
	Bitrate     string   `json:"bitrate"`     // AC3 stereo; il multicanale va sempre a 640k
	Passthrough []string `json:"passthrough"` // codec copiati così come sono
}

type PresetVerified struct {
	At     string  `json:"at"`
	FFmpeg string  `json:"ffmpeg"`
	FPS    float64 `json:"fps"`
}

type PresetSpec struct {
	Schema      int             `json:"schema"`
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Encoder     string          `json:"encoder"` // "copy" per il remux
	Rate        PresetRate      `json:"rate"`
	Speed       string          `json:"speed,omitempty"`
	Scale       int             `json:"scale,omitempty"`  // larghezza, 0 = originale
	Params      string          `json:"params,omitempty"` // parametri dell'encoder (x265-params ecc.)
	Extra       string          `json:"extra,omitempty"`  // argomenti FFmpeg aggiuntivi
	Audio       PresetAudio     `json:"audio"`
	Verified    *PresetVerified `json:"verified,omitempty"`
	Builtin     bool            `json:"builtin,omitempty"`
}

// ---------- preset di sistema ----------

var fullPass = []string{"aac", "ac3", "eac3", "truehd", "dts", "opus", "flac"}

// Grana (tarati su L'Impero colpisce ancora, BDRemux 4K): niente SAO e deblock leggero per non
// cancellarla, psy-rd/psy-rdoq alti per conservarne l'energia, aq-mode 3 per le ombre,
// ipratio/pbratio bassi contro il pulsare tra I e B. Grana conservata: slow 75–87%, medium 58–81%,
// contro 26–52% del preset 3. tune=grain non serve: il file supera la sorgente.
const grainX265Params = "no-sao=1:aq-mode=3:aq-strength=0.8:psy-rd=3:psy-rdoq=10:deblock=-2,-2:ipratio=1.2:pbratio=1.1:" +
	"rskip=2:rskip-edge-threshold=2:strong-intra-smoothing=0"

var builtinSpecs = []PresetSpec{
	{ID: "0", Name: "Remux (Copia Video - Audio/Sub Only)", Encoder: "copy",
		Description: "Video copiato così com'è (HDR, DV e HDR10+ inclusi); si cambiano solo audio e sottotitoli.",
		Audio:       PresetAudio{Bitrate: "320k"}},
	{ID: "1", Name: "4K VideoToolbox (CQ 65)", Encoder: "hevc_videotoolbox", Rate: PresetRate{Mode: "quality", Value: 65},
		Description: "Veloce, con la GPU del Mac.", Audio: PresetAudio{Bitrate: "320k", Passthrough: fullPass}},
	{ID: "2", Name: "1080p VideoToolbox (CQ 65)", Encoder: "hevc_videotoolbox", Rate: PresetRate{Mode: "quality", Value: 65}, Scale: 1920,
		Description: "Ridotto a 1080p per la compatibilità.", Audio: PresetAudio{Bitrate: "256k", Passthrough: []string{"aac", "ac3", "eac3", "dts", "truehd"}}},
	{ID: "3", Name: "4K CPU x265 (Medium - CRF 18)", Encoder: "libx265", Rate: PresetRate{Mode: "crf", Value: 18}, Speed: "medium",
		Params: "sao=0:aq-mode=2", Description: "Il miglior rapporto qualità/dimensione per materiale pulito.",
		Audio: PresetAudio{Bitrate: "320k", Passthrough: fullPass}},
	{ID: "4", Name: "4K High Bitrate VBR (24Mbps)", Encoder: "hevc_videotoolbox", Rate: PresetRate{Mode: "bitrate", Bitrate: 24000, Maxrate: 35000},
		Description: "Bitrate fisso alto con la GPU del Mac.", Audio: PresetAudio{Bitrate: "320k", Passthrough: fullPass}},
	{ID: "5", Name: "4K CPU x265 Grana (Slow - CRF 17)", Encoder: "libx265", Rate: PresetRate{Mode: "crf", Value: 17}, Speed: "slow",
		Params: grainX265Params, Description: "Film con grana: la conserva al 75–87%. Lento.",
		Audio: PresetAudio{Bitrate: "320k", Passthrough: fullPass}},
	{ID: "6", Name: "4K CPU x265 Grana veloce (Medium - CRF 17)", Encoder: "libx265", Rate: PresetRate{Mode: "crf", Value: 17}, Speed: "medium",
		Params: grainX265Params, Description: "Film con grana, più veloce: la conserva al 58–81%.",
		Audio: PresetAudio{Bitrate: "320k", Passthrough: fullPass}},
}

// Presets: tutti i preset usabili, per ID (sistema + utente)
var Presets = map[string]Preset{}

// PresetSpecs: le specifiche da cui nascono, per l'editor dell'app
var PresetSpecs = map[string]PresetSpec{}

// Errori dei file preset dell'utente che non si caricano
var PresetLoadErrors []string

func init() {
	for _, s := range builtinSpecs {
		s.Schema = PresetSchema
		s.Builtin = true
		p, err := compileSpec(s)
		if err != nil {
			panic(fmt.Sprintf("preset di sistema %s non valido: %v", s.ID, err))
		}
		Presets[s.ID] = p
		PresetSpecs[s.ID] = s
	}
}

func userPresetsDir() string {
	if d := os.Getenv("ENCODY_PRESETS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Encody", "presets")
}

// Carica i preset dell'utente; un file rotto non blocca gli altri
func loadUserPresets() {
	PresetLoadErrors = []string{}
	dir := userPresetsDir()
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s PresetSpec
		if err := json.Unmarshal(data, &s); err != nil {
			PresetLoadErrors = append(PresetLoadErrors, fmt.Sprintf("%s: JSON non valido (%v)", filepath.Base(f), err))
			continue
		}
		s.Builtin = false
		if _, clash := PresetSpecs[s.ID]; clash || s.ID == "" {
			PresetLoadErrors = append(PresetLoadErrors, fmt.Sprintf("%s: ID mancante o già usato", filepath.Base(f)))
			continue
		}
		if issues := validateSpec(s); hasErrors(issues) {
			PresetLoadErrors = append(PresetLoadErrors, fmt.Sprintf("%s: %s", filepath.Base(f), firstError(issues)))
			continue
		}
		p, err := compileSpec(s)
		if err != nil {
			PresetLoadErrors = append(PresetLoadErrors, fmt.Sprintf("%s: %v", filepath.Base(f), err))
			continue
		}
		Presets[s.ID] = p
		PresetSpecs[s.ID] = s
	}
}

// ---------- compilazione in argomenti FFmpeg ----------

func compileSpec(s PresetSpec) (Preset, error) {
	p := Preset{ID: s.ID, Name: s.Name, Description: s.Description, Scale: s.Scale, Builtin: s.Builtin,
		AudioBitrate: s.Audio.Bitrate, Passthrough: s.Audio.Passthrough, Encoder: s.Encoder}
	if p.AudioBitrate == "" {
		p.AudioBitrate = "320k"
	}
	if s.Encoder == "copy" {
		p.Type, p.Codec, p.TenBit = "copy", "copy", true
		p.VideoOpts = []string{"-c:v", "copy"}
		return p, nil
	}
	e, ok := encoderCap(s.Encoder)
	if !ok {
		return Preset{}, fmt.Errorf("encoder sconosciuto: %s", s.Encoder)
	}
	p.Codec, p.TenBit = e.Codec, e.TenBit
	p.Type = "cpu"
	if e.Hardware {
		p.Type = "gpu"
	}

	num := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	rate := func() []string {
		switch s.Rate.Mode {
		case "crf":
			return []string{"-crf", num(s.Rate.Value)}
		case "quality":
			return []string{"-q:v", num(s.Rate.Value)}
		}
		r := []string{"-b:v", fmt.Sprintf("%dk", s.Rate.Bitrate)}
		if s.Rate.Maxrate > 0 {
			r = append(r, "-maxrate", fmt.Sprintf("%dk", s.Rate.Maxrate), "-bufsize", fmt.Sprintf("%dk", s.Rate.Maxrate))
		}
		return r
	}
	params := strings.Trim(strings.TrimSpace(s.Params), ":")

	o := []string{"-c:v", s.Encoder}
	switch s.Encoder {
	case "libx265":
		o = append(o, "-preset", s.Speed)
		o = append(o, rate()...)
		// repeat-headers: parametri HDR ripetuti a ogni keyframe (seek e player esigenti)
		xp := "repeat-headers=1"
		if params != "" {
			xp = params + ":" + xp
		}
		o = append(o, "-profile:v", "main10", "-pix_fmt", "yuv420p10le", "-x265-params", xp, "-tag:v", "hvc1")
	case "hevc_videotoolbox":
		o = append(o, "-profile:v", "main10", "-pix_fmt", "p010le")
		o = append(o, rate()...)
		if s.Rate.Mode == "bitrate" {
			o = append(o, "-tag:v", "hvc1")
		}
		o = append(o, "-fps_mode", "vfr")
		p.FilterFormat = "p010le"
	case "libsvtav1":
		o = append(o, "-preset", s.Speed)
		o = append(o, rate()...)
		o = append(o, "-pix_fmt", "yuv420p10le")
		if params != "" {
			o = append(o, "-svtav1-params", params)
		}
	case "libx264":
		o = append(o, "-preset", s.Speed)
		o = append(o, rate()...)
		o = append(o, "-profile:v", "high", "-pix_fmt", "yuv420p")
		if params != "" {
			o = append(o, "-x264-params", params)
		}
	case "h264_videotoolbox":
		o = append(o, "-profile:v", "high", "-pix_fmt", "yuv420p")
		o = append(o, rate()...)
		o = append(o, "-fps_mode", "vfr")
	}
	extra, err := splitArgs(s.Extra)
	if err != nil {
		return Preset{}, fmt.Errorf("parametri extra: %w", err)
	}
	p.VideoOpts = append(o, extra...)
	return p, nil
}

// "-g" è un'opzione, "-1" è un valore
func isOption(a string) bool {
	return len(a) > 1 && a[0] == '-' && (a[1] < '0' || a[1] > '9') && a[1] != '.'
}

// Divide una riga di argomenti rispettando apici singoli e doppi
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	inToken := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inToken = true
		case r == ' ' || r == '\t' || r == '\n':
			if inToken {
				out = append(out, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}
	if quote != 0 {
		return nil, errors.New("apice non chiuso")
	}
	if inToken {
		out = append(out, cur.String())
	}
	return out, nil
}

// ---------- controllo immediato ----------

type PresetIssue struct {
	Level   string `json:"level"` // error | warning | info
	Field   string `json:"field"`
	Message string `json:"message"`
}

func hasErrors(is []PresetIssue) bool {
	for _, i := range is {
		if i.Level == "error" {
			return true
		}
	}
	return false
}

func firstError(is []PresetIssue) string {
	for _, i := range is {
		if i.Level == "error" {
			return i.Message
		}
	}
	return ""
}

// Opzioni che il motore gestisce da sé: nei parametri extra non sono ammesse
var reservedArgs = map[string]string{
	"-i": "l'input", "-map": "la scelta delle tracce", "-map_metadata": "i metadati", "-map_chapters": "i capitoli",
	"-c": "i codec", "-codec": "i codec", "-c:v": "l'encoder", "-vcodec": "l'encoder", "-codec:v": "l'encoder",
	"-c:a": "l'audio", "-acodec": "l'audio", "-codec:a": "l'audio", "-b:a": "l'audio", "-ac": "l'audio", "-ar": "l'audio",
	"-af": "l'audio", "-filter:a": "l'audio", "-c:s": "i sottotitoli", "-scodec": "i sottotitoli",
	"-vf": "i filtri video (crop, scala, tonemap)", "-filter:v": "i filtri video", "-filter_complex": "i filtri", "-lavfi": "i filtri",
	"-pix_fmt": "il formato dei pixel", "-profile:v": "il profilo", "-color_primaries": "i tag colore",
	"-color_trc": "i tag colore", "-colorspace": "i tag colore", "-color_range": "i tag colore",
	"-y": "la sovrascrittura", "-n": "la sovrascrittura", "-f": "il contenitore", "-t": "la durata", "-ss": "l'inizio", "-to": "la durata",
	"-an": "l'audio", "-sn": "i sottotitoli", "-dn": "i dati",
	"-preset": "la velocità (usa il campo Velocità)", "-crf": "la qualità (usa il campo Qualità)", "-q:v": "la qualità (usa il campo Qualità)",
	"-b:v": "il bitrate (usa il campo Bitrate)", "-maxrate": "il bitrate massimo", "-bufsize": "il bitrate massimo",
	"-x265-params": "i parametri x265 (usa il campo Parametri encoder)", "-x264-params": "i parametri x264 (usa il campo Parametri encoder)",
	"-svtav1-params": "i parametri SVT-AV1 (usa il campo Parametri encoder)",
}

// Chiavi dei parametri encoder che il motore imposta da sé (HDR10 e tag colore)
var reservedParams = map[string]bool{
	"master-display": true, "max-cll": true, "colorprim": true, "transfer": true, "colormatrix": true,
	"range": true, "hdr10": true, "hdr10-opt": true, "repeat-headers": true, "dolby-vision-profile": true,
	"dolby-vision-rpu": true, "mastering-display": true, "content-light": true, "color-primaries": true,
	"transfer-characteristics": true, "matrix-coefficients": true, "input-depth": true, "output-depth": true,
}

var paramKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func validateSpec(s PresetSpec) []PresetIssue {
	var is []PresetIssue
	add := func(level, field, msg string) { is = append(is, PresetIssue{level, field, msg}) }

	if strings.TrimSpace(s.Name) == "" {
		add("error", "name", "Il nome è obbligatorio.")
	}
	if s.Schema > PresetSchema {
		add("error", "schema", "Preset creato con una versione più recente di Encody.")
	}
	if s.Encoder == "copy" {
		return append(is, validateAudio(s)...)
	}
	e, ok := encoderCap(s.Encoder)
	if !ok {
		add("error", "encoder", fmt.Sprintf("Encoder sconosciuto: %q.", s.Encoder))
		return is
	}
	if len(Tools.Encoders) > 0 && !Tools.Encoders[s.Encoder] {
		add("error", "encoder", fmt.Sprintf("Il tuo FFmpeg non include %s.", e.Label))
	}

	// qualità e bitrate
	modeOK := false
	for _, m := range e.RateModes {
		modeOK = modeOK || m == s.Rate.Mode
	}
	switch {
	case !modeOK:
		add("error", "rate", fmt.Sprintf("%s non supporta la modalità %q.", e.Label, s.Rate.Mode))
	case s.Rate.Mode == "bitrate":
		if s.Rate.Bitrate <= 0 {
			add("error", "rate", "Indica il bitrate in kbit/s.")
		} else if s.Rate.Bitrate < 500 {
			add("warning", "rate", "Bitrate molto basso: qualità scarsa su contenuti 4K e 1080p.")
		}
		if s.Rate.Maxrate > 0 && s.Rate.Maxrate < s.Rate.Bitrate {
			add("error", "rate", "Il bitrate massimo deve essere maggiore o uguale a quello medio.")
		}
	default:
		v := s.Rate.Value
		if v < e.QualityMin || v > e.QualityMax {
			add("error", "rate", fmt.Sprintf("%s fuori intervallo: per %s va da %g a %g.", e.QualityLabel, e.Label, e.QualityMin, e.QualityMax))
		} else if e.LowerBetter && v > e.QualityDef+10 {
			add("warning", "rate", fmt.Sprintf("%s %g: qualità bassa, artefatti probabili.", e.QualityLabel, v))
		} else if e.LowerBetter && v < e.QualityDef-8 {
			add("warning", "rate", fmt.Sprintf("%s %g: file molto grandi, spesso vicini alla sorgente.", e.QualityLabel, v))
		} else if !e.LowerBetter && v < 40 {
			add("warning", "rate", fmt.Sprintf("Qualità %g: artefatti probabili.", v))
		}
	}

	// velocità
	if len(e.Speeds) > 0 {
		found := false
		for _, sp := range e.Speeds {
			found = found || sp == s.Speed
		}
		if !found {
			add("error", "speed", fmt.Sprintf("Velocità %q non valida per %s.", s.Speed, e.Label))
		}
	} else if s.Speed != "" {
		add("warning", "speed", fmt.Sprintf("%s non ha una scelta di velocità: il valore viene ignorato.", e.Label))
	}

	// risoluzione
	if s.Scale != 0 && (s.Scale < 320 || s.Scale > 7680 || s.Scale%2 != 0) {
		add("error", "scale", "Larghezza non valida: 0 (originale) oppure un numero pari tra 320 e 7680.")
	}

	// parametri dell'encoder
	if p := strings.Trim(strings.TrimSpace(s.Params), ":"); p != "" {
		if e.ParamsFlag == "" {
			add("error", "params", fmt.Sprintf("%s non accetta parametri encoder.", e.Label))
		} else {
			for _, kv := range strings.Split(p, ":") {
				k, v, hasV := strings.Cut(kv, "=")
				switch {
				case !hasV || v == "":
					add("error", "params", fmt.Sprintf("Parametro %q senza valore: scrivi chiave=valore.", kv))
				case !paramKeyRe.MatchString(k):
					add("error", "params", fmt.Sprintf("Nome di parametro non valido: %q.", k))
				case reservedParams[k]:
					add("error", "params", fmt.Sprintf("%s lo gestisce Encody (HDR10 e tag colore dalla sorgente).", k))
				}
			}
		}
	}

	// parametri extra: solo aggiuntivi
	if args, err := splitArgs(s.Extra); err != nil {
		add("error", "extra", "Parametri extra: "+err.Error()+".")
	} else {
		// un valore deve seguire subito un'opzione, altrimenti FFmpeg lo prende per un file di output
		afterOption := false
		for _, a := range args {
			if isOption(a) {
				if what, bad := reservedArgs[a]; bad {
					add("error", "extra", fmt.Sprintf("%s non è ammesso: %s lo gestisce Encody.", a, what))
				}
				afterOption = true
				continue
			}
			if !afterOption {
				add("error", "extra", fmt.Sprintf("%q non segue un'opzione: FFmpeg lo userebbe come file di output.", a))
			}
			afterOption = false
		}
	}

	// compatibilità con HDR e metadati dinamici
	if !e.TenBit {
		add("warning", "encoder", "Encoder a 8 bit: le sorgenti HDR verranno convertite in SDR (tonemap).")
	}
	if !e.Dynamic {
		add("warning", "encoder", "Dolby Vision e HDR10+ non si possono reinserire: resterà l'HDR10 statico (o SDR).")
	} else if s.Scale > 0 {
		add("info", "scale", "Con la riduzione di risoluzione Dolby Vision e HDR10+ non si possono reinserire.")
	}
	if e.ID == "libsvtav1" && strings.Contains(s.Params, "film-grain") {
		add("info", "params", "Sintesi della grana: alcuni player e TV non la riproducono.")
	}
	return append(is, validateAudio(s)...)
}

var knownAudioCodecs = map[string]bool{"aac": true, "ac3": true, "eac3": true, "truehd": true, "dts": true, "opus": true, "flac": true, "mp3": true, "vorbis": true, "alac": true, "pcm_s16le": true, "pcm_s24le": true}

func validateAudio(s PresetSpec) []PresetIssue {
	var is []PresetIssue
	switch s.Audio.Bitrate {
	case "", "128k", "160k", "192k", "224k", "256k", "320k", "384k", "448k", "640k":
	default:
		is = append(is, PresetIssue{"error", "audio", fmt.Sprintf("Bitrate audio %q non valido per AC3 (es. 256k, 320k).", s.Audio.Bitrate)})
	}
	for _, c := range s.Audio.Passthrough {
		if !knownAudioCodecs[c] {
			is = append(is, PresetIssue{"warning", "audio", fmt.Sprintf("Codec audio %q sconosciuto: verrà ignorato.", c)})
		}
	}
	return is
}

// Comando FFmpeg di esempio per l'anteprima nell'editor (sorgente HDR10 4K senza crop)
func previewCommand(p Preset) string {
	job := Job{InputPath: "input.mkv", OutputPath: "output.mkv", Preset: p, MetaType: "HDR10",
		ColorPrim: "bt2020", ColorTrc: "smpte2084", ColorSpace: "bt2020nc", AudioMode: "copy"}
	if !p.TenBit && p.Type != "copy" {
		job.ToneMap = true
	}
	args := append([]string{"ffmpeg", "-i", "input.mkv"}, encodeVideoArgs(job)...)
	args = append(args, "[audio e sottotitoli]", "output.mkv")
	for i, a := range args {
		if strings.ContainsAny(a, " ,;()[]") && !strings.HasPrefix(a, "[") {
			args[i] = "'" + a + "'"
		}
	}
	return strings.Join(args, " ")
}
