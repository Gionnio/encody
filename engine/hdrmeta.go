// Metadati HDR10 statici nel mux del percorso con inject.
//
// VideoToolbox non scrive mastering display e MaxCLL nel bitstream: nel percorso diretto li salva
// il muxer MKV di FFmpeg, ma nel percorso con inject il video passa da HEVC grezzo e si perdevano.
// Il motore li legge dal primo frame della sorgente e li passa a mkvmerge insieme ai tag colore.
package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// "34000/50000" → 0.68
func ratio(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		n, d, ok := strings.Cut(x, "/")
		a, err1 := strconv.ParseFloat(n, 64)
		if !ok {
			return a, err1 == nil
		}
		b, err2 := strconv.ParseFloat(d, 64)
		if err1 != nil || err2 != nil || b == 0 {
			return 0, false
		}
		return a / b, true
	}
	return 0, false
}

func hdrMuxArgs(job Job, track int) []string {
	tid := strconv.Itoa(track) + ":"
	args := []string{"--color-primaries", tid + "9", "--color-matrix-coefficients", tid + "9", "--color-range", tid + "1"}
	trc := "16" // PQ
	if job.ColorTrc == "arib-std-b67" {
		trc = "18"
	}
	args = append(args, "--color-transfer-characteristics", tid+trc)

	out, err := exec.Command(Tools.FFprobe, "-v", "error", "-select_streams", strings.TrimPrefix(videoMap(job), "0:"),
		"-read_intervals", "%+#1", "-show_frames", "-show_entries", "frame=side_data_list", "-of", "json", job.InputPath).Output()
	if err != nil {
		return args
	}
	var r struct {
		Frames []struct {
			SideData []map[string]any `json:"side_data_list"`
		} `json:"frames"`
	}
	if json.Unmarshal(out, &r) != nil || len(r.Frames) == 0 {
		return args
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	for _, sd := range r.Frames[0].SideData {
		switch sd["side_data_type"] {
		case "Mastering display metadata":
			var v [10]float64
			ok := true
			for i, k := range []string{"red_x", "red_y", "green_x", "green_y", "blue_x", "blue_y", "white_point_x", "white_point_y", "max_luminance", "min_luminance"} {
				var good bool
				v[i], good = ratio(sd[k])
				ok = ok && good
			}
			if ok {
				args = append(args,
					"--chromaticity-coordinates", tid+fmt.Sprintf("%s,%s,%s,%s,%s,%s", f(v[0]), f(v[1]), f(v[2]), f(v[3]), f(v[4]), f(v[5])),
					"--white-color-coordinates", tid+f(v[6])+","+f(v[7]),
					"--max-luminance", tid+f(v[8]), "--min-luminance", tid+f(v[9]))
			}
		case "Content light level metadata":
			if c, ok := ratio(sd["max_content"]); ok && c > 0 {
				args = append(args, "--max-content-light", tid+f(c))
			}
			if a, ok := ratio(sd["max_average"]); ok && a > 0 {
				args = append(args, "--max-frame-light", tid+f(a))
			}
		}
	}
	return args
}
