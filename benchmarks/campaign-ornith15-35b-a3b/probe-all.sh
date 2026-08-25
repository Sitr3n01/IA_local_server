#!/usr/bin/env bash
# Remote GGUF header probe for the Ornith-1.5-35B-A3B candidates.
# No tensor data is downloaded: the header and tensor-info block are at the head
# of the file, so architecture, tensor census and quantization mix are knowable
# for ~12 MB per artifact instead of 15 GB.
set -u
REPO="bartowski/Ornith-1.5-35B-A3B-GGUF"
REV="64b0493d34a5ca4c1b4ad67bb99b41d74b4f07d6"
OUT="/c/IA/IA_local_server/benchmarks/campaign-ornith15-35b-a3b"
PROBE="/c/IA/IA_local_server/scripts/v2/eval/gguf_probe.py"

declare -A FILES=(
  [iq2m]="Ornith-1.5-35B-A3B-IQ2_M.gguf"
  [q2k]="Ornith-1.5-35B-A3B-Q2_K.gguf"
  [iq3xxs]="Ornith-1.5-35B-A3B-IQ3_XXS.gguf"
  [q3ks]="Ornith-1.5-35B-A3B-Q3_K_S.gguf"
  [iq3xs]="Ornith-1.5-35B-A3B-IQ3_XS.gguf"
  [q3kxl]="Ornith-1.5-35B-A3B-Q3_K_XL.gguf"
  [iq4xs]="Ornith-1.5-35B-A3B-IQ4_XS.gguf"
)

for k in "${!FILES[@]}"; do
  f="${FILES[$k]}"
  echo "[probe] $k -> $f"
  python "$PROBE" "${REPO}::${f}" --revision "$REV" --json \
    --out "$OUT/probe-bartowski-ornith15-35b-${k}.json" \
    --census-out "$OUT/census/tensor-census-${k}.jsonl" > "$OUT/census/probe-${k}.stdout" 2> "$OUT/census/probe-${k}.err"
  echo "[done ] $k exit=$?"
done
echo "ALL PROBES FINISHED"
