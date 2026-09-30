/**
 * Presentation formatters specific to the Activity screen, kept in
 * `features/` (not inlined in the page) so they are independently testable -
 * same placement rule as `src/features/system-status/format.ts` and
 * `src/features/models/format.ts`.
 */

/**
 * Fixed 24-hour clock, no locale variation: `Intl.DateTimeFormat` with an
 * explicit locale and `hour12: false`, rather than `toLocaleTimeString()`
 * with the host default. `en-GB` is chosen for its format, not its
 * language - it is the only part of this string that could have varied,
 * and an operator comparing a request time against a log line does not
 * want the separator or the ordering to depend on a Windows regional
 * setting.
 */
const clockFormatter = new Intl.DateTimeFormat('en-GB', {
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
});

/**
 * `"2026-08-27T13:53:59.4612286Z"` -> `"13:53:59"`, in the *machine's own*
 * time zone.
 *
 * Local, not UTC, deliberately: this console runs on the same machine as
 * the provider, and "was that just now?" is the only question this column
 * exists to answer. The provider's raw UTC timestamp is not lost - the full
 * request log one disclosure below renders `event.time` verbatim, which is
 * the string to quote in an incident report. The screen says which is
 * which.
 *
 * An unparseable timestamp returns the original string untouched rather
 * than "Invalid Date" or an empty cell: whatever the provider sent is more
 * useful to whoever has to debug it than a formatter's opinion of it.
 */
export function formatEventClock(time: string): string {
  const ms = Date.parse(time);
  if (Number.isNaN(ms)) return time;
  return clockFormatter.format(new Date(ms));
}
