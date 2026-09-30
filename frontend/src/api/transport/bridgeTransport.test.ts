import { afterEach, describe, expect, it, vi } from 'vitest';
import { bridgeTransport } from './bridgeTransport';
import { NativeHostUnavailableError } from '../errors';

type WindowWithBridge = typeof window & {
  chrome?: { webview: { postMessage: (message: unknown) => void } };
  __ciaConsoleReply?: (reply: unknown) => void;
};

function installFakeWebview(postMessage: (message: unknown) => void): void {
  (window as WindowWithBridge).chrome = { webview: { postMessage } };
}

function deliverReply(reply: unknown): void {
  (window as WindowWithBridge).__ciaConsoleReply?.(reply);
}

/**
 * Decodes one posted web message, asserting first that it really was posted
 * as a JSON *string*.
 *
 * That assertion is the point of this helper, not a formality. The host's
 * MessageReceived handler (pkg/edge/chromium.go) reads the message with
 * `ICoreWebView2WebMessageReceivedEventArgs::TryGetWebMessageAsString`,
 * which returns E_INVALIDARG for a message posted as an object - WebView2
 * delivers those as JSON on a different accessor. That HRESULT reaches the
 * host's error callback and tears the window down immediately after
 * navigation, so the whole console goes blank with no message naming a
 * cause. `bridgeTransport` therefore stringifies before posting, and these
 * tests assert the stringness rather than quietly accepting either shape -
 * a test that parsed an object just as happily would not have caught the
 * regression that produced that blank window.
 */
function decodePosted(message: unknown): Record<string, unknown> {
  expect(typeof message).toBe('string');
  return JSON.parse(message as string) as Record<string, unknown>;
}

