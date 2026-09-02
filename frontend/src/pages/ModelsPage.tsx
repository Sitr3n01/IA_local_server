import { useId, useMemo, useState } from 'react';
import {
  Button,
  IconAlertCircle,
  IconCheck,
  IconChevronDown,
  IconInfo,
  Skeleton,
  Surface,
} from '../design-system/primitives';
import { FieldList, ResourceMeter } from '../design-system/components';
import { loadModel, switchModel, unloadModel } from '../api/commands/models';
import { useModelsViewModel } from '../features/models/useModelsViewModel';
import { useModelAction } from '../features/models/useModelAction';
import type { ModelActionKind, ModelCardData } from '../features/models/deriveModels';
import { formatContextTokens, formatReclaimableGib } from '../features/models/format';
import { assertNever } from '../lib/assertNever';
import './ModelsPage.css';

function yesNo(value: boolean): string {
  return value ? 'Yes' : 'No';
}

/**
 * The wording an operator sees on every model-lifecycle button. Deliberately
 * "Request ___", not "Load"/"Unload"/"Switch" alone: ADR 0018 control 1
 * puts the actual authorization in a native Win32 dialog owned by
 * `cia-console.exe`, outside this DOM, so this button must never read as
 * the confirmation step itself. The caption underneath (`ACTION_CAPTION`)
 * says the same thing in a full sentence for anyone who wants it spelled
 * out, and is wired to the button via `aria-describedby` so assistive tech
 * announces it as part of the control's description.
 */
const ACTION_LABEL: Record<ModelActionKind, string> = {
  load: 'Request load',
  unload: 'Request unload',
  switch: 'Request switch',
};

const ACTION_CAPTION: Record<ModelActionKind, string> = {
  load: 'Asks the native console host to confirm before this model is loaded. This button does not load it by itself.',
  unload:
    'Asks the native console host to confirm before this model is unloaded. This button does not unload it by itself.',
  switch:
    'Asks the native console host to confirm before switching to this model. This button does not switch it by itself.',
};

/**
 * The caption for one action button, returned with a flag saying whether it
 * is *specific to this model* or the generic non-authorization statement.
 *
 * The distinction drives presentation. The generic sentence is identical on
 * every enabled card, so rendering it visibly under each button repeated one
 * paragraph six times on a six-model provider - twelve lines of the same
 * text on one screen, which reads as noise and trains the operator to skip
 * exactly the sentence that matters. It is stated once above the list
 * instead, and kept per-button for assistive technology, where there is no
 * "once above the list" and each control must carry its own description.
 *
 * A refusal reason is different: it is about *this* model and is the only
 * place the operator learns why a button is dead. That stays visible.
 *
 * The refusal wording is deliberately a short pointer, not
 * `model.action.disabledReason` verbatim, so the always-visible primary tier
 * never carries the capacity verdict in full - that sentence is what the
 * secondary tier behind "Show capacity details" is for, per the Sprint 4
 * brief. The full reason stays one click away: the gate-busy sentence in the
 * page-level banner above the list, the capacity sentence in
 * `model.reason.sentence` once expanded - the same value
 * `model.action.disabledReason` holds for the `'capacity'` cause, so the two
 * can never drift apart.
 */
function actionCaption(model: ModelCardData): { text: string; specific: boolean } {
  if (model.action.enabled) {
    return { text: ACTION_CAPTION[model.action.kind], specific: false };
  }
  if (model.action.disabledCause === 'gate-busy') {
    return { text: 'Refused while the admission gate is busy right now - see the banner above.', specific: true };
  }
  return {
    text: 'Not offered right now because this model cannot be admitted - see "Show capacity details" below for why.',
    specific: true,
  };
}

function commandFor(kind: ModelActionKind): (modelId: string) => Promise<void> {
  switch (kind) {
    case 'load':
      return (modelId: string) => loadModel({ modelId });
    case 'unload':
      return (modelId: string) => unloadModel({ modelId });
    case 'switch':
      return (modelId: string) => switchModel({ modelId });
    default:
      return assertNever(kind, 'ModelsPage.commandFor');
  }
}

