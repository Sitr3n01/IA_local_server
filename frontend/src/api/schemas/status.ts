import { z } from 'zod';

/**
 * Zod schemas for `GET /api/v1/status` on cia-edge (127.0.0.1:18091 in dev;
 * reached through the native bridge in production). Derived directly from a
 * captured real response - see src/api/__fixtures__/status.sample.json - not
 * invented from the endpoint's name or from guesswork.
 *
 * Every object schema below ends in `.passthrough()`. The backend is an
 * actively developed admission-control plane and is expected to add fields
 * (new capacity signals, new maintenance flags, new model capabilities)
 * without a corresponding console release. `.passthrough()` means an
 * unrecognized key is carried through untouched instead of stripping it or
 * throwing - the console should degrade by ignoring fields it doesn't know
 * about yet, not by refusing to render the page.
 */

// ---------------------------------------------------------------------------
// Capacity: one admission-control snapshot for one model. Reused for the
// top-level `capacity` (the model that would be admitted next) and for each
// entry's `capacity` inside `model_statuses`.
//
// Every headroom/requirement/reserve figure is a hardware measurement, and
// `null` means "not measured" - a distinct, stricter condition than zero. A
// measured headroom of 0 GiB is a real and alarming number; a null headroom
// means no reading was taken and the UI must not silently treat it as 0.
// ---------------------------------------------------------------------------
const CapacitySchema = z
  .object({
    admission: z.string(),
    model: z.string(),
    model_running: z.boolean(),
    commit_headroom_gib: z.number().nullable(),
    required_commit_gib: z.number().nullable(),
    reserve_commit_gib: z.number().nullable(),
    physical_headroom_gib: z.number().nullable(),
    required_physical_gib: z.number().nullable(),
    reserve_physical_gib: z.number().nullable(),
    required_vram_gib: z.number().nullable(),
    device_vram_gib: z.number().nullable(),
    reserve_vram_gib: z.number().nullable(),
    measured: z.boolean(),
    available: z.boolean(),
    // Absent/omitted when `available` is true in principle; nullable to be
    // safe since only the "unavailable" case has been observed so far.
    reason: z.string().nullable().optional(),
    // Sprint 4 (Models) addition: memory a *loaded* model is holding that
    // could be reclaimed by unloading it - explains why headroom can look
    // wrong while a model is running. Not in any fixture captured so far
    // (every captured `model_running` is `false`), hence `.optional()` on
    // top of `.nullable()` - "optional" means the key itself may not exist
    // yet on this backend version; "nullable" means the key exists but no
    // measurement was taken. The two states render differently (see
    // src/features/models/format.ts's `formatReclaimableGib`): absent means
    // "don't show this row"; `null` still shows as "not measured".
    reclaimable_commit_gib: z.number().nullable().optional(),
    reclaimable_physical_gib: z.number().nullable().optional(),
  })
  .passthrough();

const DeploymentSchema = z
  .object({
    commit: z.string(),
    created_utc: z.string(),
    environment: z.string(),
    healthy: z.boolean(),
    previous_release: z.string().nullable().optional(),
    release: z.string(),
    source_dirty: z.boolean(),
    status: z.string(),
    version: z.string(),
  })
  .passthrough();

const GateSchema = z
  .object({
    active: z.number(),
    queued: z.number(),
    max_active: z.number(),
    max_queue: z.number(),
    wait_timeout_seconds: z.number(),
    rejected_total: z.number(),
    timed_out_total: z.number(),
  })
  .passthrough();

const GpuMemorySchema = z
  .object({
    state: z.string(),
    dedicated_mib: z.number().nullable(),
    shared_mib: z.number().nullable(),
    adapter: z.string().nullable().optional(),
    message: z.string().nullable().optional(),
  })
  .passthrough();

const MaintenanceSchema = z
  .object({
    state: z.string(),
    draining: z.boolean(),
    drained: z.boolean(),
    active: z.number(),
    queued: z.number(),
    rejected_total: z.number(),
  })
  .passthrough();

// Only ever observed as null in every captured payload - no runtime on this
// machine has checkpointing configured yet. `ctx_checkpoints` is typed from
// its name (a list of context-checkpoint step numbers), not from an observed
// non-null example. Revisit once a checkpoint-capable runtime is live and the
// real populated shape can be confirmed against a fixture.
const CheckpointsSchema = z
  .object({
    checkpoint_min_step: z.number().nullable(),
    configured: z.boolean(),
    ctx_checkpoints: z.array(z.number()).nullable(),
    runtime_capable: z.boolean(),
  })
  .passthrough();

