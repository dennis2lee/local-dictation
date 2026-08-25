#!/usr/bin/env bash
#
# Download a Whisper model for Local Dictation.
#
#   ./fetch-model.sh                        # large-v3      into the default dir
#   ./fetch-model.sh large-v3-turbo         # turbo         into the default dir
#   ./fetch-model.sh base                   # draft model, for live partials
#   ./fetch-model.sh large-v3-turbo-mlx     # for --backend mlx (Apple Silicon)
#   ./fetch-model.sh large-v3-turbo-openvino-int8
#                                           # for --backend openvino (Intel GPU)
#   ./fetch-model.sh all --dest ./models    # all three + VAD, into ./models
#   ./fetch-model.sh --list                 # show sizes, download nothing
#   ./fetch-model.sh --verify --dest ./models
#
# Models are NOT shipped inside the server or the client packages: they are
# large, they have their own licence, and sites mirror them differently. This
# script is the one supported way to put them on disk.
#
# Closed network? Run it on a machine that has internet, then copy the whole
# directory across and re-run with --verify on the far side:
#
#   ./fetch-model.sh all --dest ./ld-models
#   tar czf ld-models.tgz ld-models && scp ld-models.tgz server:/tmp/
#   ssh server 'tar xzf /tmp/ld-models.tgz -C /opt/local-dictation/ --strip-components=1'
#   ssh server '/opt/local-dictation/app/scripts/fetch-model.sh --verify'
#
# Options:
#   --dest DIR        install root          (default: $LD_MODELS_DIR, else
#                                            /opt/local-dictation/models)
#   --repo ID         override the source repository. Any conversion in the same
#                     format works, including a smaller model or a mirror.
#   --metadata-only   skip the multi-GB weights; useful to test connectivity
#   --force           re-download files that are already present
#   --list            print the file list with sizes and exit
#   --verify          re-check an existing install against its SHA256SUMS
#   --help

set -euo pipefail

DEST="${LD_MODELS_DIR:-/opt/local-dictation/models}"
HF_ENDPOINT="${HF_ENDPOINT:-https://huggingface.co}"

# Both entries are CTranslate2 conversions — the format faster-whisper loads
# directly. The plain openai/* repos are PyTorch and will not work here.
REPO_LARGE_V3="Systran/faster-whisper-large-v3"
REPO_LARGE_V3_TURBO="deepdml/faster-whisper-large-v3-turbo-ct2"
# The draft model. It transcribes nothing anyone keeps — it exists to put text
# on screen in under a second while the accurate model above decodes the
# utterance once at the end. 140 MB against 1.5 GB, and the single largest
# latency change this server has. See model.draft_path in docs/server-usage.md.
REPO_BASE="Systran/faster-whisper-base"
# A different conversion of the same weights, for --backend mlx. CTranslate2
# has no Metal backend, so on an Apple Silicon Mac this is the only way to the
# GPU — worth roughly seven times the decode speed. It holds config.json and
# weights.safetensors rather than model.bin, and the two formats are not
# interchangeable: each backend refuses the other's directory at startup.
REPO_LARGE_V3_TURBO_MLX="mlx-community/whisper-large-v3-turbo"
# A third conversion of the same weights, for --backend openvino. CTranslate2
# has no Intel GPU backend, so on a machine whose fastest processor is an Arc
# iGPU this is the only route off the CPU. These hold openvino_encoder_model.xml
# and its siblings rather than model.bin, and no two of the three formats are
# interchangeable: each backend refuses the others' directory at startup.
#
# Three precisions because which one to ship is a measurement, not an opinion.
# INT8 is the expected answer — smaller and faster than FP16, and usually
# indistinguishable in accuracy — but INT4 can cost more Korean accuracy than
# it buys in speed, and that has to be checked on the machine rather than
# assumed. openvino-benchmark.py measures all three.
REPO_LARGE_V3_TURBO_OV_INT8="OpenVINO/whisper-large-v3-turbo-int8-ov"
REPO_LARGE_V3_TURBO_OV_FP16="OpenVINO/whisper-large-v3-turbo-fp16-ov"
REPO_LARGE_V3_TURBO_OV_INT4="OpenVINO/whisper-large-v3-turbo-int4-ov"
SILERO_URL="https://raw.githubusercontent.com/snakers4/silero-vad/master/src/silero_vad/data/silero_vad.onnx"