describe('bridgeTransport', () => {
  afterEach(() => {
    delete (window as WindowWithBridge).chrome;
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('rejects with NativeHostUnavailableError(host-not-present) when window.chrome.webview is absent', async () => {
    const rejection = bridgeTransport.request('status');
    await expect(rejection).rejects.toBeInstanceOf(NativeHostUnavailableError);
    await expect(rejection).rejects.toMatchObject({ reason: 'host-not-present' });
  });

  it('posts {id, kind} with no model_id key for an operation with no params', async () => {
    let captured: Record<string, unknown> | undefined;
    installFakeWebview((message) => {
      captured = decodePosted(message);
    });

    const pending = bridgeTransport.request('status');
    expect(captured).toBeDefined();
    expect(typeof captured?.id).toBe('string');
    expect((captured?.id as string).length).toBeGreaterThan(0);
    expect(captured?.kind).toBe('status');
    expect(Object.prototype.hasOwnProperty.call(captured ?? {}, 'model_id')).toBe(false);

    deliverReply({ id: captured?.id, ok: true, operation: 'status', status: { service: 'cia-edge' } });
    await expect(pending).resolves.toEqual({ service: 'cia-edge' });
  });

  it('posts model_id when params carries one', async () => {
    let captured: Record<string, unknown> | undefined;
    installFakeWebview((message) => {
      captured = decodePosted(message);
    });

    const pending = bridgeTransport.request('load', { model_id: 'gemma4-12b-qat-ud-q4xl' });
    expect(captured?.kind).toBe('load');
    expect(captured?.model_id).toBe('gemma4-12b-qat-ud-q4xl');

    deliverReply({ id: captured?.id, ok: true, operation: 'load', result: { operation: 'load' } });
    await pending;
  });

  it('resolves with the status payload (reply.status), not the whole envelope, on a matching ok reply', async () => {
    let captured: Record<string, unknown> | undefined;
    installFakeWebview((message) => {
      captured = decodePosted(message);
    });

    const pending = bridgeTransport.request('status');
    deliverReply({ id: captured?.id, ok: true, operation: 'status', status: { service: 'cia-edge', ready: true } });
    await expect(pending).resolves.toEqual({ service: 'cia-edge', ready: true });
  });

  it('resolves with reply.result for a mutation-shaped reply', async () => {
    let captured: Record<string, unknown> | undefined;
    installFakeWebview((message) => {
      captured = decodePosted(message);
    });

    const pending = bridgeTransport.request('load', { model_id: 'm' });
    deliverReply({ id: captured?.id, ok: true, operation: 'load', result: { operation: 'load', model: 'm' } });
    await expect(pending).resolves.toEqual({ operation: 'load', model: 'm' });
  });

  it('rejects with the bridge error code and message on a matching failure reply', async () => {
    let captured: Record<string, unknown> | undefined;
    installFakeWebview((message) => {
      captured = decodePosted(message);
    });

    const pending = bridgeTransport.request('load', { model_id: 'm' });
    deliverReply({
      id: captured?.id,
      ok: false,
      operation: 'load',
      error: { code: 'approval_denied', message: 'the operation was not approved' },
    });
    await expect(pending).rejects.toThrow(/approval_denied/);
  });

  it('ignores a reply whose id does not match any pending request, and later still resolves the real one', async () => {
    let captured: Record<string, unknown> | undefined;
    installFakeWebview((message) => {
      captured = decodePosted(message);
    });

    const pending = bridgeTransport.request('status');
    let settled = false;
    void pending.then(
      () => {
        settled = true;
      },
      () => {
        settled = true;
      },
    );

    deliverReply({ id: 'some-unrelated-request-id', ok: true, operation: 'status', status: {} });
    await Promise.resolve();
    await Promise.resolve();
    expect(settled).toBe(false);

    deliverReply({ id: captured?.id, ok: true, operation: 'status', status: { service: 'cia-edge' } });
    await expect(pending).resolves.toEqual({ service: 'cia-edge' });
  });

  it('ignores a reply with no id, an empty id, or a shape that is not an envelope at all', async () => {
    installFakeWebview(() => {});
    // None of these should throw - handleReply must silently ignore them.
    expect(() => deliverReply(null)).not.toThrow();
    expect(() => deliverReply('a raw string')).not.toThrow();
    expect(() => deliverReply({ ok: true })).not.toThrow();
    expect(() => deliverReply({ id: '', ok: true, status: {} })).not.toThrow();
  });

  it('times out with a clear error when no reply ever arrives', async () => {
    vi.useFakeTimers();
    installFakeWebview(() => {});
    const pending = bridgeTransport.request('status');
    const assertion = expect(pending).rejects.toThrow(/timed out/);
    await vi.advanceTimersByTimeAsync(40_000);
    await assertion;
  });

  it('a late reply arriving after the timeout is ignored rather than resolving a promise twice', async () => {
    vi.useFakeTimers();
    let captured: Record<string, unknown> | undefined;
    installFakeWebview((message) => {
      captured = decodePosted(message);
    });
    const pending = bridgeTransport.request('status');
    const assertion = expect(pending).rejects.toThrow(/timed out/);
    await vi.advanceTimersByTimeAsync(40_000);
    await assertion;

    // A reply for the now-timed-out id must not throw or resolve anything -
    // the pending entry was already deleted when the timeout fired.
    expect(() =>
      deliverReply({ id: captured?.id, ok: true, operation: 'status', status: { service: 'cia-edge' } }),
    ).not.toThrow();
  });

  it('generates ids via crypto.randomUUID when available', async () => {
    const spy = vi.spyOn(crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111');
    let captured: Record<string, unknown> | undefined;
    installFakeWebview((message) => {
      captured = decodePosted(message);
    });

    const pending = bridgeTransport.request('status');
    expect(captured?.id).toBe('11111111-1111-4111-8111-111111111111');

    deliverReply({ id: captured?.id, ok: true, operation: 'status', status: {} });
    await pending;
    spy.mockRestore();
  });

  it('falls back to a counter-plus-random id (not Math.random() alone) when crypto.randomUUID is unavailable', async () => {
    vi.stubGlobal('crypto', {});
    const captures: Record<string, unknown>[] = [];
    installFakeWebview((message) => {
      captures.push(decodePosted(message));
    });

    const first = bridgeTransport.request('status');
    const second = bridgeTransport.request('status');
    expect(captures).toHaveLength(2);

    const id1 = captures[0]?.id as string;
    const id2 = captures[1]?.id as string;
    expect(id1).not.toBe(id2);
    // Not purely Math.random(): both ids embed this module's own,
    // predictable prefix and counter, rather than being opaque random
    // strings with no shared structure.
    expect(id1).toMatch(/^cia-console-\d+-/);
    expect(id2).toMatch(/^cia-console-\d+-/);

    deliverReply({ id: id1, ok: true, operation: 'status', status: {} });
    deliverReply({ id: id2, ok: true, operation: 'status', status: {} });
    await Promise.all([first, second]);
  });
});
