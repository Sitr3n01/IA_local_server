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
import { FieldList, Notice, ResourceMeter } from '../design-system/components';
import { loadModel, switchModel, unloadModel } from '../api/commands/models';
import { useModelsViewModel } from '../features/models/useModelsViewModel';
import { useModelAction } from '../features/models/useModelAction';
import type { ModelActionKind, ModelCardData } from '../features/models/deriveModels';
import { formatContextTokens, formatReclaimableGib } from '../features/models/format';
import { assertNever } from '../lib/assertNever';
import './page.css';
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
 * every enabled entry, so rendering it visibly under each button repeated
 * one paragraph six times on a six-model provider - twelve lines of the
 * same text on one screen, which reads as noise and trains the operator to
 * skip exactly the sentence that matters. It is stated once above the list
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
 * page-level notice above the list, the capacity sentence in
 * `model.reason.sentence` once expanded - the same value
 * `model.action.disabledReason` holds for the `'capacity'` cause, so the two
 * can never drift apart.
 */
function actionCaption(model: ModelCardData): { text: string; specific: boolean } {
  if (model.action.enabled) {
    return { text: ACTION_CAPTION[model.action.kind], specific: false };
  }
  if (model.action.disabledCause === 'gate-busy') {
    return { text: 'Refused while the admission gate is busy right now - see the notice above.', specific: true };
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
    <div className="model-entry__action">
      {/* Stays `primary`. Ghost exists now and this is exactly what it is
          not for: every one of these three verbs changes what the provider
          is running. */}
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
      <p id={captionId} className={caption.specific ? 'model-entry__action-caption' : 'ds-visually-hidden'}>
        {caption.text}
      </p>
      {outcome.status === 'no-native-host' && (
        <Notice tone="warning" role="alert" className="model-entry__outcome model-entry__outcome--no-native-host">
          {outcome.message}
        </Notice>
      )}
      {outcome.status === 'error' && (
        <Notice tone="danger" role="alert" className="model-entry__outcome model-entry__outcome--error">
          The request could not be completed: {outcome.message}
        </Notice>
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

/**
 * `ghost`, as of Sprint 10. A disclosure is the least consequential control
 * on an entry, and it used to be the only outlined box on one - the widest,
 * heaviest element on the screen advertising the least important thing an
 * operator could do.
 */
function DisclosureToggle({ open, onToggle, labelWhenClosed, labelWhenOpen, controlsId }: DisclosureToggleProps) {
  return (
    <Button
      variant="ghost"
      size="sm"
      className="model-entry__disclosure"
      aria-expanded={open}
      aria-controls={controlsId}
      onClick={onToggle}
      trailingIcon={
        <IconChevronDown className={`page-disclosure-icon${open ? ' page-disclosure-icon--open' : ''}`} />
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

/**
 * Capabilities as a list of words, not as a row of pills.
 *
 * Each supported capability keeps its own element - that is load-bearing,
 * not incidental: they are separate facts, and a single joined string would
 * make "Function calling" unfindable as a thing the payload reported. What
 * went away is the border, the fill and the `--radius-full` capsule around
 * each one. Six pills in a row is six boxes claiming the visual weight of
 * six controls, for six words that are not controls at all.
 */
function CapabilitiesRow({ capabilities }: { capabilities: ModelCardData['capabilities'] }) {
  // Only a flag that is *exactly* `true` in the payload counts as
  // supported - `undefined` (never sent) and any other value never render
  // as if the capability were present. Never derived from the model id.
  const supported = CAPABILITY_KEYS.filter((key) => capabilities?.[key] === true);

  return (
    <div className="model-entry__capabilities">
      <h3 className="page-subsection__title">Capabilities</h3>
      <p className="model-entry__capability-list">
        {supported.length === 0 ? (
          <span className="model-entry__capability model-entry__capability--none">None reported</span>
        ) : (
          supported.map((key, index) => (
            <span key={key} className="model-entry__capability">
              {index > 0 && <span aria-hidden="true">·</span>}
              {CAPABILITY_LABELS[key]}
            </span>
          ))
        )}
      </p>
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

/**
 * The five facts an operator needs before deciding anything, on one line:
 * whether it is loaded, whether it can be admitted, and how much context it
 * has. Every state is a glyph plus a word, so none of the three is carried
 * by colour.
 *
 * This replaced three pill badges. The words are identical; what is gone is
 * three tinted, rounded, filled boxes per entry - eighteen of them on the
 * fixture's six-model provider, on a screen whose actual controls number
 * six.
 */
function ModelFacts({ model }: { model: ModelCardData }) {
  return (
    <p className="model-entry__facts">
      <span className={`page-status${model.isActive ? ' page-status--positive' : ' page-status--neutral'}`}>
        {model.isActive ? <IconCheck /> : <IconInfo />}
        {model.isActive ? 'Loaded' : 'Not loaded'}
      </span>
      <span className="model-entry__facts-separator" aria-hidden="true">
        ·
      </span>
      <span className={`page-status${model.isAvailableNow ? ' page-status--positive' : ' page-status--negative'}`}>
        {model.isAvailableNow ? <IconCheck /> : <IconAlertCircle />}
        {model.isAvailableNow ? 'Available now' : 'Not available now'}
      </span>
      <span className="model-entry__facts-separator" aria-hidden="true">
        ·
      </span>
      <span className="model-entry__context">{formatContextTokens(model.contextTokens)}</span>
    </p>
  );
}

/**
 * One model.
 *
 * Not a card any more - an entry in a list, separated from its neighbours
 * by a hairline and by space. The exception is the loaded model, which is
 * the one thing on this screen an operator has to be able to find without
 * reading: it is hoisted to the top of the list, labelled "Active model",
 * set on a tonal ground with room around it, and given the largest name on
 * the page. Five channels - position, label, ground, spacing, type size -
 * none of which is a colour, so it survives a greyscale print and a
 * colour-blind reader intact.
 */
function ModelEntry({ model }: { model: ModelCardData }) {
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const detailsId = useId();
  const advancedId = useId();
  const nameId = useId();

  const reclaimableCommit = formatReclaimableGib(model.reclaimableCommitGib);
  const reclaimablePhysical = formatReclaimableGib(model.reclaimablePhysicalGib);
  const hasReclaimable = reclaimableCommit !== null || reclaimablePhysical !== null;

  return (
    <article
      className={`model-entry${model.isActive ? ' model-entry--active' : ''}`}
      aria-labelledby={nameId}
    >
      <div className="model-entry__header">
        <div className="model-entry__identity">
          {model.isActive && <p className="page-caption model-entry__eyebrow">Active model</p>}
          <h2 id={nameId} className="model-entry__name">
            {model.displayName}
          </h2>
          <ModelFacts model={model} />
        </div>
        <ModelActionButton model={model} />
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
        className="model-entry__details"
        role="region"
        aria-label={`${model.displayName} capacity details`}
        hidden={!detailsOpen}
      >
        <p className="model-entry__reason">
          {model.reason.recognized ? model.reason.sentence : `Reason reported by the provider: ${model.reason.code}`}
        </p>

        <div className="model-entry__meters">
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
          className="model-entry__advanced"
          role="region"
          aria-label={`${model.displayName} advanced detail`}
          hidden={!advancedOpen}
        >
          <h3 className="page-subsection__title">Profile</h3>
          <ProfileFields profile={model.profile} />
          <h3 className="page-subsection__title">Checkpoints</h3>
          <CheckpointsFields checkpoints={model.checkpoints} />
          <h3 className="page-subsection__title">Runtime</h3>
          <RuntimeFields runtime={model.runtime} />
        </div>
      </div>
    </article>
  );
}

function ModelsSkeleton() {
  return (
    <div className="models-page__body app-shell__sparse-state" aria-busy="true">
      <span className="ds-visually-hidden" role="status">
        Loading models…
      </span>
      <div className="models-skeleton">
        {[0, 1, 2].map((key) => (
          <div key={key} className="models-skeleton__entry">
            <Skeleton variant="text" className="models-skeleton__name" />
            <Skeleton variant="text" />
          </div>
        ))}
      </div>
    </div>
  );
}

/**
 * The loaded model first, then everything else in payload order.
 *
 * Position is one of the channels that makes the active model findable
 * without reading (see `ModelEntry`), and it is the only one that works
 * before the operator's eye has landed anywhere. The sort is stable, so the
 * order of the inactive models is exactly the order the provider listed
 * them in - this reorders one entry, it does not re-rank the list.
 */
function activeFirst(models: readonly ModelCardData[]): ModelCardData[] {
  return [...models.filter((model) => model.isActive), ...models.filter((model) => !model.isActive)];
}

/**
 * Screen 3: Models. Primary tier (always visible, per entry): display name,
 * whether it is the loaded model, whether it can be admitted right now,
 * and its context window. Secondary tier (behind "Show capacity details"):
 * the full capacity verdict (reused from `reasonExplanations.ts`), three
 * `ResourceMeter`s (reused from `shortfall.ts`), reclaimable-memory figures
 * when present, and reported capabilities. Advanced tier (behind a further
 * "Show advanced detail" nested inside that): profile, checkpoints, and
 * runtime identity - see `useModelsViewModel`/`deriveModels.ts` for the
 * derivation this renders.
 *
 * Sprint 10 changed the composition and nothing else: the six bordered
 * cards became a list of entries, the badges became a line of words, and
 * the loaded model became the protagonist of the screen instead of a card
 * with a "Loaded" pill on it. Every field, every gate, every disabled
 * reason and every disclosure is still here.
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
      <div className="models-page__header">
        <h1 id="models-heading" className="page-heading">
          Models
        </h1>
        <p className="page-lead">
          Every action on this page asks the console host to confirm in a native dialog before anything happens.
          No button here performs the operation by itself.
        </p>
      </div>

      {vm.state === 'loading' && <ModelsSkeleton />}

      {vm.state === 'error' && (
        <div className="models-page__body app-shell__sparse-state">
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

      {vm.state === 'empty' && (
        <div className="models-page__body app-shell__sparse-state">
          <p className="page-empty">No models are configured on this provider yet.</p>
        </div>
      )}

      {vm.state === 'populated' && (
        <div className="models-page__body">
          {vm.data.gateBusy && (
            <Notice tone="warning" role="status">
              {vm.data.gateBusyReason}
            </Notice>
          )}
          <div className="models-page__list">
            {activeFirst(vm.data.models).map((model) => (
              <ModelEntry key={model.id} model={model} />
            ))}
          </div>
        </div>
      )}
    </Surface>
  );
}
