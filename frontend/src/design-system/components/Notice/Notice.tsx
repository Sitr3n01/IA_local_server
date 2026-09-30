import type { ElementType, HTMLAttributes, ReactNode } from 'react';
import { IconAlertCircle, IconInfo } from '../../primitives';
import './Notice.css';

export type NoticeTone = 'info' | 'warning' | 'danger';

export interface NoticeProps extends HTMLAttributes<HTMLElement> {
  tone: NoticeTone;
  /**
   * An optional control belonging to this notice - in practice a "Retry"
   * button on an error state. Rendered inside the notice so the action and
   * the sentence explaining it can never be separated by a reflow.
   */
  action?: ReactNode;
  /** Polymorphic root - e.g. `"section"` where the notice is a region of the page. */
  as?: ElementType;
  children: ReactNode;
}

const TONE_ICON: Record<NoticeTone, typeof IconInfo> = {
  info: IconInfo,
  warning: IconAlertCircle,
  danger: IconAlertCircle,
};

/**
 * One flat, unmissable band for the things this console has to say about
 * itself: the figures on screen are stale, the admission gate is refusing
 * every action, the provider could not be reached, an operation failed.
 *
 * It exists for two reasons at once.
 *
 * The visual one: before Sprint 10 every one of those was a `Surface`
 * `bordered rounded` with a status-coloured border - which is to say, a
 * card. On a page whose whole direction is "a bordered card is the
 * exception", the states an operator most needs to read were the loudest
 * boxes on the screen, and they looked like leftovers from the old
 * language. This draws no box: a tonal ground, a 3px tone-coloured rule
 * down the leading edge, and the sentence itself in ordinary body colour.
 *
 * The structural one: those states were written out longhand five times
 * across four pages plus the shell, byte-similar each time - the same
 * signal that produced `FieldList`.
 *
 * Two contracts it deliberately does NOT own:
 *
 *  - It sets no ARIA role of its own. Whether a given notice is an `alert`,
 *    a `status`, or silent chrome depends entirely on the situation it
 *    describes, and only the caller knows that. `role` is passed straight
 *    through; the shell's stale banner passes none on purpose (see
 *    AppShell.tsx for why announcing it would be silent in practice).
 *  - It never carries meaning by colour. The tone paints the icon and the
 *    edge rule; the sentence a caller passes as `children` has to say the
 *    thing outright, and every current caller does.
 */
export function Notice({ tone, action, as: Component = 'div', className, children, ...rest }: NoticeProps) {
  const Icon = TONE_ICON[tone];
  const classes = ['ds-notice', `ds-notice--${tone}`];
  if (className) classes.push(className);

  return (
    <Component className={classes.join(' ')} {...rest}>
      <Icon className="ds-notice__icon" />
      <div className="ds-notice__body">{children}</div>
      {action !== undefined && <div className="ds-notice__action">{action}</div>}
    </Component>
  );
}
