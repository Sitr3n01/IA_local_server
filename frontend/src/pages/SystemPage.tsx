import { Button, IconAlertCircle, IconCheck, Skeleton, Surface } from '../design-system/primitives';
import { FieldList } from '../design-system/components';
import { useSystemViewModel } from '../features/system-status/useSystemViewModel';
import type { SystemData } from '../features/system-status/deriveSystem';
import { formatUptime } from '../features/system-status/format';
import './SystemPage.css';

function yesNo(value: boolean): string {
  return value ? 'Yes' : 'No';
}

function DeploymentSection({ deployment }: { deployment: SystemData['deployment'] }) {
  return (
    <Surface as="section" level="base" bordered rounded className="system-section" aria-labelledby="deployment-heading">
      <h2 id="deployment-heading" className="system-section__title">
        Deployment &amp; release
      </h2>
      {deployment ? (
        <FieldList
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
        <p className="system-section__empty">No deployment recorded yet on this machine.</p>
      )}
    </Surface>
  );
}

function RuntimesSection({ runtimes }: { runtimes: SystemData['runtimes'] }) {
  return (
    <Surface as="section" level="base" bordered rounded className="system-section" aria-labelledby="runtimes-heading">
      <h2 id="runtimes-heading" className="system-section__title">
        Runtimes
      </h2>
      {runtimes.length === 0 ? (
        <p className="system-section__empty">No runtime information reported.</p>
      ) : (
        <div className="system-runtimes">
          {runtimes.map((runtime) => (
            <div key={runtime.id} className="system-runtimes__item">
              <FieldList
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
    </Surface>
  );
}

function UpstreamSection({ upstream }: { upstream: SystemData['upstream'] }) {
  return (
    <Surface as="section" level="base" bordered rounded className="system-section" aria-labelledby="upstream-heading">
      <h2 id="upstream-heading" className="system-section__title">
        Upstream
      </h2>
      <FieldList
        fields={[
          { label: 'URL', value: upstream.url, mono: true },
          {
            label: 'Reachable',
            value: (
              <span className={`system-reachability${upstream.reachable ? '' : ' system-reachability--down'}`}>
                {upstream.reachable ? <IconCheck /> : <IconAlertCircle />}
                {upstream.reachable ? 'Reachable' : 'Unreachable'}
              </span>
            ),
          },
        ]}
      />
    </Surface>
  );
}

function UptimeSection({ uptimeSeconds }: { uptimeSeconds: number }) {
  return (
    <Surface as="section" level="base" bordered rounded className="system-section" aria-labelledby="uptime-heading">
      <h2 id="uptime-heading" className="system-section__title">
        Uptime
      </h2>
      <p className="system-section__value system-section__value--mono">
        {formatUptime(uptimeSeconds)} ({uptimeSeconds}s)
      </p>
    </Surface>
  );
}

function SystemSkeleton() {
  return (
    <div className="system-page__body app-shell__sparse-state" aria-busy="true">
      <span className="ds-visually-hidden" role="status">
        Loading system detail…
      </span>
      {[0, 1, 2, 3].map((key) => (
        <Surface key={key} level="base" bordered rounded className="system-section">
          <Skeleton variant="text" lines={3} />
        </Surface>
      ))}
    </div>
  );
}

function SystemError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <Surface level="subtle" bordered rounded className="system-section system-error" role="alert">
      <p className="system-error__message">{message}</p>
      <Button variant="secondary" onClick={onRetry}>
        Retry
      </Button>
    </Surface>
  );
}

/**
 * Screen 2: the "what machine is this" tier. Denser and deliberately
 * unexpressive - see `useSystemViewModel` for the derivation this renders.
 * Every column of digits/ids/hashes uses monospace with tabular-nums
 * (inherited globally from reset.css for `code`/`pre`).
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
      <h1 id="system-heading" className="system-page__title">
        System
      </h1>

      {vm.state === 'loading' && <SystemSkeleton />}

      {vm.state === 'error' && (
        <div className="system-page__body app-shell__sparse-state">
          <SystemError message={vm.message} onRetry={vm.retry} />
        </div>
      )}

      {(vm.state === 'empty' || vm.state === 'populated') && (
        <div className="system-page__body">
          <DeploymentSection deployment={vm.data.deployment} />
          <RuntimesSection runtimes={vm.data.runtimes} />
          <UpstreamSection upstream={vm.data.upstream} />
          <UptimeSection uptimeSeconds={vm.data.uptimeSeconds} />
        </div>
      )}
    </Surface>
  );
}