// Per-model runtime profile. Every model in the captured fixture ships a
// different subset of tuning keys (n_predict, reasoning_budget,
// compact_threshold_tokens, moe_offload) - only the four fields present on
// every model are required; the rest are optional, and unknown future keys
// pass through untouched via the object's own passthrough.
const ProfileSchema = z
  .object({
    weights: z.string(),
    cache_type_k: z.string(),
    cache_type_v: z.string(),
    max_output_tokens: z.number(),
    n_predict: z.number().optional(),
    reasoning_budget: z.number().optional(),
    compact_threshold_tokens: z.number().optional(),
    moe_offload: z
      .object({
        cpu_layers: z.number(),
      })
      .passthrough()
      .optional(),
  })
  .passthrough();

const RuntimeInfoSchema = z
  .object({
    id: z.string(),
    state: z.string(),
    engine: z.string(),
    variant: z.string(),
    backend: z.string(),
    artifact_sha256_prefix: z.string(),
    checkpoint_capable: z.boolean(),
  })
  .passthrough();

const ModelStatusSchema = z
  .object({
    active: z.boolean(),
    available: z.boolean(),
    capacity: CapacitySchema,
    checkpoints: CheckpointsSchema,
    context_tokens: z.number(),
    id: z.string(),
    profile: ProfileSchema,
    reason: z.string().nullable().optional(),
    runtime: RuntimeInfoSchema,
  })
  .passthrough();

// Capability flags are the ONLY sanctioned way to feature-detect a model in
// this console. Never branch on `id` substrings anywhere in the app - if a
// component needs to know whether a model supports something, add the flag
// here (as optional: models that don't support a capability omit the key
// entirely rather than sending `false`) and read it from `capabilities`.
//
// `responses`, `structured_output`, and `reasoning` are Sprint 4 (Models)
// additions to the sanctioned vocabulary - named by the Models brief, but
// not yet exercised by any captured fixture (every model in
// src/api/__fixtures__/status.sample.json only ever sends a subset of
// `chat_completions`/`streaming`/`function_calling`). They're declared here
// anyway, optional like the rest, so the console can render them the
// instant the backend starts sending them, rather than only after the next
// fixture recapture - `.passthrough()` on this object means an entirely
// unlisted future flag still passes through untouched either way.
const ModelCapabilitiesSchema = z
  .object({
    responses: z.boolean().optional(),
    chat_completions: z.boolean().optional(),
    streaming: z.boolean().optional(),
    function_calling: z.boolean().optional(),
    structured_output: z.boolean().optional(),
    reasoning: z.boolean().optional(),
  })
  .passthrough();

const ModelSchema = z
  .object({
    id: z.string(),
    object: z.string(),
    owned_by: z.string(),
    display_name: z.string(),
    capabilities: ModelCapabilitiesSchema,
  })
  .passthrough();

const RecentEventSchema = z
  .object({
    time: z.string(),
    request_id: z.string(),
    method: z.string(),
    path: z.string(),
    status: z.number(),
    duration_ms: z.number(),
  })
  .passthrough();

const UpstreamSchema = z
  .object({
    reachable: z.boolean(),
    url: z.string(),
  })
  .passthrough();

export const StatusSchema = z
  .object({
    service: z.string(),
    version: z.string(),
    ready: z.boolean(),
    uptime_seconds: z.number(),
    upstream: UpstreamSchema,
    models: z.array(ModelSchema),
    runtimes: z.array(RuntimeInfoSchema),
    active_model: z.string(),
    gate: GateSchema,
    capacity: CapacitySchema,
    gpu_memory: GpuMemorySchema,
    maintenance: MaintenanceSchema,
    model_statuses: z.array(ModelStatusSchema),
    recent_events: z.array(RecentEventSchema),
    // Present only once a release manifest is installed on this machine -
    // absent entirely (not null) on a fresh checkout with no deployment yet.
    deployment: DeploymentSchema.optional(),
  })
  .passthrough();

export type Status = z.infer<typeof StatusSchema>;
export type Capacity = z.infer<typeof CapacitySchema>;
export type Deployment = z.infer<typeof DeploymentSchema>;
export type ModelInfo = z.infer<typeof ModelSchema>;
export type ModelCapabilities = z.infer<typeof ModelCapabilitiesSchema>;
export type ModelStatus = z.infer<typeof ModelStatusSchema>;
export type RuntimeInfo = z.infer<typeof RuntimeInfoSchema>;
export type RecentEvent = z.infer<typeof RecentEventSchema>;
export type Gate = z.infer<typeof GateSchema>;
export type Maintenance = z.infer<typeof MaintenanceSchema>;
export type GpuMemory = z.infer<typeof GpuMemorySchema>;