# The file set differs between conversions: the large-v3 repos carry
# vocabulary.json and preprocessor_config.json, the smaller Systran ones ship
# vocabulary.txt and no preprocessor config. So the list is discovered from the
# repository and this is only the fallback for when the API is unreachable —
# which is the normal case behind an internal mirror.
FALLBACK_MODEL_FILES=(config.json model.bin preprocessor_config.json tokenizer.json vocabulary.json)

MODEL="large-v3"
REPO_OVERRIDE=""
METADATA_ONLY=0
FORCE=0
LIST_ONLY=0
VERIFY_ONLY=0

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '%s\n' "$*"; }
usage() { sed -n '3,40p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum   >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else die "need sha256sum or shasum on PATH"; fi
}

# `wc -c` pads with spaces on macOS, which would make every size read "unknown".
file_size() { wc -c < "$1" | tr -d '[:space:]'; }

human() {
  local bytes="$1"
  if [[ ! "$bytes" =~ ^[0-9]+$ ]]; then printf 'unknown'; return; fi
  awk -v b="$bytes" 'BEGIN{
    split("B KiB MiB GiB TiB", u, " ");
    i = 1; while (b >= 1024 && i < 5) { b /= 1024; i++ }
    printf (i == 1 ? "%d %s" : "%.1f %s"), b, u[i]
  }'
}

# Ask the repository what it actually contains, skipping repository furniture.
list_repo_files() {
  local repo="$1" listing
  listing="$(curl -sf --max-time 20 "$HF_ENDPOINT/api/models/$repo" 2>/dev/null || true)"
  if [[ -z "$listing" ]] || ! command -v python3 >/dev/null 2>&1; then
    printf '%s\n' "${FALLBACK_MODEL_FILES[@]}"
    return
  fi
  printf '%s' "$listing" | python3 -c '
import json, sys
skip = {".gitattributes", "README.md", ".gitignore", "SHA256SUMS"}
try:
    data = json.load(sys.stdin)
except ValueError:
    sys.exit(1)
names = [s["rfilename"] for s in data.get("siblings", [])]
names = [n for n in names if n not in skip and "/" not in n]
if not names:
    sys.exit(1)
print("\n".join(names))
' 2>/dev/null || printf '%s\n' "${FALLBACK_MODEL_FILES[@]}"
}

repo_for() {
  case "$1" in
    large-v3)       printf '%s' "${REPO_OVERRIDE:-$REPO_LARGE_V3}" ;;
    large-v3-turbo) printf '%s' "${REPO_OVERRIDE:-$REPO_LARGE_V3_TURBO}" ;;
    base)           printf '%s' "${REPO_OVERRIDE:-$REPO_BASE}" ;;
    large-v3-turbo-mlx) printf '%s' "${REPO_OVERRIDE:-$REPO_LARGE_V3_TURBO_MLX}" ;;
    large-v3-turbo-openvino-int8) printf '%s' "${REPO_OVERRIDE:-$REPO_LARGE_V3_TURBO_OV_INT8}" ;;
    large-v3-turbo-openvino-fp16) printf '%s' "${REPO_OVERRIDE:-$REPO_LARGE_V3_TURBO_OV_FP16}" ;;
    large-v3-turbo-openvino-int4) printf '%s' "${REPO_OVERRIDE:-$REPO_LARGE_V3_TURBO_OV_INT4}" ;;
    *) die "unknown model '$1' (expected: large-v3, large-v3-turbo, base, large-v3-turbo-mlx, large-v3-turbo-openvino-{int8,fp16,int4}, vad or all)" ;;
  esac
}

