import type { Status } from '../../api/schemas/status';
import { formatTokenCount } from './format';

export interface ActiveModelSummary {
  /** The provider's own id for the loaded model - `status.active_model`, verbatim. */
  id: string;
  /**
   * `models[].display_name` for that id, matched exactly. Falls back to the
   * id itself when `models[]` carries no entry for it: an id is an honest,
   * if ugly, name for the thing that is loaded, and inventing a prettier
   * one would name something the provider never described.
   */
  displayName: string;
  /**
   * The quantized weights file behind it, from `model_statuses[].profile`.
   * `null` when the provider reports a loaded model that has no matching
   * `model_statuses` entry - which should not happen, and is rendered as an
   * absent line rather than as a guess if it ever does.
   */
  weights: string | null;
  /** Its context window, or `null` under the same "no matching entry" condition. */
  contextTokens: number | null;
}

/**
 * Which model is loaded on the provider right now, and the two facts about
 * it worth carrying in the shell's header.
 *
 * This is the one derivation for "the active model" in the console.
 * `deriveOverview` used to inline its own copy of the id lookup, and Sprint
 * 10 needed the same answer in the app shell - at which point there would
 * have been two lookups to keep in step, one of which would silently be the
 * stale one on the day the payload's shape moves. Overview now calls this.
 *
 * `null` means no model is loaded, which the payload states as
 * `active_model === ''` - not as an absent key, and not as a null.
 */
export function deriveActiveModel(status: Status): ActiveModelSummary | null {
  if (status.active_model === '') {
    return null;
  }

  const id = status.active_model;
  // Exact id match on both lookups. Never a substring or prefix match: two
  // entries in the fixture differ only by a `-256k` suffix, so a loose match
  // would confidently attribute one model's context window to the other.
  const info = status.models.find((model) => model.id === id);
  const runtime = status.model_statuses.find((model) => model.id === id);

  return {
    id,
    displayName: info?.display_name ?? id,
    weights: runtime?.profile.weights ?? null,
    contextTokens: runtime?.context_tokens ?? null,
  };
}

/**
 * The quiet second line under the model's name in the header: what it is
 * running, and how much context it has.
 *
 * Returns `null` when the payload gave neither fact, so the caller renders
 * no line at all rather than an empty one or a "not measured" placeholder -
 * this is chrome, and chrome that says nothing should occupy nothing.
 */
export function describeActiveModel(model: ActiveModelSummary): string | null {
  const parts: string[] = [];
  if (model.weights !== null) parts.push(model.weights);
  if (model.contextTokens !== null) parts.push(`${formatTokenCount(model.contextTokens)} context`);
  return parts.length === 0 ? null : parts.join(' · ');
}
