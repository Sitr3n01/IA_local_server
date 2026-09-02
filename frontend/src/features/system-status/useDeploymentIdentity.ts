import { useStatusQuery } from '../../api/queries/useStatus';

/**
 * The environment the console is currently driving, for the top bar.
 *
 * The top bar used to repeat the active destination's name, which the page's
 * own `<h1>` already states about 60px below it and which the rail's selected
 * item states a third time. Three statements of the same fact, in one
 * viewport, on a strip that is now permanently visible.
 *
 * The environment is the fact worth putting there instead. This provider runs
 * as `canary` or `final`, the console can be pointed at either, and the
 * screens look identical - so "which one am I about to load a model on" is
 * exactly the question the persistent chrome should answer and previously
 * only the System page could.
 *
 * `deployment` is `.optional()` in the schema - absent entirely on a checkout
 * that has never been deployed - so this returns `undefined` rather than
 * inventing a label, and the top bar renders nothing at all in that case.
 * Shares the one cached poll with every other consumer of `useStatusQuery`.
 */
export function useDeploymentIdentity(): string | undefined {
  const { data } = useStatusQuery();
  return data?.deployment?.environment;
}
