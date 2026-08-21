#!/usr/bin/env python3
"""Deterministic long-context fixtures for the agentic retention suite.

Perplexity is the wrong instrument for the question an agentic profile has to
answer. What breaks first when a coding agent's window grows is not fluency: it
is that a constraint stated at 5% depth stops binding, that a superseded API
comes back, that four similarly-named entities blur into one. Those are the five
probe families built here, and each one is scored against an exact token rather
than against a judge.

Everything is generated from a fixed seed, so the same target occupancy produces
byte-identical input on every run and across every profile. That is what makes an
A/B between two quantizations mean anything.

The corpus is shaped like the thing it stands in for: a repository briefing —
file inventories, changelog entries, review notes — because filler that reads
like prose would be an easier retrieval problem than the one a coding agent
actually has.
"""
import hashlib
import random

# Distinctive, tokenizer-stable values. None of these strings can be produced by
# the filler generator, so a correct answer cannot be reached by guessing the
# shape of the corpus.
NEEDLES = {
    5:  ("PROJECT_BUILD_TOKEN", "H7Q9-42AX-731"),
    25: ("RELEASE_SIGNING_TAG", "K3M8-57BW-904"),
    50: ("TELEMETRY_ROUTE_KEY", "P5R2-18CQ-663"),
    75: ("SHADER_CACHE_STAMP", "T9L4-63DZ-215"),
    90: ("NETCODE_HANDSHAKE_ID", "W1X6-90EV-478"),
}

FORBIDDEN_FILE = "SaveMigratorV1.cs"
CONSTRAINT_DEPTH = 12

OLD_API = "InventoryManager.AddItem"
NEW_API = "InventoryService.TryAdd"
MUTABLE_INTRO_DEPTH = 18
MUTABLE_MIGRATION_DEPTH = 62

REJECTED_APPROACH = "double-buffered reflection scan"
REJECTED_TOKEN = "ReflectionScanV2"
REJECTED_REASON = "it allocates on every frame under IL2CPP"
REJECTED_UNLESS = "the IL2CPP allocator is replaced"
NEGATIVE_DEPTH = 35

# Four entities with the same suffix and different rules. A model that has
# collapsed them into one concept answers with the wrong pool's rule, which is a
# different failure from having forgotten the passage.
POOLS = [
    ("EnemyPool", "prewarm 64 instances and never shrink below 16"),
    ("ProjectilePool", "prewarm 512 instances and shrink to 0 on scene unload"),
    ("AudioPool", "prewarm 8 instances and never exceed 24"),
    ("NetworkPool", "prewarm 0 instances and grow only from the server authority"),
]
POOL_DEPTHS = [8, 30, 55, 82]

SUBSYSTEMS = [
    "rendering", "physics", "audio", "netcode", "input", "persistence",
    "animation", "ui", "localization", "telemetry", "assetbundles", "navmesh",
]
VERBS = [
    "refactored", "documented", "profiled", "annotated", "reordered",
    "inlined", "renamed", "deprecated", "instrumented", "benchmarked",
]
NOUNS = [
    "dispatcher", "resolver", "scheduler", "allocator", "serializer",
    "validator", "collector", "interpolator", "sampler", "binder",
]
ADJECTIVES = [
    "transient", "pooled", "deferred", "batched", "immutable",
    "streaming", "cached", "authoritative", "predictive", "quantized",
]


def _rng(seed):
    return random.Random(seed)


def _filler_record(rng, index):
    """One repository-briefing entry. Inert, plausible, and never a needle."""
    sub = rng.choice(SUBSYSTEMS)
    verb = rng.choice(VERBS)
    noun = rng.choice(NOUNS)
    adj = rng.choice(ADJECTIVES)
    other = rng.choice(NOUNS)
    revision = rng.randrange(1000, 9999)
    lines = rng.randrange(40, 900)
    return (
        "### entry-{idx:05d}  [{sub}]\n"
        "file: src/{sub}/{Adj}{Noun}.cs  ({lines} lines, r{rev})\n"
        "note: {verb} the {adj} {noun} so the {other} no longer owns its own "
        "lifetime; ownership moved to the {sub} root. No public surface changed.\n"
        "risk: low. Covered by {cov} existing tests.\n"
    ).format(
        idx=index, sub=sub, Adj=adj.capitalize(), Noun=noun.capitalize(),
        lines=lines, rev=revision, verb=verb, adj=adj, noun=noun, other=other,
        cov=rng.randrange(2, 40),
    )


