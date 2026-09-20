// Console front end. No framework and no CDN on purpose: the workbench has to come up
// on a laptop with no internet, and the point of the page is the numbers, not the tooling.

const state = {
  catalog: null,
  services: [],
  service: null,
  values: {},
  errors: {},
  faults: [],
  // Fault deadlines come from the service's clock; this keeps the countdown honest if
  // the browser's clock disagrees.
  clockOffset: 0,
  cards: {},
  seriesMinutes: 30,
};

const $ = (id) => document.getElementById(id);

// ---------------------------------------------------------------- api helpers

async function api(path, options = {}) {
  const res = await fetch(path, options);
  const text = await res.text();
  let body = null;
  if (text) {
    try { body = JSON.parse(text); } catch { body = { error: text }; }
  }
  if (!res.ok) throw new Error((body && body.error) || `${res.status} ${res.statusText}`);
  return body;
}

const withService = (path) => `${path}${path.includes('?') ? '&' : '?'}service=${encodeURIComponent(state.service)}`;
const withJob = (path) => `${path}${path.includes('?') ? '&' : '?'}job=${encodeURIComponent(state.service)}`;

// ---------------------------------------------------------------- formatting

function formatValue(v, unit) {
  if (v === null || v === undefined) return { text: '—', unit: '' };
  switch (unit) {
    case 'ratio': return { text: (v * 100).toFixed(2), unit: '%' };
    case 'rps': return { text: v.toFixed(1), unit: 'req/s' };
    case 'rate': return { text: v.toFixed(2), unit: '× budget' };
    case 'seconds': return v < 1
      ? { text: (v * 1000).toFixed(0), unit: 'ms' }
      : { text: v.toFixed(2), unit: 's' };
    case 'count': return { text: Math.round(v).toString(), unit: '' };
    default: return { text: v.toFixed(2), unit: '' };
  }
}

// statusOf turns a number into a colour. The thresholds encode the SLO: a burn rate of 6
// is the SRE Workbook's second paging threshold, and a budget below a quarter means the
// window is in trouble even if nothing is failing right now.
function statusOf(sig, v) {
  if (v === null || v === undefined) return 'none';
  if (sig.key === 'availability_budget' || sig.key === 'latency_budget') {
    if (v <= 0) return 'bad';
    return v < 0.25 ? 'warn' : 'good';
  }
  if (sig.invert) {
    if (v >= 6) return 'bad';
    return v >= 1 ? 'warn' : 'good';
  }
  if (sig.target) {
    if (v >= sig.target) return 'good';
    return v >= sig.target - (1 - sig.target) ? 'warn' : 'bad';
  }
  return 'plain';
}

const STATUS_COLOR = {
  good: 'var(--good)', warn: 'var(--warn)', bad: 'var(--bad)',
  plain: 'var(--accent)', none: 'var(--text-faint)',
};

