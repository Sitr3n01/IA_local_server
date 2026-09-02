import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { useRailCollapsed } from './useRailCollapsed';

const STORAGE_KEY = 'ia-console:nav-rail-collapsed';

afterEach(() => {
  window.localStorage.clear();
  vi.restoreAllMocks();
});

describe('useRailCollapsed', () => {
  it('defaults to expanded (false) when nothing has been persisted yet', () => {
    const { result } = renderHook(() => useRailCollapsed());
    expect(result.current[0]).toBe(false);
  });

  it('reads a previously persisted "true" as collapsed', () => {
    window.localStorage.setItem(STORAGE_KEY, 'true');
    const { result } = renderHook(() => useRailCollapsed());
    expect(result.current[0]).toBe(true);
  });

  it('persists a toggle so a later mount reads it back', () => {
    const { result, unmount } = renderHook(() => useRailCollapsed());
    act(() => result.current[1](true));
    expect(result.current[0]).toBe(true);
    unmount();

    const { result: second } = renderHook(() => useRailCollapsed());
    expect(second.current[0]).toBe(true);
  });

  it('survives a localStorage.getItem that throws, defaulting to expanded', () => {
    vi.spyOn(window.localStorage.__proto__, 'getItem').mockImplementation(() => {
      throw new Error('storage disabled');
    });

    const { result } = renderHook(() => useRailCollapsed());
    expect(result.current[0]).toBe(false);
  });

  it('survives a localStorage.setItem that throws, still updating in-memory state', () => {
    vi.spyOn(window.localStorage.__proto__, 'setItem').mockImplementation(() => {
      throw new Error('storage disabled');
    });

    const { result } = renderHook(() => useRailCollapsed());
    expect(() => act(() => result.current[1](true))).not.toThrow();
    expect(result.current[0]).toBe(true);
  });

  it('survives localStorage.getItem returning nothing meaningful (empty string)', () => {
    vi.spyOn(window.localStorage.__proto__, 'getItem').mockImplementation(() => '');
    const { result } = renderHook(() => useRailCollapsed());
    expect(result.current[0]).toBe(false);
  });
});
