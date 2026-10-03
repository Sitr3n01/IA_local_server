import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { JSDOM } from 'jsdom';

const webRoot = new URL('../../internal/monitor/web/', import.meta.url);
const html = readFileSync(new URL('index.html', webRoot), 'utf8');
const script = readFileSync(new URL('assets/app.js', webRoot), 'utf8');
const startedAt = '2026-09-29T12:00:00.000Z';

function snapshot() {
  return {
    monitor: { started_at: startedAt, environment: 'canary', interval_ms: 1000 },
    activity: { phase: 'idle', queued: 0 },
    control: { available: true, stop_available: true },
    edge: {
      reachable: true, telemetry: 'available', status_age_ms: 0,
      status: {
        active_model: '',
        models: [{ id: 'test-model', display_name: 'Test model', capabilities: {} }],
        model_statuses: [{ id: 'test-model', available: true, active: false, context_tokens: 8192, profile: {}, capacity: {} }],
        capacity: { model: 'test-model', available: true },
        gate: { active: 0, queued: 0 }, maintenance: { draining: false },
        upstream: { reachable: true }, runtimes: [], gpu_memory: {},
      },
      inference: { active: [], recent: [] },
    },
    hardware: {}, machine: {}, energy: {}, history: {},
    sources: [], requests: [], external_activity: [],
  };
}

function operation(id, state = 'awaiting_confirmation') {
  return { id, state, action: 'switch', model: 'test-model', requested_at: startedAt };
}

function response(body) {
  return { ok: true, status: 200, json: () => Promise.resolve(body) };
}

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

// Run the unchanged page script through its real DOM events and polling.
// Fetch and time are local fakes: no request or action reaches a real service.
async function page(t, snap = snapshot(), options = {}) {
  const dom = new JSDOM(html, {
    url: 'http://127.0.0.1:18095/', runScripts: 'outside-only', pretendToBeVisual: true,
  });
  t.after(() => dom.window.close());
  const { window } = dom;
  let now = Date.parse(startedAt);
  let nextTimer = 0;
  const timers = new Map();
  const posts = [];
  const state = { snapshot: snap, sampleTime: null, action: () => new Promise(() => {}) };
  window.Date.now = () => now;
  window.setTimeout = (callback, delay) => {
    const id = ++nextTimer;
    timers.set(id, { callback, at: now + delay });
    return id;
  };
  window.clearTimeout = (id) => timers.delete(id);
  if (options.withoutAbortController) window.AbortController = undefined;
  window.fetch = (url, init) => {
    if (url === '/api/actions') {
      posts.push(init);
      return state.action(init);
    }
    assert.equal(url, '/api/snapshot');
    return Promise.resolve(response({
      ...structuredClone(state.snapshot),
      generated_at: new Date(state.sampleTime ?? now).toISOString(),
    }));
  };
  const settle = () => new Promise((resolve) => setImmediate(resolve));
  async function advance(ms) {
    const target = now + ms;
    while (true) {
      const due = [...timers].filter(([, timer]) => timer.at <= target)
        .sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) break;
      now = due[1].at;
      timers.delete(due[0]);
      due[1].callback();
      await settle();
    }
    now = target;
    await settle();
  }
  window.eval(script);
  await settle();
  assert.equal(window.document.body.getAttribute('data-monitor'), 'up');
  return {
    state, posts, advance, settle,
    get: (id) => window.document.getElementById(id),
    now: () => now,
  };
}

for (const feed of ['requests', 'external_activity']) {
  test(`${feed}: historical context survives a model change and unload`, async (t) => {
    const snap = snapshot();
    const record = {
      source_id: 'other-tool', source_label: 'Other tool', label: 'Other tool',
      via: 'server-log', measured: 'server-log', model: 'old-model', started_at: startedAt,
      prompt_tokens: 4000, output_tokens: 96, cached_tokens: 0, context_tokens: 8192,
    };
    if (feed === 'external_activity') delete snap.requests;
    snap[feed] = [record];
    snap.sources = [{ id: 'other-tool', models: [{ id: 'new-model', context_loaded: 32768 }] }];
    const p = await page(t, snap);
    assert.equal(p.get('gauge-number').textContent, '50');
    snap.sources = [];
    await p.advance(1000);
    assert.equal(p.get('gauge-number').textContent, '50');
  });
}

