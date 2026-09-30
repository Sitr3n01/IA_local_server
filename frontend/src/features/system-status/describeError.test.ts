import { describe, expect, it } from 'vitest';
import { NativeHostUnavailableError } from '../../api/errors';
import { describeStatusError } from './describeError';

const HOST_NOT_PRESENT_SENTENCE =
  'IA Local needs the native console host. Open the console inside cia-console.exe - this build is running outside it (a browser tab or `vite dev`), so there is no way to reach the provider from here.';

const PROVIDER_UNREACHABLE_SENTENCE =
  'IA Local is not reachable right now. Check that the provider (cia-edge) is running, then retry.';

describe('describeStatusError', () => {
  it('gives a distinct, actionable sentence for host-not-present, mentioning cia-console.exe rather than the provider', () => {
    const error = new NativeHostUnavailableError('window.chrome.webview is unavailable', 'host-not-present');
    expect(describeStatusError(error)).toBe(HOST_NOT_PRESENT_SENTENCE);
  });

  it('the host-not-present sentence is distinct from the provider-unreachable sentence', () => {
    expect(HOST_NOT_PRESENT_SENTENCE).not.toBe(PROVIDER_UNREACHABLE_SENTENCE);
  });

  it('falls back to the provider-unreachable sentence for a plain Error (network failure, timeout, non-2xx)', () => {
    expect(describeStatusError(new Error('fetch failed: ECONNREFUSED'))).toBe(PROVIDER_UNREACHABLE_SENTENCE);
  });

  it('falls back to the provider-unreachable sentence for a schema-mismatch-shaped error', () => {
    expect(describeStatusError(new Error('ZodError: invalid_type at "ready"'))).toBe(PROVIDER_UNREACHABLE_SENTENCE);
  });

  it('falls back to the provider-unreachable sentence for a non-Error rejection', () => {
    expect(describeStatusError('a raw string rejection')).toBe(PROVIDER_UNREACHABLE_SENTENCE);
    expect(describeStatusError(undefined)).toBe(PROVIDER_UNREACHABLE_SENTENCE);
  });

  it('branches on the error type, not on message text: a plain Error whose message reads like the host-not-present sentence still gets the generic provider-unreachable sentence', () => {
    const error = new Error(
      'IA Local needs the native console host. Open the console inside cia-console.exe, so there is no way to reach the provider from here.',
    );
    expect(describeStatusError(error)).toBe(PROVIDER_UNREACHABLE_SENTENCE);
  });
});
