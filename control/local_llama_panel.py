#!/usr/bin/env python3
import argparse
import json
import os
import queue
import socket
import subprocess
import sys
import threading
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPTS_DIR = ROOT / "scripts"
MCP_DIR = ROOT / "mcp"
LOG_DIR = ROOT / "logs"
STATE_DIR = ROOT / "state"
STATIC_DIR = Path(__file__).resolve().parent / "static"
MATRIX_PATH = ROOT / "model-test-matrix.json"
CONTROL_STATE_PATH = STATE_DIR / "control-state.json"
CURRENT_PROFILE_PATH = STATE_DIR / "current-profile.json"
MCP_STATE_PATH = MCP_DIR / "context-state.json"
UNSLOTH_CATALOG_ROOT = Path("C:/IA/unsloth-catalog")
CURRENT_CATALOG_DIR = UNSLOTH_CATALOG_ROOT / "00 CURRENT - local-model executor"

DEFAULT_PROFILE_ID = "ornith10-9b-q4km-kv-q4-128k"
SMALL_PROFILE_ID = "qwen35-4b-q4km-kv-q4-64k"
LLAMA_HOST = "127.0.0.1"
LLAMA_PORT = 8080
SMALL_LLAMA_PORT = 8081
LLAMA_BASE_URL = f"http://{LLAMA_HOST}:{LLAMA_PORT}/v1"
SMALL_LLAMA_BASE_URL = f"http://{LLAMA_HOST}:{SMALL_LLAMA_PORT}/v1"
OPENAI_FALLBACK_BASE_URL = os.environ.get("LOCAL_LLAMA_OPENAI_FALLBACK_BASE_URL", "https://api.openai.com/v1").rstrip("/")
PANEL_MODEL_ALIAS = "local-model"
SMALL_MODEL_ALIAS = "local-small-model"
DEFAULT_CONTEXT_LIMIT = 131072


def now_stamp():
    return time.strftime("%Y%m%d-%H%M%S")


def log_exception(context, exc):
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    with (LOG_DIR / "panel-error.log").open("a", encoding="utf-8", errors="replace") as f:
        f.write(f"[{time.strftime('%Y-%m-%dT%H:%M:%S%z')}] {context}\n")
        f.write("".join(traceback.format_exception(type(exc), exc, exc.__traceback__)))
        f.write("\n")


def read_json(path, default):
    try:
        return json.loads(Path(path).read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError):
        return default


def write_json(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2), encoding="utf-8")


def resolve_model_path(local_path):
    path = Path(local_path)
    if path.is_absolute():
        return path.resolve()
    return (ROOT / path).resolve()