remote_size() {
  curl -sIL --max-time 30 "$1" | awk 'BEGIN{IGNORECASE=1} /^content-length:/{n=$2} END{gsub(/\r/,"",n); print n}'
}

download() {
  local url="$1" target="$2"
  if [[ -f "$target" && "$FORCE" != "1" ]]; then
    info "  = $(basename "$target") (already present)"
    return 0
  fi
  mkdir -p "$(dirname "$target")"
  # --continue-at resumes a half-finished multi-GB download instead of starting
  # over, which matters on the kind of link these files are usually pulled over.
  curl --fail --location --progress-bar --continue-at - \
       --output "$target.part" "$url" \
    || die "download failed: $url"
  mv "$target.part" "$target"
  info "  + $(basename "$target") ($(human "$(file_size "$target")"))"
}

fetch_model() {
  local model="$1" repo target
  repo="$(repo_for "$model")"
  target="$DEST/$model"
  info "$model  <-  $repo"

  local files=()
  while IFS= read -r line; do [[ -n "$line" ]] && files+=("$line"); done < <(list_repo_files "$repo")

  for file in "${files[@]}"; do
    if [[ "$file" == "model.bin" && "$METADATA_ONLY" == "1" ]]; then
      info "  ~ model.bin (skipped: --metadata-only)"
      continue
    fi
    download "$HF_ENDPOINT/$repo/resolve/main/$file" "$target/$file"
  done
  write_sums "$target"
}

fetch_vad() {
  info "silero VAD  <-  github.com/snakers4/silero-vad"
  download "$SILERO_URL" "$DEST/silero_vad.onnx"
}

write_sums() {
  local dir="$1"
  ( cd "$dir" && : > SHA256SUMS
    for file in *; do
      [[ "$file" == "SHA256SUMS" ]] && continue
      printf '%s  %s\n' "$(sha256_of "$file")" "$file" >> SHA256SUMS
    done )
  info "  wrote $dir/SHA256SUMS"
}

