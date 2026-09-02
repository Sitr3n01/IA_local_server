import { afterEach, describe, expect, it } from 'vitest';
import { drainProvider, NativeHostUnavailableError, resumeProvider } from './maintenance';

/**
 * Same jsdom-has-no-native-host premise as `models.test.ts` - see that
 * file's doc comment. Every call here is expected to reject with
 * `NativeHostUnavailableError` under Vitest's jsdom environment.
 */
describe('maintenance commands: no native host present (jsdom, like `vite dev`)', () => {
  afterEach(() => {
    delete (window as typeof window & { chrome?: unknown }).chrome;
  });

  it('drainProvider rejects with NativeHostUnavailableError', async () => {
    await expect(drainProvider()).rejects.toBeInstanceOf(NativeHostUnavailableError);
  });

  it('resumeProvider rejects with NativeHostUnavailableError', async () => {
    await expect(resumeProvider()).rejects.toBeInstanceOf(NativeHostUnavailableError);
  });

  it('states plainly, in the rejection message, that the native console host is needed', async () => {
    await expect(drainProvider()).rejects.toThrow(/needs the native console host/);
  });

  it('never resolves - a missing host never looks like success', async () => {
    await expect(drainProvider()).rejects.toBeDefined();
  });
});

/**
 * Native host present and wired - same fake-host technique as
 * `models.test.ts` (see that file's `installFakeWiredHost` doc comment for
 * why the reply arrives via `window.__ciaConsoleReply`, not a
 * `postMessage` event).
 */
describe('maintenance commands: native host present and wired', () => {
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

  it('sends the "drain" kind (matching bridge.go\'s KindDrain) with NO model_id key at all, and resolves on ok:true', async () => {
    let sentMessage: Record<string, unknown> | undefined;
    installFakeWiredHost(
      (id) => ({ id, ok: true, operation: 'drain', maintenance: { state: 'draining', draining: true, drained: false, active: 1, queued: 0, rejected_total: 0 } }),
      (message) => {
        sentMessage = JSON.parse(message as string) as Record<string, unknown>;
      },
    );

    await expect(drainProvider()).resolves.toBeUndefined();
    expect(sentMessage).toMatchObject({ kind: 'drain' });
    // The envelope must carry no model_id key at all - not `undefined`, not
    // an empty string - since bridge.go's `modelIDForbidden` rejects the
    // operation outright if the key is present with any non-blank value,
    // and mirrors a check the administrative pipe already makes.
    expect(sentMessage).not.toHaveProperty('model_id');
  });

  it('sends the "resume" kind (matching bridge.go\'s KindResume) with NO model_id key at all', async () => {
    let sentMessage: Record<string, unknown> | undefined;
    installFakeWiredHost(
      (id) => ({ id, ok: true, operation: 'resume', maintenance: { state: 'running', draining: false, drained: false, active: 0, queued: 0, rejected_total: 0 } }),
      (message) => {
        sentMessage = JSON.parse(message as string) as Record<string, unknown>;
      },
    );

    await expect(resumeProvider()).resolves.toBeUndefined();
    expect(sentMessage).toMatchObject({ kind: 'resume' });
    expect(sentMessage).not.toHaveProperty('model_id');
  });

  it('propagates a plain Error (not NativeHostUnavailableError) when the bridge replies with a denied approval', async () => {
    installFakeWiredHost((id) => ({
      id,
      ok: false,
      operation: 'drain',
      error: { code: 'approval_denied', message: 'the operation was not approved' },
    }));

    const rejection = drainProvider();
    await expect(rejection).rejects.not.toBeInstanceOf(NativeHostUnavailableError);
    await expect(rejection).rejects.toThrow(/approval_denied/);
  });
});
