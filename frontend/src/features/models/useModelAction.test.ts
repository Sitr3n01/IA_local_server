import { act, renderHook } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { NativeHostUnavailableError } from '../../api/commands/models';
import { useModelAction } from './useModelAction';

describe('useModelAction', () => {
  it('starts idle', () => {
    const { result } = renderHook(() => useModelAction(vi.fn()));
    expect(result.current[0]).toEqual({ status: 'idle' });
  });

  it('goes pending immediately, then back to idle on a resolved command (no fake success state exists)', async () => {
    let resolveCommand: () => void = () => {};
    const command = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveCommand = resolve;
        }),
    );
    const { result } = renderHook(() => useModelAction(command));

    act(() => {
      void result.current[1]('gemma4-12b-qat-ud-q4xl');
    });
    expect(result.current[0]).toEqual({ status: 'pending' });
    expect(command).toHaveBeenCalledWith('gemma4-12b-qat-ud-q4xl');

    await act(async () => {
      resolveCommand();
      await Promise.resolve();
    });
    expect(result.current[0]).toEqual({ status: 'idle' });
  });

  it('classifies NativeHostUnavailableError into its own "no-native-host" state, not the generic error state', async () => {
    const command = vi
      .fn()
      .mockRejectedValue(new NativeHostUnavailableError('needs the native console host', 'host-not-present'));
    const { result } = renderHook(() => useModelAction(command));

    await act(async () => {
      await result.current[1]('gemma4-12b-qat-ud-q4xl');
    });

    expect(result.current[0]).toEqual({ status: 'no-native-host', message: 'needs the native console host' });
  });

  it('classifies any other rejection as the generic "error" state', async () => {
    const command = vi.fn().mockRejectedValue(new Error('bridge is present but not wired up yet'));
    const { result } = renderHook(() => useModelAction(command));

    await act(async () => {
      await result.current[1]('gemma4-12b-qat-ud-q4xl');
    });

    expect(result.current[0]).toEqual({ status: 'error', message: 'bridge is present but not wired up yet' });
  });

  it('falls back to a plain message for a non-Error rejection', async () => {
    const command = vi.fn().mockRejectedValue('a raw string rejection');
    const { result } = renderHook(() => useModelAction(command));

    await act(async () => {
      await result.current[1]('gemma4-12b-qat-ud-q4xl');
    });

    expect(result.current[0]).toEqual({ status: 'error', message: 'The request could not be completed.' });
  });
});