function ModelActionButton({ model }: { model: ModelCardData }) {
  // `useModelAction` is re-created only when the action kind changes (the
  // action kind is stable for a given model/gate/active-model combination
  // within one render, so this never churns on every keystroke elsewhere on
  // the page).
  const command = useMemo(() => commandFor(model.action.kind), [model.action.kind]);
  const [outcome, run] = useModelAction(command);
  const captionId = useId();
  const caption = actionCaption(model);
  const isPending = outcome.status === 'pending';

  return (
    <div className="model-card__action">
      <Button
        variant="primary"
        size="sm"
        disabled={!model.action.enabled || isPending}
        loading={isPending}
        aria-describedby={captionId}
        onClick={() => void run(model.id)}
      >
        {ACTION_LABEL[model.action.kind]}
      </Button>
      <p
        id={captionId}
        className={
          caption.specific ? 'model-card__action-caption' : 'ds-visually-hidden'
        }
      >
        {caption.text}
      </p>
      {outcome.status === 'no-native-host' && (
        <p className="model-card__action-outcome model-card__action-outcome--no-native-host" role="alert">
          <IconAlertCircle />
          {outcome.message}
        </p>
      )}
      {outcome.status === 'error' && (
        <p className="model-card__action-outcome model-card__action-outcome--error" role="alert">
          <IconAlertCircle />
          The request could not be completed: {outcome.message}
        </p>
      )}
    </div>
  );
}

interface DisclosureToggleProps {
  open: boolean;
  onToggle: () => void;
  labelWhenClosed: string;
  labelWhenOpen: string;
  controlsId: string;
}

function DisclosureToggle({ open, onToggle, labelWhenClosed, labelWhenOpen, controlsId }: DisclosureToggleProps) {
  return (
    <Button
      variant="secondary"
      size="sm"
      className="model-card__disclosure"
      aria-expanded={open}
      aria-controls={controlsId}
      onClick={onToggle}
      trailingIcon={
        <IconChevronDown className={`model-card__disclosure-icon${open ? ' model-card__disclosure-icon--open' : ''}`} />
      }
    >
      {open ? labelWhenOpen : labelWhenClosed}
    </Button>
  );
}

const CAPABILITY_LABELS = {
  responses: 'Responses API',
  chat_completions: 'Chat completions',
  streaming: 'Streaming',
  function_calling: 'Function calling',
  structured_output: 'Structured output',
  reasoning: 'Reasoning',
} as const;

const CAPABILITY_KEYS = Object.keys(CAPABILITY_LABELS) as (keyof typeof CAPABILITY_LABELS)[];

function CapabilitiesRow({ capabilities }: { capabilities: ModelCardData['capabilities'] }) {
  // Only a flag that is *exactly* `true` in the payload counts as
  // supported - `undefined` (never sent) and any other value never render
  // as if the capability were present. Never derived from the model id.
  const supported = CAPABILITY_KEYS.filter((key) => capabilities?.[key] === true);

  return (
    <div className="model-card__capabilities">
      <span className="model-card__capabilities-label">Capabilities</span>
      <div className="model-card__capability-chips">
        {supported.length === 0 ? (
          <span className="model-card__capability-chip model-card__capability-chip--none">None reported</span>
        ) : (
          supported.map((key) => (
            <span key={key} className="model-card__capability-chip">
              {CAPABILITY_LABELS[key]}
            </span>
          ))
        )}
      </div>
    </div>
  );
}

function ProfileFields({ profile }: { profile: ModelCardData['profile'] }) {
  return (
    <FieldList
      fields={[
        { label: 'Weights', value: profile.weights, mono: true },
        { label: 'KV cache (K)', value: profile.cache_type_k, mono: true },
        { label: 'KV cache (V)', value: profile.cache_type_v, mono: true },
        { label: 'Max output tokens', value: profile.max_output_tokens, mono: true },
        ...(profile.n_predict !== undefined ? [{ label: 'n_predict', value: profile.n_predict, mono: true }] : []),
        ...(profile.reasoning_budget !== undefined
          ? [{ label: 'Reasoning budget', value: profile.reasoning_budget, mono: true }]
          : []),
        ...(profile.compact_threshold_tokens !== undefined
          ? [{ label: 'Compact threshold tokens', value: profile.compact_threshold_tokens, mono: true }]
          : []),
        ...(profile.moe_offload
          ? [{ label: 'MoE CPU offload layers', value: profile.moe_offload.cpu_layers, mono: true }]
          : []),
      ]}
    />
  );
}

