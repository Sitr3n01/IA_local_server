import { Button, IconAlertCircle, IconCheck, Skeleton, Surface } from '../design-system/primitives';
import { FieldList, Notice } from '../design-system/components';
import { useSystemViewModel } from '../features/system-status/useSystemViewModel';
import type { SystemData } from '../features/system-status/deriveSystem';
import { formatUptime } from '../features/system-status/format';
import './page.css';
import './SystemPage.css';

function yesNo(value: boolean): string {
  return value ? 'Yes' : 'No';
}

function DeploymentSection({ deployment }: { deployment: SystemData['deployment'] }) {
  return (
    <section className="page-section" aria-labelledby="deployment-heading">
      <h2 id="deployment-heading" className="page-section__title">
        Deployment &amp; release
      </h2>
      {deployment ? (
        <FieldList
          className="system-fields"
          fields={[
            { label: 'Environment', value: deployment.environment },
            { label: 'Release', value: deployment.release, mono: true },
            { label: 'Version', value: deployment.version, mono: true },
            { label: 'Commit', value: deployment.commit, mono: true },
            { label: 'Source dirty', value: yesNo(deployment.source_dirty) },
            { label: 'Status', value: deployment.status },
            { label: 'Healthy', value: yesNo(deployment.healthy) },
            { label: 'Created (UTC)', value: deployment.created_utc, mono: true },
            ...(deployment.previous_release
              ? [{ label: 'Previous release', value: deployment.previous_release, mono: true }]
              : []),
          ]}
        />
      ) : (
        <p className="page-empty">No deployment recorded yet on this machine.</p>
      )}
    </section>
  );
}

/**
 * One block per runtime, separated by a hairline. The blocks are not
 * bordered and not padded into panels: this is a reference table with
 * repeating groups, and the group boundary needs exactly one line to be
 * unambiguous.
 */
function RuntimesSection({ runtimes }: { runtimes: SystemData['runtimes'] }) {
  return (
    <section className="page-section page-section--ruled" aria-labelledby="runtimes-heading">
      <h2 id="runtimes-heading" className="page-section__title">
        Runtimes
      </h2>
      {runtimes.length === 0 ? (
        <p className="page-empty">No runtime information reported.</p>
      ) : (
        <div className="system-runtimes">
          {runtimes.map((runtime) => (
            <div key={runtime.id} className="system-runtimes__item">
              <FieldList
                className="system-fields"
                fields={[
                  { label: 'ID', value: runtime.id, mono: true },
                  { label: 'State', value: runtime.state },
                  { label: 'Engine', value: runtime.engine },
                  { label: 'Variant', value: runtime.variant },
                  { label: 'Backend', value: runtime.backend },
                  { label: 'Artifact SHA-256 (prefix)', value: runtime.artifact_sha256_prefix, mono: true },
                  { label: 'Checkpoint-capable', value: yesNo(runtime.checkpoint_capable) },
                ]}
              />
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

/**
 * cia-edge itself: the upstream it proxies to, whether that upstream can be
 * reached, and how long this process has been up.
 *
 * Uptime used to be a section of its own holding exactly one figure, which
 * on a screen made of label/value lists meant a heading, a hairline and
 * `--space-2xl` of air spent on a single number. It is a row here, beside
 * the two other facts about the same running process.
 */
function ServiceSection({ upstream, uptimeSeconds }: { upstream: SystemData['upstream']; uptimeSeconds: number }) {
  return (
    <section className="page-section page-section--ruled" aria-labelledby="service-heading">
      <h2 id="service-heading" className="page-section__title">
        Service
      </h2>
      <FieldList
        className="system-fields"
        fields={[
          { label: 'URL', value: upstream.url, mono: true },
          {
            label: 'Reachable',
            value: (
              <span className={`page-status${upstream.reachable ? ' page-status--positive' : ' page-status--negative'}`}>
                {upstream.reachable ? <IconCheck /> : <IconAlertCircle />}
                {upstream.reachable ? 'Reachable' : 'Unreachable'}
              </span>
            ),
          },
          { label: 'Uptime', value: `${formatUptime(uptimeSeconds)} (${uptimeSeconds}s)`, mono: true },
        ]}
      />
    </section>
  );
}

function SystemSkeleton() {
  return (
    <div className="system-page__body app-shell__sparse-state" aria-busy="true">
      <span className="ds-visually-hidden" role="status">
        Loading system detail…
      </span>
      <div className="system-skeleton">
        {[0, 1, 2].map((key) => (
          <div key={key} className="system-skeleton__section">
            <Skeleton variant="text" className="system-skeleton__heading" />
            <Skeleton variant="text" lines={3} />
          </div>
        ))}
      </div>
    </div>
  );
}

/**
 * Screen 2: the "what machine is this" tier. The densest screen in the
 * console and deliberately the least expressive - see `useSystemViewModel`
 * for the derivation it renders. Every column of digits/ids/hashes uses
 * monospace with tabular-nums (inherited globally from reset.css for
 * `code`/`pre`).
 *
 * Sprint 10 kept the density and removed the panels: four bordered
 * `Surface` sections became three flat ones separated by hairlines, on a
 * tighter vertical rhythm than any other screen. That tightness is this
 * page's personality - a reference page earns its density, where Overview
 * earns its air.
 *
 * As of Sprint 6, gate counters, GPU memory, and the recent-events feed
 * live on the Activity screen instead (`src/pages/ActivityPage.tsx`) -
 * "what is the provider doing right now" is a different question from
 * "what machine is this", per the Sprint 6 brief. This page keeps
 * deployment, runtimes, upstream, and uptime only.
 */
export function SystemPage() {
  const vm = useSystemViewModel();

  return (
    <Surface as="main" level="canvas" className="system-page" aria-labelledby="system-heading">
      <div className="system-page__header">
        <h1 id="system-heading" className="page-heading">
          System
        </h1>
        <p className="page-lead">
          What machine this is: the release installed on it, the runtimes it can start, and the service this
          console is talking to.
        </p>
      </div>

      {vm.state === 'loading' && <SystemSkeleton />}

      {vm.state === 'error' && (
        <div className="system-page__body app-shell__sparse-state">
          <Notice
            tone="danger"
            role="alert"
            action={
              <Button variant="secondary" onClick={vm.retry}>
                Retry
              </Button>
            }
          >
            {vm.message}
          </Notice>
        </div>
      )}

      {(vm.state === 'empty' || vm.state === 'populated') && (
        <div className="system-page__body">
          <DeploymentSection deployment={vm.data.deployment} />
          <RuntimesSection runtimes={vm.data.runtimes} />
          <ServiceSection upstream={vm.data.upstream} uptimeSeconds={vm.data.uptimeSeconds} />
        </div>
      )}
    </Surface>
  );
}
