#!/usr/bin/env python3
"""Long-context + tool-call stress test against a running llama-server endpoint.

Plants one unique fact around the middle of a large synthetic haystack
(~100k tokens by default) and requires the model to retrieve it AND reuse it
correctly as a tool-call argument in the same turn. This stresses long-range
attention precision (the model's sparse full-attention layers) and structured
tool output together, which the short quality-eval battery does not exercise.
"""
import argparse
import json
import random
import time
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path

NEEDLE_PATH = "/srv/dados/relatorio_final_XK472.csv"
NEEDLE_LINE = (
    "[2026-07-23 09:41:07] ATENCAO: arquivo critico localizado em "
    f"{NEEDLE_PATH} — necessario processar antes de prosseguir."
)

FILLER_TEMPLATES = [
    "[{ts}] arquivo processado: /var/cache/build/module_{n:04d}.obj — status ok",
    "[{ts}] verificacao de integridade concluida para pacote_{n:04d}.tar.gz",
    "[{ts}] limpeza de cache temporaria: {n} arquivos removidos de /tmp/build_{n:04d}",
    "[{ts}] job de compilacao #{n} finalizado em {n_mod}s, sem erros",
    "[{ts}] sincronizacao de indice concluida: {n} entradas atualizadas",
    "[{ts}] rotina de backup incremental executada para volume_{n:04d}",
]

TOOL_DEF = {
    "type": "function",
    "function": {
        "name": "abrir_arquivo",
        "description": "Abre um arquivo pelo caminho absoluto para processamento imediato.",
        "parameters": {
            "type": "object",
            "properties": {
                "caminho": {
                    "type": "string",
                    "description": "Caminho absoluto exato do arquivo a abrir.",
                }
            },
            "required": ["caminho"],
        },
    },
}


def build_haystack(target_chars, needle_fraction, seed):
    rnd = random.Random(seed)
    lines = []
    total_chars = 0
    n = 0
    needle_inserted = False
    while total_chars < target_chars:
        n += 1
        if not needle_inserted and total_chars >= target_chars * needle_fraction:
            lines.append(NEEDLE_LINE)
            total_chars += len(NEEDLE_LINE) + 1
            needle_inserted = True
            continue
        template = rnd.choice(FILLER_TEMPLATES)
        line = template.format(
            ts=f"2026-07-23 {rnd.randint(0, 23):02d}:{rnd.randint(0, 59):02d}:{rnd.randint(0, 59):02d}",
            n=n,
            n_mod=rnd.randint(1, 300),
        )
        lines.append(line)
        total_chars += len(line) + 1
    return "\n".join(lines)


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


def chat_with_tools(base_url, alias, haystack, max_tokens, timeout_s):
    system = (
        "Voce e um assistente que le logs de sistema e aciona ferramentas. "
        "Use a ferramenta abrir_arquivo assim que encontrar um arquivo marcado como critico no log."
    )
    user = (
        f"Log do sistema:\n{haystack}\n\n"
        "Fim do log. Chame a ferramenta abrir_arquivo com o caminho exato do arquivo "
        "critico mencionado no log acima."
    )
    payload = {
        "model": alias,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "tools": [TOOL_DEF],
        "tool_choice": "required",
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
        return json.loads(resp.read().decode("utf-8"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", required=True)
    parser.add_argument("--alias", required=True)
    parser.add_argument("--base-url", default="http://127.0.0.1:18380/v1")
    parser.add_argument("--cache-type-k", required=True)
    parser.add_argument("--cache-type-v", required=True)
    parser.add_argument("--context-size", type=int, required=True)
    parser.add_argument("--runtime", default="amd")
    parser.add_argument("--target-chars", type=int, default=420000, help="haystack filler size (~100k tokens at ~4.2 chars/token)")
    parser.add_argument("--needle-fraction", type=float, default=0.5, help="depth of the planted fact, 0-1")
    parser.add_argument("--seed", type=int, default=20260723)
    parser.add_argument("--max-tokens", type=int, default=1024)
    parser.add_argument("--startup-timeout", type=int, default=300)
    parser.add_argument("--request-timeout", type=int, default=420)
    parser.add_argument("--out-dir", default=str(Path(__file__).resolve().parent.parent / "benchmarks"))
    args = parser.parse_args()

    ready = wait_ready(args.base_url, args.startup_timeout)

    test = {
        "id": "long_context_tool_call_stress",
        "passed": False,
        "prompt_tokens": None,
        "completion_tokens": None,
        "needle_path": NEEDLE_PATH,
        "needle_fraction": args.needle_fraction,
        "tool_call": None,
        "raw_message": None,
        "error": None,
    }

    if ready:
        haystack = build_haystack(args.target_chars, args.needle_fraction, args.seed)
        try:
            body = chat_with_tools(args.base_url, args.alias, haystack, args.max_tokens, args.request_timeout)
            usage = body.get("usage", {})
            test["prompt_tokens"] = usage.get("prompt_tokens")
            test["completion_tokens"] = usage.get("completion_tokens")
            message = body["choices"][0]["message"]
            test["raw_message"] = message.get("content")
            tool_calls = message.get("tool_calls") or []
            if tool_calls:
                fn = tool_calls[0].get("function", {})
                test["tool_call"] = {"name": fn.get("name"), "arguments": fn.get("arguments")}
                if fn.get("name") == "abrir_arquivo":
                    try:
                        parsed_args = json.loads(fn.get("arguments") or "{}")
                        test["passed"] = parsed_args.get("caminho") == NEEDLE_PATH
                    except json.JSONDecodeError:
                        test["passed"] = False
        except Exception as exc:  # noqa: BLE001 - report the failure, don't crash the batch
            test["error"] = str(exc)

    report = {
        "profile": args.profile,
        "runtime": args.runtime,
        "context_size": args.context_size,
        "cache_type_k": args.cache_type_k,
        "cache_type_v": args.cache_type_v,
        "ready": ready,
        "test": test,
    }

    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    out_path = out_dir / f"stress-{args.profile}-{stamp}.json"
    out_path.write_text(json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")

    print(json.dumps(report, indent=2, ensure_ascii=False))
    print(f"Result: {out_path}")


if __name__ == "__main__":
    main()