verify() {
  local failures=0 checked=0
  shopt -s nullglob
  for sums in "$DEST"/*/SHA256SUMS; do
    local dir; dir="$(dirname "$sums")"
    info "verifying $(basename "$dir")"
    while read -r expected file; do
      [[ -z "$file" ]] && continue
      checked=$((checked + 1))
      if [[ ! -f "$dir/$file" ]]; then
        printf '  MISSING %s\n' "$file"; failures=$((failures + 1)); continue
      fi
      local actual; actual="$(sha256_of "$dir/$file")"
      if [[ "$actual" == "$expected" ]]; then printf '  ok      %s\n' "$file"
      else printf '  CORRUPT %s\n' "$file"; failures=$((failures + 1)); fi
    done < "$sums"
  done
  if [[ -f "$DEST/silero_vad.onnx" ]]; then
    checked=$((checked + 1)); printf '  ok      silero_vad.onnx (%s)\n' "$(human "$(file_size "$DEST/silero_vad.onnx")")"
  fi
  [[ "$checked" -gt 0 ]] || die "nothing to verify under $DEST"
  [[ "$failures" -eq 0 ]] || die "$failures file(s) failed verification"
  info "all $checked file(s) verified"
}

list_sizes() {
  local total=0
  for model in large-v3 large-v3-turbo base large-v3-turbo-mlx \
               large-v3-turbo-openvino-int8 large-v3-turbo-openvino-fp16 \
               large-v3-turbo-openvino-int4; do
    local repo; repo="$(repo_for "$model")"
    printf '%-16s %s\n' "$model" "$repo"
    local files=()
    while IFS= read -r line; do [[ -n "$line" ]] && files+=("$line"); done < <(list_repo_files "$repo")
    for file in "${files[@]}"; do
      local size; size="$(remote_size "$HF_ENDPOINT/$repo/resolve/main/$file")"
      printf '  %-28s %10s\n' "$file" "$(human "$size")"
      [[ "$size" =~ ^[0-9]+$ ]] && total=$((total + size))
    done
  done
  local vad; vad="$(remote_size "$SILERO_URL")"
  printf '%-16s %s\n  %-28s %10s\n' "vad" "silero-vad" "silero_vad.onnx" "$(human "$vad")"
  [[ "$vad" =~ ^[0-9]+$ ]] && total=$((total + vad))
  printf '\n%-16s %10s  (everything above; `all` fetches every one but the MLX)\n' "total" "$(human "$total")"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    large-v3|large-v3-turbo|base|large-v3-turbo-mlx|vad|all) MODEL="$1" ;;
    large-v3-turbo-openvino-int8|large-v3-turbo-openvino-fp16|large-v3-turbo-openvino-int4) MODEL="$1" ;;
    --dest)          DEST="${2:?--dest needs a directory}"; shift ;;
    --repo)          REPO_OVERRIDE="${2:?--repo needs an id}"; shift ;;
    --metadata-only) METADATA_ONLY=1 ;;
    --force)         FORCE=1 ;;
    --list)          LIST_ONLY=1 ;;
    --verify)        VERIFY_ONLY=1 ;;
    -h|--help)       usage 0 ;;
    *) die "unexpected argument: $1" ;;
  esac
  shift
done

command -v curl >/dev/null 2>&1 || die "curl is required"

if [[ "$LIST_ONLY" == "1" ]]; then list_sizes; exit 0; fi
if [[ "$VERIFY_ONLY" == "1" ]]; then verify; exit 0; fi

mkdir -p "$DEST"
case "$MODEL" in
  vad) fetch_vad ;;
  all) fetch_model large-v3; fetch_model large-v3-turbo; fetch_model base; fetch_vad ;;
  *)   fetch_model "$MODEL"; fetch_vad ;;
esac

info ""
info "installed under $DEST"
# base is a draft model: it produces the live partial text and nothing that is
# kept, so it belongs on draft_path. Naming it as model.path would quietly
# downgrade every transcript the server commits.
if [[ "$MODEL" == large-v3-turbo-openvino-* ]]; then
  info ""
  info "an OpenVINO export, for an Intel GPU:"
  info "  model.path:                     $DEST/${MODEL}"
  info "  model.device:                   GPU"
  info "  streaming.silero_model_path:    $DEST/silero_vad.onnx"
  info "  run the server with:            --backend openvino"
  info ""
  info "measure it before trusting it — app/scripts/openvino-benchmark.py"
  info "reports decode time and real-time factor per precision on this machine."
elif [[ "$MODEL" == "large-v3-turbo-mlx" ]]; then
  info ""
  info "an MLX conversion, for the Apple Silicon GPU:"
  info "  model.path:                     $DEST/${MODEL}"
  info "  streaming.silero_model_path:    $DEST/silero_vad.onnx"
  info ""
  info "Start the server with --backend mlx (or LD_BACKEND=mlx), and clear"
  info "model.draft_path — on the GPU the accurate model is already fast"
  info "enough that a draft one buys nothing."
elif [[ "$MODEL" == "base" ]]; then
  info ""
  info "base is a draft model — it belongs on draft_path, not model.path:"
  info "  model.draft_path:               $DEST/base"
  info ""
  info "Leave model.path pointing at the accurate model. The draft one only"
  info "writes the partial text you watch appear; what you keep is decoded"
  info "once, at the end, by the model above it."
elif [[ "$MODEL" != "vad" ]]; then
  info ""
  info "put these two lines in your server config:"
  info "  model.path:                     $DEST/${MODEL/all/large-v3}"
  info "  streaming.silero_model_path:    $DEST/silero_vad.onnx"
fi
