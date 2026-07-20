#!/usr/bin/env python3
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path


BASE_URL = os.environ.get("LOCAL_LLAMA_BASE_URL", "http://127.0.0.1:8080/v1").rstrip("/")
PANEL_URL = os.environ.get("LOCAL_LLAMA_PANEL_URL", "http://127.0.0.1:8090").rstrip("/")
STATE_PATH = Path(__file__).with_name("context-state.json")
ROOT = Path(__file__).resolve().parents[1]
DEFAULT_MODEL = os.environ.get("LOCAL_LLAMA_MODEL", "local-model")
DEFAULT_PROFILE = os.environ.get("LOCAL_LLAMA_DEFAULT_PROFILE", "ornith10-9b-q4km-kv-q4-128k")
DEFAULT_RUNTIME = os.environ.get("LOCAL_LLAMA_DEFAULT_RUNTIME", "amd")
DEFAULT_THRESHOLD = float(os.environ.get("LOCAL_LLAMA_COMPACTION_THRESHOLD", "0.85"))
DEFAULT_CONTEXT_LIMIT = int(os.environ.get("LOCAL_LLAMA_CONTEXT_LIMIT", "131072"))
AUTOSTART = os.environ.get("LOCAL_LLAMA_AUTOSTART", "1").lower() in ("1", "true", "yes", "on")
AUTOSTART_PANEL = os.environ.get("LOCAL_LLAMA_AUTOSTART_PANEL", "1").lower() in ("1", "true", "yes", "on")
AUTOSTART_TIMEOUT = int(os.environ.get("LOCAL_LLAMA_AUTOSTART_TIMEOUT", "300"))
SOTA_URL = os.environ.get("LOCAL_LLAMA_SOTA_URL", "https://api.openai.com/v1").rstrip("/")
SOTA_MODEL = os.environ.get("LOCAL_LLAMA_SOTA_MODEL", "gpt-5")
SOTA_API_KEY = os.environ.get("LOCAL_LLAMA_SOTA_API_KEY") or os.environ.get("OPENAI_API_KEY")

SESSIONS = {}