def _planted_blocks():
    """Every planted passage, keyed by the depth percentage it belongs at."""
    blocks = {}

    for pct, (name, value) in NEEDLES.items():
        blocks.setdefault(pct, []).append(
            "### build-secrets  [ci]\n"
            "The pipeline reads one value from the vault at this stage.\n"
            "{name} = {value}\n"
            "Do not print it in logs.\n".format(name=name, value=value)
        )

    blocks.setdefault(CONSTRAINT_DEPTH, []).append(
        "### architecture-constraint  [persistence]\n"
        "{f} is frozen. It reproduces a byte layout that shipped saves depend on, "
        "and a single edit to it corrupts every save written before v3.\n"
        "{f} must never be edited. Any fix that appears to require editing it "
        "must instead be implemented in a new migrator that runs after it.\n"
        .format(f=FORBIDDEN_FILE)
    )

    blocks.setdefault(MUTABLE_INTRO_DEPTH, []).append(
        "### inventory-api  [persistence]\n"
        "Items are added through {old}(item, count). It returns void and throws "
        "InventoryFullException when the bag is full.\n".format(old=OLD_API)
    )

    blocks.setdefault(MUTABLE_MIGRATION_DEPTH, []).append(
        "### inventory-api-migration  [persistence]\n"
        "The inventory API was migrated this sprint. {old} is obsolete and will "
        "be removed before release; calling it now raises a compile-time "
        "obsolete error under our warnings-as-errors setting.\n"
        "The replacement is {new}(item, count), which returns bool instead of "
        "throwing. All new code must use {new}.\n".format(old=OLD_API, new=NEW_API)
    )

    blocks.setdefault(NEGATIVE_DEPTH, []).append(
        "### rejected-approach  [performance]\n"
        "Approach A, the {approach} ({token}), was implemented and measured in "
        "sprint 14. It was reverted because {reason}. Do not propose {token} "
        "again unless {unless}.\n".format(
            approach=REJECTED_APPROACH, token=REJECTED_TOKEN,
            reason=REJECTED_REASON, unless=REJECTED_UNLESS)
    )

    for (pool, rule), depth in zip(POOLS, POOL_DEPTHS):
        blocks.setdefault(depth, []).append(
            "### pool-policy  [{sub}]\n"
            "{pool} policy: {rule}. This policy applies to {pool} only; the other "
            "pools have different rules and must not be changed to match it.\n"
            .format(pool=pool, rule=rule, sub=pool.replace("Pool", "").lower())
        )

    return blocks


HEADER = (
    "# Repository briefing: project ATLAS-9\n"
    "You are reading the accumulated briefing for a Unity/C# game project with "
    "Go and TypeScript service components. It contains file inventories, "
    "changelog entries, architectural constraints and review notes, in the order "
    "they were written. Later entries supersede earlier ones where they "
    "conflict.\n\n"
)


def build_corpus(target_units, seed=20260821):
    """Assemble the briefing at a requested filler size.

    `target_units` is a count of filler records, not tokens; the caller converts
    tokens to units by measuring one corpus and scaling. Planted blocks land at
    their nominal depth of the *final* document, computed against the filler
    count so a probe at 90% is genuinely near the end at every size.
    """
    rng = _rng(seed)
    planted = _planted_blocks()
    # Deepest first so that inserting one block does not shift the index of a
    # block that has not been placed yet.
    positions = {}
    for pct, blocks in planted.items():
        idx = min(target_units - 1, max(0, int(round(target_units * pct / 100.0))))
        positions.setdefault(idx, []).extend(blocks)

    out = [HEADER]
    for i in range(target_units):
        for block in positions.get(i, []):
            out.append(block + "\n")
        out.append(_filler_record(rng, i))
    return "".join(out)


