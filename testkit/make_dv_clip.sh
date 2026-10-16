#!/bin/bash
# Genera un clip di prova Dolby Vision profilo 8.1 (base HDR10) senza contenuti di terzi:
# testsrc2 → HEVC 10 bit PQ con metadati HDR10 → RPU generato con dovi_tool → MKV con mkvmerge.
# Uso: testkit/make_dv_clip.sh <cartella di uscita> [secondi]
set -euo pipefail
OUT="${1:?cartella di uscita}"; SECS="${2:-6}"
mkdir -p "$OUT"; T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
FPS=24; FRAMES=$((SECS * FPS))
ffmpeg -hide_banner -v error -y -f lavfi -i "testsrc2=s=1920x1080:r=$FPS:d=$SECS" \
  -vf "format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv" \
  -c:v libx265 -preset ultrafast -crf 20 \
  -x265-params "master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,50):max-cll=1000,400:hdr10=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:repeat-headers=1:log-level=error" \
  -f hevc "$T/base.hevc"
cat > "$T/rpu.json" <<JSON
{ "cm_version": "V40", "length": $FRAMES,
  "level6": { "max_display_mastering_luminance": 1000, "min_display_mastering_luminance": 1,
              "max_content_light_level": 1000, "max_frame_average_light_level": 400 } }
JSON
dovi_tool generate -j "$T/rpu.json" -o "$T/rpu.bin" >/dev/null
dovi_tool inject-rpu -i "$T/base.hevc" --rpu-in "$T/rpu.bin" -o "$T/dv.hevc" >/dev/null
mkvmerge -q -o "$OUT/dv_p81_test.mkv" --default-duration "0:${FPS}p" "$T/dv.hevc"
echo "$OUT/dv_p81_test.mkv"
