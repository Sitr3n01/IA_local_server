import { useEffect, useRef, useState } from 'react';
import { useStatusQuery } from '../../api/queries/useStatus';
import { maintenanceLabel } from '../maintenanceState';

/**
 * The console's single spoken channel: what a screen-reader user is told when
 * the provider's state changes underneath them.
 *
 * Three rules define it, and each exists because the obvious alternative is
 * worse.
 *
 * **Only transitions, never values.** The status query polls every 5 seconds.
 * Announcing what it returns would speak roughly 720 times an hour and drown
 * out everything else on the machine, which is indistinguishable from
 * announcing nothing - worse, actually, since it also buries whatever the
 * user was actually reading. This hook compares each poll against the last
 * and emits only when something crossed a boundary.
 *
 * **Only semantic state, never quantities.** Readiness, maintenance, and
 * whether the figures on screen are still live. Never the gate counters,
 * never the queue depth, never the request log - those change constantly and
 * mean nothing as isolated spoken numbers. An operator who wants a number
 * navigates to it; the announcement's job is to say that something they were
 * relying on stopped being true.
 *
 * **The region is mounted for the app's whole life, and only its text
 * changes.** This is the part that is easy to get wrong: assistive technology
 * generally announces mutations *within* an existing live region, and a
 * region inserted into the DOM already carrying its message is frequently not
 * announced at all. An earlier version of the stale-data warning mounted and
 * unmounted with the condition it described, which reads correctly in the
 * markup and would have been silent in practice. `AppShell` therefore renders
 * an always-present `role="status"` element whose content this hook drives.
 *
 * Returns the empty string when there is nothing to say - which is the normal
 * state, and leaves the region present but empty.
 */
export function useLiveAnnouncement(): string {
  const { data, isError } = useStatusQuery();
  const [message, setMessage] = useState('');

  // What the last poll observed. `undefined` means "nothing observed yet", and
  // is deliberately distinct from a known value: the first successful poll
  // must not announce, because nothing changed - the operator just arrived.
  const lastReady = useRef<boolean | undefined>(undefined);
  const lastMaintenance = useRef<string | undefined>(undefined);
  const lastReachable = useRef<boolean | undefined>(undefined);

  useEffect(() => {
    const reachable = !isError;

    // Reachability first: when the provider stops answering, the other two
    // signals are stale by definition and announcing them would assert
    // something this console cannot currently observe.
    if (lastReachable.current !== undefined && lastReachable.current !== reachable) {
      lastReachable.current = reachable;
      setMessage(
        reachable
          ? 'IA Local is answering again. The figures on screen are live.'
          : 'IA Local stopped answering. The figures on screen are the last ones received and are no longer updating.',
      );
      return;
    }
    lastReachable.current = reachable;

    if (!reachable || data === undefined) {
      return;
    }

    if (lastReady.current !== undefined && lastReady.current !== data.ready) {
      lastReady.current = data.ready;
      setMessage(data.ready ? 'IA Local is ready to serve.' : 'IA Local is no longer ready to serve.');
      return;
    }
    lastReady.current = data.ready;

    const maintenance = data.maintenance.state;
    if (lastMaintenance.current !== undefined && lastMaintenance.current !== maintenance) {
      lastMaintenance.current = maintenance;
      setMessage(`Maintenance state changed to ${maintenanceLabel(maintenance)}.`);
      return;
    }
    lastMaintenance.current = maintenance;
  }, [data, isError]);

  return message;
}
