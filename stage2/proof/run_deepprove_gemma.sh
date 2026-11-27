#!/usr/bin/env bash
set -euo pipefail

# Run DeepProve's checked-in Gemma 3 prover and export the generated proof.
# The upstream benchmark verifies the proof before returning. The exported
# MessagePack object is an experiment artifact, not yet a PoML certificate,
# because it does not contain the VRF/HPKE binding relation or its statement.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKSPACE="${POML_DEEPPROVE_WORKSPACE:-${ROOT}/.cache/deep-prove}"
MODEL="${POML_DEEPPROVE_MODEL:-google/gemma-3-270m-it}"
SEQUENCE="${POML_DEEPPROVE_SEQUENCE:-8}"
THREADS="${POML_DEEPPROVE_THREADS:-16}"
OUTPUT="${POML_DEEPPROVE_OUTPUT:-${ROOT}/proof-runs/gemma3-270m}"
REVISION="${POML_DEEPPROVE_REVISION:-9d1a53e2ef49ffa2c902b8689cd3c58057a4e662}"
TOOLCHAIN="${RUSTUP_TOOLCHAIN:-nightly-2026-01-27}"

usage() {
  cat <<'EOF'
Usage: run_deepprove_gemma.sh [options]

Options:
  --model REF       Hugging Face model ID or local SafeTensors directory
  --sequence N      Maximum context and two generated tokens (default 8)
  --threads N       CPU threads, capped at 16 (default 16)
  --output DIR      Run artifacts (default proof-runs/gemma3-270m)
  --workspace DIR   DeepProve checkout and Cargo target directory
  --revision SHA    DeepProve source revision
EOF
}

while (($#)); do
  case "$1" in
    --model) MODEL="$2"; shift 2 ;;
    --sequence) SEQUENCE="$2"; shift 2 ;;
    --threads) THREADS="$2"; shift 2 ;;
    --output) OUTPUT="$2"; shift 2 ;;
    --workspace) WORKSPACE="$2"; shift 2 ;;
    --revision) REVISION="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if ! [[ "$THREADS" =~ ^[0-9]+$ ]] || ((THREADS < 1 || THREADS > 16)); then
  echo "--threads must be between 1 and 16" >&2
  exit 2
fi
if ! [[ "$SEQUENCE" =~ ^[0-9]+$ ]] || ((SEQUENCE < 2)); then
  echo "--sequence must be an integer of at least 2" >&2
  exit 2
fi

mkdir -p "$OUTPUT" "$(dirname "$WORKSPACE")"
if [[ ! -d "$WORKSPACE/.git" ]]; then
  git clone --filter=blob:none "https://github.com/Lagrange-Labs/deep-prove.git" "$WORKSPACE"
fi
git -C "$WORKSPACE" fetch --depth=1 origin "$REVISION"
git -C "$WORKSPACE" checkout --detach --quiet "$REVISION"
git -C "$WORKSPACE" reset --hard --quiet "$REVISION"

# The public benchmark does not expose its proof bytes. Add one small,
# deterministic export option to this pinned checkout instead of copying the
# whole upstream project into this repository.
WORKSPACE="$WORKSPACE" python3 - <<'PY'
from pathlib import Path
import os

path = Path(os.environ["WORKSPACE"]) / "zkml/src/bin/bench/llm.rs"
text = path.read_text(encoding="utf-8")
field = '    #[arg(long)]\n    proof_out: Option<String>,\n'
if "proof_out: Option<String>" not in text:
    marker = '    #[arg(long)]\n    memory: bool,\n'
    if marker not in text:
        raise SystemExit("DeepProve CLI layout changed around the memory flag")
    text = text.replace(marker, field + marker, 1)
old = '        let proof_size = rmp_serde::to_vec(&proof)?.len();\n        measure::set(HEADER_PROOF_SIZE, proof_size);\n'
new = '        let serialized_proof = rmp_serde::to_vec(&proof)?;\n        let proof_size = serialized_proof.len();\n        if let Some(ref output) = args.proof_out {\n            std::fs::write(output, &serialized_proof)?;\n        }\n        measure::set(HEADER_PROOF_SIZE, proof_size);\n'
if old in text:
    text = text.replace(old, new, 1)
elif "serialized_proof = rmp_serde::to_vec(&proof)?" not in text:
    raise SystemExit("DeepProve proof serialization site changed")
path.write_text(text, encoding="utf-8")
PY

export RUSTUP_TOOLCHAIN="$TOOLCHAIN"
export RUSTFLAGS="${RUSTFLAGS:-} --cfg tokio_unstable"
export CARGO_BUILD_JOBS="$THREADS"
export RAYON_NUM_THREADS="$THREADS"
export OMP_NUM_THREADS="$THREADS"
export MKL_NUM_THREADS="$THREADS"
export OPENBLAS_NUM_THREADS="$THREADS"

# Rebuild after applying the small proof-export patch. Cargo reuses all
# unchanged dependencies, so this is quick after the first checkout.
(cd "$WORKSPACE" && taskset -c "0-$((THREADS - 1))" cargo build --release -p zkml --bin bench-llm)

rm -f "$OUTPUT/bench.csv" "$OUTPUT/proof.msgpack" "$OUTPUT/time.log"
set +e
(cd "$WORKSPACE" && /usr/bin/time -v taskset -c "0-$((THREADS - 1))" \
  "$WORKSPACE/target/release/bench-llm" \
  --model gemma3 \
  --hf "$MODEL" \
  --sequence "$SEQUENCE" \
  --num-threads "$THREADS" \
  --bench "$OUTPUT/bench.csv" \
  --proof-out "$OUTPUT/proof.msgpack" \
  >"$OUTPUT/run.log" 2>"$OUTPUT/time.log")
status=$?
set -e
if ((status != 0)); then
  echo "DeepProve failed. See $OUTPUT/run.log and $OUTPUT/time.log" >&2
  exit "$status"
fi

OUTPUT="$OUTPUT" MODEL="$MODEL" REVISION="$REVISION" SEQUENCE="$SEQUENCE" THREADS="$THREADS" python3 - <<'PY'
import csv
import json
import os
from pathlib import Path

output = Path(os.environ["OUTPUT"])
with (output / "bench.csv").open(newline="", encoding="utf-8") as handle:
    rows = list(csv.DictReader(handle))
manifest = {
    "backend": "deep-prove",
    "source_revision": os.environ["REVISION"],
    "model": os.environ["MODEL"],
    "sequence": int(os.environ["SEQUENCE"]),
    "threads": int(os.environ["THREADS"]),
    "cpu_affinity": f"0-{int(os.environ['THREADS']) - 1}",
    "gpu": "none",
    "proof_verified_in_process": True,
    "proof_artifact": "proof.msgpack",
    "proof_artifact_bytes": (output / "proof.msgpack").stat().st_size,
    "bench_rows": rows,
    "poml_complete_relation": False,
    "relation_scope": "DeepProve Gemma inference and next-token argmax",
    "binding_scope": "VRF, output commitment and HPKE are not included",
    "warning": "DeepProve is not audited or production-ready according to its upstream README.",
}
(output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
print(json.dumps(manifest, indent=2))
PY