function CheckpointsFields({ checkpoints }: { checkpoints: ModelCardData['checkpoints'] }) {
  return (
    <FieldList
      fields={[
        { label: 'Configured', value: yesNo(checkpoints.configured) },
        { label: 'Runtime-capable', value: yesNo(checkpoints.runtime_capable) },
        { label: 'Checkpoint min step', value: checkpoints.checkpoint_min_step ?? 'not measured', mono: true },
        {
          label: 'Context checkpoints',
          value: checkpoints.ctx_checkpoints ? checkpoints.ctx_checkpoints.join(', ') : 'not measured',
          mono: true,
        },
      ]}
    />
  );
}

function RuntimeFields({ runtime }: { runtime: ModelCardData['runtime'] }) {
  return (
    <FieldList
      fields={[
        { label: 'Runtime ID', value: runtime.id, mono: true },
        { label: 'State (runtime, not model lifecycle)', value: runtime.state },
        { label: 'Engine', value: runtime.engine },
        { label: 'Variant', value: runtime.variant },
        { label: 'Backend', value: runtime.backend },
        { label: 'Artifact SHA-256 (prefix)', value: runtime.artifact_sha256_prefix, mono: true },
        { label: 'Checkpoint-capable', value: yesNo(runtime.checkpoint_capable) },
      ]}
    />
  );
}

function ModelCard({ model }: { model: ModelCardData }) {
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const detailsId = useId();
  const advancedId = useId();
  const nameId = useId();

  const reclaimableCommit = formatReclaimableGib(model.reclaimableCommitGib);
  const reclaimablePhysical = formatReclaimableGib(model.reclaimablePhysicalGib);
  const hasReclaimable = reclaimableCommit !== null || reclaimablePhysical !== null;

  return (
    <Surface as="article" level="base" bordered rounded className="model-card" aria-labelledby={nameId}>
      <div className="model-card__header">
        <h2 id={nameId} className="model-card__name">
          {model.displayName}
        </h2>
        <ModelActionButton model={model} />
      </div>

      <div className="model-card__badges">
        <span className={`model-card__badge${model.isActive ? ' model-card__badge--positive' : ''}`}>
          {model.isActive ? <IconCheck /> : <IconInfo />}
          {model.isActive ? 'Loaded' : 'Not loaded'}
        </span>
        <span
          className={`model-card__badge${model.isAvailableNow ? ' model-card__badge--positive' : ' model-card__badge--negative'}`}
        >
          {model.isAvailableNow ? <IconCheck /> : <IconAlertCircle />}
          {model.isAvailableNow ? 'Available now' : 'Not available now'}
        </span>
        <span className="model-card__badge model-card__badge--context">{formatContextTokens(model.contextTokens)}</span>
      </div>

      <DisclosureToggle
        open={detailsOpen}
        onToggle={() => setDetailsOpen((value) => !value)}
        labelWhenClosed="Show capacity details"
        labelWhenOpen="Hide capacity details"
        controlsId={detailsId}
      />

      {/* Stays mounted while collapsed, hidden by attribute rather than
          unmounted: the toggle's `aria-controls` must resolve to a real
          element in BOTH states, and conditionally rendering this left the
          control pointing at an id that did not exist whenever it was
          closed - which a screen reader follows to nothing. See
          `[hidden]` in design-system/styles/reset.css for why the attribute
          alone is not enough here. */}
      <div
        id={detailsId}
        className="model-card__details"
        role="region"
        aria-label={`${model.displayName} capacity details`}
        hidden={!detailsOpen}
      >
          <p className="model-card__reason">
            {model.reason.recognized ? model.reason.sentence : `Reason reported by the provider: ${model.reason.code}`}
          </p>

          <div className="model-card__meters">
            <ResourceMeter result={model.shortfalls.commit} />
            <ResourceMeter result={model.shortfalls.physical} />
            <ResourceMeter result={model.shortfalls.vram} />
          </div>

          {hasReclaimable && (
            <FieldList
              fields={[
                ...(reclaimableCommit !== null
                  ? [{ label: 'Reclaimable committed memory', value: reclaimableCommit, mono: true }]
                  : []),
                ...(reclaimablePhysical !== null
                  ? [{ label: 'Reclaimable physical memory', value: reclaimablePhysical, mono: true }]
                  : []),
              ]}
            />
          )}

          <CapabilitiesRow capabilities={model.capabilities} />

          <DisclosureToggle
            open={advancedOpen}
            onToggle={() => setAdvancedOpen((value) => !value)}
            labelWhenClosed="Show advanced detail"
            labelWhenOpen="Hide advanced detail"
            controlsId={advancedId}
          />

          <div
            id={advancedId}
            className="model-card__advanced"
            role="region"
            aria-label={`${model.displayName} advanced detail`}
            hidden={!advancedOpen}
          >
              <h3 className="model-card__advanced-title">Profile</h3>
              <ProfileFields profile={model.profile} />
              <h3 className="model-card__advanced-title">Checkpoints</h3>
              <CheckpointsFields checkpoints={model.checkpoints} />
              <h3 className="model-card__advanced-title">Runtime</h3>
              <RuntimeFields runtime={model.runtime} />
            </div>
        </div>
    </Surface>
  );
}