function humanSeconds(s) {
  if (s <= 0) return '0s';
  if (s < 60) return `${Math.ceil(s)}s`;
  const m = Math.floor(s / 60);
  const rest = Math.ceil(s % 60);
  return rest ? `${m}m ${rest}s` : `${m}m`;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

// ---------------------------------------------------------------- sparkline

// A hand-rolled sparkline. Gaps in the data stay gaps: a missing sample and a zero mean
// very different things when you are reading an SLI.
function sparkline(points, { status = 'plain', target = null, width = 300, height = 46 }) {
  const usable = points.filter((p) => p[0] !== null && p[1] !== null);
  if (usable.length < 2) return '';

  const values = usable.map((p) => p[1]);
  let lo = Math.min(...values);
  let hi = Math.max(...values);
  if (target !== null && target !== undefined) {
    lo = Math.min(lo, target);
    hi = Math.max(hi, target);
  }
  if (hi - lo < 1e-9) { hi = lo + Math.max(Math.abs(lo) * 0.05, 1e-6); }
  const pad = (hi - lo) * 0.15;
  lo -= pad; hi += pad;

  const t0 = usable[0][0];
  const t1 = usable[usable.length - 1][0];
  const x = (t) => (t1 === t0 ? width : ((t - t0) / (t1 - t0)) * width);
  const y = (v) => height - ((v - lo) / (hi - lo)) * height;

  let path = '';
  let open = false;
  for (const [t, v] of points) {
    if (t === null || v === null) { open = false; continue; }
    path += `${open ? 'L' : 'M'}${x(t).toFixed(1)} ${y(v).toFixed(1)}`;
    open = true;
  }

  const color = STATUS_COLOR[status] || STATUS_COLOR.plain;
  const gradientId = `g${Math.random().toString(36).slice(2, 8)}`;
  const areaPath = `${path}L${x(t1).toFixed(1)} ${height}L${x(t0).toFixed(1)} ${height}Z`;

  const targetLine = (target !== null && target !== undefined)
    ? `<line x1="0" x2="${width}" y1="${y(target).toFixed(1)}" y2="${y(target).toFixed(1)}"
         stroke="var(--text-faint)" stroke-width="1" stroke-dasharray="3 3" vector-effect="non-scaling-stroke"/>`
    : '';

  return `<svg class="spark" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" role="img">
    <defs><linearGradient id="${gradientId}" x1="0" x2="0" y1="0" y2="1">
      <stop offset="0%" stop-color="${color}" stop-opacity="0.22"/>
      <stop offset="100%" stop-color="${color}" stop-opacity="0"/>
    </linearGradient></defs>
    <path d="${areaPath}" fill="url(#${gradientId})"/>
    ${targetLine}
    <path d="${path}" fill="none" stroke="${color}" stroke-width="1.6"
      stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"/>
  </svg>`;
}

// ---------------------------------------------------------------- signal cards

function signalCard(sig) {
  const card = document.createElement('div');
  card.className = 'card';

  const targetPill = sig.target
    ? `<span class="pill pill-quiet">SLO ${(sig.target * 100).toFixed(sig.target >= 0.99 ? 1 : 0)}%</span>`
    : '';

  card.innerHTML = `
    <div class="card-head">
      <div>
        <div class="card-title">${escapeHTML(sig.name)}</div>
        <div class="card-spec">${escapeHTML(sig.spec)}</div>
      </div>
      ${targetPill}
    </div>
    <div class="metric">
      <span class="metric-value metric-none" data-role="value">—</span>
      <span class="metric-unit" data-role="unit"></span>
    </div>
    <div data-role="spark"><div class="spark-empty">waiting for data…</div></div>
    <details class="explain">
      <summary>Why this matters</summary>
      <div class="explain-body">
        <p>${escapeHTML(sig.why)}</p>
        <pre class="promql">${escapeHTML(sig.query)}</pre>
      </div>
    </details>`;

  state.cards[sig.key] = {
    sig,
    value: card.querySelector('[data-role="value"]'),
    unit: card.querySelector('[data-role="unit"]'),
    spark: card.querySelector('[data-role="spark"]'),
  };
  return card;
}

function updateSignalValues() {
  for (const [key, card] of Object.entries(state.cards)) {
    const v = state.values[key] ?? null;
    const { text, unit } = formatValue(v, card.sig.unit);
    const status = statusOf(card.sig, v);
    card.value.textContent = text;
    card.value.className = `metric-value metric-${status === 'plain' ? 'plain' : status}`;
    card.unit.textContent = unit;
    card.lastStatus = status;
  }
}

async function refreshSeries() {
  const keys = Object.keys(state.cards);
  await Promise.all(keys.map(async (key) => {
    const card = state.cards[key];
    try {
      const data = await api(withJob(`/api/series?key=${encodeURIComponent(key)}&minutes=${state.seriesMinutes}`));
      const svg = sparkline(data.points, {
        status: card.lastStatus || 'plain',
        target: card.sig.target || null,
      });
      card.spark.innerHTML = svg || '<div class="spark-empty">no samples in this window</div>';
    } catch {
      card.spark.innerHTML = '<div class="spark-empty">series unavailable</div>';
    }
  }));
}

// ---------------------------------------------------------------- fault forms

function faultCard(def) {
  const card = document.createElement('div');
  card.className = 'card';

  const fields = def.fields.map((f) => {
    const id = `fault-${def.type}-${f.name}`;
    if (f.kind === 'ratio') {
      return `<div class="fault-field">
        <div class="fault-field-row">
          <label for="${id}">${escapeHTML(f.label)}</label>
          <span class="fault-field-value" data-display="${id}">${(f.default * 100).toFixed(0)}%</span>
        </div>
        <input type="range" id="${id}" data-field="${f.name}" data-kind="ratio"
          min="${f.min}" max="${f.max}" step="${f.step}" value="${f.default}">
        ${f.help ? `<div class="fault-field-help">${escapeHTML(f.help)}</div>` : ''}
      </div>`;
    }
    const suffix = f.kind === 'ms' ? 'ms' : f.kind === 'seconds' ? 's' : '';
    return `<div class="fault-field">
      <div class="fault-field-row">
        <label for="${id}">${escapeHTML(f.label)}${suffix ? ` (${suffix})` : ''}</label>
        <input type="number" id="${id}" data-field="${f.name}" data-kind="${f.kind}"
          min="${f.min}" max="${f.max}" step="${f.step}" value="${f.default}">
      </div>
      ${f.help ? `<div class="fault-field-help">${escapeHTML(f.help)}</div>` : ''}
    </div>`;
  }).join('');

  card.innerHTML = `
    <div class="card-head">
      <div>
        <div class="card-title">${escapeHTML(def.name)}</div>
        <div class="card-spec">${escapeHTML(def.what)}</div>
      </div>
    </div>
    <div class="fault-fields">${fields}</div>
    <details class="explain">
      <summary>What it teaches</summary>
      <div class="explain-body"><p>${escapeHTML(def.teaches)}</p></div>
    </details>
    <button class="btn btn-primary" data-role="inject">Inject ${escapeHTML(def.name.toLowerCase())}</button>`;

  card.querySelectorAll('input[data-kind="ratio"]').forEach((input) => {
    const display = card.querySelector(`[data-display="${input.id}"]`);
    input.addEventListener('input', () => {
      display.textContent = `${(Number(input.value) * 100).toFixed(0)}%`;
    });
  });

  const button = card.querySelector('[data-role="inject"]');
  button.addEventListener('click', async () => {
    const body = { type: def.type };
    card.querySelectorAll('[data-field]').forEach((input) => {
      body[input.dataset.field] = Number(input.value);
    });

    button.disabled = true;
    try {
      const fault = await api(withService('/api/faults'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      toast(`Injected ${fault.type} (${fault.id}) for ${humanSeconds(fault.ttl_seconds)}`, 'good');
      await refreshFaults();
    } catch (err) {
      toast(`Rejected: ${err.message}`, 'bad');
    } finally {
      button.disabled = false;
    }
  });

  return card;
}

// ---------------------------------------------------------------- scenarios

function scenarioCard(sc) {
  const card = document.createElement('div');
  card.className = 'card';
  card.innerHTML = `
    <div class="card-head">
      <div class="card-title">${escapeHTML(sc.name)}</div>
      <span class="pill pill-quiet">${humanSeconds(sc.duration_seconds)}</span>
    </div>
    <div class="scenario-block"><b>Hypothesis.</b> ${escapeHTML(sc.hypothesis)}</div>
    <div class="scenario-block"><b>Watch.</b> ${escapeHTML(sc.watch)}</div>
    <details class="explain">
      <summary>What it injects</summary>
      <div class="explain-body">
        <pre class="promql">${escapeHTML(JSON.stringify(sc.faults.map(stripFault), null, 2))}</pre>
      </div>
    </details>
    <div class="scenario-actions">
      <button class="btn btn-primary" data-role="run">Run scenario</button>
    </div>`;

  const button = card.querySelector('[data-role="run"]');
  button.addEventListener('click', async () => {
    button.disabled = true;
    try {
      await api(withService(`/api/scenarios/${encodeURIComponent(sc.id)}/run`), { method: 'POST' });
      toast(`Running "${sc.name}" for ${humanSeconds(sc.duration_seconds)}`, 'good');
      await refreshFaults();
    } catch (err) {
      toast(`Could not start: ${err.message}`, 'bad');
    } finally {
      button.disabled = false;
    }
  });
  return card;
}

// stripFault hides the fields the engine fills in, so the preview shows the request the
// console actually sends rather than a half-empty object.
function stripFault(f) {
  const out = {};
  for (const [k, v] of Object.entries(f)) {
    if (['id', 'created_at', 'expires_at'].includes(k)) continue;
    if (v === '' || v === 0 || v === null) continue;
    out[k] = v;
  }
  return out;
}

// ---------------------------------------------------------------- active faults

function describeFault(f) {
  const bits = [];
  if (f.probability !== undefined) bits.push(`${(f.probability * 100).toFixed(0)}% of requests`);
  if (f.type === 'error') bits.push(`status ${f.status}`);
  if (f.type === 'latency') bits.push(`+${f.delay_ms}ms${f.jitter_ms ? ` ±${f.jitter_ms}ms` : ''}`);
  if (f.type === 'cpu_burn') bits.push(`${f.cores} core${f.cores > 1 ? 's' : ''} at ${(f.duty * 100).toFixed(0)}% duty`);
  if (f.path) bits.push(`path ${f.path}`);
  if (f.note) bits.push(`scenario ${f.note}`);
  return bits.join(' · ');
}

function renderActiveFaults() {
  const host = $('active-faults');
  if (!state.faults.length) {
    host.innerHTML = '<div class="empty-state">Nothing injected. The service is behaving exactly as designed — which is the baseline every experiment is measured against.</div>';
    return;
  }

  host.innerHTML = '';
  for (const f of state.faults) {
    const remaining = (Date.parse(f.expires_at) - (Date.now() + state.clockOffset)) / 1000;
    const row = document.createElement('div');
    row.className = 'fault-row';
    row.innerHTML = `
      <div class="fault-row-main">
        <div class="fault-row-title">${escapeHTML(f.type)} <span class="fault-row-detail">${escapeHTML(f.id)}</span></div>
        <div class="fault-row-detail">${escapeHTML(describeFault(f))}</div>
      </div>
      <div class="fault-ttl">${humanSeconds(remaining)}</div>
      <button class="btn btn-sm" data-role="remove">Remove</button>`;

    row.querySelector('[data-role="remove"]').addEventListener('click', async () => {
      try {
        await api(withService(`/api/faults/${encodeURIComponent(f.id)}`), { method: 'DELETE' });
        toast(`Removed ${f.id}`, 'good');
        await refreshFaults();
      } catch (err) {
        toast(`Could not remove: ${err.message}`, 'bad');
      }
    });
    host.appendChild(row);
  }
}

// ---------------------------------------------------------------- polling

async function refreshSummary() {
  try {
    const data = await api(withJob('/api/summary'));
    state.values = data.values || {};
    state.errors = data.errors || {};
    updateSignalValues();

    const failed = Object.keys(state.errors);
    if (failed.length) {
      setStatus('warn', `Prometheus: ${state.errors[failed[0]]}`);
    } else {
      setStatus('good', 'live');
    }
    $('pill-updated').textContent = `updated ${new Date().toLocaleTimeString()}`;
  } catch (err) {
    setStatus('bad', `console unreachable: ${err.message}`);
  }
}

async function refreshFaults() {
  try {
    const data = await api(withService('/api/faults'));
    state.faults = data.faults || [];
    if (data.now) state.clockOffset = Date.parse(data.now) - Date.now();
    renderActiveFaults();
  } catch {
    // A service that is down cannot list its faults. That is a legitimate state here:
    // the crash button exists precisely to produce it.
    state.faults = [];
    $('active-faults').innerHTML = '<div class="empty-state">Service unreachable — it may be restarting after a crash.</div>';
  }
}

function setStatus(kind, text) {
  const pill = $('pill-status');
  pill.className = `pill pill-${kind}`;
  pill.textContent = text;
}

function toast(message, kind = 'good') {
  const node = document.createElement('div');
  node.className = `toast toast-${kind}`;
  node.textContent = message;
  $('toasts').appendChild(node);
  setTimeout(() => node.remove(), 5000);
}

// ---------------------------------------------------------------- boot

async function boot() {
  let data;
  try {
    data = await api('/api/catalog');
  } catch (err) {
    setStatus('bad', `cannot load the catalog: ${err.message}`);
    return;
  }

  state.catalog = data.catalog;
  state.services = data.services;
  state.service = data.services[0];

  const select = $('service-select');
  select.innerHTML = state.services.map((s) => `<option value="${escapeHTML(s)}">${escapeHTML(s)}</option>`).join('');
  select.addEventListener('change', async () => {
    state.service = select.value;
    await Promise.all([refreshSummary(), refreshFaults(), refreshSeries()]);
  });

  $('link-grafana').href = data.links.grafana;
  $('link-prometheus').href = data.links.prometheus;

  const slo = state.catalog.slo;
  $('pill-slo').textContent =
    `SLO ${(slo.availability_target * 100).toFixed(1)}% available · ${(slo.latency_target * 100).toFixed(0)}% under ${slo.latency_threshold}s · ${slo.window} window`;

  const signals = $('signals');
  state.catalog.signals.forEach((sig) => signals.appendChild(signalCard(sig)));
  const budgets = $('budgets');
  state.catalog.budgets.forEach((sig) => budgets.appendChild(signalCard(sig)));
  const faultsHost = $('faults');
  state.catalog.faults.forEach((def) => faultsHost.appendChild(faultCard(def)));
  const scenarios = $('scenarios');
  state.catalog.scenarios.forEach((sc) => scenarios.appendChild(scenarioCard(sc)));

  $('topics').innerHTML = state.catalog.topics.map((t) => `
    <details class="topic">
      <summary>${escapeHTML(t.title)}</summary>
      <div class="topic-body">${t.body.map((p) => `<p>${escapeHTML(p)}</p>`).join('')}</div>
    </details>`).join('');

  $('clear-all').addEventListener('click', async () => {
    try {
      const res = await api(withService('/api/faults'), { method: 'DELETE' });
      toast(res.removed ? `Removed ${res.removed} fault(s)` : 'Nothing to remove', 'good');
      await refreshFaults();
    } catch (err) {
      toast(`Could not clear: ${err.message}`, 'bad');
    }
  });

  $('crash').addEventListener('click', async () => {
    if (!window.confirm('Kill the service process? It restarts automatically, with its counters reset.')) return;
    try {
      await api(withService('/api/crash'), { method: 'POST' });
      toast('Process killed. Watch the server-side line stop while the client keeps going.', 'good');
    } catch (err) {
      toast(`Could not crash it: ${err.message}`, 'bad');
    }
  });

  await refreshSummary();
  await refreshFaults();
  await refreshSeries();

  setInterval(refreshSummary, 2000);
  setInterval(refreshFaults, 3000);
  setInterval(refreshSeries, 10000);
  // The countdown is local: re-polling once a second just to move a number would be rude
  // to a Prometheus that is scraping every five.
  setInterval(renderActiveFaults, 1000);
}

boot();
