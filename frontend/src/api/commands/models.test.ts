import { afterEach, describe, expect, it } from 'vitest';
import { loadModel, NativeHostUnavailableError, switchModel, unloadModel } from './models';

/**
 * These commands go straight to `bridgeTransport` (see models.ts's doc
 * comment for why), so under Vitest's jsdom environment - which has no
 * `window.chrome.webview`, exactly like `vite dev` - every call here is
 * expected to reject with `NativeHostUnavailableError`. That IS the
 * behaviour under test: Sprint 4's brief requires this refusal to surface
 * honestly rather than being hidden or faked.
 */
describe('model-lifecycle commands: no native host present (jsdom, like `vite dev`)', () => {
  afterEach(() => {
    delete (window as typeof window & { chrome?: unknown }).chrome;
  });

  it('loadModel rejects with NativeHostUnavailableError', async () => {
    await expect(loadModel({ modelId: 'gemma4-12b-qat-ud-q4xl' })).rejects.toBeInstanceOf(
      NativeHostUnavailableError,
    );
  });

  it('unloadModel rejects with NativeHostUnavailableError', async () => {
    await expect(unloadModel({ modelId: 'gemma4-12b-qat-ud-q4xl' })).rejects.toBeInstanceOf(
      NativeHostUnavailableError,
    );
  });

  it('switchModel rejects with NativeHostUnavailableError', async () => {
    await expect(switchModel({ modelId: 'qwen38-27b-agent-128k' })).rejects.toBeInstanceOf(
      NativeHostUnavailableError,
    );
  });

  it('states plainly, in the rejection message, that the native console host is needed', async () => {
    await expect(loadModel({ modelId: 'gemma4-12b-qat-ud-q4xl' })).rejects.toThrow(
      /needs the native console host/,
    );
  });

  it('never resolves - a missing host never looks like success', async () => {
    await expect(loadModel({ modelId: 'gemma4-12b-qat-ud-q4xl' })).rejects.toBeDefined();
  });
});

/**
 * The native bridge work wired this for real (see `../transport/bridgeTransport.ts`)
 * so "host present but not wired up yet" - the state the describe block this
 * replaced used to assert on - can no longer occur; see `errors.ts` for the
 * `NativeHostUnavailableReason` value that was removed alongside it. These
 * tests drive a fake `window.chrome.webview` that behaves like a genuinely
 * wired host: its `postMessage` triggers a reply through
 * `window.__ciaConsoleReply`, correlated by the id the command's own request
 * carried - exactly what `cia-console.exe` does.
 */
describe('model-lifecycle commands: native host present and wired', () => {
  afterEach(() => {
    delete (window as typeof window & { chrome?: unknown }).chrome;
  });

  function installFakeWiredHost(
    reply: (id: string) => unknown,
    onMessage?: (message: unknown) => void,
  ): void {
    (window as typeof window & { chrome?: { webview: { postMessage: (message: unknown) => void } } }).chrome = {
      webview: {
        postMessage: (message: unknown) => {
          onMessage?.(message);
          // The real host receives a JSON string, not an object - see
          // `decodePosted` in src/api/transport/bridgeTransport.test.ts for
          // why the wire shape is a string and why asserting it matters.
          expect(typeof message).toBe('string');
          const { id } = JSON.parse(message as string) as { id: string };
          queueMicrotask(() => {
            (window as typeof window & { __ciaConsoleReply?: (reply: unknown) => void }).__ciaConsoleReply?.(
              reply(id),
            );
          });
        },
      },
    };
  }

  it('propagates a plain Error (not NativeHostUnavailableError) when the bridge replies with a denied approval', async () => {
    installFakeWiredHost((id) => ({
      id,
      ok: false,
      operation: 'load',
      error: { code: 'approval_denied', message: 'the operation was not approved' },
    }));

    const rejection = loadModel({ modelId: 'gemma4-12b-qat-ud-q4xl' });
    await expect(rejection).rejects.not.toBeInstanceOf(NativeHostUnavailableError);
    await expect(rejection).rejects.toThrow(/approval_denied/);
  });

  it('sends the "load" kind (matching bridge.go\'s KindLoad) and resolves once the bridge replies ok:true', async () => {
    let sentMessage: Record<string, unknown> | undefined;
    installFakeWiredHost(
      (id) => ({ id, ok: true, operation: 'load', result: { operation: 'load', model: 'gemma4-12b-qat-ud-q4xl' } }),
      (message) => {
        sentMessage = JSON.parse(message as string) as Record<string, unknown>;
      },
    );

    await expect(loadModel({ modelId: 'gemma4-12b-qat-ud-q4xl' })).resolves.toBeUndefined();
    expect(sentMessage).toMatchObject({ kind: 'load', model_id: 'gemma4-12b-qat-ud-q4xl' });
  });
});
