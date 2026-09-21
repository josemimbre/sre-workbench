// Boot, routing and the polling loop. Views are plain functions that render into a
// container; only the control room needs to keep state between polls, so only it is a
// class.

import { api } from './api.js';
import { clone, mount } from './dom.js';
import { renderOverview } from './views/overview.js';
import { ControlView } from './views/control.js';

const VIEWS = ['overview', 'control'];

const EVENTS_KEY = 'workbench.events';

const state = {
  catalog: null,
  services: [],
  service: null,
  values: {},
  faults: [],
  knownFaultIds: new Set(),
  clockOffset: 0,
  events: loadEvents(),
  view: null,
};

const $ = (sel) => document.querySelector(sel);

const ctx = {
  get catalog() { return state.catalog; },
  get events() { return state.events; },
  service: () => state.service,
  toast,
  log: logEvent,
  refreshFaults,
};

let control = null;

// ---------------------------------------------------------------- events

function loadEvents() {
  // Browser storage can be unavailable or throw outright; the log is a convenience, so
  // losing it must never stop the page from working.
  try {
    return JSON.parse(sessionStorage.getItem(EVENTS_KEY) || '[]');
  } catch {
    return [];
  }
}

function saveEvents() {
  try {
    sessionStorage.setItem(EVENTS_KEY, JSON.stringify(state.events.slice(0, 60)));
  } catch { /* nothing to do: the log is disposable */ }
}

function logEvent(kind, text, ref = '') {
  state.events.unshift({ at: Date.now(), kind, text, ref });
  state.events = state.events.slice(0, 60);
  saveEvents();
  if (control && state.view === 'control') control.renderEventLog();
}

// ---------------------------------------------------------------- polling

async function refreshSummary() {
  try {
    const data = await api.summary(state.service);
    state.values = data.values || {};
    const errors = data.errors || {};
    if (control) control.applySummary(state.values);

    const failed = Object.keys(errors);
    setStatus(failed.length ? 'warn' : 'good', failed.length ? errors[failed[0]] : 'live');
    $('#pill-updated').textContent = new Date().toLocaleTimeString();
  } catch (err) {
    setStatus('bad', `console unreachable: ${err.message}`);
  }
}

async function refreshFaults() {
  try {
    const data = await api.faults(state.service);
    const faults = data.faults || [];
    if (data.now) state.clockOffset = Date.parse(data.now) - Date.now();

    // A fault that disappears without us removing it has served its TTL. Saying so
    // explicitly is half the argument for making TTLs mandatory.
    const ids = new Set(faults.map((f) => f.id));
    for (const id of state.knownFaultIds) {
      if (!ids.has(id)) logEvent('expire', 'fault expired on its own', id);
    }
    state.knownFaultIds = ids;
    state.faults = faults;

    if (control) control.applyFaults(faults, state.clockOffset);
    $('#pill-faults').hidden = faults.length === 0;
    $('#pill-faults').textContent = `${faults.length} fault${faults.length > 1 ? 's' : ''} active`;
  } catch {
    // A crashed service cannot list its faults. That is a legitimate state here — the
    // crash button exists to produce it.
    state.faults = [];
    if (control) control.applyFaults([], state.clockOffset);
    $('#pill-faults').hidden = true;
  }
}

// ---------------------------------------------------------------- chrome

function setStatus(kind, text) {
  const pill = $('#pill-status');
  pill.className = `pill pill-${kind}`;
  pill.textContent = text;
}

function toast(message, kind = 'good') {
  const node = document.createElement('div');
  node.className = `toast toast-${kind}`;
  node.textContent = message;
  $('#toasts').appendChild(node);
  setTimeout(() => node.classList.add('toast-out'), 4200);
  setTimeout(() => node.remove(), 4800);
}

function setView(id) {
  const view = VIEWS.includes(id) ? id : VIEWS[0];
  if (state.view === view) return;

  if (control) control.unmount();
  state.view = view;

  document.querySelectorAll('[data-view]').forEach((b) => {
    b.classList.toggle('nav-on', b.dataset.view === view);
  });

  const root = $('#view');
  if (view === 'control') {
    control.mount(root);
    control.applyFaults(state.faults, state.clockOffset);
  } else {
    renderOverview(root, state.catalog);
  }
  window.scrollTo({ top: 0 });
}

function routeFromHash() {
  setView((location.hash || '').replace('#/', '') || 'overview');
}

// ---------------------------------------------------------------- boot

async function boot() {
  let data;
  try {
    data = await api.catalog();
  } catch (err) {
    mount($('#view'), clone('tpl-empty', { '': `Cannot reach the console API: ${err.message}` }));
    setStatus('bad', 'offline');
    return;
  }

  state.catalog = data.catalog;
  state.services = data.services;
  state.service = data.services[0];

  document.querySelectorAll('[data-view]').forEach((b) => {
    b.addEventListener('click', () => { location.hash = `#/${b.dataset.view}`; });
  });

  const select = $('#service-select');
  mount(select, state.services.map((name) => {
    const option = document.createElement('option');
    option.value = name;
    option.textContent = name;
    return option;
  }));
  select.addEventListener('change', async () => {
    state.service = select.value;
    state.knownFaultIds = new Set();
    await Promise.all([refreshSummary(), refreshFaults()]);
    if (control && state.view === 'control') control.refreshChart();
  });

  $('#link-grafana').href = data.links.grafana;
  $('#link-prometheus').href = data.links.prometheus;

  const slo = state.catalog.slo;
  $('#pill-slo').textContent =
    `${(slo.availability_target * 100).toFixed(1)}% available · ${(slo.latency_target * 100).toFixed(0)}% under ${slo.latency_threshold}s · ${slo.window} window`;

  control = new ControlView(ctx);

  window.addEventListener('hashchange', routeFromHash);
  routeFromHash();

  await refreshSummary();
  await refreshFaults();

  setInterval(refreshSummary, 2000);
  setInterval(refreshFaults, 3000);
  setInterval(() => { if (control && state.view === 'control') control.tick(); }, 1000);
  setInterval(() => { if (control && state.view === 'control') control.refreshChart(); }, 10000);
}

boot();
