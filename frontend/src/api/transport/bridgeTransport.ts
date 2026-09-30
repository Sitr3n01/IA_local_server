import type { Transport } from './Transport';
import { NativeHostUnavailableError } from '../errors';

/**
 * Production transport: the console's only sanctioned way to reach the
 * operator's machine, via the native message bridge `cia-console.exe` (a
 * WebView2 host) exposes as `window.chrome.webview`.
 *
 * Wire contract (see `cmd/cia-console/bridge.go`'s `Envelope`/
 * `ReplyEnvelope`, and `main_windows.go`'s `handleBridgeMessage`/
 * `postBridgeReply`):
 *
 *   Console -> host : { id, kind, model_id? }              (postMessage)
 *   Host -> console : { id?, ok, operation, model_id?,
 *                        status?, result?, maintenance?, error? }
 *
 * Delivery of the reply is NOT the ordinary `window.chrome.webview`
 * "message" event. pkg/edge's WebView2 binding unconditionally echoes the
 * page's own original `postMessage` payload back over that channel
 * immediately after the host's `MessageCallback` returns (see
 * `postBridgeReply`'s doc comment in `main_windows.go`), so that channel
 * cannot carry the host's reply too. The host instead invokes
 * `window.__ciaConsoleReply(reply)` directly via `ExecuteScript` - so this
 * module's real "receive" path is exposing that function as a module-level
 * side effect (below), not registering an event listener.
 *
 * `id` is a correlation token only: generated here, echoed back by the host
 * verbatim, and used below purely to resolve the matching pending promise.
 * It carries no authorization meaning on either side - see bridge.go's
 * `validateRequestID` doc comment.
 */
export interface BridgeRequestEnvelope {
  readonly id: string;
  readonly kind: string;
  readonly model_id?: string;
}

export interface BridgeErrorPayload {
  readonly code: string;
  readonly message: string;
}

export interface BridgeResponseEnvelope {
  readonly id?: string;
  readonly ok: boolean;
  readonly operation?: string;
  readonly model_id?: string;
  readonly status?: unknown;
  readonly result?: unknown;
  readonly maintenance?: unknown;
  readonly error?: BridgeErrorPayload;
}

/**
 * Minimal shape of `window.chrome.webview` this module depends on. Only
 * `postMessage` - the reply path is `window.__ciaConsoleReply` (below), not
 * an event listener on this object; see the module doc comment above for
 * why `MessageReceived`'s own "message" event cannot carry the host's reply.
 */
interface ChromeWebView {
  postMessage(message: unknown): void;
}

function getChromeWebView(): ChromeWebView | undefined {
  if (typeof window === 'undefined') {
    return undefined;
  }
  const chrome = (window as typeof window & { chrome?: { webview?: ChromeWebView } }).chrome;
  return chrome?.webview;
}

declare global {
  interface Window {
    /**
     * Invoked directly by the host via `ExecuteScript`/`Eval` (not
     * `postMessage` - see the module doc comment) with the JSON-decoded
     * `ReplyEnvelope` for one prior request. Installed once, below, as this
     * module loads.
     */
    __ciaConsoleReply?: (reply: unknown) => void;
  }
}

/**
 * Bounds the length of a request id this module generates. It plays no
 * authorization role on either side of the bridge - the host only ever
 * echoes it back (bridge.go's `validateRequestID`) - but staying well under
 * the host's own bound (`maxRequestIDBytes` in bridge.go) is simply good
 * hygiene for a value that crosses a process boundary: an id this module
 * would refuse to accept back from itself is not one it should ever send.
 */
const MAX_GENERATED_ID_LENGTH = 128;

/**
 * How long one request waits locally for a correlated reply before giving
 * up. The host bounds its own side of one operation to 30s
 * (`cmd/cia-console/main_windows.go`'s `handleBridgeMessage`,
 * `context.WithTimeout`), and `Bridge.Handle` always turns a context
 * timeout into an error `Result` rather than dropping the request silently
 * - so the reply is always eventually sent. This is set a little above that
 * 30s host-side budget, not to bound the host's own work a second time, but
 * to leave room for that reply to actually arrive before this side gives up
 * on its own.
 */
const REQUEST_TIMEOUT_MS = 35_000;

interface PendingRequest {
  readonly resolve: (value: unknown) => void;
  readonly reject: (reason: unknown) => void;
  readonly timer: ReturnType<typeof setTimeout>;
}

const pendingRequests = new Map<string, PendingRequest>();

let fallbackIDCounter = 0;

/**
 * Generates one request id. `crypto.randomUUID()` is used when available -
 * every environment this build actually ships to has it (WebView2's own
 * Chromium, and any modern browser under `vite dev`) - falling back to a
 * monotonic counter plus a random component only if it is somehow missing.
 * The fallback is deliberately not `Math.random()` alone: two ids generated
 * in the same millisecond stay distinct via the counter even if the random
 * component were ever to collide.
 */
