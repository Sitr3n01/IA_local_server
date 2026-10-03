// IA Local monitor. One snapshot a second from /api/snapshot, rendered with
// textContent and DOM nodes only: nothing the edge or the machine reports is
// ever parsed as HTML, so a model name cannot become markup.
(function () {
  'use strict';

  var POLL_MS = 1000;
  var MAX_BACKOFF_MS = 5000;
  var FETCH_TIMEOUT_MS = 3000;
  var HISTORY = 120;
  var STALE_STATUS_MS = 10000;
  var THEME_KEY = 'cia-monitor-theme';
  var TAB_KEY = 'cia-monitor-tab';
  var DASH = '–';
  var GIB = 1073741824;

  var TABS = ['monitor', 'modelos', 'conexao'];
  var currentTab = 'monitor';

  // ------------------------------------------------------------------ helpers

  function $(id) {
    return document.getElementById(id);
  }

  function isNum(value) {
    return typeof value === 'number' && isFinite(value);
  }

  function setText(id, text) {
    var element = typeof id === 'string' ? $(id) : id;
    if (!element) {
      return;
    }
    if (element.textContent !== text) {
      element.textContent = text;
    }
    // A missing figure is drawn quieter than a measured one, so a dash never
    // reads as a heavy number.
    if (element.classList.contains('num')) {
      var empty = text === DASH ? 'true' : 'false';
      if (element.getAttribute('data-empty') !== empty) {
        element.setAttribute('data-empty', empty);
      }
    }
  }

  function setTone(element, tone) {
    if (!element) {
      return;
    }
    if (tone) {
      if (element.getAttribute('data-tone') !== tone) {
        element.setAttribute('data-tone', tone);
      }
    } else if (element.hasAttribute('data-tone')) {
      element.removeAttribute('data-tone');
    }
  }

  function show(id, visible) {
    var element = typeof id === 'string' ? $(id) : id;
    if (element && element.hidden === visible) {
      element.hidden = !visible;
    }
  }

  // node builds an element from plain values. Text always goes through
  // textContent; there is deliberately no way to pass HTML.
  function node(tag, options, children) {
    var element = document.createElement(tag);
    options = options || {};
    if (options.className) {
      element.className = options.className;
    }
    if (options.text !== undefined) {
      element.textContent = options.text;
    }
    if (options.title) {
      element.title = options.title;
    }
    if (options.tone) {
      element.setAttribute('data-tone', options.tone);
    }
    if (options.data) {
      Object.keys(options.data).forEach(function (key) {
        element.setAttribute('data-' + key, String(options.data[key]));
      });
    }
    (children || []).forEach(function (child) {
      if (child === null || child === undefined) {
        return;
      }
      element.appendChild(typeof child === 'string' ? document.createTextNode(child) : child);
    });
    return element;
  }

  function storageGet(key) {
    try {
      return window.localStorage.getItem(key);
    } catch (error) {
      return null;
    }
  }

  function storageSet(key, value) {
    try {
      window.localStorage.setItem(key, value);
    } catch (error) {
      // Storage is a convenience; the page works without it.
    }
  }

  // ------------------------------------------------------------------ format

  var nf0 = new Intl.NumberFormat('pt-BR', { maximumFractionDigits: 0 });
  var nf1 = new Intl.NumberFormat('pt-BR', { minimumFractionDigits: 1, maximumFractionDigits: 1 });
  var nfCompact = new Intl.NumberFormat('pt-BR', { maximumFractionDigits: 1 });

  function int(value) {
    return isNum(value) ? nf0.format(Math.round(value)) : DASH;
  }

  function one(value) {
    return isNum(value) ? nf1.format(value) : DASH;
  }

  function gib(bytes) {
    return isNum(bytes) ? nf1.format(bytes / GIB) : DASH;
  }

  function gibFromMiB(mib) {
    return isNum(mib) ? nf1.format(mib / 1024) : DASH;
  }

  function percent(ratio) {
    return isNum(ratio) ? nf0.format(Math.round(ratio * 100)) + '%' : DASH;
  }

  // tokens abbreviates the way the rest of the project writes context sizes:
  // 262144 is "262,1k", 950 stays "950".
  function tokens(value) {
    if (!isNum(value)) {
      return DASH;
    }
    var magnitude = Math.abs(value);
    if (magnitude >= 1e6) {
      return nfCompact.format(value / 1e6) + 'M';
    }
    if (magnitude >= 1e3) {
      return nfCompact.format(value / 1e3) + 'k';
    }
    return nf0.format(value);
  }

  function rate(value) {
    if (!isNum(value)) {
      return DASH;
    }
    return value >= 100 ? nf0.format(value) : nf1.format(value);
  }

  function pad2(value) {
    return value < 10 ? '0' + value : String(value);
  }

  function duration(ms) {
    if (!isNum(ms)) {
      return DASH;
    }
    if (ms < 1000) {
      return nf0.format(Math.round(ms)) + ' ms';
    }
    var seconds = ms / 1000;
    if (seconds < 60) {
      return nf1.format(seconds) + ' s';
    }
    var minutes = Math.floor(seconds / 60);
    if (minutes < 60) {
      return minutes + ' min ' + pad2(Math.floor(seconds % 60)) + ' s';
    }
    return Math.floor(minutes / 60) + ' h ' + pad2(minutes % 60) + ' min';
  }

  function uptime(seconds) {
    if (!isNum(seconds)) {
      return DASH;
    }
    var days = Math.floor(seconds / 86400);
    var hours = Math.floor((seconds % 86400) / 3600);
    var minutes = Math.floor((seconds % 3600) / 60);
    if (days > 0) {
      return days + ' d ' + hours + ' h';
    }
    if (hours > 0) {
      return hours + ' h ' + pad2(minutes) + ' min';
    }
    return Math.max(minutes, 0) + ' min';
  }

  function parseTime(iso) {
    var date = new Date(iso);
    return isNaN(date.getTime()) ? null : date;
  }

  function clock(iso) {
    var date = parseTime(iso);
    if (!date) {
      return DASH;
    }
    return date.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }

  function dateTime(iso) {
    var date = parseTime(iso);
    if (!date) {
      return DASH;
    }
    return date.toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' });
  }

  function ago(iso, now) {
    var date = parseTime(iso);
    if (!date) {
      return DASH;
    }
    var seconds = Math.max(0, Math.round((now - date.getTime()) / 1000));
    if (seconds < 60) {
      return 'há ' + seconds + ' s';
    }
    if (seconds < 3600) {
      return 'há ' + Math.floor(seconds / 60) + ' min';
    }
    return 'há ' + Math.floor(seconds / 3600) + ' h';
  }

  // Disk throughput uses decimal units, as Windows' own tools do.
  function throughput(bytesPerSecond) {
    if (!isNum(bytesPerSecond)) {
      return { value: DASH, unit: 'MB/s' };
    }
    if (bytesPerSecond >= 1e9) {
      return { value: nf1.format(bytesPerSecond / 1e9), unit: 'GB/s' };
    }
    if (bytesPerSecond >= 1e6) {
      return { value: nf1.format(bytesPerSecond / 1e6), unit: 'MB/s' };
    }
    return { value: nf0.format(bytesPerSecond / 1e3), unit: 'KB/s' };
  }

  function shortCommit(commit) {
    return typeof commit === 'string' && commit.length > 0 ? commit.slice(0, 10) : DASH;
  }

  function shortCPU(name) {
    if (!name) {
      return '';
    }
    return name.replace(/\s+\d+-Core Processor$/i, '').replace(/\s+Processor$/i, '');
  }

  function plural(count, one, many) {
    return count === 1 ? one : many;
  }

  // ------------------------------------------------------------------ lookup

  function modelStatuses(status) {
    return status && Array.isArray(status.model_statuses) ? status.model_statuses : [];
  }

  function modelStatus(status, id) {
    var list = modelStatuses(status);
    for (var index = 0; index < list.length; index++) {
      if (list[index].id === id) {
        return list[index];
      }
    }
    return null;
  }

  function displayName(status, id) {
    if (!id) {
      return DASH;
    }
    var models = status && Array.isArray(status.models) ? status.models : [];
    for (var index = 0; index < models.length; index++) {
      if (models[index].id === id && models[index].display_name) {
        return models[index].display_name;
      }
    }
    return id;
  }

  // outputCeiling mirrors the edge: n_predict when the profile pins one,
  // otherwise the model's advertised max_output_tokens.
  function outputCeiling(status, id) {
    var entry = modelStatus(status, id);
    if (!entry || !entry.profile) {
      return null;
    }
    if (isNum(entry.profile.n_predict)) {
      return entry.profile.n_predict;
    }
    return isNum(entry.profile.max_output_tokens) ? entry.profile.max_output_tokens : null;
  }

  var REASONS = {
    model_already_running: 'em execução',
    commit_headroom_available: 'há folga de commit para carregar',
    insufficient_commit_headroom: 'sem folga de commit para carregar agora',
    insufficient_physical_memory: 'RAM física insuficiente para manter os pesos residentes',
    insufficient_vram_budget: 'excede o orçamento de VRAM declarado',
    resource_profile_incomplete: 'perfil de recursos incompleto',
    resource_measurement_required: 'sem medição de recursos',
    canary_resource_measurement_pending: 'candidato canary ainda sem medição',
    artifact_missing: 'o arquivo dos pesos não está mais no disco',
    artifact_size_mismatch: 'o arquivo dos pesos tem tamanho diferente do implantado'
  };

  // whyNot says, for a model that cannot be loaded, the reason the edge gave,
  // instead of one answer for every refusal: a deleted file is not a memory
  // problem.
  function whyNot(status, id) {
    var entry = modelStatus(status, id);
    if (entry && entry.reason === 'artifact_missing') {
      return 'O arquivo deste modelo não está no disco.';
    }
    if (entry && entry.reason === 'artifact_size_mismatch') {
      return 'O arquivo deste modelo tem tamanho diferente do implantado.';
    }
    return 'Sem memória para este modelo agora.';
  }

  function reasonText(reason) {
    return REASONS[reason] || reason || DASH;
  }

  // ------------------------------------------------------------------ sparkline

  // sparkPaths draws up to HISTORY samples right-aligned, so the newest value
  // is always at the right edge and a young history grows in from it. A null
  // sample breaks the line instead of being drawn as zero.
  function sparkPaths(values, top) {
    var count = values.length;
    var offset = HISTORY - count;
    var line = '';
    var area = '';
    var segment = [];

    function point(entry) {
      return entry[0].toFixed(2) + ' ' + entry[1].toFixed(2);
    }

    function flush() {
      if (segment.length === 0) {
        return;
      }
      var points = segment.map(point);
      line += 'M' + points.join('L');
      if (segment.length === 1) {
        line += 'L' + points[0];
      }
      area += 'M' + segment[0][0].toFixed(2) + ' 32L' + points.join('L') +
        'L' + segment[segment.length - 1][0].toFixed(2) + ' 32Z';
      segment = [];
    }

    for (var index = 0; index < count; index++) {
      var value = values[index];
      if (!isNum(value)) {
        flush();
        continue;
      }
      var x = ((offset + index) / (HISTORY - 1)) * 100;
      var ratio = top > 0 ? Math.min(Math.max(value / top, 0), 1) : 0;
      segment.push([x, 30 - ratio * 26]);
    }
    flush();
    return { line: line, area: area };
  }

  function seriesMax(values) {
    var max = 0;
    (values || []).forEach(function (value) {
      if (isNum(value) && value > max) {
        max = value;
      }
    });
    return max;
  }

  function drawSpark(name, values, top) {
    var paths = sparkPaths(values || [], top);
    var line = $('spark-' + name + '-line');
    var area = $('spark-' + name + '-area');
    if (line.getAttribute('d') !== paths.line) {
      line.setAttribute('d', paths.line);
    }
    if (area.getAttribute('d') !== paths.area) {
      area.setAttribute('d', paths.area);
    }
  }

  function card(name) {
    return document.querySelector('.metric[data-metric="' + name + '"]');
  }

  function setTag(id, text, tone) {
    var element = $(id);
    if (!text) {
      setText(element, '');
      element.className = 'metric-tag';
      setTone(element, null);
      return;
    }
    element.className = 'metric-tag chip';
    setText(element, text);
    setTone(element, tone);
  }

  // ------------------------------------------------------------------ state

  var PHASES = {
    offline: { title: 'Edge indisponível', step: null, tone: 'danger' },
    draining: { title: 'Em manutenção', step: null, tone: 'warn' },
    loading: { title: 'Carregando o modelo', step: 'loading', tone: 'info' },
    prompt: { title: 'Lendo o prompt', step: 'prompt', tone: 'info' },
    working: { title: 'Processando', step: null, tone: 'info' },
    generating: { title: 'Gerando', step: 'generating', tone: 'accent' },
    queued: { title: 'Na fila', step: 'queued', tone: 'warn' },
    ready: { title: 'Pronto', step: 'rest', tone: 'accent' },
    idle: { title: 'Ocioso', step: 'rest', tone: 'muted' },
    external: { title: 'Em uso em outra ferramenta', step: null, tone: 'accent' },
    external_ready: { title: 'Modelo carregado em outra ferramenta', step: 'rest', tone: 'accent' }
  };

  // The edge is what this page was built around; when it is not answering, the
  // rest of the page still describes the machine, so "edge down" is decided by
  // the edge's own reachability and not by the phase, which another tool's
  // activity can now be.
  function edgeDown(snap) {
    return !snap.edge || snap.edge.reachable === false;
  }

  function sourceById(snap, id) {
    var list = Array.isArray(snap.sources) ? snap.sources : [];
    for (var index = 0; index < list.length; index++) {
      if (list[index].id === id) {
        return list[index];
      }
    }
    return null;
  }

  function modelDetail(model) {
    var parts = [];
    if (model.arch) {
      parts.push(model.arch);
    }
    if (model.params) {
      parts.push(model.params);
    }
    if (model.type && model.type !== 'llm') {
      parts.push(model.type);
    }
    if (isNum(model.context_loaded)) {
      parts.push('janela ' + tokens(model.context_loaded) + (isNum(model.context_max) && model.context_max > model.context_loaded ? ' de ' + tokens(model.context_max) : ''));
    } else if (isNum(model.context_max)) {
      parts.push('até ' + tokens(model.context_max) + ' de contexto');
    }
    if (isNum(model.vram_mib)) {
      parts.push(gibFromMiB(model.vram_mib) + ' GiB na VRAM');
    }
    if (isNum(model.slots) && model.slots > 1) {
      parts.push(model.slots + ' slots');
    }
    if (isNum(model.gpu_layers) && model.gpu_layers > 0) {
      // Tools pass a huge number to mean "every layer".
      parts.push(model.gpu_layers >= 999 ? 'todas as camadas na GPU' : model.gpu_layers + ' camadas na GPU');
    }
    return parts.join(' · ');
  }

  function externalSummary(snap, external, phase) {
    var source = sourceById(snap, external.source_id);
    var model = source && source.models && source.models[0] ? source.models[0] : null;
    var parts = [];
    if (model) {
      parts.push(model.id + (model.quantization ? ' · ' + model.quantization : ''));
      var detail = modelDetail(model);
      if (detail) {
        parts.push(detail);
      }
    } else if (source && (source.status === 'no-api' || source.status === 'protected')) {
      parts.push('modelo não identificado' + (source.status === 'protected' ? ' (API protegida)' : ''));
      if (isNum(source.vram_dedicated_mib)) {
        parts.push(gibFromMiB(source.vram_dedicated_mib) + ' GiB na VRAM');
      }
    }
    parts.push(phase === 'external' ? (external.basis === 'slots' ? 'atividade informada pelo servidor' : 'atividade inferida pelo uso da GPU') : 'aguardando pedidos');
    return parts.join(' · ');
  }

  var app = {
    snapshot: null,
    lastOk: 0,
    monitorDown: false,
    failures: 0,
    timer: null,
    inFlight: false,
    lastInference: null,
    lastPhase: null,
    selectedModel: '',
    actionInFlight: false,
    actionRefreshAfter: 0,
    monitorStartedAt: '',
    localOperation: null,
    localError: null,
    requestsKey: '',
    modelsKey: '',
    connectionKey: ''
  };

  function renderState(snap) {
    var activity = snap.activity || { phase: 'idle' };
    var phase = PHASES[activity.phase] ? activity.phase : 'idle';
    var meta = PHASES[phase];
    var status = snap.edge.status;
    var live = activity.live || null;
    var stateCard = $('state-card');

    stateCard.setAttribute('data-phase', phase);
    setText('rest-label', phase === 'ready' || phase === 'external_ready' ? 'Pronto' : 'Ocioso');

    var modelName = displayName(status, activity.model);
    var title = meta.title;
    var sub;
    var external = activity.external || null;
    var step = meta.step;
    if ((phase === 'external' || phase === 'external_ready') && external) {
      title = (phase === 'external' ? 'Em uso · ' : 'Pronto · ') + external.label;
      if (phase === 'external' && external.basis === 'slots') {
        step = 'generating';
      }
    }
    Array.prototype.forEach.call(document.querySelectorAll('.phase-track .badge'), function (badge) {
      var active = badge.getAttribute('data-step') === step;
      badge.setAttribute('data-active', active ? 'true' : 'false');
      setTone(badge, active ? meta.tone : null);
    });
    switch (phase) {
      case 'offline':
        sub = 'O cia-edge não respondeu em ' + snap.monitor.control_url + '. O monitor tenta de novo a cada segundo.';
        break;
      case 'draining':
        sub = 'O edge está drenando: requisições novas são recusadas até a manutenção terminar.';
        break;
      case 'loading':
        sub = modelName + ' · o roteador está iniciando o processo do modelo.';
        break;
      case 'prompt':
        sub = modelName + ' · processando o prompt antes do primeiro token.';
        break;
      case 'working':
        sub = modelName + ' · requisição sem streaming: os números chegam ao final.';
        break;
      case 'generating':
        sub = modelName;
        break;
      case 'queued':
        sub = activity.queued + ' ' + plural(activity.queued, 'requisição aguarda', 'requisições aguardam') + ' a vaga de execução.';
        break;
      case 'ready':
        sub = modelName + ' carregado · aguardando requisições.';
        break;
      case 'external':
      case 'external_ready':
        sub = external ? externalSummary(snap, external, phase) : DASH;
        break;
      default:
        var publicModel = status && status.capacity ? displayName(status, status.capacity.model) : null;
        sub = publicModel ? 'Nenhum modelo carregado · o modelo público, ' + publicModel + ', carrega na primeira requisição.' : 'Nenhum modelo carregado.';
    }
    setText('state-title', title);
    setText('state-sub', sub);

    if (app.lastPhase !== phase) {
      setText('phase-announcer', title);
      app.lastPhase = phase;
    }

    var queueChip = $('queue-chip');
    if (activity.queued > 0 && phase !== 'queued') {
      setText(queueChip, '+' + activity.queued + ' na fila');
      setTone(queueChip, 'warn');
      show(queueChip, true);
    } else {
      show(queueChip, false);
    }

    var externalRate = phase === 'external' && external && isNum(external.tokens_per_second) ? external.tokens_per_second : null;
    show('state-facts', !!live || externalRate !== null);
    if (!live && externalRate !== null) {
      setText('fact-output', DASH);
      setText('fact-rate', rate(externalRate) + ' tok/s');
      setText('fact-ttft', DASH);
      setText('fact-elapsed', DASH);
    }
    if (live) {
      var ceiling = isNum(live.max_output_tokens) ? live.max_output_tokens : null;
      setText('fact-output', int(live.output_tokens) + (ceiling ? ' / ' + tokens(ceiling) : ''));
      setText('fact-rate', live.phase === 'generating' ? rate(live.tokens_per_second) + ' tok/s' : DASH);
      setText('fact-ttft', duration(live.ttft_ms));
      setText('fact-elapsed', duration(live.elapsed_ms));
    }

    var progress = $('state-progress');
    var bar = $('state-progress-bar');
    var legend = $('state-progress-legend');
    if (phase === 'generating' && live && isNum(live.max_output_tokens) && live.max_output_tokens > 0) {
      var ratio = Math.min(live.output_tokens / live.max_output_tokens, 1);
      progress.setAttribute('data-mode', 'determinate');
      setTone(progress, 'accent');
      bar.style.width = (ratio * 100).toFixed(2) + '%';
      setText(legend, int(live.output_tokens) + ' de ' + int(live.max_output_tokens) + ' tokens de saída (' + percent(ratio) + ')');
      show(legend, true);
      show(progress, true);
    } else if (phase === 'loading' || phase === 'prompt' || phase === 'working' || phase === 'external' || (phase === 'generating' && live)) {
      progress.setAttribute('data-mode', 'indeterminate');
      setTone(progress, meta.tone);
      bar.style.width = '';
      show(legend, false);
      show(progress, true);
    } else {
      show(progress, false);
    }

    renderNotices(snap, phase);
    document.title = title + (phase === 'generating' && live && isNum(live.tokens_per_second) ? ' · ' + rate(live.tokens_per_second) + ' tok/s' : '') + ' · IA Local';
  }

  function renderNotices(snap, phase) {
    var status = snap.edge.status;
    var notices = [];
    if (edgeDown(snap) && phase !== 'offline') {
      notices.push(['info', 'O cia-edge não respondeu em ' + snap.monitor.control_url + '. O painel continua mostrando o que a máquina revela sobre as outras ferramentas.']);
    }
    if (!edgeDown(snap)) {
      if (isNum(snap.edge.status_age_ms) && snap.edge.status_age_ms > STALE_STATUS_MS) {
        notices.push(['warn', 'O status do edge está desatualizado: a última resposta foi há ' + duration(snap.edge.status_age_ms) + '.']);
      }
      if (status && status.capacity && !status.capacity.available) {
        notices.push(['warn', 'O modelo público não pode ser admitido agora: ' + reasonText(status.capacity.reason) + '.']);
        if (status.capacity.reason === 'insufficient_physical_memory' && Array.isArray(snap.sources)) {
          var holder = snap.sources.find(function (source) { return source.models && source.models.length > 0 && source.stoppable; });
          if (holder) {
            notices.push(['info', holder.label + ' também mantém um modelo carregado. A aba Modelos permite encerrar esse processo mediante confirmação; depois o CIA verifica a capacidade novamente.']);
          }
        }
      }
      if (snap.edge.telemetry === 'unavailable') {
        notices.push(['info', 'Este cia-edge ainda não publica telemetria por requisição. Velocidade, contexto e a lista de requisições aparecem depois que o edge for atualizado pelo processo de release.']);
      }
    }
    if (snap.coverage && snap.coverage.external_activity_only > 0) {
      notices.push(['info', snap.coverage.external_activity_only + ' fonte(s) externa(s) sem contagem por requisição. O monitor mostra apenas a atividade observável; para medir tokens e cache, a ferramenta precisa passar pelo edge ou publicar métricas por resposta ou log.']);
    }
    var key = JSON.stringify(notices);
    var container = $('state-notices');
    if (container.getAttribute('data-key') === key) {
      return;
    }
    container.setAttribute('data-key', key);
    container.replaceChildren.apply(container, notices.map(function (notice) {
      return node('p', { className: 'notice', text: notice[1], tone: notice[0] });
    }));
  }

  function renderPill(snap) {
    var pill = $('status-pill');
    var tone;
    var label = 'Conectando…';
    var hint = '';
    var live = false;
    var phase = snap && snap.activity ? snap.activity.phase : null;
    var status = snap ? snap.edge.status : null;
    if (app.monitorDown) {
      tone = 'danger';
      label = 'Monitor sem resposta';
    } else if (!snap) {
      tone = 'muted';
    } else if (phase === 'offline') {
      tone = 'danger';
      label = 'Edge offline';
    } else if (phase === 'external') {
      tone = 'accent';
      label = 'Em uso';
      live = true;
      if (edgeDown(snap)) {
        hint = 'O cia-edge não responde; o uso vem de outra ferramenta.';
      }
    } else if (edgeDown(snap) && phase === 'external_ready') {
      tone = 'warn';
      label = 'Edge offline';
      hint = 'O cia-edge não responde; outra ferramenta mantém um modelo carregado.';
    } else if (phase === 'draining') {
      tone = 'warn';
      label = 'Manutenção';
    } else if (!status) {
      tone = 'muted';
      label = 'Aguardando status';
    } else if (!status.ready) {
      tone = 'warn';
      label = 'Não pronto';
      hint = !status.upstream || !status.upstream.reachable ? 'O roteador não responde ao edge.' :
        status.capacity && !status.capacity.available ? 'O modelo público não pode ser admitido: ' + reasonText(status.capacity.reason) + '.' : '';
    } else if (phase === 'generating' || phase === 'prompt' || phase === 'loading' || phase === 'working') {
      tone = 'accent';
      label = 'Em uso';
      live = true;
    } else {
      tone = 'accent';
      label = 'Operacional';
    }
    setTone(pill, tone);
    pill.setAttribute('data-live', live ? 'true' : 'false');
    if (pill.title !== hint) {
      pill.title = hint;
    }
    setText('status-label', label);
  }

  // ------------------------------------------------------------------ metrics

  // admission reads one of the edge's per-resource tests for the public model.
  // The edge refuses a load for the first limit it would break; showing the
  // verdict on the card of that limit keeps a RAM refusal from being read as a
  // commit one.
  function admission(capacity, headroomField, requiredField) {
    if (!capacity || !capacity.model) {
      return null;
    }
    if (capacity.model_running) {
      // A loaded model has already been admitted; there is no verdict left
      // to show, and the card goes back to describing the machine.
      return null;
    }
    var headroom = capacity[headroomField];
    var required = capacity[requiredField];
    if (!isNum(headroom) || !isNum(required)) {
      return null;
    }
    var fits = headroom >= required;
    return {
      tag: fits ? 'admite' : 'sem folga',
      tone: fits ? 'accent' : 'danger',
      fits: fits,
      detail: 'folga ' + one(headroom) + ' GiB · requer ' + one(required) + ' GiB'
    };
  }

  function applyVerdict(tagID, verdict) {
    setTag(tagID, verdict ? verdict.tag : '', verdict ? verdict.tone : null);
  }

  function renderMetrics(snap) {
    var hardware = snap.hardware || {};
    var history = snap.history || {};
    // The edge's verdicts describe the moment they were taken; with the edge
    // gone they would be stale claims about memory, so the cards drop them.
    var status = edgeDown(snap) ? null : snap.edge.status;
    var inference = snap.edge.inference;
    var activity = snap.activity || {};
    var live = activity.live;
    var telemetry = snap.edge.telemetry;
    var capacity = status ? status.capacity : null;

    // Speed: live while tokens arrive, otherwise the last finished request of
    // whatever served it - the edge, or another tool whose server log was read.
    var speedCard = card('speed');
    var measured = measuredRequests(snap, inference);
    var lastMeasured = measured.length > 0 ? measured[0] : null;
    var externalTool = activity.phase === 'external' ? activity.external : null;
    if (externalTool && isNum(externalTool.tokens_per_second)) {
      setText('speed-value', rate(externalTool.tokens_per_second));
      setTag('speed-tag', 'ao vivo', 'accent');
      setText('speed-sub', externalTool.label + ' · informado pelo servidor');
    } else if (activity.phase === 'generating' && live) {
      setText('speed-value', rate(live.tokens_per_second));
      setTag('speed-tag', 'ao vivo', 'accent');
      setText('speed-sub', int(live.output_tokens) + ' tokens · 1º em ' + duration(live.ttft_ms));
    } else if (lastMeasured && isNum(lastMeasured.decodeRate)) {
      setText('speed-value', rate(lastMeasured.decodeRate));
      setTag('speed-tag', externalTool ? 'última · gerando' : 'última', 'muted');
      setText('speed-sub', 'leitura ' + rate(lastMeasured.promptRate) + ' tok/s · ' +
        (lastMeasured.origin === 'external' ? lastMeasured.label + ' · ' : '') + clock(lastMeasured.record.started_at));
    } else if (externalTool) {
      // Another tool whose server gives no numbers: saying so is better than a
      // dash that looks like a fault.
      setText('speed-value', DASH);
      setTag('speed-tag', 'externa', 'muted');
      setText('speed-sub', externalTool.label + ' não informa a velocidade; aponte a ferramenta para o edge para medir por requisição');
    } else {
      setText('speed-value', DASH);
      setTag('speed-tag', '', null);
      setText('speed-sub', telemetry === 'unavailable' ? 'telemetria indisponível neste edge' :
        edgeDown(snap) ? 'edge offline' : 'nenhuma requisição ainda');
    }
    speedCard.setAttribute('data-stale', edgeDown(snap) && !externalTool && !lastMeasured ? 'true' : 'false');
    drawSpark('speed', history.tokens_per_second, Math.max(10, seriesMax(history.tokens_per_second) * 1.15));

    // GPU
    setText('gpu-value', isNum(hardware.gpu_util) ? int(hardware.gpu_util) : DASH);
    setText('gpu-sub', hardware.gpu_name || 'utilização do motor mais ocupado');
    drawSpark('gpu', history.gpu_util, 100);

    // Power: what the driver says the card draws, and the energy that adds up to.
    var energy = snap.energy || {};
    setText('power-value', isNum(hardware.power_w) ? int(hardware.power_w) : DASH);
    setTag('power-tag', hardware.power_source ? sensorLabel(hardware.power_source) : '', 'muted');
    if (isNum(hardware.power_w)) {
      var powerParts = [];
      if (isNum(energy.session_wh)) {
        powerParts.push('sessão ' + energyText(energy.session_wh));
        if (isNum(energy.observed_seconds) && energy.observed_seconds >= 60) {
          powerParts.push('média ' + int(energy.session_wh * 3600 / energy.observed_seconds) + ' W');
        }
      }
      setText('power-sub', powerParts.join(' · ') || 'consumo da placa');
    } else {
      setText('power-sub', hardware.gpu_name ? 'o driver não informa a energia desta GPU' : DASH);
    }
    drawSpark('power', history.power_w, Math.max(50, seriesMax(history.power_w) * 1.15));

    // Temperature and clocks, from the same driver reading.
    var tempCard = card('temp');
    setText('temp-value', isNum(hardware.gpu_temp_c) ? int(hardware.gpu_temp_c) : DASH);
    var tempParts = [];
    if (isNum(hardware.gpu_hotspot_c)) {
      tempParts.push('ponto quente ' + int(hardware.gpu_hotspot_c) + ' °C');
    }
    if (isNum(hardware.gpu_clock_mhz)) {
      tempParts.push('núcleo ' + nf1.format(hardware.gpu_clock_mhz / 1000) + ' GHz');
    }
    setText('temp-sub', tempParts.join(' · ') || (isNum(hardware.gpu_temp_c) ? 'temperatura da GPU' : DASH));
    // The memory clock is worth having but not worth a truncated line.
    $('temp-sub').title = isNum(hardware.mem_clock_mhz) ? 'memória ' + nf1.format(hardware.mem_clock_mhz / 1000) + ' GHz' : '';
    var hot = isNum(hardware.gpu_hotspot_c) ? hardware.gpu_hotspot_c : null;
    var edgeTemp = isNum(hardware.gpu_temp_c) ? hardware.gpu_temp_c : null;
    var tempTone = hot !== null ? (hot >= 100 ? 'danger' : hot >= 90 ? 'warn' : null) :
      edgeTemp !== null ? (edgeTemp >= 85 ? 'danger' : edgeTemp >= 78 ? 'warn' : null) : null;
    setTone(tempCard, tempTone);
    setTag('temp-tag', tempTone === 'danger' ? 'muito quente' : tempTone === 'warn' ? 'quente' : '', tempTone);
    drawSpark('temp', history.gpu_temp_c, Math.max(90, seriesMax(history.gpu_temp_c) * 1.1));

    // VRAM
    var vramCard = card('vram');
    var gpuMemory = status ? status.gpu_memory : null;
    var vramTotal = isNum(hardware.vram_total_mib) ? hardware.vram_total_mib :
      gpuMemory && isNum(gpuMemory.budget_mib) ? gpuMemory.budget_mib : null;
    setText('vram-value', gibFromMiB(hardware.vram_dedicated_mib));
    setText('vram-unit', vramTotal ? '/ ' + gibFromMiB(vramTotal) + ' GiB' : 'GiB');
    var occupancy = isNum(hardware.vram_dedicated_mib) && vramTotal ? hardware.vram_dedicated_mib / vramTotal : null;
    var process = hardware.model_process;
    var vramSub = occupancy !== null ? percent(occupancy) + ' ocupada' : 'memória dedicada da GPU';
    if (process && isNum(process.vram_dedicated_mib)) {
      vramSub += ' · modelo ' + gibFromMiB(process.vram_dedicated_mib) + ' GiB';
    } else if (capacity && !capacity.model_running && isNum(capacity.required_vram_gib)) {
      vramSub += ' · modelo requer ' + one(capacity.required_vram_gib) + ' GiB';
    }
    setText('vram-sub', vramSub);
    // The VRAM test is against a declared budget, not live usage, so it is
    // only ever the manifest that fails it.
    var overBudget = capacity && capacity.reason === 'insufficient_vram_budget';
    applyVerdict('vram-tag', overBudget ? { tag: 'excede', tone: 'danger' } : null);
    setTone(vramCard, overBudget || (gpuMemory && gpuMemory.state === 'pressured') ? 'danger' :
      occupancy !== null && occupancy >= 0.95 ? 'warn' : null);
    drawSpark('vram', history.vram_dedicated_mib, vramTotal || Math.max(1024, seriesMax(history.vram_dedicated_mib) * 1.15));

    // Shared memory: the edge's verdict decides the tone, because it is the
    // edge that knows the thresholds the benchmarks established.
    var sharedCard = card('shared');
    setText('shared-value', gibFromMiB(hardware.vram_shared_mib));
    var verdict = gpuMemory ? gpuMemory.state : null;
    var verdicts = {
      ok: { tag: 'normal', tone: 'accent', sub: 'sem paginação da VRAM para a RAM', card: null },
      elevated: { tag: 'elevada', tone: 'warn', sub: 'acima do normal: a VRAM está quase cheia', card: 'warn' },
      pressured: { tag: 'paginando', tone: 'danger', sub: 'VRAM cheia: o driver pagina pela PCIe e o prompt fica lento', card: 'danger' }
    };
    var judged = verdicts[verdict];
    if (judged) {
      setTag('shared-tag', judged.tag, judged.tone);
      setText('shared-sub', judged.sub);
      setTone(sharedCard, judged.card);
    } else {
      setTag('shared-tag', '', null);
      setText('shared-sub', 'memória do sistema usada pela GPU');
      setTone(sharedCard, null);
    }
    drawSpark('shared', history.vram_shared_mib, Math.max(2048, seriesMax(history.vram_shared_mib) * 1.15));

    // CPU
    var machine = snap.machine || {};
    setText('cpu-value', isNum(hardware.cpu_util) ? int(hardware.cpu_util) : DASH);
    var cpuName = shortCPU(machine.cpu_name);
    var topology = machine.cores && machine.threads ? machine.cores + ' núcleos · ' + machine.threads + ' threads' :
      machine.threads ? machine.threads + ' threads' : '';
    setText('cpu-sub', [cpuName, topology].filter(Boolean).join(' · ') || DASH);
    drawSpark('cpu', history.cpu_util, 100);

    // RAM: its admission verdict is the edge's physical-memory test.
    var ramCard = card('ram');
    setText('ram-value', gib(hardware.ram_used_bytes));
    setText('ram-unit', isNum(hardware.ram_total_bytes) ? '/ ' + gib(hardware.ram_total_bytes) + ' GiB' : 'GiB');
    var ramRatio = isNum(hardware.ram_used_bytes) && isNum(hardware.ram_total_bytes) && hardware.ram_total_bytes > 0 ?
      hardware.ram_used_bytes / hardware.ram_total_bytes : null;
    var ramVerdict = admission(capacity, 'physical_headroom_gib', 'required_physical_gib');
    applyVerdict('ram-tag', ramVerdict);
    var ramSub;
    if (process && isNum(process.private_bytes)) {
      ramSub = 'modelo ' + gib(process.private_bytes) + ' GiB' + (process.count > 1 ? ' (' + process.count + ' processos)' : '') +
        (ramRatio !== null ? ' · ' + percent(ramRatio) + ' em uso' : '');
    } else if (ramVerdict && ramVerdict.detail) {
      ramSub = ramVerdict.detail;
    } else {
      ramSub = ramRatio !== null ? percent(ramRatio) + ' em uso' : DASH;
    }
    setText('ram-sub', ramSub);
    setTone(ramCard, ramVerdict && !ramVerdict.fits ? 'danger' :
      ramRatio !== null && ramRatio >= 0.97 ? 'danger' : ramRatio !== null && ramRatio >= 0.9 ? 'warn' : null);
    drawSpark('ram', history.ram_used_bytes, isNum(hardware.ram_total_bytes) ? hardware.ram_total_bytes : seriesMax(history.ram_used_bytes) * 1.15);

    // Commit: the limit a model is charged against before it may load.
    var commitCard = card('commit');
    setText('commit-value', gib(hardware.commit_used_bytes));
    setText('commit-unit', isNum(hardware.commit_limit_bytes) ? '/ ' + gib(hardware.commit_limit_bytes) + ' GiB' : 'GiB');
    var commitRatio = isNum(hardware.commit_used_bytes) && isNum(hardware.commit_limit_bytes) && hardware.commit_limit_bytes > 0 ?
      hardware.commit_used_bytes / hardware.commit_limit_bytes : null;
    var commitVerdict = admission(capacity, 'commit_headroom_gib', 'required_commit_gib');
    applyVerdict('commit-tag', commitVerdict);
    setText('commit-sub', commitVerdict && commitVerdict.detail ? commitVerdict.detail :
      commitRatio !== null ? percent(commitRatio) + ' do limite' : 'limite de memória comprometida');
    setTone(commitCard, commitVerdict && !commitVerdict.fits ? 'danger' : commitRatio !== null && commitRatio >= 0.9 ? 'warn' : null);
    drawSpark('commit', history.commit_used_bytes, isNum(hardware.commit_limit_bytes) ? hardware.commit_limit_bytes : seriesMax(history.commit_used_bytes) * 1.15);

    // Disk
    var total = isNum(hardware.disk_read_bytes_per_second) || isNum(hardware.disk_write_bytes_per_second) ?
      (hardware.disk_read_bytes_per_second || 0) + (hardware.disk_write_bytes_per_second || 0) : null;
    var diskTotal = throughput(total);
    setText('disk-value', diskTotal.value);
    setText('disk-unit', diskTotal.unit);
    var read = throughput(hardware.disk_read_bytes_per_second);
    var write = throughput(hardware.disk_write_bytes_per_second);
    setText('disk-sub', 'leitura ' + read.value + ' ' + read.unit + ' · escrita ' + write.value + ' ' + write.unit);
    drawSpark('disk', history.disk_bytes_per_second, Math.max(1e6, seriesMax(history.disk_bytes_per_second) * 1.15));
  }

  // ------------------------------------------------------------------ context

  function gaugeTickPoints(fraction) {
    var angle = (135 + 270 * fraction) * Math.PI / 180;
    return {
      x1: 60 + 43 * Math.cos(angle),
      y1: 60 + 43 * Math.sin(angle),
      x2: 60 + 57 * Math.cos(angle),
      y2: 60 + 57 * Math.sin(angle)
    };
  }

  function setBar(prefix, ratio, figure, tone) {
    var fill = $(prefix + '-fill');
    fill.style.width = isNum(ratio) ? (Math.min(Math.max(ratio, 0), 1) * 100).toFixed(2) + '%' : '0%';
    if (tone) {
      setTone(fill, tone);
    }
    setText(prefix + '-figure', figure);
  }

  var ROUTES = { '/v1/responses': 'Responses', '/v1/chat/completions': 'Chat', '/v1/messages': 'Messages' };

  function routeLabel(route) {
    return ROUTES[route] || route || '';
  }

  // measuredRequests lists the requests the page can show numbers for, newest
  // first, whichever way they were measured: by the edge, which serves them, or
  // from the log of another tool's llama.cpp server. The two look the same to
  // the cards below.
  function measuredRequests(snap, inference) {
    if (Array.isArray(snap.requests)) {
      return snap.requests.map(function (record) {
        var external = record.via === 'server-log';
        return {
          at: parseTime(record.started_at), origin: external ? 'external' : 'edge',
          label: record.source_label, model: record.model,
          prompt: record.prompt_tokens, cached: record.cached_tokens, output: record.output_tokens,
          promptRate: record.prompt_tokens_per_second, decodeRate: record.decode_tokens_per_second,
          // A historical request keeps its own window, even if its source
          // now runs another model or no longer exists.
          window: record.context_tokens,
          record: record
        };
      });
    }
    var list = [];
    var recent = inference && Array.isArray(inference.recent) ? inference.recent : [];
    recent.forEach(function (record) {
      list.push({
        at: parseTime(record.started_at), origin: 'edge', label: 'cia-edge', model: record.model,
        prompt: record.prompt_tokens, cached: record.cached_tokens, output: record.output_tokens,
        promptRate: record.prompt_tokens_per_second, decodeRate: record.decode_tokens_per_second,
        window: record.context_tokens, record: record
      });
    });
    var external = Array.isArray(snap.external_activity) ? snap.external_activity : [];
    external.forEach(function (record) {
      if (record.measured !== 'server-log') {
        return;
      }
      list.push({
        at: parseTime(record.started_at), origin: 'external', label: record.label, model: record.model,
        prompt: record.prompt_tokens, cached: record.cached_tokens, output: record.output_tokens,
        promptRate: record.prompt_tokens_per_second, decodeRate: record.tokens_per_second,
        window: record.context_tokens,
        record: record
      });
    });
    list.sort(function (a, b) {
      return (b.at ? b.at.getTime() : 0) - (a.at ? a.at.getTime() : 0);
    });
    return list;
  }

  function renderContext(snap, inference) {
    var status = snap.edge.status;
    var latest = null;
    var measured = measuredRequests(snap, inference);
    for (var index = 0; index < measured.length; index++) {
      if (isNum(measured[index].prompt)) {
        latest = measured[index];
        break;
      }
    }
    var gauge = $('gauge-value');
    var tick = $('gauge-tick');
    // Without a figure the arc is hidden, not drawn at zero: a round cap on an
    // empty dash would still paint a dot that reads as "0%".
    gauge.setAttribute('visibility', latest ? 'visible' : 'hidden');
    var externalModel = null;
    if (!latest && snap.activity && snap.activity.external) {
      var externalSource = sourceById(snap, snap.activity.external.source_id);
      externalModel = externalSource && externalSource.models && externalSource.models[0] && isNum(externalSource.models[0].context_loaded) ?
        { label: externalSource.label, model: externalSource.models[0] } : null;
    }
    if (!latest && externalModel) {
      // A tool that reports the window it loaded, without per-request usage:
      // the size is known, the occupancy is not.
      gauge.style.strokeDasharray = '0 314.2';
      tick.setAttribute('visibility', 'hidden');
      setText('gauge-number', DASH);
      setText('gauge-label', tokens(externalModel.model.context_loaded) + ' tokens de janela');
      setText('context-hint', externalModel.label + ' · janela carregada');
      setBar('bar-cache', null, DASH);
      setBar('bar-output', null, DASH);
      show('bar-compact', false);
      return;
    }
    if (!latest) {
      gauge.style.strokeDasharray = '0 314.2';
      setText('gauge-number', DASH);
      setText('gauge-label', snap.edge.telemetry === 'unavailable' ? 'sem telemetria' : 'sem requisições');
      setText('context-hint', 'última requisição');
      tick.setAttribute('visibility', 'hidden');
      setBar('bar-cache', null, DASH);
      setBar('bar-output', null, DASH);
      show('bar-compact', false);
      return;
    }

    var output = isNum(latest.output) ? latest.output : 0;
    var used = latest.prompt + output;
    var contextWindow = isNum(latest.window) && latest.window > 0 ? latest.window : null;
    var fill = contextWindow ? used / contextWindow : null;
    var tone = fill === null ? 'accent' : fill >= 0.9 ? 'danger' : fill >= 0.75 ? 'warn' : 'accent';
    setTone(gauge, tone);
    gauge.style.strokeDasharray = fill === null ? '0 314.2' : (235.6 * Math.min(fill, 1)).toFixed(2) + ' 314.2';
    setText('gauge-number', fill === null ? DASH : nf0.format(Math.round(fill * 100)));
    setText('gauge-label', tokens(used) + (contextWindow ? ' de ' + tokens(contextWindow) : '') + ' tokens');
    setText('context-hint', (latest.origin === 'external' ? latest.label + ' · ' : '') + 'última requisição · ' + clock(latest.record.started_at));

    // The compaction point and the output ceiling are the edge's to know.
    var entry = latest.origin === 'edge' ? modelStatus(status, latest.model) : null;
    var threshold = entry && entry.profile && isNum(entry.profile.compact_threshold_tokens) ? entry.profile.compact_threshold_tokens : null;
    if (threshold && contextWindow && threshold < contextWindow) {
      var points = gaugeTickPoints(threshold / contextWindow);
      tick.setAttribute('x1', points.x1.toFixed(2));
      tick.setAttribute('y1', points.y1.toFixed(2));
      tick.setAttribute('x2', points.x2.toFixed(2));
      tick.setAttribute('y2', points.y2.toFixed(2));
      tick.setAttribute('visibility', 'visible');
    } else {
      tick.setAttribute('visibility', 'hidden');
    }

    var cached = isNum(latest.cached) ? latest.cached : null;
    var cacheRatio = cached !== null && latest.prompt > 0 ? cached / latest.prompt : null;
    setBar('bar-cache', cacheRatio, cached === null ? DASH : tokens(cached) + ' de ' + tokens(latest.prompt) + ' · ' + percent(cacheRatio));

    var ceiling = latest.origin === 'edge' ? outputCeiling(status, latest.model) : null;
    setBar('bar-output', ceiling ? output / ceiling : null,
      isNum(latest.output) ? tokens(output) + (ceiling ? ' de ' + tokens(ceiling) : '') + (latest.record.output_estimated ? ' (estimado)' : '') : DASH,
      ceiling && output >= ceiling ? 'warn' : 'info');

    if (threshold) {
      var compactRatio = used / threshold;
      setBar('bar-compact', compactRatio, tokens(used) + ' de ' + tokens(threshold), compactRatio >= 0.9 ? 'danger' : 'warn');
      show('bar-compact', true);
    } else {
      show('bar-compact', false);
    }
  }

  // ------------------------------------------------------------------ requests

  var FINISH = {
    stop: { text: 'ok', tone: 'accent' },
    length: { text: 'limite', tone: 'warn' },
    tool_calls: { text: 'ferramenta', tone: 'info' },
    filtered: { text: 'filtrada', tone: 'warn' },
    cancelled: { text: 'cancelada', tone: 'muted' },
    other: { text: 'concluída', tone: 'muted' },
    unknown: { text: 'concluída', tone: 'muted' }
  };

  function finishBadge(record) {
    if (record.finish === 'error' || record.status >= 400) {
      return node('span', { className: 'badge', text: 'erro ' + record.status, tone: 'danger' });
    }
    var finish = FINISH[record.finish] || FINISH.unknown;
    return node('span', { className: 'badge', text: finish.text, tone: finish.tone });
  }

  function modelCell(name, via, title) {
    return node('td', { className: 'model-cell', title: title }, [
      node('span', { className: 'model-name', text: name }),
      node('span', { className: 'via', text: via })
    ]);
  }

  function edgeRow(status, record) {
    var cell = modelCell(displayName(status, record.model), 'cia-edge' + (record.route ? ' · ' + routeLabel(record.route) : ''), record.model);
    var statusCell = node('td', { className: 'status-cell' }, [finishBadge(record)]);
    if (record.cold_start) {
      statusCell.appendChild(node('span', { className: 'badge', text: 'frio', tone: 'info', title: 'Esta requisição esperou o modelo carregar' }));
    }
    var outputCell = node('td', { className: 'n' }, [int(record.output_tokens)]);
    if (record.output_estimated) {
      outputCell.insertBefore(node('span', { className: 'estimate', text: '≈', title: 'Contado pelos eventos de token: o runtime não informou o uso' }), outputCell.firstChild);
    }
    var evidence = record.measurements || {};
    outputCell.title = metricEvidence(evidence.output || (record.output_estimated ? 'stream-events-estimate' : ''), record.output_tokens);
    return node('tr', {}, [
      node('td', { className: 'dim', text: clock(record.started_at) }),
      cell,
      statusCell,
      node('td', { className: 'n', text: int(record.prompt_tokens), title: metricEvidence(evidence.prompt, record.prompt_tokens) }),
      node('td', { className: 'n dim', text: int(record.cached_tokens), title: metricEvidence(evidence.cache, record.cached_tokens) }),
      outputCell,
      node('td', { className: 'n', text: (evidence.decode_rate === 'wall-clock-estimate' ? '≈' : '') + rate(record.decode_tokens_per_second), title: metricEvidence(evidence.decode_rate, record.decode_tokens_per_second) }),
      node('td', { className: 'n dim', text: duration(record.duration_ms) })
    ]);
  }

  // externalRow is a stretch of activity in a tool the edge never sees. When the
  // tool's own server log described it, it carries the same numbers as an
  // edge request; when only the GPU did, it carries what the GPU showed.
  function externalRow(record) {
    var logged = record.measured === 'server-log';
    var details = [];
    if (isNum(record.peak_gpu_util)) {
      details.push('pico de GPU ' + int(record.peak_gpu_util) + '%');
    }
    if (isNum(record.peak_power_w)) {
      details.push('pico de ' + int(record.peak_power_w) + ' W');
    }
    if (isNum(record.energy_wh)) {
      details.push('energia da placa no período: ' + energyText(record.energy_wh));
    }
    if (isNum(record.ttft_ms)) {
      details.push('1º token em ' + duration(record.ttft_ms));
    }
    if (isNum(record.prompt_tokens_per_second)) {
      details.push('leitura ' + rate(record.prompt_tokens_per_second) + ' tok/s');
    }
    var how = logged ? 'contagens do log do servidor; prompt e cache derivados do estado do slot' : 'estimado pelo uso da GPU';
    var evidence = record.measurements || {};
    var title = record.label + ' · ' + how + (details.length ? ' · ' + details.join(' · ') : '');
    var badge = node('span', {
      className: 'badge',
      text: record.truncated ? 'limite' : 'externa',
      tone: record.truncated ? 'warn' : logged ? 'accent' : 'info',
      title: title
    });
    return node('tr', {}, [
      node('td', { className: 'dim', text: clock(record.started_at) }),
      modelCell(record.model || record.label, record.label + ' · ' + how, title),
      node('td', { className: 'status-cell' }, [badge]),
      node('td', { className: 'n' + (logged ? '' : ' dim'), text: logged ? int(record.prompt_tokens) : DASH, title: metricEvidence(evidence.prompt, record.prompt_tokens) }),
      node('td', { className: 'n dim', text: isNum(record.cached_tokens) ? int(record.cached_tokens) : DASH, title: metricEvidence(evidence.cache, record.cached_tokens) }),
      node('td', { className: 'n' + (logged ? '' : ' dim'), text: logged ? int(record.output_tokens) : DASH, title: metricEvidence(evidence.output, record.output_tokens) }),
      node('td', { className: 'n' + (isNum(record.tokens_per_second) ? '' : ' dim'), text: isNum(record.tokens_per_second) ? rate(record.tokens_per_second) : DASH, title: metricEvidence(evidence.decode_rate, record.tokens_per_second) }),
      node('td', { className: 'n dim', text: duration(record.duration_ms), title: title })
    ]);
  }

  function metricEvidence(source, value) {
    if (!source) {
      return isNum(value) ? 'Origem não informada por esta versão da telemetria' : 'Valor não informado pela engine';
    }
    return {
      'runtime-usage': 'Contagem informada pela engine na resposta',
      'runtime-timings': 'Contagem ou velocidade informada pelos tempos da engine',
      'server-log': 'Valor informado pelo log da engine',
      'server-log-derived': 'Calculado a partir dos contadores e do estado do slot no log da engine',
      'stream-events-estimate': 'Estimativa pelos eventos da resposta; um evento pode conter mais de um token',
      'wall-clock-estimate': 'Estimativa pela duração observada no edge'
    }[source] || 'Origem não reconhecida pela página';
  }

  // combinedTotals adds what other tools' logs described to the edge's own
  // totals, so the strip under the table counts every request the page shows.
  function combinedTotals(totals, logged) {
    if (!totals && logged.length === 0) {
      return null;
    }
    var sum = totals ? {
      requests: totals.requests, failed: totals.failed, prompt_tokens: totals.prompt_tokens, cached_tokens: totals.cached_tokens,
      output_tokens: totals.output_tokens, estimated_output_events: isNum(totals.estimated_output_events) ? totals.estimated_output_events : 0,
      timed_prompt_tokens: totals.timed_prompt_tokens, prompt_ms: totals.prompt_ms,
      timed_output_tokens: totals.timed_output_tokens, decode_ms: totals.decode_ms, since: totals.since
    } : {
      requests: 0, failed: 0, prompt_tokens: 0, cached_tokens: 0, output_tokens: 0, estimated_output_events: 0, timed_prompt_tokens: 0, prompt_ms: 0,
      timed_output_tokens: 0, decode_ms: 0, since: null
    };
    logged.forEach(function (record) {
      var prompt = isNum(record.prompt_tokens) ? record.prompt_tokens : 0;
      var cached = isNum(record.cached_tokens) ? record.cached_tokens : 0;
      var output = isNum(record.output_tokens) ? record.output_tokens : 0;
      sum.requests += 1;
      sum.prompt_tokens += prompt;
      sum.cached_tokens += cached;
      sum.output_tokens += output;
      if (isNum(record.prompt_ms) && record.prompt_ms > 0) {
        sum.timed_prompt_tokens += Math.max(prompt - cached, 0);
        sum.prompt_ms += record.prompt_ms;
      }
      if (isNum(record.decode_ms) && record.decode_ms > 0) {
        sum.timed_output_tokens += output;
        sum.decode_ms += record.decode_ms;
      }
    });
    return sum;
  }

  function renderRequests(snap, inference, stale) {
    var status = snap.edge.status;
    var recent = inference && Array.isArray(inference.recent) ? inference.recent.slice(0, 12) : [];
    var external = Array.isArray(snap.external_activity) ? snap.external_activity.slice(0, 12) : [];
    var loggedExternal = (Array.isArray(snap.external_activity) ? snap.external_activity : []).filter(function (record) {
      return record.measured === 'server-log';
    });
    var totals = combinedTotals(inference ? inference.totals : null, loggedExternal);

    var key = (stale ? 's' : 'f') + recent.length + '|' + (recent.length ? recent[0].started_at + recent[0].duration_ms : '') +
      '|' + external.length + '|' + (external.length ? external[0].started_at + external[0].duration_ms : '') +
      '|' + (status ? modelStatuses(status).length : 0);
    if (key !== app.requestsKey) {
      app.requestsKey = key;
      var body = $('requests-body');
      var entries = recent.map(function (record) {
        return { at: parseTime(record.started_at), row: edgeRow(status, record) };
      }).concat(external.map(function (record) {
        return { at: parseTime(record.started_at), row: externalRow(record) };
      }));
      entries.sort(function (a, b) {
        return (b.at ? b.at.getTime() : 0) - (a.at ? a.at.getTime() : 0);
      });
      body.replaceChildren.apply(body, entries.slice(0, 12).map(function (entry) { return entry.row; }));
      body.parentNode.setAttribute('data-stale', stale ? 'true' : 'false');
    }
    var empty = recent.length === 0 && external.length === 0;
    show('requests-empty', empty);
    setText('requests-empty', snap.edge.telemetry === 'unavailable' ?
      'Este edge ainda não publica telemetria por requisição.' :
      'Nenhuma requisição ainda. As de outras ferramentas aparecem aqui como "externa", com tokens e cache quando o log do servidor delas é lido.');

    if (totals) {
      var hints = [];
      if (inference && inference.totals) {
        hints.push('edge desde ' + dateTime(inference.totals.since));
      }
      if (loggedExternal.length > 0) {
        hints.push('+' + loggedExternal.length + ' de outras ferramentas');
      }
      if (totals.estimated_output_events > 0) {
        hints.push(int(totals.estimated_output_events) + ' eventos de saída estimados fora do total exato');
      }
      setText('requests-hint', hints.join(' · '));
      setText('total-requests', int(totals.requests));
      setText('total-failed', int(totals.failed));
      setTone($('total-failed'), totals.failed > 0 ? 'danger' : null);
      setText('total-prompt', tokens(totals.prompt_tokens));
      setText('total-cached', tokens(totals.cached_tokens) + (totals.prompt_tokens > 0 ? ' · ' + percent(totals.cached_tokens / totals.prompt_tokens) : ''));
      setText('total-output', tokens(totals.output_tokens));
      $('total-output').title = inference && inference.totals && !isNum(inference.totals.estimated_output_events) ?
        'Esta versão do edge não separa eventos estimados do total de saída.' :
        'Total de tokens informados pela engine; eventos de streaming estimados ficam separados.';
      setText('total-prompt-rate', totals.prompt_ms > 0 ? rate(totals.timed_prompt_tokens / (totals.prompt_ms / 1000)) + ' tok/s' : DASH);
      setText('total-decode-rate', totals.decode_ms > 0 ? rate(totals.timed_output_tokens / (totals.decode_ms / 1000)) + ' tok/s' : DASH);
    } else {
      setText('requests-hint', external.length > 0 ? 'inclui atividade de outras ferramentas' : '');
      ['total-requests', 'total-failed', 'total-prompt', 'total-cached', 'total-output', 'total-prompt-rate', 'total-decode-rate'].forEach(function (id) {
        setText(id, DASH);
      });
    }
  }

  // ------------------------------------------------------------------ sources

  var SOURCE_STATUS = {
    ok: null,
    protected: { text: 'API protegida', tone: 'warn' },
    'no-api': { text: 'sem API', tone: 'muted' }
  };

  function sensorLabel(source) {
    return source === 'amd-adl' ? 'AMD' : source;
  }

  // energyText writes watt-hours the way a power bill would once they add up.
  function energyText(wh) {
    if (!isNum(wh)) {
      return DASH;
    }
    if (wh >= 1000) {
      return nf1.format(wh / 1000) + ' kWh';
    }
    return (wh >= 100 ? nf0.format(wh) : nf1.format(wh)) + ' Wh';
  }

  function modelLine(model) {
    var line = [node('span', { className: 'model-id', text: model.id })];
    if (model.quantization) {
      line.push(node('span', { className: 'chip', text: model.quantization }));
    }
    var detail = modelDetail(model);
    if (detail) {
      line.push(node('span', { className: 'detail', text: detail }));
    }
    return node('li', {}, line);
  }

  function stopOptions(snap) {
    var control = snap.control || {};
    var operation = currentOperation(snap);
    return { available: !!control.stop_available, busy: app.actionInFlight || app.actionRefreshAfter > 0 || operationPending(operation) };
  }

  function sourceCard(source, options) {
    options = typeof options === 'object' && options ? options : {};
    var generating = source.activity === 'generating';
    var statusBadge = SOURCE_STATUS[source.status] || null;
    var head = [
      node('span', { className: 'source-name', text: source.label || source.kind })
    ];
    if (statusBadge) {
      head.push(node('span', { className: 'badge', text: statusBadge.text, tone: statusBadge.tone }));
    }
    head.push(node('span', {
      className: 'badge',
      text: generating ? 'gerando' : 'ocioso',
      tone: generating ? 'accent' : 'muted',
      title: source.activity_basis === 'slots' ? 'Informado pelo servidor' : 'Inferido pelo uso da GPU pelo processo'
    }));
    var meta = [source.process, source.pid ? 'PID ' + source.pid : '', source.endpoint].filter(Boolean).join(' · ');
    head.push(node('span', { className: 'source-meta', text: meta }));

    var children = [node('div', { className: 'source-head' }, head)];
    var models = Array.isArray(source.models) ? source.models : [];
    if (models.length > 0) {
      children.push(node('ul', { className: 'source-models' }, models.map(modelLine)));
    } else if (source.status === 'ok') {
      children.push(node('p', { className: 'source-note', text: 'Nenhum modelo carregado.' }));
    }
    var library = Array.isArray(source.library) ? source.library : [];
    if (options.library && library.length > 0) {
      var summary = node('summary', { text: 'Instalados, disponíveis para carregar (' + library.length + ')' });
      var body = node('ul', { className: 'source-models' }, library.map(modelLine));
      var details = node('details', { className: 'source-library' }, [summary, body]);
      details.open = library.length <= 6;
      children.push(details);
    }
    if (source.note) {
      children.push(node('p', { className: 'source-note', text: source.note }));
    }
    if (source.metering === 'server-log') {
      children.push(node('p', { className: 'source-note', text: 'Requisições medidas no log do servidor: aparecem na tabela com tokens, cache e velocidade.' }));
    } else if (source.metering_note) {
      children.push(node('p', { className: 'source-note', text: source.metering_note }));
    }
    var usage = [];
    if (isNum(source.vram_dedicated_mib)) {
      usage.push('VRAM ' + gibFromMiB(source.vram_dedicated_mib) + ' GiB');
    }
    if (isNum(source.ram_bytes)) {
      usage.push('RAM ' + gib(source.ram_bytes) + ' GiB');
    }
    if (isNum(source.gpu_util)) {
      usage.push('GPU ' + int(source.gpu_util) + '%');
    }
    if (isNum(source.tokens_per_second)) {
      usage.push(rate(source.tokens_per_second) + ' tok/s');
    }
    if (source.started_at) {
      usage.push('processo iniciado ' + ago(source.started_at, Date.now()));
    }
    if (usage.length > 0) {
      children.push(node('p', { className: 'source-usage', text: usage.join(' · ') }));
    }
    if (options.stop && options.stop.available && source.stoppable) {
      var stopButton = node('button', {
        className: 'button source-stop',
        data: { variant: 'danger', source: source.id },
        title: options.stop.busy ? 'Já há uma operação em andamento.' :
          'Encerra ' + source.stop_target + ' e libera a memória do modelo. O Windows pede a sua confirmação.'
      }, [svgIcon('i-stop'), 'Encerrar processo do modelo']);
      stopButton.type = 'button';
      stopButton.disabled = !!options.stop.busy;
      stopButton.addEventListener('click', function () {
        requestAction('stop_source', '', source.id);
      });
      children.push(node('div', { className: 'source-actions' }, [stopButton]));
    }
    return node('div', { className: 'source', data: { active: generating ? 'true' : 'false' } }, children);
  }

  function renderSources(snap) {
    var sources = Array.isArray(snap.sources) ? snap.sources : [];
    var key = JSON.stringify(sources.map(function (source) {
      return [source.id, source.status, source.activity, source.activity_basis, isNum(source.tokens_per_second) ? Math.round(source.tokens_per_second) : null,
        isNum(source.vram_dedicated_mib) ? Math.round(source.vram_dedicated_mib / 64) : null, isNum(source.gpu_util) ? Math.round(source.gpu_util / 5) : null,
        isNum(source.ram_bytes) ? Math.round(source.ram_bytes / 67108864) : null, source.started_at ? Math.floor(Date.now() / 60000) : null,
        source.stoppable, source.note, source.metering, source.metering_note,
        (source.models || []).map(function (model) { return [model.id, model.context_loaded, model.quantization, model.slots]; })];
    }));
    var stop = stopOptions(snap);
    key += JSON.stringify(stop);
    var list = $('source-list');
    if (list.getAttribute('data-key') !== key) {
      list.setAttribute('data-key', key);
      list.replaceChildren.apply(list, sources.map(function (source) {
        return sourceCard(source, { stop: stop });
      }));
    }
    show('sources-empty', sources.length === 0);
    show(list, sources.length > 0);
    setText('sources-hint', sources.length > 0 ? sources.length + ' ' + plural(sources.length, 'fonte', 'fontes') + ' além do edge' : 'além do edge');
  }

  function renderProcesses(snap) {
    var hardware = snap.hardware || {};
    var processes = Array.isArray(hardware.gpu_processes) ? hardware.gpu_processes : [];
    var key = JSON.stringify(processes.map(function (process) {
      return [process.pid, process.name, process.tool, isNum(process.vram_dedicated_mib) ? Math.round(process.vram_dedicated_mib / 16) : null,
        isNum(process.gpu_util) ? Math.round(process.gpu_util) : null];
    }));
    var body = $('process-body');
    if (body.getAttribute('data-key') !== key) {
      body.setAttribute('data-key', key);
      body.replaceChildren.apply(body, processes.map(function (process) {
        var name = node('td', { className: 'process-name', title: process.name + ' · PID ' + process.pid }, [process.name]);
        if (process.tool) {
          name.appendChild(node('span', { className: 'badge', text: toolName(process.tool), tone: 'info' }));
        }
        var vram = process.vram_dedicated_mib >= 1024 ? gibFromMiB(process.vram_dedicated_mib) + ' GiB' : int(process.vram_dedicated_mib) + ' MiB';
        return node('tr', {}, [
          name,
          node('td', { className: 'n', text: vram }),
          node('td', { className: 'n dim', text: isNum(process.gpu_util) ? int(process.gpu_util) + '%' : DASH })
        ]);
      }));
    }
    show('processes-empty', processes.length === 0);
    setText('processes-hint', hardware.gpu_name || '');
  }

  var TOOL_NAMES = {
    'lm-studio': 'LM Studio',
    ollama: 'Ollama',
    'llama.cpp': 'llama.cpp',
    koboldcpp: 'KoboldCpp',
    jan: 'Jan',
    gpt4all: 'GPT4All'
  };

  function toolName(tool) {
    return TOOL_NAMES[tool] || tool;
  }

  // ------------------------------------------------------------------ models

  // Each tag is a manifest capability the edge enforces before the inference
  // slot: internal/edge/capabilities.go on the OpenAI routes; on /v1/messages,
  // internal/edge/anthropic.go checks chat_completions, streaming and
  // function_calling, and that route never forwards structured-output or
  // thinking fields for any model. A struck-through tag is a feature the edge
  // refuses for this model with a 400 until a qualification of this exact file
  // declares it (docs/MODEL_PROMOTION.md). The OpenAI routes answer
  // unsupported_feature; /v1/messages answers invalid_request_error and omits
  // optional tools instead of refusing them. Responses is its own contract and
  // does not imply tools, although Codex also requires streaming and tools.
  var CAPABILITIES = [
    ['responses', 'Responses', 'API Responses (usada pelo Codex, que também exige Streaming e Ferramentas), qualificada por contrato próprio (saída nativa da API Responses e SSE, cancelamento, recuperação) e independente de ferramentas. Riscado (não qualificado): o edge recusa com 400 unsupported_feature as requisições em /v1/responses.'],
    ['chat_completions', 'Chat', 'API Chat Completions (OpenAI); /v1/messages (Anthropic) também depende dela. Riscado (não qualificado): o edge recusa requisições nas duas rotas com 400.'],
    ['streaming', 'Streaming', 'Respostas em streaming. Riscado (não qualificado): o edge recusa com 400 qualquer requisição com "stream": true.'],
    ['function_calling', 'Ferramentas', 'Chamada de ferramentas qualificada para este arquivo. Riscado (não qualificado): o edge recusa com 400 requisições com ferramentas ou histórico de ferramentas; em /v1/messages, ferramentas opcionais são omitidas em vez de recusadas.'],
    ['structured_output', 'JSON estruturado', 'Saída JSON estruturada qualificada para este arquivo. Riscado (não qualificado): o edge recusa com 400 unsupported_feature requisições com response_format ou text.format diferente de "text".'],
    ['reasoning', 'Raciocínio', 'O modelo emite raciocínio antes da resposta, observado nas campanhas de qualificação. Riscado (não qualificado): o edge recusa com 400 unsupported_feature requisições com reasoning ou reasoning_effort diferente de "none".']
  ];

  function fact(term, value) {
    return [node('dt', { text: term }), node('dd', { text: value })];
  }

  function renderModels(snap) {
    var status = snap.edge.status;
    var notice = $('models-notice');
    if (!status) {
      setText('models-summary', DASH);
      setText(notice, edgeDown(snap) ? 'O edge está offline e ainda não enviou nenhum status.' : 'Aguardando o status do edge…');
      show(notice, true);
      $('model-grid').replaceChildren();
      app.modelsKey = '';
      return;
    }
    var stale = isNum(snap.edge.status_age_ms) && snap.edge.status_age_ms > STALE_STATUS_MS;
    setText(notice, stale ? 'Mostrando o último status conhecido (' + duration(snap.edge.status_age_ms) + ' atrás).' : '');
    setTone(notice, 'warn');
    show(notice, stale);

    var models = Array.isArray(status.models) ? status.models : [];
    var publicID = status.capacity ? status.capacity.model : '';
    var active = status.active_model || '';
    setText('models-summary', models.length + ' ' + plural(models.length, 'modelo', 'modelos') + ' · público: ' +
      displayName(status, publicID) + ' · ' + (active ? 'carregado: ' + displayName(status, active) : 'nenhum carregado'));

    var key = JSON.stringify([models, status.model_statuses, publicID, active, controlShown(snap)]);
    if (key === app.modelsKey) {
      return;
    }
    app.modelsKey = key;

    var cards = models.map(function (model) {
      var entry = modelStatus(status, model.id) || {};
      var profile = entry.profile || {};
      var capacity = entry.capacity || {};
      var runtime = entry.runtime || {};
      var checkpoints = entry.checkpoints || {};

      var badges = [];
      if (model.id === publicID) {
        badges.push(node('span', { className: 'badge', text: 'Público', tone: 'accent' }));
      }
      if (entry.process_state === 'starting') {
        badges.push(node('span', { className: 'badge', text: 'Carregando', tone: 'info' }));
      } else if (entry.active) {
        badges.push(node('span', { className: 'badge', text: 'Carregado', tone: 'accent' }));
      }
      var artifact = entry.artifact || {};
      var fileMissing = artifact.present === false;
      badges.push(entry.available ?
        node('span', { className: 'badge', text: 'Admissível', tone: 'muted' }) :
        node('span', { className: 'badge', text: fileMissing ? 'Indisponível' : 'Sem capacidade', tone: 'danger' }));
      if (fileMissing) {
        badges.push(node('span', { className: 'badge', text: 'Arquivo ausente', tone: 'danger', title: 'O arquivo dos pesos não está mais no disco.' }));
      } else if (artifact.size_matches === false) {
        badges.push(node('span', { className: 'badge', text: 'Tamanho diferente', tone: 'warn', title: 'O arquivo dos pesos não tem o tamanho registrado na implantação.' }));
      }

      var ceiling = isNum(profile.n_predict) ? profile.n_predict : profile.max_output_tokens;
      var facts = [];
      facts = facts.concat(fact('Contexto', tokens(entry.context_tokens)));
      facts = facts.concat(fact('Saída máx.', tokens(ceiling)));
      facts = facts.concat(fact('Cache K/V', profile.cache_type_k ? profile.cache_type_k + ' / ' + profile.cache_type_v : DASH));
      if (profile.weights) {
        facts = facts.concat(fact('Pesos', profile.weights));
      }
      if (profile.moe_offload) {
        facts = facts.concat(fact('MoE na CPU', profile.moe_offload.cpu_all ? 'todos os especialistas' :
          isNum(profile.moe_offload.cpu_layers) ? profile.moe_offload.cpu_layers + ' camadas' : DASH));
      }
      if (isNum(profile.reasoning_budget)) {
        facts = facts.concat(fact('Limite de raciocínio', profile.reasoning_budget < 0 ? 'sem limite' : tokens(profile.reasoning_budget) + ' tokens'));
      }
      if (isNum(profile.compact_threshold_tokens)) {
        facts = facts.concat(fact('Compactação', 'em ' + tokens(profile.compact_threshold_tokens)));
      }
      facts = facts.concat(fact('Runtime', [runtime.engine, runtime.variant].filter(Boolean).join(' ') + (runtime.backend ? ' · ' + runtime.backend : '') || DASH));
      facts = facts.concat(fact('Checkpoints', checkpoints.configured ?
        (isNum(checkpoints.ctx_checkpoints) ? checkpoints.ctx_checkpoints + ' por sessão' : 'configurados') + (checkpoints.runtime_capable ? '' : ' (runtime sem suporte)') :
        'não configurados'));
      var requirements = [];
      if (isNum(capacity.required_commit_gib)) {
        requirements.push('commit ' + one(capacity.required_commit_gib) + ' GiB');
      }
      if (isNum(capacity.required_physical_gib)) {
        requirements.push('RAM ' + one(capacity.required_physical_gib) + ' GiB');
      }
      if (isNum(capacity.required_vram_gib)) {
        requirements.push('VRAM ' + one(capacity.required_vram_gib) + ' GiB');
      }
      facts = facts.concat(fact('Requer', requirements.length ? requirements.join(' · ') : DASH));

      var capabilities = CAPABILITIES.map(function (pair) {
        var on = !!(model.capabilities && model.capabilities[pair[0]]);
        return node('span', { className: 'chip', text: pair[1], tone: on ? 'info' : 'muted', data: { on: on }, title: pair[2] });
      });

      var actions = null;
      if (controlShown(snap)) {
        var loaded = model.id === active;
        var button = node('button', {
          className: 'button',
          data: { variant: loaded ? 'danger' : 'primary', model: model.id, action: loaded ? 'unload' : 'switch' }
        }, [svgIcon(loaded ? 'i-stop' : 'i-play'), loaded ? 'Descarregar' : 'Carregar']);
        button.type = 'button';
        button.addEventListener('click', function () {
          requestAction(loaded ? 'unload' : 'switch', model.id);
        });
        actions = node('div', { className: 'model-actions' }, [button]);
      }

      return node('article', { className: 'card model-card', data: { active: !!entry.active, missing: fileMissing } }, [
        node('div', { className: 'model-title' }, [
          node('h2', { text: model.display_name || model.id }),
          node('code', { text: model.id })
        ]),
        node('div', { className: 'model-badges' }, badges),
        node('dl', { className: 'facts' }, facts),
        node('div', { className: 'capabilities' }, capabilities),
        entry.available ? null : node('p', { className: 'model-reason', text: reasonText(entry.reason) }),
        actions
      ]);
    });
    var grid = $('model-grid');
    grid.replaceChildren.apply(grid, cards);
    renderControls(snap);
  }

  // renderOtherModels lists what the other tools on the machine hold, from each
  // tool's own API: the loaded models, and for the tools that say so, the ones
  // they have installed. The page never loads any of them.
  function renderOtherModels(snap) {
    var sources = (Array.isArray(snap.sources) ? snap.sources : []).filter(function (source) {
      return source.status !== 'ok' || (source.models || []).length > 0 || (source.library || []).length > 0;
    });
    var key = JSON.stringify(sources.map(function (source) {
      return [source.id, source.status, source.activity, source.stoppable, source.note, source.metering, source.metering_note,
        (source.models || []).map(function (m) { return [m.id, m.context_loaded, m.slots]; }),
        (source.library || []).map(function (m) { return m.id; })];
    }));
    var stop = stopOptions(snap);
    key += JSON.stringify(stop);
    var list = $('other-models');
    if (list.getAttribute('data-key') !== key) {
      list.setAttribute('data-key', key);
      list.replaceChildren.apply(list, sources.map(function (source) {
        return sourceCard(source, { library: true, stop: stop });
      }));
    }
    show(list, sources.length > 0);
    show('other-models-empty', sources.length === 0);
  }

  // ------------------------------------------------------------------ controls

  // The page can ask for two things: switch to a model, or unload the loaded
  // one. The monitor refuses anything else, and nothing happens until the
  // operator confirms in a Windows dialog this page cannot see or answer.

  var ACTION_ERRORS = {
    inference_busy: 'Há uma requisição em andamento ou na fila; tente quando ela terminar.',
    insufficient_capacity: 'Sem memória para esse modelo agora.',
    model_conflict: 'Outro modelo está carregado.',
    model_not_found: 'O edge não conhece esse modelo.',
    unknown_model: 'O edge não conhece esse modelo.',
    upstream_unavailable: 'O roteador não conseguiu concluir a operação.',
    admin_pipe_not_listening: 'O edge não está aceitando comandos administrativos.',
    admin_pipe_unsupported: 'Comandos administrativos só existem no Windows.',
    admin_timeout: 'A operação demorou demais e foi abandonada.',
    admin_transport_failed: 'Falha ao falar com o edge pelo pipe administrativo.',
    operation_in_progress: 'Já há uma operação em andamento.',
    model_already_loaded: 'Esse modelo já está carregado.',
    model_not_loaded: 'Esse modelo não está carregado.',
    status_unavailable: 'O monitor ainda não recebeu o status do edge.',
    controls_unavailable: 'Os controles de modelo estão indisponíveis.',
    cross_origin: 'Pedido recusado: só esta página pode controlar modelos.',
    peer_not_allowed: 'Pedido recusado: só o navegador do usuário que roda o servidor pode controlar modelos.',
    monitor_unreachable: 'O monitor não respondeu ao pedido.',
    action_timeout: 'O monitor não confirmou o pedido a tempo. Confira o estado antes de tentar novamente.',
    unknown_source: 'Essa ferramenta não está mais na lista; aguarde a próxima leitura.',
    invalid_source: 'Pedido inválido.',
    nothing_to_stop: 'Não há um processo de modelo para encerrar nessa ferramenta.',
    process_gone: 'O processo já tinha terminado.',
    process_changed: 'O processo mudou desde que a página o mostrou; nada foi encerrado.',
    not_permitted: 'O Windows não permite encerrar esse processo.',
    terminate_failed: 'O Windows recusou encerrar o processo.',
    terminate_timeout: 'O processo não terminou a tempo.'
  };

  var CONTROL_REASONS = {
    unsupported: 'Os controles de modelo só funcionam no Windows.',
    misconfigured: 'O pipe administrativo do monitor está mal configurado.'
  };

  var RECENT_RESULT_MS = 15000;

  function actionError(code) {
    return ACTION_ERRORS[code] || 'Falha: ' + code + '.';
  }

  function controlShown(snap) {
    return !!(snap && snap.control && snap.control.reason !== 'disabled');
  }

  function svgIcon(id) {
    var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'icon');
    svg.setAttribute('aria-hidden', 'true');
    var use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', '#' + id);
    svg.appendChild(use);
    return svg;
  }

  function operationNumber(operation) {
    var match = operation && /^op-(\d+)$/.exec(operation.id || '');
    return match ? parseInt(match[1], 10) : 0;
  }

  // The POST answers at once; the snapshot catches up within a second. Until
  // it does, the operation the POST returned is the freshest one known.
  function currentOperation(snap) {
    var reported = snap && snap.control ? snap.control.operation : null;
    var local = app.localOperation;
    if (local && (!reported || operationNumber(local) > operationNumber(reported))) {
      return local;
    }
    return reported || null;
  }

  function operationPending(operation) {
    return !!operation && (operation.state === 'awaiting_confirmation' || operation.state === 'running');
  }

  // blocker names the first reason no action may start now, or '' if one may.
  function blocker(snap, operation) {
    var control = snap.control || {};
    var status = snap.edge.status;
    var activity = snap.activity || {};
    if (!control.available) {
      return CONTROL_REASONS[control.reason] || 'Os controles de modelo estão indisponíveis.';
    }
    if (edgeDown(snap) || !status) {
      return 'O edge está indisponível.';
    }
    if (app.actionInFlight || operationPending(operation)) {
      return 'Já há uma operação em andamento.';
    }
    if (app.actionRefreshAfter > 0) {
      return 'Aguardando uma atualização do estado da operação.';
    }
    if (activity.live || activity.queued > 0 || (status.gate && (status.gate.active > 0 || status.gate.queued > 0))) {
      return 'Aguarde a requisição em andamento terminar.';
    }
    if (status.maintenance && status.maintenance.draining) {
      return 'O edge está em manutenção.';
    }
    return '';
  }

  function selectable(status, id) {
    var entry = modelStatus(status, id);
    return !!entry && (entry.available || status.active_model === id);
  }

  function renderModelSelect(status) {
    var select = $('model-select');
    var models = Array.isArray(status.models) ? status.models : [];
    var active = status.active_model || '';
    var key = JSON.stringify(models.map(function (model) {
      var entry = modelStatus(status, model.id) || {};
      return [model.id, model.display_name, !!entry.available, model.id === active];
    }));
    if (select.getAttribute('data-key') !== key) {
      select.setAttribute('data-key', key);
      var options = models.map(function (model) {
        var refused = modelStatus(status, model.id) || {};
        var suffix = model.id === active ? ' · carregado' : selectable(status, model.id) ? '' :
          refused.reason === 'artifact_missing' ? ' · arquivo ausente' : ' · sem capacidade';
        var option = node('option', { text: (model.display_name || model.id) + suffix });
        option.value = model.id;
        option.disabled = !selectable(status, model.id);
        return option;
      });
      select.replaceChildren.apply(select, options);
    }
    // The operator's choice survives every refresh; before there is one, the
    // list opens on something useful to load - not the model already loaded.
    var wanted = app.selectedModel;
    if (!wanted || !modelStatus(status, wanted)) {
      var publicID = status.capacity ? status.capacity.model : '';
      wanted = publicID && publicID !== active ? publicID : '';
      if (!wanted) {
        for (var index = 0; index < models.length; index++) {
          if (models[index].id !== active && selectable(status, models[index].id)) {
            wanted = models[index].id;
            break;
          }
        }
      }
      wanted = wanted || active;
    }
    if (select.value !== wanted) {
      select.value = wanted;
    }
    return select.value;
  }

  function renderControlStatus(snap, operation) {
    var line = $('control-status');
    var status = snap.edge.status;
    var text = '';
    var tone = null;
    var busy = false;
    var now = Date.now();
    if (app.localError && now - app.localError.at < RECENT_RESULT_MS && !operationPending(operation)) {
      text = actionError(app.localError.code);
      tone = 'danger';
    } else if (operation) {
      var isStop = operation.action === 'stop_source';
      var name = isStop ? operation.target : displayName(status, operation.model);
      var finished = parseTime(operation.finished_at);
      var generated = parseTime(snap.generated_at);
      var recent = !finished || !generated || generated.getTime() - finished.getTime() < RECENT_RESULT_MS;
      switch (operation.state) {
        case 'awaiting_confirmation':
          text = 'Confirme na janela do Windows que abriu. Sem resposta em 45 s, nada é feito.';
          tone = 'info';
          busy = true;
          break;
        case 'running':
          text = isStop ? 'Encerrando ' + name + '…' : (operation.action === 'unload' ? 'Descarregando ' : 'Carregando ') + name + '…';
          tone = 'info';
          busy = true;
          break;
        case 'succeeded':
          if (recent) {
            text = isStop ? name + ' encerrado: o modelo foi descarregado.' : name + (operation.action === 'unload' ? ' descarregado.' : ' carregado.');
            tone = 'accent';
          }
          break;
        case 'failed':
          if (recent) {
            text = actionError(operation.error);
            tone = 'danger';
          }
          break;
        case 'declined':
          if (recent) {
            text = 'Operação cancelada na confirmação.';
            tone = 'muted';
          }
          break;
        case 'expired':
          if (recent) {
            text = 'A confirmação expirou; nada foi feito.';
            tone = 'muted';
          }
          break;
      }
    } else if (snap.control && !snap.control.available) {
      text = CONTROL_REASONS[snap.control.reason] || '';
      tone = 'muted';
    }
    setText(line, text);
    setTone(line, tone);
    line.setAttribute('data-busy', busy ? 'true' : 'false');
    show(line, text !== '');

    // A stop is started from a source card far below this line, so the same
    // message is shown next to the cards.
    var stopContext = (operation && operation.action === 'stop_source') ||
      !!(app.localError && app.localError.stop && now - app.localError.at < RECENT_RESULT_MS);
    ['sources-status', 'other-status'].forEach(function (id) {
      var element = $(id);
      var shown = stopContext && text !== '';
      setText(element, shown ? text : '');
      setTone(element, shown ? tone : null);
      element.setAttribute('data-busy', shown && busy ? 'true' : 'false');
      show(element, shown);
    });
  }

  function renderControls(snap) {
    if (!snap) {
      return;
    }
    var shown = controlShown(snap);
    show('control', shown);
    if (!shown) {
      return;
    }
    var status = snap.edge.status;
    var operation = currentOperation(snap);
    var blocked = blocker(snap, operation);
    var select = $('model-select');
    var load = $('load-button');
    var unload = $('unload-button');

    var selected = status ? renderModelSelect(status) : '';
    var active = status ? status.active_model || '' : '';
    select.disabled = !status || app.actionInFlight || app.actionRefreshAfter > 0 || operationPending(operation);

    var loadReason = blocked ||
      (!selected ? 'Escolha um modelo.' : '') ||
      (selected === active ? 'Este modelo já está carregado.' : '') ||
      (status && !selectable(status, selected) ? whyNot(status, selected) : '');
    load.disabled = loadReason !== '';
    load.title = loadReason || (active ? 'Troca ' + displayName(status, active) + ' pelo modelo escolhido' : 'Carrega o modelo escolhido');

    show(unload, !!active);
    unload.disabled = blocked !== '';
    unload.title = blocked || 'Encerra o processo de ' + displayName(status, active) + ' e libera a VRAM';

    Array.prototype.forEach.call(document.querySelectorAll('.model-actions .button'), function (button) {
      var model = button.getAttribute('data-model');
      var reason = blocked ||
        (button.getAttribute('data-action') === 'switch' && status && !selectable(status, model) ? whyNot(status, model) : '');
      button.disabled = reason !== '';
      button.title = reason;
    });

    renderControlStatus(snap, operation);
  }

  function requestAction(action, model, source) {
    if (app.actionInFlight || app.actionRefreshAfter > 0 || (!model && !source)) {
      return;
    }
    app.actionInFlight = true;
    app.localError = null;
    renderControls(app.snapshot);
    var instance = app.monitorStartedAt;
    var controller = typeof AbortController === 'function' ? new AbortController() : null;
    var timedOut = false;
    var timeout;
    // Bound receipt of the operation, including its response body. Approval
    // and model loading happen separately and are observed through snapshots.
    var expiry = new Promise(function (resolve, reject) {
      timeout = window.setTimeout(function () {
        timedOut = true;
        if (controller) {
          controller.abort();
        }
        reject(new Error('action_timeout'));
      }, FETCH_TIMEOUT_MS);
    });
    var request = fetch('/api/actions', {
      method: 'POST',
      cache: 'no-store',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'X-CIA-Monitor-Action': '1', Accept: 'application/json' },
      body: JSON.stringify(source ? { action: action, source: source } : { action: action, model: model }),
      signal: controller ? controller.signal : undefined
    }).then(function (response) {
      return response.json().catch(function () { return {}; }).then(function (body) {
        return { ok: response.ok, status: response.status, body: body };
      });
    });
    // Only the winning reply may change state: a response arriving after the
    // deadline or from a previous monitor instance cannot revive its operation.
    Promise.race([request, expiry]).then(function (reply) {
      if (app.monitorStartedAt !== instance) {
        return;
      }
      if (reply.ok && reply.body.operation) {
        app.localOperation = reply.body.operation;
      } else {
        app.localError = { code: reply.body.error || 'http_' + reply.status, at: Date.now(), stop: !!source };
      }
    }).catch(function () {
      if (app.monitorStartedAt !== instance) {
        return;
      }
      app.localError = { code: timedOut ? 'action_timeout' : 'monitor_unreachable', at: Date.now(), stop: !!source };
      // Acceptance is uncertain. Wait for a snapshot taken after this failure
      // before allowing another action; never retry the mutation automatically.
      app.actionRefreshAfter = Date.now();
    }).then(function () {
      window.clearTimeout(timeout);
      app.actionInFlight = false;
      renderControls(app.snapshot);
      poll();
    });
  }

  $('model-select').addEventListener('change', function (event) {
    app.selectedModel = event.target.value;
    renderControls(app.snapshot);
  });

  $('load-button').addEventListener('click', function () {
    requestAction('switch', $('model-select').value);
  });

  $('unload-button').addEventListener('click', function () {
    var status = app.snapshot ? app.snapshot.edge.status : null;
    if (status && status.active_model) {
      requestAction('unload', status.active_model);
    }
  });

  // ------------------------------------------------------------------ connection

  function renderConnection(snap) {
    var monitor = snap.monitor || {};
    var status = snap.edge.status;
    var dataURL = monitor.data_url || '';
    setText('endpoint-openai', dataURL ? dataURL + '/v1' : DASH);
    setText('endpoint-anthropic', dataURL || DASH);
    setText('endpoint-control', monitor.control_url || DASH);
    setText('monitor-listen', window.location.host);
    setText('public-model', status && status.capacity ? displayName(status, status.capacity.model) : DASH);

    var key = JSON.stringify([status ? [status.version, status.deployment, status.runtimes, status.gate, status.upstream] : null,
      monitor.version, monitor.environment, status ? Math.floor(status.uptime_seconds / 60) : null]);
    if (key === app.connectionKey) {
      return;
    }
    app.connectionKey = key;

    var deployment = status && status.deployment ? status.deployment : null;
    var gate = status ? status.gate : null;
    var facts = [];
    facts = facts.concat(fact('Ambiente', deployment ? deployment.environment : monitor.environment || DASH));
    facts = facts.concat(fact('Release', deployment ? deployment.release : 'fora do processo de release'));
    facts = facts.concat(fact('Versão do edge', status ? status.version || DASH : DASH));
    if (deployment) {
      facts = facts.concat(fact('Commit', shortCommit(deployment.commit) + (deployment.source_dirty ? ' · árvore com alterações' : '')));
      facts = facts.concat(fact('Instalado em', dateTime(deployment.created_utc)));
    }
    facts = facts.concat(fact('Edge no ar há', status ? uptime(status.uptime_seconds) : DASH));
    facts = facts.concat(fact('Roteador', status ? (status.upstream && status.upstream.reachable ? 'acessível' : 'inacessível') : DASH));
    if (gate) {
      facts = facts.concat(fact('Admissão', gate.max_active + ' em execução · ' + gate.max_queue + ' na fila · espera ' + gate.wait_timeout_seconds + ' s'));
      facts = facts.concat(fact('Recusas', int(gate.rejected_total) + ' fila cheia · ' + int(gate.timed_out_total) + ' por tempo'));
    }
    facts = facts.concat(fact('Monitor', (monitor.version || DASH) + ' · desde ' + clock(monitor.started_at)));
    var list = $('deployment-facts');
    list.replaceChildren.apply(list, facts);

    var runtimes = status && Array.isArray(status.runtimes) ? status.runtimes : [];
    var items = runtimes.map(function (runtime) {
      var detail = [[runtime.engine, runtime.variant].filter(Boolean).join(' '), runtime.backend, shortCommit(runtime.commit)]
        .filter(function (part) { return part && part !== DASH; }).join(' · ');
      return node('li', {}, [
        node('strong', { text: runtime.id }),
        node('span', { text: detail || DASH }),
        node('span', { text: runtime.checkpoint_capable ? 'restaura checkpoints de contexto' : 'sem restauração de checkpoints' })
      ]);
    });
    if (items.length === 0) {
      items.push(node('li', {}, [node('span', { text: status ? 'O edge não informou runtimes.' : 'Aguardando o status do edge…' })]));
    }
    var runtimeList = $('runtime-list');
    runtimeList.replaceChildren.apply(runtimeList, items);
  }

  // ------------------------------------------------------------------ frame

  function render(snap) {
    var instance = snap.monitor.started_at || '';
    if (instance && app.monitorStartedAt && instance !== app.monitorStartedAt) {
      // Operation IDs and inference history belong to one process instance.
      // A restarted monitor begins counting op-N again from one.
      app.localOperation = null;
      app.localError = null;
      app.actionRefreshAfter = 0;
      app.lastInference = null;
      app.requestsKey = '';
      app.modelsKey = '';
      app.connectionKey = '';
    }
    if (instance) {
      app.monitorStartedAt = instance;
    }
    var sampledAt = parseTime(snap.generated_at);
    if (app.actionRefreshAfter > 0 && sampledAt && sampledAt.getTime() > app.actionRefreshAfter) {
      app.actionRefreshAfter = 0;
    }
    document.body.setAttribute('data-monitor', 'up');
    var inference = snap.edge.inference;
    var stale = false;
    if (inference) {
      app.lastInference = inference;
    } else if (edgeDown(snap) && app.lastInference) {
      // Keep the last list on screen while the edge restarts, dimmed.
      inference = app.lastInference;
      stale = true;
    }

    var env = $('env-chip');
    setText(env, snap.monitor.environment || '');
    show(env, !!snap.monitor.environment);

    renderPill(snap);
    renderState(snap);
    renderControls(snap);
    renderMetrics(snap);
    renderContext(snap, inference);
    renderRequests(snap, inference, stale);
    renderSources(snap);
    renderProcesses(snap);
    if (currentTab === 'modelos') {
      renderModels(snap);
      renderOtherModels(snap);
    }
    if (currentTab === 'conexao') {
      renderConnection(snap);
    }
    setText('footer-version', 'IA Local · monitor ' + (snap.monitor.version || '') + ' · amostra a cada ' + nf0.format((snap.monitor.interval_ms || POLL_MS) / 1000) + ' s');
    setText('footer-updated', 'atualizado às ' + clock(snap.generated_at));
  }

  function renderMonitorDown() {
    renderPill(app.snapshot);
    // What is on screen stopped updating; it stays readable but visibly old.
    document.body.setAttribute('data-monitor', 'down');
    document.title = 'Monitor sem resposta · IA Local';
    var since = app.lastOk ? ' (última resposta ' + ago(new Date(app.lastOk).toISOString(), Date.now()) + ')' : '';
    setText('footer-updated', 'monitor sem resposta' + since);
    if (!app.snapshot) {
      setText('state-title', 'Monitor sem resposta');
      setText('state-sub', 'O processo cia-monitor não respondeu. Verifique se ele ainda está rodando.');
    }
  }

  // ------------------------------------------------------------------ polling

  function schedule(delay) {
    window.clearTimeout(app.timer);
    if (!document.hidden) {
      app.timer = window.setTimeout(poll, delay);
    }
  }

  function poll() {
    if (app.inFlight) {
      return;
    }
    app.inFlight = true;
    var controller = typeof AbortController === 'function' ? new AbortController() : null;
    var timeout = controller ? window.setTimeout(function () { controller.abort(); }, FETCH_TIMEOUT_MS) : null;
    var startedAt = Date.now();
    fetch('/api/snapshot', {
      cache: 'no-store',
      credentials: 'omit',
      headers: { Accept: 'application/json' },
      signal: controller ? controller.signal : undefined
    }).then(function (response) {
      if (response.status === 503) {
        return null;
      }
      if (!response.ok) {
        throw new Error('HTTP ' + response.status);
      }
      return response.json();
    }).then(function (snap) {
      app.monitorDown = false;
      app.failures = 0;
      if (snap) {
        app.snapshot = snap;
        app.lastOk = Date.now();
        render(snap);
      }
    }).catch(function () {
      app.monitorDown = true;
      app.failures += 1;
      renderMonitorDown();
    }).then(function () {
      if (timeout) {
        window.clearTimeout(timeout);
      }
      app.inFlight = false;
      if (app.monitorDown) {
        // Back off to one try every five seconds while nothing answers.
        schedule(Math.min(MAX_BACKOFF_MS, POLL_MS * Math.pow(2, Math.min(app.failures - 1, 3))));
        return;
      }
      // Keep a steady one-second cadence however long the request took.
      schedule(Math.max(200, POLL_MS - (Date.now() - startedAt)));
    });
  }

  document.addEventListener('visibilitychange', function () {
    if (document.hidden) {
      window.clearTimeout(app.timer);
    } else {
      poll();
    }
  });

  // ------------------------------------------------------------------ tabs

  function selectTab(name, focus) {
    if (TABS.indexOf(name) < 0) {
      name = 'monitor';
    }
    currentTab = name;
    TABS.forEach(function (tab) {
      var button = $('tab-' + tab);
      var selected = tab === name;
      button.setAttribute('aria-selected', selected ? 'true' : 'false');
      button.tabIndex = selected ? 0 : -1;
      show('panel-' + tab, selected);
      if (selected && focus) {
        button.focus();
      }
    });
    storageSet(TAB_KEY, name);
    if (window.location.hash !== '#' + name) {
      window.history.replaceState(null, '', '#' + name);
    }
    if (app.snapshot) {
      if (name === 'modelos') {
        app.modelsKey = '';
        renderModels(app.snapshot);
      } else if (name === 'conexao') {
        app.connectionKey = '';
        renderConnection(app.snapshot);
      }
    }
  }

  Array.prototype.forEach.call(document.querySelectorAll('.tab'), function (button) {
    button.addEventListener('click', function () {
      selectTab(button.getAttribute('data-tab'), false);
    });
    button.addEventListener('keydown', function (event) {
      var index = TABS.indexOf(currentTab);
      var next = null;
      if (event.key === 'ArrowRight') {
        next = TABS[(index + 1) % TABS.length];
      } else if (event.key === 'ArrowLeft') {
        next = TABS[(index - 1 + TABS.length) % TABS.length];
      } else if (event.key === 'Home') {
        next = TABS[0];
      } else if (event.key === 'End') {
        next = TABS[TABS.length - 1];
      }
      if (next) {
        event.preventDefault();
        selectTab(next, true);
      }
    });
  });

  window.addEventListener('hashchange', function () {
    selectTab(window.location.hash.slice(1), false);
  });

  // ------------------------------------------------------------------ theme

  function effectiveTheme() {
    var chosen = document.documentElement.getAttribute('data-theme');
    if (chosen === 'light' || chosen === 'dark') {
      return chosen;
    }
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }

  $('theme-toggle').addEventListener('click', function () {
    var next = effectiveTheme() === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    storageSet(THEME_KEY, next);
  });

  // ------------------------------------------------------------------ copy

  Array.prototype.forEach.call(document.querySelectorAll('.copy'), function (button) {
    button.addEventListener('click', function () {
      var source = $(button.getAttribute('data-copy'));
      var text = source ? source.textContent : '';
      if (!text || text === DASH) {
        return;
      }
      var done = function () {
        button.setAttribute('data-copied', 'true');
        window.setTimeout(function () { button.removeAttribute('data-copied'); }, 1500);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function () {});
      }
    });
  });

  // ------------------------------------------------------------------ start

  var initial = window.location.hash.slice(1) || storageGet(TAB_KEY) || 'monitor';
  selectTab(initial, false);
  poll();
})();
