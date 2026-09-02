import { IconAlertCircle, IconCheck, Skeleton } from '../../design-system/primitives';
import { assertNever } from '../../lib/assertNever';
import type { HealthSummary } from '../../features/system-status/useHealthSummary';
import './TopBar.css';

function HealthIndicator({ health }: { health: HealthSummary }) {
  switch (health.state) {
    case 'loading':
      return (
        <span className="top-bar__health top-bar__health--loading">
          <Skeleton variant="text" className="top-bar__health-skeleton" />
        </span>
      );
    case 'error':
      // Not colour-only: paired with the alert glyph and the word "Unreachable".
      return (
        <span className="top-bar__health top-bar__health--error">
          <IconAlertCircle />
          Unreachable
        </span>
      );
    case 'ready':
      // Not colour-only: paired with a check glyph and the word "Ready".
      return (
        <span className="top-bar__health top-bar__health--ready">
          <IconCheck />
          Ready
        </span>
      );
    case 'not-ready':
      // Not colour-only: paired with the alert glyph and the words "Not ready".
      return (
        <span className="top-bar__health top-bar__health--not-ready">
          <IconAlertCircle />
          Not ready
        </span>
      );
    default:
      return assertNever(health, 'TopBar.HealthIndicator');
  }
}

export interface TopBarProps {
  health: HealthSummary;
  /**
   * The deployment environment being driven (`canary`, `final`), or
   * `undefined` when the provider reports no deployment at all.
   */
  environment: string | undefined;
}

/**
 * The persistent status strip: which provider is being driven, and whether it
 * is healthy.
 *
 * It used to carry the active destination's name. That was the same word the
 * page's own `<h1>` says immediately below it and the same word the rail's
 * selected item says to its left - one fact, three times, in one viewport.
 * Since the shell was given a definite height this strip never scrolls away,
 * which makes the space too valuable to spend on a third restatement.
 *
 * The environment earns it: `canary` and `final` render identically
 * everywhere else in this console, and every button on the Models screen is a
 * model-lifecycle operation against whichever one is wired. Naming it here
 * means an operator cannot act on the wrong provider without having seen
 * which one it is.
 */
export function TopBar({ health, environment }: TopBarProps) {
  return (
    <header className="top-bar">
      {environment === undefined ? (
        <span />
      ) : (
        <span className="top-bar__environment">
          <span className="top-bar__environment-label">Environment</span>
          <span className="top-bar__environment-value">{environment}</span>
        </span>
      )}
      <HealthIndicator health={health} />
    </header>
  );
}