function ModelsSkeleton() {
  return (
    <div className="models-page__body app-shell__sparse-state" aria-busy="true">
      <span className="ds-visually-hidden" role="status">
        Loading models…
      </span>
      {[0, 1, 2].map((key) => (
        <Surface key={key} level="base" bordered rounded className="model-card">
          <Skeleton variant="text" lines={4} />
        </Surface>
      ))}
    </div>
  );
}

function ModelsError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <Surface level="subtle" bordered rounded className="model-card models-error" role="alert">
      <p className="models-error__message">{message}</p>
      <Button variant="secondary" onClick={onRetry}>
        Retry
      </Button>
    </Surface>
  );
}

/**
 * Screen 3: Models. Primary tier (always visible, per card): display name,
 * whether it is the loaded model, whether it can be admitted right now,
 * and its context window. Secondary tier (behind "Show capacity details"):
 * the full capacity verdict (reused from `reasonExplanations.ts`), three
 * `ResourceMeter`s (reused from `shortfall.ts`), reclaimable-memory figures
 * when present, and reported capabilities. Advanced tier (behind a further
 * "Show advanced detail" nested inside that): profile, checkpoints, and
 * runtime identity - see `useModelsViewModel`/`deriveModels.ts` for the
 * derivation this renders.
 *
 * There is deliberately no model *lifecycle* state (candidate/qualified/
 * enabled/retired) rendered anywhere on this screen: `edge.Model.State` is
 * tagged `json:"-"` and never reaches this payload. `runtime.state` here is
 * the *runtime's* state, not the model's - see `RuntimeFields` above and
 * `frontend/README.md`'s "Models (Sprint 4)" section for this backend
 * contract gap.
 */
export function ModelsPage() {
  const vm = useModelsViewModel();

  return (
    <Surface as="main" level="canvas" className="models-page" aria-labelledby="models-heading">
      <h1 id="models-heading" className="models-page__title">
        Models
      </h1>

      {vm.state === 'loading' && <ModelsSkeleton />}

      {vm.state === 'error' && (
        <div className="models-page__body app-shell__sparse-state">
          <ModelsError message={vm.message} onRetry={vm.retry} />
        </div>
      )}

      {vm.state === 'empty' && (
        <div className="models-page__body app-shell__sparse-state">
          <Surface level="subtle" bordered rounded className="model-card models-page__empty">
            <p>No models are configured on this provider yet.</p>
          </Surface>
        </div>
      )}

      {vm.state === 'populated' && (
        <div className="models-page__body">
          <p className="models-page__action-note">
            Every action on this page asks the console host to confirm in a
            native dialog before anything happens. No button here performs the
            operation by itself.
          </p>
          {vm.data.gateBusy && (
            <Surface level="subtle" bordered rounded className="models-page__gate-banner" role="status">
              <IconAlertCircle />
              <span>{vm.data.gateBusyReason}</span>
            </Surface>
          )}
          <div className="models-page__list">
            {vm.data.models.map((model) => (
              <ModelCard key={model.id} model={model} />
            ))}
          </div>
        </div>
      )}
    </Surface>
  );
}
