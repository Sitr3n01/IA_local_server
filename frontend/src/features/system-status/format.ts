/**
 * Small presentation formatters shared by System's denser sections. Kept in
 * `features/` (not inlined in the page) so they're independently testable
 * and reusable if a later screen needs the same uptime/MiB formatting.
 */

/** `9620` -> `"2h 40m 20s"`. Omits leading zero-valued units, but always keeps seconds. */
export function formatUptime(totalSeconds: number): string {
  const safeSeconds = Math.max(0, Math.floor(totalSeconds));
  const days = Math.floor(safeSeconds / 86_400);
  const hours = Math.floor((safeSeconds % 86_400) / 3600);
  const minutes = Math.floor((safeSeconds % 3600) / 60);
  const seconds = safeSeconds % 60;

  const parts: string[] = [];
  if (days > 0) parts.push(`${days}d`);
  if (days > 0 || hours > 0) parts.push(`${hours}h`);
  if (days > 0 || hours > 0 || minutes > 0) parts.push(`${minutes}m`);
  parts.push(`${seconds}s`);
  return parts.join(' ');
}

/** `null` means "not measured" (see status.ts's schema doc comment) - never silently rendered as 0. */
export function formatMib(value: number | null): string {
  if (value === null) return 'not measured';
  return `${(Math.round(value * 10) / 10).toFixed(1)} MiB`;
}