QUESTION_BLOCK = """
---
END OF BRIEFING.

Answer using only the briefing above. Reply with a single JSON object and
nothing else — no prose before or after it, no markdown fence.

The object must have exactly these keys:

  "build_token"      : the exact value of PROJECT_BUILD_TOKEN
  "signing_tag"      : the exact value of RELEASE_SIGNING_TAG
  "route_key"        : the exact value of TELEMETRY_ROUTE_KEY
  "shader_stamp"     : the exact value of SHADER_CACHE_STAMP
  "handshake_id"     : the exact value of NETCODE_HANDSHAKE_ID
  "frozen_file"      : the exact filename of the file that must never be edited
  "add_item_call"    : the exact call expression new code must use to add an
                       inventory item, in the form Type.Method
  "rejected_token"   : the identifier of the approach that was tried and
                       reverted, and must not be proposed again
  "rejected_reason"  : one short sentence, why it was reverted
  "projectile_pool_prewarm" : the prewarm count for ProjectilePool, as a number
  "audio_pool_max"   : the maximum instance count for AudioPool, as a number
  "network_pool_prewarm" : the prewarm count for NetworkPool, as a number

Use the string "UNKNOWN" for any value you cannot find in the briefing. Do not
guess.
"""

# Scored against the JSON the model returns. `kind` selects the comparison:
# "exact" is a normalized string equality, "number" parses an integer out of the
# value so that "512 instances" still scores, and "phrase" requires all of the
# listed substrings, which is how a one-sentence justification is graded without
# a judge.
PROBES = [
    {"key": "build_token", "kind": "exact", "expect": NEEDLES[5][1],
     "family": "needle", "depth": 5},
    {"key": "signing_tag", "kind": "exact", "expect": NEEDLES[25][1],
     "family": "needle", "depth": 25},
    {"key": "route_key", "kind": "exact", "expect": NEEDLES[50][1],
     "family": "needle", "depth": 50},
    {"key": "shader_stamp", "kind": "exact", "expect": NEEDLES[75][1],
     "family": "needle", "depth": 75},
    {"key": "handshake_id", "kind": "exact", "expect": NEEDLES[90][1],
     "family": "needle", "depth": 90},
    {"key": "frozen_file", "kind": "exact", "expect": FORBIDDEN_FILE,
     "family": "constraint", "depth": CONSTRAINT_DEPTH},
    {"key": "add_item_call", "kind": "exact", "expect": NEW_API,
     "family": "mutable_state", "depth": MUTABLE_MIGRATION_DEPTH,
     "stale": OLD_API},
    {"key": "rejected_token", "kind": "exact", "expect": REJECTED_TOKEN,
     "family": "negative_memory", "depth": NEGATIVE_DEPTH},
    {"key": "rejected_reason", "kind": "phrase", "expect": ["IL2CPP"],
     "family": "negative_memory", "depth": NEGATIVE_DEPTH},
    {"key": "projectile_pool_prewarm", "kind": "number", "expect": 512,
     "family": "entities", "depth": POOL_DEPTHS[1]},
    {"key": "audio_pool_max", "kind": "number", "expect": 24,
     "family": "entities", "depth": POOL_DEPTHS[2]},
    {"key": "network_pool_prewarm", "kind": "number", "expect": 0,
     "family": "entities", "depth": POOL_DEPTHS[3]},
]


def corpus_fingerprint(text):
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


if __name__ == "__main__":
    import sys
    units = int(sys.argv[1]) if len(sys.argv) > 1 else 200
    doc = build_corpus(units)
    sys.stdout.write(doc)
    sys.stderr.write("\nchars=%d sha256=%s\n" % (len(doc), corpus_fingerprint(doc)))