function generateRequestId(): string {
  const cryptoObj = typeof crypto === 'undefined' ? undefined : crypto;
  if (cryptoObj && typeof cryptoObj.randomUUID === 'function') {
    return cryptoObj.randomUUID();
  }
  fallbackIDCounter += 1;
  const randomPart = Math.random().toString(36).slice(2, 10);
  const id = `cia-console-${fallbackIDCounter}-${Date.now().toString(36)}-${randomPart}`;
  // Belt and suspenders: the format above is always well under the bound,
  // but this module should never hand out an id it would refuse to accept
  // back from itself.
  return id.slice(0, MAX_GENERATED_ID_LENGTH);
}

/**
 * Pulls `model_id` out of a command's params, when it is a string. `params`
 * is `unknown` by the `Transport` contract (see `Transport.ts`) - every
 * caller today passes either nothing (`status`) or `{ model_id: string }`
 * (`src/api/commands/models.ts`), so this is the one place that shape is
 * assumed, and it degrades to "no model id" for anything else rather than
 * throwing.
 */
function extractModelId(params: unknown): string | undefined {
  if (typeof params !== 'object' || params === null) {
    return undefined;
  }
  const value = (params as Record<string, unknown>).model_id;
  return typeof value === 'string' ? value : undefined;
}

/**
 * Picks the one payload field a given reply actually carries. Bridge.go's
 * `Result` sets exactly one of `status`/`result`/`maintenance` on success
 * (see `bridge.go`'s `handleRead`/`mutationResult`/`maintenanceResult`), so
 * this hands the caller the operation's own data - the `Status` object for
 * `status`, the `OperationOutput` for a load/unload/switch, the
 * `MaintenanceOutput` for drain/resume - rather than the whole envelope.
 */
function extractPayload(reply: BridgeResponseEnvelope): unknown {
  if (reply.status !== undefined) {
    return reply.status;
  }
  if (reply.result !== undefined) {
    return reply.result;
  }
  if (reply.maintenance !== undefined) {
    return reply.maintenance;
  }
  return undefined;
}

function isBridgeResponseEnvelope(value: unknown): value is BridgeResponseEnvelope {
  return typeof value === 'object' && value !== null && 'ok' in value;
}

/**
 * The single `window.__ciaConsoleReply` handler for the lifetime of this
 * module. A reply with no matching pending entry - an id this side never
 * sent, or one already resolved/rejected/timed out - is silently ignored
 * rather than thrown at anything: nothing in this module calls into
 * application code from here, so there would be no caller to throw at, and
 * an unmatched or unknown id is an expected, benign condition (e.g. a stray
 * reply after this side already gave up and timed out), not an error.
 */
function handleReply(raw: unknown): void {
  if (!isBridgeResponseEnvelope(raw)) {
    return;
  }
  const id = raw.id;
  if (typeof id !== 'string' || id.length === 0) {
    return;
  }
  const pending = pendingRequests.get(id);
  if (!pending) {
    return;
  }
  pendingRequests.delete(id);
  clearTimeout(pending.timer);

  if (!raw.ok || raw.error) {
    const detail = raw.error
      ? `${raw.error.code}: ${raw.error.message}`
      : 'the bridge reported failure with no error detail';
    pending.reject(new Error(`IA Local native bridge request failed: ${detail}`));
    return;
  }
  pending.resolve(extractPayload(raw));
}

if (typeof window !== 'undefined') {
  window.__ciaConsoleReply = handleReply;
}

export const bridgeTransport: Transport = {
  request<_T>(op: string, params?: unknown): Promise<unknown> {
    const webview = getChromeWebView();
    if (!webview) {
      return Promise.reject(
        new NativeHostUnavailableError(
          'IA Local native host bridge is not present: window.chrome.webview is unavailable. ' +
            'This build must run inside cia-console.exe (WebView2), not a plain browser tab.',
          'host-not-present',
        ),
      );
    }

    const id = generateRequestId();
    const modelId = extractModelId(params);
    const envelope: BridgeRequestEnvelope =
      modelId !== undefined ? { id, kind: op, model_id: modelId } : { id, kind: op };

    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pendingRequests.delete(id);
        reject(
          new Error(
            `IA Local native bridge request "${op}" timed out after ${REQUEST_TIMEOUT_MS}ms waiting for a reply.`,
          ),
        );
      }, REQUEST_TIMEOUT_MS);

      pendingRequests.set(id, { resolve, reject, timer });
      // Serialize before posting. `postMessage` with an object makes WebView2
      // deliver the message as JSON, and the host binding's MessageReceived
      // handler reads it with TryGetWebMessageAsString, which rejects a
      // non-string message with E_INVALIDARG ("The parameter is incorrect").
      // That error surfaces through the host's error callback and kills the
      // window immediately after navigation - a blank console with a message
      // that names no cause. Posting a string keeps the host's read valid.
      webview.postMessage(JSON.stringify(envelope));
    });
  },
};
