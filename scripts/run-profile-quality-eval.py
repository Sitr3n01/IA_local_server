#!/usr/bin/env python3
"""Quality-eval battery against a running llama-server OpenAI-compatible endpoint.

Python port of run-profile-quality-eval.ps1 (same prompts, same grading, same
report shape) so results are directly comparable across profiles. This script
only talks HTTP to an already-running server; it does not start or stop
llama-server itself.
"""
import argparse
import json
import os
import re
import time
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path

TESTS = [
    {
        "id": "instruction_exact",
        "prompt": "Responda exatamente, sem aspas e sem pontuacao: ALFA-739",
        "expected": "ALFA-739",
        "mode": "exact",
    },
    {
        "id": "arithmetic",
        "prompt": "Quanto e (137 * 19) - 48? Responda apenas o numero.",
        "expected": "2555",
        "mode": "contains",
    },
    {
        "id": "list_reasoning",
        "prompt": "Em Python, qual e o resultado de sorted(set([5,2,5,4,2,6]))? Responda apenas a lista.",
        "expected": "[2, 4, 5, 6]",
        "mode": "contains",
    },
    {
        "id": "local_config_recall",
        "prompt": "Fatos: porta do painel=8090; alias do servidor=local-model; compactacao automatica=85%. Responda apenas: porta, alias, porcentagem.",
        "expected": "8090 local-model 85%",
        "mode": "all_terms",
        "terms": ["8090", "local-model", "85"],
    },
]

THINK_BLOCK_RE = re.compile(r"(?is)<think>.*?</think>")
STRAY_THINK_TAG_RE = re.compile(r"(?is)</think>")
PROMPT_TPS_RE = re.compile(r"prompt eval time =\s+[\d.]+ ms /\s+\d+ tokens.*?([\d.]+) tokens per second")
GEN_TPS_RE = re.compile(r"\beval time =\s+[\d.]+ ms /\s+\d+ tokens.*?([\d.]+) tokens per second")


def normalize_answer(text):
    if text is None:
        return ""
    text = THINK_BLOCK_RE.sub("", text)
    text = STRAY_THINK_TAG_RE.sub("", text)
    return text.strip()


def wait_ready(base_url, timeout_s):
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(f"{base_url}/models", timeout=2) as resp:
                if resp.status == 200:
                    return True
        except (urllib.error.URLError, TimeoutError, ConnectionError, OSError):
            pass
        time.sleep(1)
    return False


def chat(base_url, alias, prompt, max_tokens, timeout_s):
    payload = {
        "model": alias,
        "messages": [
            {"role": "system", "content": "Siga a instrucao do usuario com resposta curta e direta."},
            {"role": "user", "content": prompt},
        ],
        "max_tokens": max_tokens,
        "temperature": 0,
    }
    req = urllib.request.Request(
        f"{base_url}/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout_s) as resp:
        body = json.loads(resp.read().decode("utf-8"))
    return body["choices"][0]["message"]["content"]


def parse_tps_from_log(log_path):
    prompt_tps = None
    gen_tps = None
    with open(log_path, "r", encoding="utf-8", errors="replace") as fh:
        for line in fh:
            m = PROMPT_TPS_RE.search(line)
            if m:
                prompt_tps = float(m.group(1))
            m = GEN_TPS_RE.search(line)
            if m:
                gen_tps = float(m.group(1))
    return prompt_tps, gen_tps


def run_tests(base_url, alias, max_tokens, request_timeout):
    results = []
    for test in TESTS:
        raw = None
        answer = ""
        passed = False
        error = None
        try:
            raw = chat(base_url, alias, test["prompt"], max_tokens, request_timeout)
            answer = normalize_answer(raw)
            if test["mode"] == "exact":
                passed = answer == test["expected"]
            elif test["mode"] == "contains":
                passed = test["expected"] in answer
            elif test["mode"] == "all_terms":
                passed = all(term in answer for term in test["terms"])
        except Exception as exc:  # noqa: BLE001 - report is per-test, must not abort the batch
            error = str(exc)
        results.append({
            "id": test["id"],
            "passed": passed,
            "expected": test["expected"],
            "answer": answer,
            "raw": raw,
            "error": error,
        })
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", required=True, help="Profile id (for labeling and the report filename)")
    parser.add_argument("--alias", required=True, help="Model alias as served by llama-server (--alias flag)")
    parser.add_argument("--base-url", default="http://127.0.0.1:18380/v1")
    parser.add_argument("--cache-type-k", required=True)
    parser.add_argument("--cache-type-v", required=True)
    parser.add_argument("--context-size", type=int, required=True)
    parser.add_argument("--runtime", default="amd", choices=["amd", "unsloth"])
    parser.add_argument("--max-tokens", type=int, default=96)
    parser.add_argument("--startup-timeout", type=int, default=300)
    parser.add_argument("--request-timeout", type=int, default=180)
    parser.add_argument("--log-file", help="llama-server --log-file path, to extract prompt/gen tokens-per-second")
    parser.add_argument(
        "--out-dir",
        default=str(Path(__file__).resolve().parent.parent / "benchmarks"),
    )
    args = parser.parse_args()

    ready = wait_ready(args.base_url, args.startup_timeout)
    results = run_tests(args.base_url, args.alias, args.max_tokens, args.request_timeout) if ready else []

    prompt_tps, gen_tps = (None, None)
    if args.log_file and os.path.isfile(args.log_file):
        prompt_tps, gen_tps = parse_tps_from_log(args.log_file)

    passed_count = sum(1 for r in results if r["passed"])
    report = {
        "profile": args.profile,
        "runtime": args.runtime,
        "context_size": args.context_size,
        "cache_type_k": args.cache_type_k,
        "cache_type_v": args.cache_type_v,
        "ready": ready,
        "passed": passed_count,
        "total": len(TESTS),
        "prompt_tps_last": prompt_tps,
        "gen_tps_last": gen_tps,
        "tests": results,
    }

    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    out_path = out_dir / f"quality-{args.profile}-{stamp}-{os.getpid()}.json"
    out_path.write_text(json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")

    print(json.dumps(report, indent=2, ensure_ascii=False))
    print(f"Result: {out_path}")


if __name__ == "__main__":
    main()