def pid_alive(pid):
    if not pid:
        return False
    if os.name == "nt":
        result = subprocess.run(
            ["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        return str(pid) in result.stdout
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def kill_tree(pid):
    if not pid:
        return
    if os.name == "nt":
        subprocess.run(["taskkill", "/PID", str(pid), "/T", "/F"], capture_output=True, text=True, timeout=15)
    else:
        try:
            os.kill(pid, 15)
        except OSError:
            pass


def tail_file(path, lines=200):
    path = Path(path)
    if not path.exists():
        return ""
    data = path.read_text(encoding="utf-8", errors="replace").splitlines()
    return "\n".join(data[-max(1, min(lines, 2000)):])


def latest_file(pattern):
    matches = sorted(LOG_DIR.glob(pattern), key=lambda p: p.stat().st_mtime, reverse=True)
    return matches[0] if matches else None


def http_json(method, url, payload=None, timeout=10):
    data = None
    headers = {"Content-Type": "application/json"}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as res:
        raw = res.read().decode("utf-8")
        return json.loads(raw) if raw else {}


def http_text(method, url, timeout=5):
    req = urllib.request.Request(url, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as res:
        return res.read().decode("utf-8", errors="replace")


def parse_metrics(text):
    values = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#") or " " not in line:
            continue
        key, value = line.rsplit(" ", 1)
        key = key.split("{", 1)[0]
        try:
            values[key] = float(value)
        except ValueError:
            pass
    prompt_tokens = values.get("llamacpp:prompt_tokens_total", 0.0)
    predicted_tokens = values.get("llamacpp:tokens_predicted_total", 0.0)
    prompt_seconds = values.get("llamacpp:prompt_tokens_seconds", 0.0)
    predicted_seconds = values.get("llamacpp:predicted_tokens_seconds", 0.0)
    total_seconds = prompt_seconds + predicted_seconds
    total_tokens = prompt_tokens + predicted_tokens
    return {
        "prompt_tokens_total": int(prompt_tokens),
        "predicted_tokens_total": int(predicted_tokens),
        "tokens_total": int(total_tokens),
        "requests_processing": int(values.get("llamacpp:requests_processing", 0.0)),
        "requests_deferred": int(values.get("llamacpp:requests_deferred", 0.0)),
        "tokens_per_second": round(total_tokens / total_seconds, 2) if total_seconds > 0 else None,
        "prompt_tokens_per_second": round(prompt_tokens / prompt_seconds, 2) if prompt_seconds > 0 else None,
        "predicted_tokens_per_second": round(predicted_tokens / predicted_seconds, 2) if predicted_seconds > 0 else None,
    }


def port_open(host, port, timeout=0.25):
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def responses_content_text(content):
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = []
        for part in content:
            if isinstance(part, str):
                parts.append(part)
            elif isinstance(part, dict):
                text = part.get("text") or part.get("output_text") or part.get("input_text")
                if text is not None:
                    parts.append(str(text))
        return "\n".join(parts)
    return str(content)


def responses_input_to_chat_messages(body):
    messages = []
    instructions = body.get("instructions")
    if instructions:
        messages.append({"role": "system", "content": str(instructions)})

    items = body.get("input")
    if isinstance(items, str):
        messages.append({"role": "user", "content": items})
        return messages
    if not isinstance(items, list):
        return messages

    for item in items:
        if isinstance(item, str):
            messages.append({"role": "user", "content": item})
            continue
        if not isinstance(item, dict):
            continue

        item_type = item.get("type")
        if item_type == "function_call_output":
            message = {
                "role": "tool",
                "content": responses_content_text(item.get("output")),
                "tool_call_id": item.get("call_id") or item.get("id") or "call_local",
            }
            messages.append(message)
            continue

        if item_type == "function_call":
            call_id = item.get("call_id") or item.get("id") or "call_local"
            messages.append({
                "role": "assistant",
                "content": None,
                "tool_calls": [{
                    "id": call_id,
                    "type": "function",
                    "function": {
                        "name": item.get("name") or "tool",
                        "arguments": item.get("arguments") or "{}",
                    },
                }],
            })
            continue

        role = item.get("role") or ("assistant" if item_type == "message" else "user")
        if role == "developer":
            role = "system"
        if role not in ("system", "user", "assistant", "tool"):
            role = "user"
        message = {"role": role, "content": responses_content_text(item.get("content"))}
        if role == "tool" and (item.get("tool_call_id") or item.get("call_id")):
            message["tool_call_id"] = item.get("tool_call_id") or item.get("call_id")
        messages.append(message)
    return messages


def responses_tools_to_chat_tools(tools):
    chat_tools = []
    if not isinstance(tools, list):
        return chat_tools
    for tool in tools:
        if not isinstance(tool, dict):
            continue
        if tool.get("type") != "function":
            continue
        name = tool.get("name") or tool.get("function", {}).get("name")
        if not name:
            continue
        chat_tools.append({
            "type": "function",
            "function": {
                "name": name,
                "description": tool.get("description") or tool.get("function", {}).get("description") or "",
                "parameters": tool.get("parameters") or tool.get("function", {}).get("parameters") or {},
            },
        })
    return chat_tools


def responses_body_to_chat_payload(body):
    payload = {
        "model": body.get("model") or PANEL_MODEL_ALIAS,
        "messages": responses_input_to_chat_messages(body),
        "stream": False,
    }
    if body.get("temperature") is not None:
        payload["temperature"] = body.get("temperature")
    if body.get("top_p") is not None:
        payload["top_p"] = body.get("top_p")
    if body.get("max_output_tokens") is not None:
        payload["max_tokens"] = body.get("max_output_tokens")
    elif body.get("max_tokens") is not None:
        payload["max_tokens"] = body.get("max_tokens")

    chat_tools = responses_tools_to_chat_tools(body.get("tools"))
    if chat_tools:
        payload["tools"] = chat_tools
        payload["tool_choice"] = body.get("tool_choice") or "auto"
    return payload


def chat_completion_to_response(chat, requested_model):
    choice = (chat.get("choices") or [{}])[0]
    message = choice.get("message") or {}
    created = int(chat.get("created") or time.time())
    response_id = "resp_" + (chat.get("id") or now_stamp()).replace("chatcmpl-", "")
    output = []
    output_text = ""

    tool_calls = message.get("tool_calls") or []
    if tool_calls:
        for index, call in enumerate(tool_calls):
            fn = call.get("function") or {}
            output.append({
                "id": call.get("id") or f"fc_{response_id}_{index}",
                "type": "function_call",
                "status": "completed",
                "call_id": call.get("id") or f"call_{index}",
                "name": fn.get("name") or "tool",
                "arguments": fn.get("arguments") or "{}",
            })
    else:
        output_text = message.get("content") or ""
        output.append({
            "id": "msg_" + response_id.removeprefix("resp_"),
            "type": "message",
            "status": "completed",
            "role": "assistant",
            "content": [{
                "type": "output_text",
                "text": output_text,
                "annotations": [],
            }],
        })

    usage = chat.get("usage") or {}
    response_usage = {
        "input_tokens": int(usage.get("prompt_tokens") or 0),
        "output_tokens": int(usage.get("completion_tokens") or 0),
        "total_tokens": int(usage.get("total_tokens") or 0),
    }
    return {
        "id": response_id,
        "object": "response",
        "created_at": created,
        "status": "completed",
        "model": requested_model or chat.get("model") or PANEL_MODEL_ALIAS,
        "output": output,
        "output_text": output_text,
        "parallel_tool_calls": True,
        "usage": response_usage,
    }


def current_catalog_filename(profile, context_size, cache_type_k, cache_type_v):
    return "CURRENT.profile.json"


def sync_current_catalog(profile, runtime, context_size, cache_type_k, cache_type_v, alias, alive=True):
    CURRENT_CATALOG_DIR.mkdir(parents=True, exist_ok=True)
    source = Path(profile["model_path"])

    for old in CURRENT_CATALOG_DIR.glob("*.gguf"):
        old.unlink(missing_ok=True)

    target = CURRENT_CATALOG_DIR / current_catalog_filename(profile, context_size, cache_type_k, cache_type_v)
    link_mode = "metadata-only"

    metadata = {
        "profile_id": profile["id"],
        "display_name": profile.get("display_name"),
        "runtime": runtime,
        "context_size": context_size,
        "cache_type_k": cache_type_k,
        "cache_type_v": cache_type_v,
        "alias": alias,
        "model_path": str(source),
        "catalog_current_path": str(target),
        "catalog_current_mode": link_mode,
        "catalog_note": "Native Unsloth model selection would load a second model copy. Use the tray, panel, or Local Llama Executor MCP to switch the shared executor.",
        "alive": alive,
        "api": LLAMA_BASE_URL,
        "webui": "http://127.0.0.1:8080",
        "updated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
    }
    write_json(CURRENT_PROFILE_PATH, metadata)
    write_json(CURRENT_CATALOG_DIR / "CURRENT.profile.json", metadata)
    return metadata


def hidden_startupinfo():
    if os.name != "nt":
        return None
    startupinfo = subprocess.STARTUPINFO()
    startupinfo.dwFlags |= subprocess.STARTF_USESHOWWINDOW
    startupinfo.wShowWindow = 0
    return startupinfo


class ManagedMcp:
    def __init__(self):
        self.process = None
        self.stdout_queue = queue.Queue()
        self.lock = threading.Lock()
        self.next_id = 1
        self.log_path = None

    def alive(self):
        return self.process is not None and self.process.poll() is None

    def start(self):
        if self.alive():
            return {"ok": True, "pid": self.process.pid, "log": str(self.log_path)}

        LOG_DIR.mkdir(parents=True, exist_ok=True)
        self.log_path = LOG_DIR / f"panel-mcp-{now_stamp()}.log"
        err = self.log_path.open("a", encoding="utf-8", errors="replace")
        cmd = [sys.executable, str(MCP_DIR / "local-llama-mcp.py")]
        self.process = subprocess.Popen(
            cmd,
            cwd=str(ROOT),
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=err,
            text=True,
            encoding="utf-8",
            startupinfo=hidden_startupinfo(),
        )
        threading.Thread(target=self._read_stdout, daemon=True).start()
        init = self.request("initialize", {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "local-llama-panel", "version": "0.1.0"}}, timeout=5)
        self.notify("notifications/initialized")
        return {"ok": True, "pid": self.process.pid, "log": str(self.log_path), "initialize": init}

    def _read_stdout(self):
        if not self.process or not self.process.stdout:
            return
        for line in self.process.stdout:
            line = line.strip()
            if line:
                self.stdout_queue.put(line)

    def stop(self):
        if self.alive():
            kill_tree(self.process.pid)
        self.process = None
        return {"ok": True}

    def request(self, method, params=None, timeout=30):
        self.start_if_needed()
        with self.lock:
            req_id = self.next_id
            self.next_id += 1
            payload = {"jsonrpc": "2.0", "id": req_id, "method": method}
            if params is not None:
                payload["params"] = params
            self.process.stdin.write(json.dumps(payload, separators=(",", ":")) + "\n")
            self.process.stdin.flush()
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                try:
                    raw = self.stdout_queue.get(timeout=0.25)
                except queue.Empty:
                    continue
                try:
                    msg = json.loads(raw.lstrip("\ufeff"))
                except json.JSONDecodeError:
                    continue
                if msg.get("id") == req_id:
                    return msg
            raise TimeoutError(f"MCP request timed out: {method}")

    def notify(self, method, params=None):
        if not self.alive():
            return
        payload = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            payload["params"] = params
        self.process.stdin.write(json.dumps(payload, separators=(",", ":")) + "\n")
        self.process.stdin.flush()

    def start_if_needed(self):
        if not self.alive():
            self.start()


class PanelState:
    def __init__(self):
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        STATE_DIR.mkdir(parents=True, exist_ok=True)
        self.model_process = None
        self.small_process = None
        self.model_panel_log = None
        self.small_panel_log = None
        self.mcp = ManagedMcp()
        self._ready_lock = threading.RLock()
        self.state = read_json(CONTROL_STATE_PATH, {})

    def save(self):
        write_json(CONTROL_STATE_PATH, self.state)

    def profiles(self):
        matrix = read_json(MATRIX_PATH, {"profiles": [], "notes": []})
        profiles = []
        for profile in matrix.get("profiles", []):
            item = dict(profile)
            abs_path = resolve_model_path(item["local_path"])
            item["model_path"] = str(abs_path)
            item["model_exists"] = abs_path.exists()
            item["recommended_runtime"] = "unsloth" if "gemma" in item["id"].lower() else "amd"
            item["is_default"] = item["id"] == DEFAULT_PROFILE_ID
            profiles.append(item)
        return {"notes": matrix.get("notes", []), "profiles": profiles, "default_profile_id": DEFAULT_PROFILE_ID}

    def profiles_by_id(self):
        return {p["id"]: p for p in self.profiles()["profiles"]}

    def local_model_slugs(self):
        return {PANEL_MODEL_ALIAS, SMALL_MODEL_ALIAS, "default"} | set(self.profiles_by_id().keys())

    def is_local_model_slug(self, model):
        return not model or model in self.local_model_slugs()

    def model_pid(self):
        if self.model_process and self.model_process.poll() is None:
            return self.model_process.pid
        return self.state.get("model", {}).get("pid")

    def model_alive(self):
        proc_alive = self.model_process is not None and self.model_process.poll() is None
        return proc_alive or pid_alive(self.state.get("model", {}).get("pid"))

    def small_pid(self):
        if self.small_process and self.small_process.poll() is None:
            return self.small_process.pid
        return self.state.get("small_parallel", {}).get("pid")

    def small_alive(self):
        proc_alive = self.small_process is not None and self.small_process.poll() is None
        return proc_alive or pid_alive(self.state.get("small_parallel", {}).get("pid"))

    def llama_health(self):
        if not port_open(LLAMA_HOST, LLAMA_PORT):
            return {"ok": False, "error": f"port {LLAMA_PORT} closed"}
        try:
            return {"ok": True, "models": http_json("GET", f"{LLAMA_BASE_URL}/models", timeout=2)}
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
            return {"ok": False, "error": str(exc)}

    def small_health(self):
        if not port_open(LLAMA_HOST, SMALL_LLAMA_PORT):
            return {"ok": False, "error": f"port {SMALL_LLAMA_PORT} closed"}
        try:
            return {"ok": True, "models": http_json("GET", f"{SMALL_LLAMA_BASE_URL}/models", timeout=2)}
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
            return {"ok": False, "error": str(exc)}

    def token_status(self):
        active = self.state.get("model") or {}
        context_size = int(active.get("context_size") or DEFAULT_CONTEXT_LIMIT)
        sessions = read_json(MCP_STATE_PATH, {"sessions": {}}).get("sessions", {})
        mcp_estimated = 0
        last_compaction = None
        for item in sessions.values():
            mcp_estimated = max(mcp_estimated, int(item.get("estimated_tokens") or 0))
            if item.get("last_compaction"):
                last_compaction = item.get("last_compaction")
        slots = []
        metrics = {}
        if port_open(LLAMA_HOST, LLAMA_PORT):
            try:
                slots = http_json("GET", f"http://{LLAMA_HOST}:{LLAMA_PORT}/slots", timeout=2)
            except Exception:
                slots = []
            try:
                metrics = parse_metrics(http_text("GET", f"http://{LLAMA_HOST}:{LLAMA_PORT}/metrics", timeout=2))
            except Exception:
                metrics = {}
        if isinstance(slots, list) and slots:
            context_size = int(slots[0].get("n_ctx") or context_size)
        used = min(context_size, mcp_estimated) if context_size > 0 else mcp_estimated
        ratio = used / context_size if context_size > 0 else 0.0
        return {
            "context_size": context_size,
            "estimated_context_tokens": used,
            "estimated_context_ratio": round(ratio, 4),
            "estimated_context_percent": round(ratio * 100, 1),
            "warning": ratio >= 0.75,
            "compact_now": ratio >= 0.85,
            "force_compact": ratio >= 0.90,
            "slots": slots,
            "metrics": metrics,
            "requests_processing": metrics.get("requests_processing", 0),
            "tokens_per_second": metrics.get("tokens_per_second"),
            "last_compaction": last_compaction,
            "source": "mcp-session-estimate plus llama.cpp /slots and /metrics",
        }

    def status(self):
        mcp_state = read_json(MCP_STATE_PATH, {"sessions": {}, "config": {}})
        return {
            "model": {
                "alive": self.model_alive(),
                "pid": self.model_pid(),
                "active": self.state.get("model"),
                "health": self.llama_health(),
            },
            "small_parallel": {
                "enabled": bool(self.state.get("orchestration", {}).get("small_parallel_enabled", True)),
                "alive": self.small_alive(),
                "pid": self.small_pid(),
                "active": self.state.get("small_parallel"),
                "health": self.small_health(),
            },
            "orchestration": self.state.get("orchestration", {
                "primary": self.state.get("model"),
                "small_parallel": self.state.get("small_parallel"),
                "sota_manager": False,
            }),
            "last_route": self.state.get("last_route"),
            "tokens": self.token_status(),
            "mcp": {
                "alive": self.mcp.alive(),
                "pid": self.mcp.process.pid if self.mcp.alive() else None,
                "log": str(self.mcp.log_path) if self.mcp.log_path else None,
            },
            "compaction": mcp_state,
            "current_profile": read_json(CURRENT_PROFILE_PATH, None),
        }

    def select_profile(self, body):
        profile_id = body.get("profile_id") or DEFAULT_PROFILE_ID
        profiles = self.profiles_by_id()
        if profile_id not in profiles:
            raise ValueError(f"Unknown profile_id: {profile_id}")
        profile = profiles[profile_id]
        if not profile["model_exists"]:
            raise ValueError(f"Model file not found: {profile['model_path']}")

        runtime = body.get("runtime") or profile["recommended_runtime"]
        context_size = int(body.get("context_size") or profile["context_size"])
        cache_type_k = body.get("cache_type_k") or profile["cache_type_k"]
        cache_type_v = body.get("cache_type_v") or profile["cache_type_v"]
        alias = body.get("alias") or PANEL_MODEL_ALIAS
        current = sync_current_catalog(profile, runtime, context_size, cache_type_k, cache_type_v, alias, alive=self.model_alive())
        self.state["selected_profile"] = current
        self.save()
        return {"ok": True, "selected_profile": current}

    def start_model(self, body):
        with self._ready_lock:
            return self._start_model_locked(body)

    def _start_model_locked(self, body):
        profile_id = body.get("profile_id") or DEFAULT_PROFILE_ID
        profiles = self.profiles_by_id()
        if profile_id not in profiles:
            raise ValueError(f"Unknown profile_id: {profile_id}")
        profile = profiles[profile_id]
        if not profile["model_exists"]:
            raise ValueError(f"Model file not found: {profile['model_path']}")

        runtime = body.get("runtime") or profile["recommended_runtime"]
        context_size = int(body.get("context_size") or profile["context_size"])
        cache_type_k = body.get("cache_type_k") or profile["cache_type_k"]
        cache_type_v = body.get("cache_type_v") or profile["cache_type_v"]
        alias = body.get("alias") or PANEL_MODEL_ALIAS

        self.stop_model()
        self.model_panel_log = LOG_DIR / f"panel-llama-server-{now_stamp()}.log"
        log = self.model_panel_log.open("a", encoding="utf-8", errors="replace")
        cmd = [
            "powershell",
            "-NoProfile",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
            str(SCRIPTS_DIR / "start-llama-server.ps1"),
            "-ModelPath",
            profile["model_path"],
            "-Runtime",
            runtime,
            "-HostName",
            LLAMA_HOST,
            "-Port",
            str(LLAMA_PORT),
            "-ContextSize",
            str(context_size),
            "-CacheTypeK",
            cache_type_k,
            "-CacheTypeV",
            cache_type_v,
            "-Alias",
            alias,
            "-Metrics",
            "-ContextShift",
            "-Reasoning",
            "off",
            "-ReasoningBudget",
            "0",
        ]
        creationflags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
        self.model_process = subprocess.Popen(
            cmd,
            cwd=str(ROOT.parents[1]),
            stdout=log,
            stderr=subprocess.STDOUT,
            text=True,
            startupinfo=hidden_startupinfo(),
            creationflags=creationflags,
        )
        self.state["model"] = {
            "pid": self.model_process.pid,
            "profile_id": profile_id,
            "runtime": runtime,
            "context_size": context_size,
            "cache_type_k": cache_type_k,
            "cache_type_v": cache_type_v,
            "alias": alias,
            "model_path": profile["model_path"],
            "started_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "panel_log": str(self.model_panel_log),
            "api": LLAMA_BASE_URL,
            "webui": "http://127.0.0.1:8080",
        }
        self.state.setdefault("orchestration", {})
        self.state["orchestration"]["primary"] = self.state["model"]
        self.state["orchestration"].setdefault("small_parallel_enabled", True)
        self.state["orchestration"].setdefault("sota_manager", False)
        current = sync_current_catalog(profile, runtime, context_size, cache_type_k, cache_type_v, alias, alive=True)
        self.state["selected_profile"] = current
        self.save()
        return {"ok": True, "model": self.state["model"], "current_profile": current}

    def stop_model(self):
        with self._ready_lock:
            return self._stop_model_locked()

    def _stop_model_locked(self):
        pid = self.state.get("model", {}).get("pid")
        if self.model_process and self.model_process.poll() is None:
            pid = self.model_process.pid
        if pid and pid_alive(pid):
            kill_tree(pid)
        self.model_process = None
        self.state.pop("model", None)
        self.state.setdefault("orchestration", {})
        self.state["orchestration"]["primary"] = None
        current = read_json(CURRENT_PROFILE_PATH, None)
        if current:
            current["alive"] = False
            current["updated_at"] = time.strftime("%Y-%m-%dT%H:%M:%S%z")
            write_json(CURRENT_PROFILE_PATH, current)
            write_json(CURRENT_CATALOG_DIR / "CURRENT.profile.json", current)
        self.save()
        return {"ok": True}

    def start_small_parallel(self, body):
        profile_id = body.get("profile_id") or SMALL_PROFILE_ID
        profiles = self.profiles_by_id()
        if profile_id not in profiles:
            raise ValueError(f"Unknown profile_id: {profile_id}")
        profile = profiles[profile_id]
        if not profile["model_exists"]:
            raise ValueError(f"Model file not found: {profile['model_path']}")
        if self.state.get("orchestration", {}).get("sota_manager"):
            raise ValueError("small_parallel is blocked while sota_manager=true.")

        self.stop_small_parallel()
        runtime = body.get("runtime") or profile["recommended_runtime"]
        context_size = int(body.get("context_size") or profile["context_size"])
        cache_type_k = body.get("cache_type_k") or profile["cache_type_k"]
        cache_type_v = body.get("cache_type_v") or profile["cache_type_v"]
        alias = body.get("alias") or SMALL_MODEL_ALIAS
        self.small_panel_log = LOG_DIR / f"panel-small-llama-server-{now_stamp()}.log"
        log = self.small_panel_log.open("a", encoding="utf-8", errors="replace")
        cmd = [
            "powershell",
            "-NoProfile",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
            str(SCRIPTS_DIR / "start-llama-server.ps1"),
            "-ModelPath",
            profile["model_path"],
            "-Runtime",
            runtime,
            "-HostName",
            LLAMA_HOST,
            "-Port",
            str(SMALL_LLAMA_PORT),
            "-ContextSize",
            str(context_size),
            "-CacheTypeK",
            cache_type_k,
            "-CacheTypeV",
            cache_type_v,
            "-Alias",
            alias,
            "-Metrics",
            "-ContextShift",
            "-Reasoning",
            "off",
            "-ReasoningBudget",
            "0",
        ]
        creationflags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
        self.small_process = subprocess.Popen(
            cmd,
            cwd=str(ROOT.parents[1]),
            stdout=log,
            stderr=subprocess.STDOUT,
            text=True,
            startupinfo=hidden_startupinfo(),
            creationflags=creationflags,
        )
        self.state["small_parallel"] = {
            "pid": self.small_process.pid,
            "profile_id": profile_id,
            "runtime": runtime,
            "context_size": context_size,
            "cache_type_k": cache_type_k,
            "cache_type_v": cache_type_v,
            "alias": alias,
            "model_path": profile["model_path"],
            "started_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "panel_log": str(self.small_panel_log),
            "api": SMALL_LLAMA_BASE_URL,
        }
        self.state.setdefault("orchestration", {})
        self.state["orchestration"]["small_parallel"] = self.state["small_parallel"]
        self.state["orchestration"]["small_parallel_enabled"] = True
        self.save()
        return {"ok": True, "small_parallel": self.state["small_parallel"]}

    def stop_small_parallel(self):
        pid = self.small_pid()
        if pid and pid_alive(pid):
            kill_tree(pid)
        self.small_process = None
        self.state.pop("small_parallel", None)
        self.state.setdefault("orchestration", {})
        self.state["orchestration"]["small_parallel"] = None
        self.save()
        return {"ok": True}

    def ensure_primary_ready(self, timeout=360, profile_id=None):
        with self._ready_lock:
            running_profile_id = self.state.get("model", {}).get("profile_id")
            if profile_id is not None:
                if running_profile_id == profile_id and self.llama_health().get("ok"):
                    return {"ok": True, "already_running": True, "model": self.state.get("model")}
                selected = self.profiles_by_id().get(profile_id, {})
            else:
                if self.llama_health().get("ok"):
                    return {"ok": True, "already_running": True, "model": self.state.get("model")}
                selected = self.state.get("selected_profile") or read_json(CURRENT_PROFILE_PATH, None) or {}
                profile_id = selected.get("profile_id") or DEFAULT_PROFILE_ID
            payload = {
                "profile_id": profile_id,
                "runtime": selected.get("runtime") or None,
                "context_size": selected.get("context_size") or None,
                "cache_type_k": selected.get("cache_type_k") or None,
                "cache_type_v": selected.get("cache_type_v") or None,
                "alias": PANEL_MODEL_ALIAS,
            }
            payload = {k: v for k, v in payload.items() if v is not None}
            started = self.start_model(payload)
            deadline = time.time() + timeout
            while time.time() < deadline:
                health = self.llama_health()
                if health.get("ok"):
                    return {"ok": True, "already_running": False, "started": started}
                time.sleep(1)
            raise TimeoutError(f"Timed out waiting for {profile_id} at {LLAMA_BASE_URL}.")

    def update_orchestration(self, body):
        self.state.setdefault("orchestration", {})
        if "sota_manager" in body:
            self.state["orchestration"]["sota_manager"] = bool(body["sota_manager"])
        if "small_parallel_enabled" in body:
            self.state["orchestration"]["small_parallel_enabled"] = bool(body["small_parallel_enabled"])
        self.save()
        return {"ok": True, "orchestration": self.state["orchestration"]}

    def should_route_small(self, payload):
        orchestration = self.state.get("orchestration", {})
        if orchestration.get("sota_manager") or not orchestration.get("small_parallel_enabled", True):
            return False
        if self.token_status().get("requests_processing", 0) <= 0:
            return False
        messages = payload.get("messages") or []
        text_len = len(json.dumps(messages, ensure_ascii=False))
        if text_len > int(os.environ.get("LOCAL_LLAMA_SMALL_MAX_INPUT_CHARS", "16000")):
            return False
        if self.small_health().get("ok"):
            return True
        if os.environ.get("LOCAL_LLAMA_SMALL_AUTOSTART", "1").strip().lower() in ("0", "false", "no", "off"):
            return False
        try:
            self.start_small_parallel({"profile_id": SMALL_PROFILE_ID})
            deadline = time.time() + int(os.environ.get("LOCAL_LLAMA_SMALL_AUTOSTART_TIMEOUT", "120"))
            while time.time() < deadline:
                if self.small_health().get("ok"):
                    return True
                time.sleep(1)
        except Exception as exc:
            self.state["last_small_parallel_error"] = {
                "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
                "error": str(exc),
            }
            self.save()
        return False

    def resolve_requested_model(self, requested_model):
        model = requested_model or PANEL_MODEL_ALIAS
        profiles = self.profiles_by_id()
        if model == SMALL_MODEL_ALIAS:
            self.start_small_parallel({"profile_id": SMALL_PROFILE_ID})
            return SMALL_LLAMA_BASE_URL, SMALL_MODEL_ALIAS, True
        if model in profiles:
            profile = profiles[model]
            if profile.get("parallel_only") or profile.get("role") == "small_parallel":
                self.start_small_parallel({"profile_id": model})
                return SMALL_LLAMA_BASE_URL, SMALL_MODEL_ALIAS, True
            self.ensure_primary_ready(profile_id=model)
            return LLAMA_BASE_URL, PANEL_MODEL_ALIAS, False
        self.ensure_primary_ready()
        return LLAMA_BASE_URL, PANEL_MODEL_ALIAS, False

    def proxy_chat_completions(self, body, raw_headers):
        requested_model = body.get("model")
        upstream, upstream_model, small_generated = self.resolve_requested_model(requested_model)
        body["model"] = upstream_model
        if self.should_route_small(body):
            upstream = SMALL_LLAMA_BASE_URL
            body["model"] = SMALL_MODEL_ALIAS
            small_generated = True
        self.state["last_route"] = {
            "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "endpoint": "/v1/chat/completions",
            "upstream": upstream,
            "small_model_generated": small_generated,
            "requested_model": requested_model or PANEL_MODEL_ALIAS,
            "upstream_model": body["model"],
        }
        self.save()
        return self.proxy_request("POST", upstream + "/chat/completions", body, raw_headers, timeout=900)

    def proxy_responses(self, body, raw_headers):
        chat_payload = responses_body_to_chat_payload(body)
        requested_model = chat_payload.get("model")
        upstream = self.proxy_chat_completions(chat_payload, raw_headers)
        try:
            raw = upstream.read().decode("utf-8")
            chat = json.loads(raw) if raw else {}
            response = chat_completion_to_response(chat, requested_model)
            response["previous_response_id"] = body.get("previous_response_id")
            response["parallel_tool_calls"] = bool(body.get("parallel_tool_calls", True))
            return response
        finally:
            try:
                upstream.close()
            except Exception:
                pass

    def proxy_request(self, method, url, body=None, raw_headers=None, timeout=900):
        data = json.dumps(body).encode("utf-8") if body is not None else None
        headers = {"Content-Type": "application/json"}
        if raw_headers:
            for name in (
                "Accept",
                "Authorization",
                "OpenAI-Organization",
                "OpenAI-Project",
                "OpenAI-Beta",
                "Idempotency-Key",
            ):
                value = raw_headers.get(name)
                if value:
                    headers[name] = value
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        return urllib.request.urlopen(req, timeout=timeout)

    def mcp_selftest(self):
        self.mcp.start_if_needed()
        return self.mcp.request("tools/list", timeout=5)

    def compact(self, body):
        self.mcp.start_if_needed()
        args = {
            "session_id": body.get("session_id") or "default",
            "layer": body.get("layer") or "local",
        }
        return self.mcp.request("tools/call", {"name": "local_model_compact", "arguments": args}, timeout=180)


PANEL = PanelState()


class Handler(BaseHTTPRequestHandler):
    server_version = "LocalLlamaPanel/0.1"

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        query = urllib.parse.parse_qs(parsed.query)
        try:
            if path == "/v1/models":
                PANEL.ensure_primary_ready()
                return self.send_upstream(PANEL.proxy_request("GET", f"{LLAMA_BASE_URL}/models", None, self.headers, timeout=30))
            if path == "/api/status":
                return self.json(PANEL.status())
            if path == "/api/profiles":
                return self.json(PANEL.profiles())
            if path == "/api/profile/current":
                return self.json(read_json(CURRENT_PROFILE_PATH, None) or {})
            if path == "/api/tokens":
                return self.json(PANEL.token_status())
            if path == "/api/compaction/status":
                return self.json(read_json(MCP_STATE_PATH, {"sessions": {}, "config": {}}))
            if path == "/api/logs":
                kind = (query.get("kind") or ["server"])[0]
                lines = int((query.get("lines") or ["200"])[0])
                path_obj = None
                if kind == "panel":
                    active = PANEL.state.get("model", {})
                    path_obj = Path(active.get("panel_log") or "") if active.get("panel_log") else latest_file("panel-llama-server-*.log")
                elif kind == "mcp":
                    path_obj = Path(PANEL.mcp.log_path) if PANEL.mcp.log_path else latest_file("panel-mcp-*.log")
                else:
                    path_obj = latest_file("llama-server-*.log")
                return self.json({"path": str(path_obj) if path_obj else None, "text": tail_file(path_obj, lines) if path_obj else ""})
            return self.static(path)
        except urllib.error.HTTPError as exc:
            return self.send_upstream(exc)
        except Exception as exc:
            log_exception(f"{self.command} {self.path}", exc)
            return self.error(500, str(exc))

    def do_POST(self):
        try:
            body = self.read_body()
            if self.path == "/v1/chat/completions":
                if not PANEL.is_local_model_slug(body.get("model")):
                    return self.send_upstream(PANEL.proxy_request("POST", OPENAI_FALLBACK_BASE_URL + "/chat/completions", body, self.headers))
                return self.send_upstream(PANEL.proxy_chat_completions(body, self.headers))
            if self.path == "/v1/responses":
                if not PANEL.is_local_model_slug(body.get("model")):
                    return self.send_upstream(PANEL.proxy_request("POST", OPENAI_FALLBACK_BASE_URL + "/responses", body, self.headers))
                response = PANEL.proxy_responses(body, self.headers)
                if body.get("stream"):
                    return self.sse_response(response)
                return self.json(response)
            if self.path == "/api/profile/select":
                return self.json(PANEL.select_profile(body))
            if self.path == "/api/model/start":
                return self.json(PANEL.start_model(body))
            if self.path == "/api/model/stop":
                return self.json(PANEL.stop_model())
            if self.path == "/api/small/start":
                return self.json(PANEL.start_small_parallel(body))
            if self.path == "/api/small/stop":
                return self.json(PANEL.stop_small_parallel())
            if self.path == "/api/orchestration":
                return self.json(PANEL.update_orchestration(body))
            if self.path == "/api/mcp/start":
                return self.json(PANEL.mcp.start())
            if self.path == "/api/mcp/stop":
                return self.json(PANEL.mcp.stop())
            if self.path == "/api/mcp/selftest":
                return self.json(PANEL.mcp_selftest())
            if self.path == "/api/compaction/compact":
                return self.json(PANEL.compact(body))
            return self.error(404, "not found")
        except urllib.error.HTTPError as exc:
            return self.send_upstream(exc)
        except Exception as exc:
            log_exception(f"{self.command} {self.path}", exc)
            return self.error(500, str(exc))

    def read_body(self):
        length = int(self.headers.get("Content-Length", "0"))
        if not length:
            return {}
        raw = self.rfile.read(length).decode("utf-8")
        return json.loads(raw) if raw else {}

    def sse_response(self, response):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.send_header("X-Local-Llama-Proxy", "panel-8090")
        self.end_headers()

        def emit(event, data):
            payload = json.dumps(data, separators=(",", ":")).encode("utf-8")
            self.wfile.write(b"event: " + event.encode("utf-8") + b"\n")
            self.wfile.write(b"data: " + payload + b"\n\n")
            self.wfile.flush()

        created = dict(response)
        created["output"] = []
        created["output_text"] = ""
        emit("response.created", created)

        for output_index, item in enumerate(response.get("output") or []):
            emit("response.output_item.added", {
                "response_id": response.get("id"),
                "output_index": output_index,
                "item": item,
            })
            if item.get("type") == "message":
                content = item.get("content") or []
                for content_index, part in enumerate(content):
                    emit("response.content_part.added", {
                        "response_id": response.get("id"),
                        "item_id": item.get("id"),
                        "output_index": output_index,
                        "content_index": content_index,
                        "part": part,
                    })
                    if part.get("type") == "output_text":
                        text = part.get("text") or ""
                        emit("response.output_text.delta", {
                            "response_id": response.get("id"),
                            "item_id": item.get("id"),
                            "output_index": output_index,
                            "content_index": content_index,
                            "delta": text,
                        })
                        emit("response.output_text.done", {
                            "response_id": response.get("id"),
                            "item_id": item.get("id"),
                            "output_index": output_index,
                            "content_index": content_index,
                            "text": text,
                        })
                    emit("response.content_part.done", {
                        "response_id": response.get("id"),
                        "item_id": item.get("id"),
                        "output_index": output_index,
                        "content_index": content_index,
                        "part": part,
                    })
            emit("response.output_item.done", {
                "response_id": response.get("id"),
                "output_index": output_index,
                "item": item,
            })

        emit("response.completed", response)
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def static(self, path):
        if path == "/":
            path = "/index.html"
        target = (STATIC_DIR / path.lstrip("/")).resolve()
        if STATIC_DIR.resolve() not in target.parents and target != STATIC_DIR.resolve():
            return self.error(403, "forbidden")
        if not target.exists() or not target.is_file():
            return self.error(404, "not found")
        ctype = "text/html; charset=utf-8" if target.suffix == ".html" else "text/plain; charset=utf-8"
        if target.suffix == ".css":
            ctype = "text/css; charset=utf-8"
        if target.suffix == ".js":
            ctype = "application/javascript; charset=utf-8"
        data = target.read_bytes()
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def json(self, data, status=200):
        raw = json.dumps(data, indent=2).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def send_upstream(self, response):
        try:
            status = getattr(response, "status", None) or response.getcode()
            self.send_response(status)
            content_type = response.headers.get("Content-Type") or "application/json"
            self.send_header("Content-Type", content_type)
            self.send_header("Cache-Control", "no-cache")
            self.send_header("X-Local-Llama-Proxy", "panel-8090")
            self.end_headers()
            while True:
                chunk = response.read(65536)
                if not chunk:
                    break
                self.wfile.write(chunk)
                self.wfile.flush()
        finally:
            try:
                response.close()
            except Exception:
                pass

    def error(self, status, message):
        return self.json({"ok": False, "error": message}, status=status)

    def log_message(self, fmt, *args):
        sys.stderr.write("%s - %s\n" % (self.log_date_time_string(), fmt % args))


def main():
    parser = argparse.ArgumentParser(description="Local llama.cpp control panel")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default=8090, type=int)
    args = parser.parse_args()
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    print(f"Local llama panel: http://{args.host}:{args.port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