def send(message):
    sys.stdout.write(json.dumps(message, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def request_json(method, base_url, path, payload=None, timeout=120, api_key=None):
    data = None
    headers = {"Content-Type": "application/json"}
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(base_url + path, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as res:
        raw = res.read().decode("utf-8")
        return json.loads(raw) if raw else {}


def try_request_json(method, base_url, path, payload=None, timeout=5):
    try:
        return request_json(method, base_url, path, payload=payload, timeout=timeout)
    except Exception:
        return None


def start_panel_background():
    panel_path = ROOT / "control" / "local_llama_panel.py"
    if not panel_path.exists():
        return
    kwargs = {
        "stdin": subprocess.DEVNULL,
        "stdout": subprocess.DEVNULL,
        "stderr": subprocess.DEVNULL,
    }
    if os.name == "nt":
        kwargs["creationflags"] = subprocess.CREATE_NO_WINDOW
    subprocess.Popen([sys.executable, str(panel_path), "--host", "127.0.0.1", "--port", "8090"], **kwargs)


def ensure_panel_ready():
    if try_request_json("GET", PANEL_URL, "/api/status", timeout=3) is not None:
        return True
    if not AUTOSTART_PANEL:
        return False
    start_panel_background()
    for _ in range(30):
        time.sleep(1)
        if try_request_json("GET", PANEL_URL, "/api/status", timeout=3) is not None:
            return True
    return False


def ensure_model_server():
    if try_request_json("GET", BASE_URL, "/models", timeout=3) is not None:
        return
    if not AUTOSTART:
        raise RuntimeError(f"Local model server is not reachable at {BASE_URL}.")
    if not ensure_panel_ready():
        raise RuntimeError(f"Local model panel is not reachable at {PANEL_URL}; cannot autostart {DEFAULT_PROFILE}.")
    payload = {
        "profile_id": DEFAULT_PROFILE,
        "runtime": DEFAULT_RUNTIME,
        "alias": DEFAULT_MODEL,
    }
    request_json("POST", PANEL_URL, "/api/model/start", payload=payload, timeout=15)
    for _ in range(AUTOSTART_TIMEOUT):
        time.sleep(1)
        if try_request_json("GET", BASE_URL, "/models", timeout=3) is not None:
            return
    raise RuntimeError(f"Timed out waiting for local model profile {DEFAULT_PROFILE} at {BASE_URL}.")


def ensure_panel_or_raise():
    if not ensure_panel_ready():
        raise RuntimeError(f"Local model panel is not reachable at {PANEL_URL}.")


def panel_request(method, path, payload=None, timeout=30):
    ensure_panel_or_raise()
    return request_json(method, PANEL_URL, path, payload=payload, timeout=timeout)


def text_of_message(message):
    content = message.get("content", "")
    if isinstance(content, str):
        return content
    return json.dumps(content, ensure_ascii=False)


def estimate_tokens_text(text):
    if not text:
        return 0
    return max(1, int(len(text) / 4))


def estimate_tokens_messages(messages):
    return sum(estimate_tokens_text(text_of_message(m)) + 6 for m in messages)


def bounded_tail(messages, token_budget=4096):
    selected = []
    used = 0
    for msg in reversed(messages):
        cost = estimate_tokens_text(text_of_message(msg)) + 6
        if used + cost <= token_budget:
            selected.append(msg)
            used += cost
            continue
        if not selected:
            copy = dict(msg)
            content = text_of_message(copy)
            keep_chars = max(1000, token_budget * 4)
            copy["content"] = (
                "[TAIL_MESSAGE_TRUNCATED_BY_COMPACTION_BUDGET]\n"
                + content[-keep_chars:]
            )
            selected.append(copy)
        break
    return list(reversed(selected))


def clean_model_text(text):
    text = re.sub(r"(?is)<think>.*?</think>", "", text or "")
    text = text.replace("<think>", "").replace("</think>", "")
    return text.strip()


def session(name):
    if name not in SESSIONS:
        SESSIONS[name] = {
            "summary": "",
            "history": [],
            "created_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "last_compaction": None,
            "last_compaction_notice": None,
        }
        write_state()
    return SESSIONS[name]


def state_snapshot():
    sessions = {}
    for name, data in SESSIONS.items():
        tokens = estimate_tokens_text(data.get("summary", "")) + estimate_tokens_messages(data.get("history", []))
        context_limit = int(data.get("context_limit") or DEFAULT_CONTEXT_LIMIT)
        ratio = tokens / context_limit if context_limit else 0
        sessions[name] = {
            "estimated_tokens": tokens,
            "context_limit": context_limit,
            "usage_ratio": round(ratio, 4),
            "warning_threshold": 0.75,
            "compact_threshold": DEFAULT_THRESHOLD,
            "force_threshold": 0.90,
            "warning": ratio >= 0.75,
            "compact_now": ratio >= DEFAULT_THRESHOLD,
            "force_compact": ratio >= 0.90,
            "history_messages": len(data.get("history", [])),
            "summary_preview": data.get("summary", "")[:1200],
            "created_at": data.get("created_at"),
            "last_compaction": data.get("last_compaction"),
            "last_compaction_notice": data.get("last_compaction_notice"),
        }
    return {
        "config": {
            "base_url": BASE_URL,
            "panel_url": PANEL_URL,
            "default_model": DEFAULT_MODEL,
            "default_profile": DEFAULT_PROFILE,
            "default_runtime": DEFAULT_RUNTIME,
            "autostart": AUTOSTART,
            "autostart_panel": AUTOSTART_PANEL,
            "default_threshold": DEFAULT_THRESHOLD,
            "default_context_limit": DEFAULT_CONTEXT_LIMIT,
            "sota_url": SOTA_URL,
            "sota_model": SOTA_MODEL,
            "sota_configured": bool(SOTA_API_KEY),
        },
        "sessions": sessions,
        "updated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
    }


def write_state():
    STATE_PATH.write_text(json.dumps(state_snapshot(), indent=2), encoding="utf-8")


def load_state():
    try:
        raw = json.loads(STATE_PATH.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError):
        write_state()
        return
    for name, meta in raw.get("sessions", {}).items():
        if name not in SESSIONS:
            SESSIONS[name] = {
                "summary": meta.get("summary_preview", ""),
                "history": [],
                "created_at": meta.get("created_at") or time.strftime("%Y-%m-%dT%H:%M:%S%z"),
                "last_compaction": meta.get("last_compaction"),
                "last_compaction_notice": meta.get("last_compaction_notice"),
            }


def build_session_messages(data, incoming):
    messages = []
    system_parts = []
    if data.get("last_compaction_notice"):
        system_parts.append(data["last_compaction_notice"])
    if data.get("summary"):
        system_parts.append("Resumo persistente da conversa anterior. Use como contexto, sem repetir ao usuario:\n" + data["summary"])
    if system_parts:
        messages.append({
            "role": "system",
            "content": "\n\n".join(system_parts),
        })
    messages.extend(data.get("history", []))
    messages.extend(incoming)
    return messages


def chat_completion(messages, model=DEFAULT_MODEL, temperature=0.2, max_tokens=512, timeout=180):
    ensure_model_server()
    payload = {
        "model": model,
        "messages": messages,
        "temperature": temperature,
        "max_tokens": max_tokens,
    }
    data = request_json("POST", BASE_URL, "/chat/completions", payload, timeout=timeout)
    choice = (data.get("choices") or [{}])[0]
    msg = choice.get("message", {})
    text = msg.get("content") or msg.get("reasoning_content") or json.dumps(data, indent=2)
    text = clean_model_text(text)
    return text, data


def compact_local(session_id, model=DEFAULT_MODEL, max_summary_tokens=1024):
    data = session(session_id)
    if not data.get("history") and not data.get("summary"):
        data["last_compaction"] = {"layer": "local", "status": "skipped-empty", "at": time.strftime("%Y-%m-%dT%H:%M:%S%z")}
        write_state()
        return "Nada para compactar."

    transcript = []
    if data.get("summary"):
        transcript.append("RESUMO ANTERIOR:\n" + data["summary"])
    for msg in data.get("history", []):
        transcript.append(f"{msg.get('role', 'user').upper()}:\n{text_of_message(msg)}")
    joined = "\n\n".join(transcript)
    # Keep the compaction prompt bounded so it can recover from a full session.
    joined = joined[-60000:]
    messages = [
        {
            "role": "system",
            "content": (
                "Voce compacta contexto para agentes de codigo. Preserve estado operacional verificavel. "
                "Nao invente fatos. Se algo estiver ambiguo, marque como incerto."
            ),
        },
        {
            "role": "user",
            "content": (
                "/no_think\n"
                "Compacte o historico abaixo em um resumo operacional estruturado para continuar a tarefa sem perda critica.\n"
                "Responda somente com secoes curtas nestes titulos exatos:\n"
                "OBJETIVO ATUAL\nDECISOES E ASSUMPTIONS\nARQUIVOS E ESTADO DO CODIGO\nCOMANDOS E RESULTADOS\nERROS E RISCOS\nRESTRICOES CRITICAS\nPENDENCIAS\nPROXIMOS PASSOS\n"
                "Preserve instrucoes criticas, fatos verificaveis, caminhos, portas, modelos, flags, resultados de testes e estado do ambiente.\n\n"
                + joined
            ),
        },
    ]
    before_tokens = estimate_tokens_text(joined)
    summary, _ = chat_completion(messages, model=model, temperature=0.0, max_tokens=max_summary_tokens, timeout=240)
    tail = bounded_tail(data.get("history", []), token_budget=int(os.environ.get("LOCAL_LLAMA_COMPACTION_TAIL_TOKENS", "4096")))
    data["summary"] = summary.strip()
    data["history"] = tail
    after_tokens = estimate_tokens_text(data["summary"]) + estimate_tokens_messages(tail)
    data["last_compaction"] = {
        "layer": "local",
        "status": "ok",
        "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "model": model,
        "input_estimated_tokens": before_tokens,
        "output_estimated_tokens": after_tokens,
        "kept_tail_messages": len(tail),
    }
    data["last_compaction_notice"] = (
        "CONTEXT_COMPACTION_OCCURRED=true\n"
        "compaction_layer=local\n"
        f"compaction_model={model}\n"
        f"timestamp={data['last_compaction']['at']}\n"
        f"tokens_before_estimated={before_tokens}\n"
        f"tokens_after_estimated={after_tokens}\n"
        "known_limitations=Resumo gerado por modelo local; audite inconsistencias contra arquivos, comandos e estado atual quando houver duvida.\n"
        "instruction=Use o resumo operacional como memoria compactada e corrija possiveis perdas antes de executar passos arriscados."
    )
    write_state()
    return data["summary"]


def compact_sota(session_id, max_summary_tokens=1536):
    if not SOTA_API_KEY:
        raise RuntimeError("SOTA fallback is not configured. Set LOCAL_LLAMA_SOTA_API_KEY or OPENAI_API_KEY to enable layer 2 compaction.")
    data = session(session_id)
    transcript = []
    if data.get("summary"):
        transcript.append("PRIOR SUMMARY:\n" + data["summary"])
    for msg in data.get("history", []):
        transcript.append(f"{msg.get('role', 'user').upper()}:\n{text_of_message(msg)}")
    joined = "\n\n".join(transcript)[-120000:]
    payload = {
        "model": SOTA_MODEL,
        "messages": [
            {"role": "system", "content": "Compact long agent context into a dense operational state summary. Preserve decisions, files, commands, errors, facts, constraints, and next actions. Audit and correct any loss from prior local compaction notices."},
            {"role": "user", "content": joined},
        ],
        "temperature": 0,
        "max_tokens": max_summary_tokens,
    }
    result = request_json("POST", SOTA_URL, "/chat/completions", payload, timeout=240, api_key=SOTA_API_KEY)
    choice = (result.get("choices") or [{}])[0]
    summary = (choice.get("message", {}) or {}).get("content", "").strip()
    if not summary:
        raise RuntimeError("SOTA fallback returned an empty summary.")
    tail = bounded_tail(data.get("history", []), token_budget=int(os.environ.get("LOCAL_LLAMA_COMPACTION_TAIL_TOKENS", "4096")))
    data["summary"] = summary
    data["history"] = tail
    data["last_compaction"] = {
        "layer": "sota",
        "status": "ok",
        "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "model": SOTA_MODEL,
        "kept_tail_messages": len(tail),
    }
    data["last_compaction_notice"] = (
        "CONTEXT_COMPACTION_OCCURRED=true\n"
        "compaction_layer=sota\n"
        f"compaction_model={SOTA_MODEL}\n"
        f"timestamp={data['last_compaction']['at']}\n"
        "known_limitations=Resumo remoto pode ter corrigido ou reinterpretado uma compactacao local anterior; valide fatos criticos antes de acoes destrutivas.\n"
        "instruction=Informe ao modelo executor que houve compactacao e preserve continuidade operacional."
    )
    write_state()
    return summary


def maybe_compact(session_id, messages, arguments):
    data = session(session_id)
    context_limit = int(arguments.get("context_limit_tokens") or DEFAULT_CONTEXT_LIMIT)
    threshold = float(arguments.get("compaction_threshold") or DEFAULT_THRESHOLD)
    estimated = estimate_tokens_text(data.get("summary", "")) + estimate_tokens_messages(data.get("history", [])) + estimate_tokens_messages(messages)
    data["context_limit"] = context_limit
    data["last_estimated_tokens"] = estimated
    data["last_usage_ratio"] = round(estimated / context_limit, 4) if context_limit else 0
    write_state()
    if estimated < int(context_limit * threshold):
        return None
    try:
        return compact_local(session_id, model=arguments.get("model", DEFAULT_MODEL))
    except Exception:
        if arguments.get("use_sota_fallback"):
            return compact_sota(session_id)
        raise


TOOLS = [
    {
        "name": "local_model_health",
        "description": "Check whether the local llama.cpp OpenAI-compatible server is reachable.",
        "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
    },
    {
        "name": "local_model_models",
        "description": "List models exposed by the local llama.cpp server.",
        "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
    },
    {
        "name": "local_model_chat",
        "description": "Send stateless chat messages to the local model through llama.cpp.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "messages": {"type": "array", "items": {"type": "object"}, "minItems": 1},
                "model": {"type": "string", "default": DEFAULT_MODEL},
                "temperature": {"type": "number", "default": 0.2},
                "max_tokens": {"type": "integer", "default": 512},
            },
            "required": ["messages"],
            "additionalProperties": False,
        },
    },
    {
        "name": "local_model_session_chat",
        "description": "Send chat messages with MCP-managed session memory and automatic context compaction.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "session_id": {"type": "string", "default": "default"},
                "messages": {"type": "array", "items": {"type": "object"}, "minItems": 1},
                "model": {"type": "string", "default": DEFAULT_MODEL},
                "temperature": {"type": "number", "default": 0.2},
                "max_tokens": {"type": "integer", "default": 512},
                "auto_compact": {"type": "boolean", "default": True},
                "context_limit_tokens": {"type": "integer", "default": DEFAULT_CONTEXT_LIMIT},
                "compaction_threshold": {"type": "number", "default": DEFAULT_THRESHOLD},
                "use_sota_fallback": {"type": "boolean", "default": False},
            },
            "required": ["messages"],
            "additionalProperties": False,
        },
    },
    {
        "name": "local_model_compact",
        "description": "Compact one MCP session now using the local model or optional SOTA fallback.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "session_id": {"type": "string", "default": "default"},
                "layer": {"type": "string", "enum": ["local", "sota", "auto"], "default": "local"},
                "max_summary_tokens": {"type": "integer", "default": 1024},
            },
            "additionalProperties": False,
        },
    },
    {
        "name": "local_model_context_status",
        "description": "Return session memory and compaction status.",
        "inputSchema": {
            "type": "object",
            "properties": {"session_id": {"type": "string"}},
            "additionalProperties": False,
        },
    },
    {
        "name": "local_model_profiles",
        "description": "List configured local executor profiles known by the control panel.",
        "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
    },
    {
        "name": "local_model_start_profile",
        "description": "Start or switch the local executor to a configured profile. This is the linked selector for tray, panel, and MCP clients.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "profile_id": {"type": "string", "default": DEFAULT_PROFILE},
                "runtime": {"type": "string", "enum": ["amd", "unsloth"]},
                "alias": {"type": "string", "default": DEFAULT_MODEL},
            },
            "additionalProperties": False,
        },
    },
    {
        "name": "local_model_stop",
        "description": "Stop the currently running local executor through the control panel.",
        "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
    },
]