test('unknown historical context is not invented from the current model', async (t) => {
  const snap = snapshot();
  snap.requests = [{
    source_id: 'other-tool', via: 'server-log', started_at: startedAt,
    prompt_tokens: 4000, output_tokens: 96, context_tokens: null,
  }];
  snap.sources = [{ id: 'other-tool', models: [{ id: 'new-model', context_loaded: 8192 }] }];
  const p = await page(t, snap);
  assert.equal(p.get('gauge-number').textContent, '–');
});

test('a restarted monitor clears an old local confirmation', async (t) => {
  const snap = snapshot();
  const p = await page(t, snap);
  p.state.action = () => Promise.resolve(response({ operation: operation('op-10') }));
  p.get('load-button').click();
  await p.settle();
  assert.equal(p.get('load-button').disabled, true);
  // The old operation completes; its POST reply remains local in the page.
  snap.control.operation = operation('op-10', 'declined');
  await p.advance(1000);
  assert.equal(p.get('load-button').disabled, false);
  snap.monitor.started_at = '2026-09-29T12:00:01.000Z';
  snap.control.operation = operation('op-1', 'declined');
  await p.advance(1000);
  assert.equal(p.get('load-button').disabled, false);
  snap.control.operation = operation('op-2', 'running');
  await p.advance(1000);
  assert.match(p.get('control-status').textContent, /Carregando Test model/);
});

test('a late POST reply from the old monitor cannot restore its operation', async (t) => {
  const snap = snapshot();
  const p = await page(t, snap);
  const post = deferred();
  p.state.action = () => post.promise;
  p.get('load-button').click();
  snap.monitor.started_at = '2026-09-29T12:00:01.000Z';
  await p.advance(1000);
  post.resolve(response({ operation: operation('op-10') }));
  await p.settle();
  assert.equal(p.get('load-button').disabled, false);
  assert.doesNotMatch(p.get('control-status').textContent, /Confirme/);
});

test('native confirmation can remain pending beyond the POST deadline', async (t) => {
  const snap = snapshot();
  const p = await page(t, snap);
  snap.control.operation = operation('op-1');
  p.state.action = () => Promise.resolve(response({ operation: snap.control.operation }));
  p.get('load-button').click();
  await p.settle();
  await p.advance(5000);
  assert.equal(p.get('load-button').disabled, true);
  assert.match(p.get('control-status').textContent, /Confirme/);
  assert.equal(p.posts[0].signal.aborted, false);
  assert.equal(p.posts.length, 1);
});

for (const withoutAbortController of [false, true]) {
  test(`an unanswered action times out and never retries (AbortController: ${!withoutAbortController})`, async (t) => {
    const p = await page(t, snapshot(), { withoutAbortController });
    p.get('load-button').click();
    assert.equal(p.get('load-button').disabled, true);
    await p.advance(5000);
    assert.equal(p.posts.length, 1);
    if (!withoutAbortController) assert.equal(p.posts[0].signal.aborted, true);
    assert.equal(p.get('load-button').disabled, false);
    assert.match(p.get('control-status').textContent, /a tempo/);
  });
}

test('the timeout also bounds a response body that never finishes', async (t) => {
  const p = await page(t);
  p.state.action = () => Promise.resolve({ ok: true, status: 200, json: () => new Promise(() => {}) });
  p.get('load-button').click();
  await p.advance(5000);
  assert.equal(p.get('load-button').disabled, false);
  assert.equal(p.posts.length, 1);
});

test('an uncertain action waits for a fresh snapshot and respects a pending operation', async (t) => {
  const snap = snapshot();
  const p = await page(t, snap);
  p.state.sampleTime = p.now();
  p.get('load-button').click();
  await p.advance(5000);
  assert.equal(p.get('load-button').disabled, true);
  p.state.sampleTime = null;
  snap.control.operation = operation('op-1', 'running');
  await p.advance(1000);
  assert.equal(p.get('load-button').disabled, true);
  assert.match(p.get('control-status').textContent, /Carregando Test model/);
  assert.equal(p.posts.length, 1);
  snap.control.operation = operation('op-1', 'declined');
  await p.advance(1000);
  assert.equal(p.get('load-button').disabled, false);
});

test('a late reply after timeout does not restore a stale confirmation', async (t) => {
  const p = await page(t);
  const post = deferred();
  p.state.action = () => post.promise;
  p.get('load-button').click();
  await p.advance(5000);
  post.resolve(response({ operation: operation('op-1') }));
  await p.settle();
  assert.equal(p.get('load-button').disabled, false);
  assert.equal(p.posts.length, 1);
});
