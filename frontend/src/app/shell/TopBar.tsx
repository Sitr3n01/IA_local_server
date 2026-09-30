import { IconAlertCircle, IconCheck, Skeleton } from '../../design-system/primitives';
import { assertNever } from '../../lib/assertNever';
import type { HealthSummary } from '../../features/system-status/useHealthSummary';
import type { ActiveModelState } from '../../features/system-status/useActiveModel';
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

/**
 * The header's subject: which model this console is driving.
 *
 * Three states, and the empty one matters as much as the loaded one. A
 * provider with nothing loaded is a completely normal condition here - it
 * is the fixture's own state - so it gets a calm, plain sentence rather
 * than a warning treatment. Nothing is invented in any state: no model
 * name appears until the payload has named one.
 */
function ActiveModelIdentity({ activeModel }: { activeModel: ActiveModelState }) {
  if (activeModel.state === 'unknown') {
    return (
      <div className="top-bar__identity">
        <Skeleton variant="text" className="top-bar__identity-skeleton" />
      </div>
    );
  }

  if (activeModel.state === 'none') {
    return (
      <div className="top-bar__identity">
        <p className="top-bar__model top-bar__model--none">No model loaded</p>
        <p className="top-bar__model-detail">Nothing is serving on this provider right now.</p>
      </div>
    );
  }

  return (
    <div className="top-bar__identity">
      <p className="top-bar__model">{activeModel.model.displayName}</p>
      {activeModel.detail !== null && <p className="top-bar__model-detail">{activeModel.detail}</p>}
    </div>
  );
}

export interface TopBarProps {
  health: HealthSummary;
  /**
   * The deployment environment being driven (`canary`, `final`), or
   * `undefined` when the provider reports no deployment at all.
   */
  environment: string | undefined;
  /** Which model is loaded. `undefined` is treated exactly as `{ state: 'unknown' }`. */
  activeModel?: ActiveModelState | undefined;
}

/**
 * The shell's header: what is loaded, where, and whether it can serve.
 *
 * Through Sprint 9 this was a status strip. It read, left to right,
 * "ENVIRONMENT canary ... READY" - which is a control plane's answer to a
 * control plane's question, and the first thing an operator saw on every
 * screen. Sprint 10 reordered it around the question the console is
 * actually about: *what is this provider running?*
 *
 * So the active model's name is now the largest thing in the header and
 * the first thing in the reading order, with its weights and context
 * window on a quiet second line. Nothing was dropped to make room. The
 * environment is still here, still monospace because it is an identifier an
 * operator matches against a deployment rather than prose, and still
 * present for the same reason it was promoted in Sprint 9: `canary` and
 * `final` render identically everywhere else in this console, and every
 * button on the Models screen is a lifecycle operation against whichever
 * one is wired. It sits beside the readiness indicator now instead of
 * leading the strip - one step quieter, not one step hidden.
 *
 * It also no longer draws a bottom border nor paints a ground of its own. It
 * sits on the same canvas as the page beneath it, which is what makes it
 * read as the top of the workspace rather than as an administrative bar
 * bolted above one.
 */
export function TopBar({ health, environment, activeModel = { state: 'unknown' } }: TopBarProps) {
  return (
    <header className="top-bar app-shell__column">
      <ActiveModelIdentity activeModel={activeModel} />

      <div className="top-bar__context">
        {environment !== undefined && (
          <span className="top-bar__environment">
            <span className="top-bar__environment-label">Environment</span>
            <span className="top-bar__environment-value">{environment}</span>
          </span>
        )}
        <HealthIndicator health={health} />
      </div>
    </header>
  );
}