def call_tool(name, arguments):
    try:
        arguments = arguments or {}
        if name == "local_model_health":
            ensure_model_server()
            request_json("GET", BASE_URL, "/models", timeout=5)
            return [{"type": "text", "text": "ok"}], False
        if name == "local_model_models":
            ensure_model_server()
            data = request_json("GET", BASE_URL, "/models", timeout=10)
            return [{"type": "text", "text": json.dumps(data, indent=2)}], False
        if name == "local_model_chat":
            text, _ = chat_completion(
                arguments["messages"],
                model=arguments.get("model", DEFAULT_MODEL),
                temperature=arguments.get("temperature", 0.2),
                max_tokens=arguments.get("max_tokens", 512),
            )
            return [{"type": "text", "text": text}], False
        if name == "local_model_session_chat":
            session_id = arguments.get("session_id", "default")
            incoming = arguments["messages"]
            data = session(session_id)
            compaction_note = None
            if arguments.get("auto_compact", True):
                compaction_note = maybe_compact(session_id, incoming, arguments)
                data = session(session_id)
            messages = build_session_messages(data, incoming)
            text, _ = chat_completion(
                messages,
                model=arguments.get("model", DEFAULT_MODEL),
                temperature=arguments.get("temperature", 0.2),
                max_tokens=arguments.get("max_tokens", 512),
            )
            data["history"].extend(incoming)
            data["history"].append({"role": "assistant", "content": text})
            write_state()
            prefix = "[context compacted]\n" if compaction_note else ""
            return [{"type": "text", "text": prefix + text}], False
        if name == "local_model_compact":
            session_id = arguments.get("session_id", "default")
            layer = arguments.get("layer", "local")
            if layer == "sota":
                summary = compact_sota(session_id, max_summary_tokens=arguments.get("max_summary_tokens", 1536))
            else:
                try:
                    summary = compact_local(session_id, max_summary_tokens=arguments.get("max_summary_tokens", 1024))
                except Exception:
                    if layer == "auto":
                        summary = compact_sota(session_id, max_summary_tokens=arguments.get("max_summary_tokens", 1536))
                    else:
                        raise
            return [{"type": "text", "text": summary}], False
        if name == "local_model_context_status":
            snapshot = state_snapshot()
            sid = arguments.get("session_id")
            if sid:
                snapshot["sessions"] = {sid: snapshot["sessions"].get(sid)}
            write_state()
            return [{"type": "text", "text": json.dumps(snapshot, indent=2)}], False
        if name == "local_model_profiles":
            data = panel_request("GET", "/api/profiles", timeout=10)
            status = try_request_json("GET", PANEL_URL, "/api/status", timeout=5)
            if status is not None:
                data["current_profile"] = status.get("current_profile")
                data["active_model"] = status.get("model", {}).get("active")
            return [{"type": "text", "text": json.dumps(data, indent=2)}], False
        if name == "local_model_start_profile":
            payload = {
                "profile_id": arguments.get("profile_id") or DEFAULT_PROFILE,
                "alias": arguments.get("alias") or DEFAULT_MODEL,
            }
            if arguments.get("runtime"):
                payload["runtime"] = arguments["runtime"]
            data = panel_request("POST", "/api/model/start", payload=payload, timeout=20)
            for _ in range(AUTOSTART_TIMEOUT):
                time.sleep(1)
                if try_request_json("GET", BASE_URL, "/models", timeout=3) is not None:
                    break
            return [{"type": "text", "text": json.dumps(data, indent=2)}], False
        if name == "local_model_stop":
            data = panel_request("POST", "/api/model/stop", payload={}, timeout=15)
            return [{"type": "text", "text": json.dumps(data, indent=2)}], False
        return [{"type": "text", "text": f"Unknown tool: {name}"}], True
    except (urllib.error.URLError, TimeoutError, KeyError, json.JSONDecodeError, RuntimeError) as exc:
        write_state()
        return [{"type": "text", "text": f"local llama.cpp call failed: {exc}"}], True


def handle(message):
    method = message.get("method")
    req_id = message.get("id")

    if method == "initialize":
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "protocolVersion": "2024-11-05",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "local-llama-mcp", "version": "0.2.0"},
            },
        })
        return

    if method == "notifications/initialized":
        return

    if method == "tools/list":
        send({"jsonrpc": "2.0", "id": req_id, "result": {"tools": TOOLS}})
        return

    if method == "tools/call":
        params = message.get("params", {})
        content, is_error = call_tool(params.get("name"), params.get("arguments") or {})
        send({"jsonrpc": "2.0", "id": req_id, "result": {"content": content, "isError": is_error}})
        return

    if req_id is not None:
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "error": {"code": -32601, "message": f"Method not found: {method}"},
        })


def main():
    load_state()
    for line in sys.stdin:
        line = line.strip().lstrip("\ufeff")
        if not line:
            continue
        try:
            handle(json.loads(line))
        except json.JSONDecodeError as exc:
            if line.startswith("{"):
                send({"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": str(exc)}})


if __name__ == "__main__":
    main()
