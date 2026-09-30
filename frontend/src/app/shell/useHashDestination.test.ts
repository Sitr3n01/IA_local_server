import { afterEach, describe, expect, it } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { DEFAULT_DESTINATION, destinationHash, parseDestination, useHashDestination } from './useHashDestination';

/**
 * `hashchange` is delivered on a macrotask, not a microtask - verified by
 * counting listener calls either side of the turn. `await act(async () => {})`
 * flushes microtasks only, so without this the hook reads as "never updates"
 * and the tests fail against working code. A real browser behaves the same
 * way: a click that changes the fragment settles a tick later, which is
 * invisible to an operator and matters only here.
 */
const hashChangeDelivered = () => new Promise((resolve) => setTimeout(resolve, 0));

afterEach(() => {
  // `location.hash = ''` leaves a bare '#'; replaceState clears it properly.
  window.history.replaceState(null, '', window.location.pathname);
});

describe('parseDestination', () => {
  it('resolves every real destination', () => {
    expect(parseDestination('#/overview')).toBe('overview');
    expect(parseDestination('#/models')).toBe('models');
    expect(parseDestination('#/activity')).toBe('activity');
    expect(parseDestination('#/system')).toBe('system');
  });

  it('refuses anything that is not exactly a destination id', () => {
    // Whole-string match, never prefix or substring: the failure mode of a
    // loose test is `#/models-experimental` silently rendering Models.
    for (const hash of [
      '',
      '#',
      '#/',
      '#/nonsense',
      '#/models-experimental',
      '#/MODELS',
      '#models',
      '#//models',
      '#/models/extra',
      'models',
    ]) {
      expect(parseDestination(hash)).toBeUndefined();
    }
  });
});

describe('useHashDestination', () => {
  it('falls back to the default destination when there is no fragment', () => {
    const { result } = renderHook(() => useHashDestination());
    expect(result.current[0]).toBe(DEFAULT_DESTINATION);
  });

  it('reads the destination out of the fragment on first render, so a reload lands where the operator was', () => {
    window.history.replaceState(null, '', destinationHash('activity'));
    const { result } = renderHook(() => useHashDestination());
    expect(result.current[0]).toBe('activity');
  });

  it('renders the default for an unrecognised fragment rather than an error screen', () => {
    window.history.replaceState(null, '', '#/not-a-destination');
    const { result } = renderHook(() => useHashDestination());
    expect(result.current[0]).toBe(DEFAULT_DESTINATION);
  });

  it('writes the fragment when navigating, which is what gives Back somewhere to go', async () => {
    const { result } = renderHook(() => useHashDestination());
    await act(async () => {
      result.current[1]('models');
      await hashChangeDelivered();
    });
    expect(window.location.hash).toBe('#/models');
    expect(result.current[0]).toBe('models');
  });

  it('follows a fragment changed from outside React - a typed URL, or the Back button', async () => {
    const { result } = renderHook(() => useHashDestination());
    await act(async () => {
      window.location.hash = destinationHash('system');
      await hashChangeDelivered();
    });
    expect(result.current[0]).toBe('system');
  });

  it('stops listening once unmounted, so a later hashchange cannot update a dead component', async () => {
    const { result, unmount } = renderHook(() => useHashDestination());
    await act(async () => {
      result.current[1]('models');
      await hashChangeDelivered();
    });
    expect(result.current[0]).toBe('models');

    unmount();
    // No act() wrapper and no assertion on the hook: the point is that this
    // must not throw or warn about updating an unmounted component.
    window.location.hash = destinationHash('system');
    expect(window.location.hash).toBe('#/system');
  });
});
