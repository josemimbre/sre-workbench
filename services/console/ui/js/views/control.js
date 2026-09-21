// The control room: the numbers, the chart, the buttons that break things, and the
// scenarios, all on one screen. Keeping the injection controls beside the chart is
// deliberate — the point is to see cause and effect in the same glance.

import { api } from '../api.js';
import { Chart } from '../chart.js';
import { clone, list, mount } from '../dom.js';
import { formatValue, statusOf, humanSeconds, clockTime } from '../format.js';

const TILES = ['availability', 'latency', 'availability_budget', 'availability_burn'];
const CHARTABLE = ['availability', 'latency', 'availability_budget', 'availability_burn', 'latency_budget', 'latency_burn', 'throughput', 'p99', 'inflight'];
const WINDOWS = [15, 30, 60];

export class ControlView {
  constructor(ctx) {
    this.ctx = ctx;
    this.chartKey = 'availability';
    this.windowMinutes = 30;
    this.faultType = ctx.catalog.faults[0].type;
    this.values = {};
    this.faults = [];
    this.alerts = [];
    this.notifications = [];
    this.clockOffset = 0;
    this.tiles = {};
    this.refs = {};
  }

  signal(key) {
    return [...this.ctx.catalog.signals, ...this.ctx.catalog.budgets].find((s) => s.key === key);
  }

  mount(root) {
    const view = clone('tpl-control', {
      '[data-slot="tiles"]': this.buildTiles(),
      '[data-slot="windows"]': this.buildWindowChips(),
      '[data-slot="metrics"]': this.buildMetricChips(),
      '[data-slot="fault-types"]': this.buildFaultTypes(),
      '[data-slot="scenarios"]': this.buildScenarios(),
      '[data-slot="am-link"]': { href: ctxLink(this.ctx, 'alertmanager') },
      '[data-slot="clear"]': { on: { click: () => this.clearFaults() } },
      '[data-slot="crash"]': { on: { click: () => this.crash() } },
    });

    this.refs = {
      banner: view.querySelector('[data-slot="banner"]'),
      alertList: view.querySelector('[data-slot="alert-list"]'),
      notifications: view.querySelector('[data-slot="notifications"]'),
      bannerText: view.querySelector('.banner-text'),
      chartHost: view.querySelector('[data-slot="chart"]'),
      chartWhy: view.querySelector('.chart-why'),
      chartQuery: view.querySelector('.chart-query'),
      faultForm: view.querySelector('[data-slot="fault-form"]'),
      activeFaults: view.querySelector('[data-slot="active-faults"]'),
      events: view.querySelector('[data-slot="events"]'),
    };

    root.replaceChildren(view);
    this.root = root;
    this.chart = new Chart(this.refs.chartHost, { height: 280 });

    this.renderFaultForm();
    this.renderAlerts();
    this.renderActiveFaults();
    this.renderEventLog();
    this.renderBanner();
    this.applySummary(this.values);
    this.refreshChart();
  }

  unmount() {
    if (this.chart) this.chart.destroy();
    this.chart = null;
    this.root = null;
    this.tiles = {};
    this.refs = {};
  }

  // ---------------------------------------------------------------- building

  buildTiles() {
    return TILES.map((key) => {
      const sig = this.signal(key);
      if (!sig) return null;
      const hint = sig.target ? `target ${(sig.target * 100).toFixed(sig.target >= 0.99 ? 1 : 0)}%`
        : sig.invert ? 'sustainable is 1×' : '';
      const node = clone('tpl-tile', {
        '.tile-name': sig.name,
        '.tile-hint': hint,
        '': { data: { status: 'none' } },
      });
      this.tiles[key] = {
        node,
        value: node.querySelector('.tile-value'),
        unit: node.querySelector('.tile-unit'),
      };
      return node;
    }).filter(Boolean);
  }

  buildWindowChips() {
    return list('tpl-chip', WINDOWS, (m) => ({
      '': {
        text: `${m}m`,
        class: m === this.windowMinutes ? 'chip-on' : [],
        data: { window: m },
        on: { click: (e) => this.pick(e.currentTarget, '[data-window]', () => { this.windowMinutes = m; }) },
      },
    }));
  }

  buildMetricChips() {
    return CHARTABLE.map((key) => {
      const sig = this.signal(key);
      if (!sig) return null;
      return clone('tpl-chip', {
        '': {
          text: sig.name,
          class: key === this.chartKey ? 'chip-on' : [],
          data: { metric: key },
          on: { click: (e) => this.pick(e.currentTarget, '[data-metric]', () => { this.chartKey = key; }) },
        },
      });
    }).filter(Boolean);
  }

  buildFaultTypes() {
    return list('tpl-seg', this.ctx.catalog.faults, (def) => ({
      '': {
        text: def.name,
        class: def.type === this.faultType ? 'seg-on' : [],
        data: { faultType: def.type },
        on: {
          click: (e) => {
            this.faultType = def.type;
            this.root.querySelectorAll('[data-fault-type]').forEach((o) => o.classList.toggle('seg-on', o === e.currentTarget));
            this.renderFaultForm();
          },
        },
      },
    }));
  }

  // pick handles the "one of these is selected" behaviour shared by both chip rows.
  pick(button, selector, apply) {
    apply();
    this.root.querySelectorAll(selector).forEach((o) => o.classList.toggle('chip-on', o === button));
    this.refreshChart();
  }

  buildScenarios() {
    return list('tpl-scenario', this.ctx.catalog.scenarios, (sc) => ({
      '.scenario-name': sc.name,
      '.scenario-duration': humanSeconds(sc.duration_seconds),
      '.scenario-hypothesis': sc.hypothesis,
      '.scenario-watch': sc.watch,
      '[data-slot="injects"]': list('tpl-li', sc.faults, (f) => ({
        '': `${f.type} · ${describeFault(f)} · ${humanSeconds(f.ttl_seconds)}`,
      })),
      '.scenario-run': { on: { click: (e) => this.runScenario(sc, e.currentTarget) } },
    }));
  }

  // ---------------------------------------------------------------- updating

  applySummary(values) {
    this.values = values || {};
    if (!this.root) return;
    for (const key of TILES) {
      const tile = this.tiles[key];
      const sig = this.signal(key);
      if (!tile || !sig) continue;
      const v = this.values[key] ?? null;
      const { text, unit } = formatValue(v, sig.unit);
      tile.value.textContent = text;
      tile.unit.textContent = unit;
      tile.node.dataset.status = statusOf(sig, v);
    }
  }

  applyFaults(faults, clockOffset) {
    this.faults = faults || [];
    this.clockOffset = clockOffset || 0;
    if (!this.root) return;
    this.renderActiveFaults();
    this.renderBanner();
  }

  applyAlerts(alerts, notifications) {
    this.alerts = alerts || [];
    this.notifications = notifications || [];
    if (this.root) this.renderAlerts();
  }

  renderAlerts() {
    const { alertList, notifications } = this.refs;
    if (!alertList) return;

    if (!this.alerts.length) {
      mount(alertList, clone('tpl-empty', {
        '': 'No alerts. Every window agrees the budget is being spent slower than 1×.',
      }));
    } else {
      mount(alertList, list('tpl-alert', this.alerts, (a) => ({
        '': { data: { state: a.state, severity: a.severity } },
        '.alert-state': a.state,
        '.alert-name': a.name,
        '.alert-meta': [a.service, a.slo].filter(Boolean).join(' · '),
        '.alert-severity': a.severity || '—',
      })));
    }

    if (!notifications) return;
    if (!this.notifications.length) {
      mount(notifications, clone('tpl-empty', { '': 'Nothing delivered yet.' }));
      return;
    }
    mount(notifications, list('tpl-notification', this.notifications, (n) => ({
      '.event-time': clockTime(Date.parse(n.at)),
      '.event-text': `${n.status} · ${n.names.join(', ') || 'no alerts'}`
        + (n.count > 1 ? ` (${n.count} grouped)` : ''),
    })));
  }

  renderBanner() {
    const { banner, bannerText } = this.refs;
    if (!banner) return;
    banner.hidden = this.faults.length === 0;
    if (!this.faults.length) return;

    const kinds = [...new Set(this.faults.map((f) => f.type))].join(', ');
    bannerText.textContent =
      `${this.faults.length} fault${this.faults.length > 1 ? 's' : ''} active (${kinds}). `
      + 'The shaded band on the chart is happening right now.';
  }

  renderFaultForm() {
    const def = this.ctx.catalog.faults.find((d) => d.type === this.faultType);
    if (!this.refs.faultForm || !def) return;

    const form = clone('tpl-fault-form', {
      '.fault-what': def.what,
      '.fault-teaches': def.teaches,
      '[data-slot="fields"]': def.fields.map((f) => this.buildField(def, f)),
      '[data-slot="inject"]': { on: { click: (e) => this.inject(e.currentTarget, def, form) } },
    });

    mount(this.refs.faultForm, form);
  }

  buildField(def, f) {
    const id = `f-${def.type}-${f.name}`;
    const attrs = { id, min: f.min, max: f.max, step: f.step, value: f.default };

    if (f.kind === 'ratio') {
      const node = clone('tpl-field-range', {
        label: { text: f.label, for: id },
        input: { ...attrs, data: { field: f.name } },
        '.fault-field-help': { text: f.help || '', hidden: !f.help },
      });
      const input = node.querySelector('input');
      const display = node.querySelector('.fault-field-value');
      const sync = () => { display.textContent = `${(Number(input.value) * 100).toFixed(0)}%`; };
      input.addEventListener('input', sync);
      sync();
      return node;
    }

    const suffix = f.kind === 'ms' ? ' (ms)' : f.kind === 'seconds' ? ' (s)' : '';
    return clone('tpl-field-number', {
      label: { text: f.label + suffix, for: id },
      input: { ...attrs, data: { field: f.name } },
      '.fault-field-help': { text: f.help || '', hidden: !f.help },
    });
  }

  renderActiveFaults() {
    const host = this.refs.activeFaults;
    if (!host) return;

    if (!this.faults.length) {
      mount(host, clone('tpl-empty', { '': 'Nothing injected. This is the baseline every experiment is measured against.' }));
      return;
    }

    mount(host, list('tpl-fault-row', this.faults, (f) => {
      const remaining = (Date.parse(f.expires_at) - (Date.now() + this.clockOffset)) / 1000;
      const pct = Math.max(0, Math.min(100, (remaining / f.ttl_seconds) * 100));
      return {
        '.fault-row-type': f.type,
        '.fault-row-id': f.id,
        '.fault-row-detail': describeFault(f),
        '.ttl-bar span': { style: { width: `${pct.toFixed(1)}%` } },
        '.fault-ttl': humanSeconds(remaining),
        'button': { on: { click: () => this.removeFault(f.id) } },
      };
    }));
  }

  renderEventLog() {
    const host = this.refs.events;
    if (!host) return;
    const events = this.ctx.events;

    if (!events.length) {
      mount(host, clone('tpl-empty', { '': 'Nothing has happened yet. Inject a fault or run a scenario.' }));
      return;
    }

    mount(host, list('tpl-event', events.slice(0, 40), (e) => ({
      '': { class: `event-${e.kind}` },
      '.event-time': clockTime(e.at),
      '.event-text': e.text,
      '.event-ref': e.ref || '',
    })));
  }

  // tick only redraws the countdowns. Re-polling once a second to move a number would be
  // rude to a Prometheus that scrapes every five.
  tick() {
    if (this.root && this.faults.length) this.renderActiveFaults();
  }

  async refreshChart() {
    if (!this.chart) return;
    const sig = this.signal(this.chartKey);
    if (!sig) return;

    this.refs.chartWhy.textContent = sig.why;
    this.refs.chartQuery.textContent = sig.query;

    try {
      const [series, annotations] = await Promise.all([
        api.series(this.chartKey, this.ctx.service(), this.windowMinutes),
        api.annotations(this.ctx.service(), this.windowMinutes),
      ]);
      this.chart.setData({
        points: series.points,
        spans: annotations.spans,
        status: statusOf(sig, this.values[this.chartKey] ?? null),
        unit: sig.unit,
        target: sig.target || null,
      });
    } catch (err) {
      mount(this.refs.chartHost, clone('tpl-empty', { '': err.message }));
    }
  }

  // ---------------------------------------------------------------- actions

  async inject(button, def, form) {
    const body = { type: def.type };
    form.querySelectorAll('[data-field]').forEach((input) => { body[input.dataset.field] = Number(input.value); });

    button.disabled = true;
    button.textContent = 'Injecting…';
    try {
      const fault = await api.injectFault(this.ctx.service(), body);
      this.ctx.log('inject', `${fault.type} injected for ${humanSeconds(fault.ttl_seconds)}`, fault.id);
      this.ctx.toast(`${fault.type} injected — watch the chart`, 'good');
      await this.ctx.refreshFaults();
      this.refreshChart();
    } catch (err) {
      this.ctx.toast(`Rejected: ${err.message}`, 'bad');
    } finally {
      button.disabled = false;
      button.textContent = 'Inject';
    }
  }

  async runScenario(sc, button) {
    button.disabled = true;
    button.textContent = 'Starting…';
    try {
      await api.runScenario(this.ctx.service(), sc.id);
      this.ctx.log('scenario', `scenario “${sc.name}” started`, humanSeconds(sc.duration_seconds));
      this.ctx.toast(`Running “${sc.name}”`, 'good');
      await this.ctx.refreshFaults();
      this.refreshChart();
    } catch (err) {
      this.ctx.toast(`Could not start: ${err.message}`, 'bad');
    } finally {
      button.disabled = false;
      button.textContent = 'Run';
    }
  }

  async removeFault(id) {
    try {
      await api.removeFault(this.ctx.service(), id);
      this.ctx.log('remove', 'fault removed by hand', id);
      await this.ctx.refreshFaults();
    } catch (err) {
      this.ctx.toast(`Could not remove: ${err.message}`, 'bad');
    }
  }

  async clearFaults() {
    try {
      const res = await api.clearFaults(this.ctx.service());
      if (res.removed) this.ctx.log('clear', `cleared ${res.removed} fault(s)`);
      this.ctx.toast(res.removed ? `Cleared ${res.removed} fault(s)` : 'Nothing to clear', 'good');
      await this.ctx.refreshFaults();
    } catch (err) {
      this.ctx.toast(`Could not clear: ${err.message}`, 'bad');
    }
  }

  async crash() {
    if (!window.confirm('Kill the service process? It restarts automatically, with its counters reset.')) return;
    try {
      await api.crash(this.ctx.service());
      this.ctx.log('crash', 'process killed');
      this.ctx.toast('Killed. Compare the server line against the client one.', 'good');
    } catch (err) {
      this.ctx.toast(`Could not crash it: ${err.message}`, 'bad');
    }
  }
}

// The header links come from the catalog, which knows the published ports.
function ctxLink(ctx, name) {
  return (ctx.links && ctx.links[name]) || '#';
}

export function describeFault(f) {
  const bits = [];
  if (f.probability !== undefined && f.type !== 'cpu_burn') {
    bits.push(`${(f.probability * 100).toFixed(0)}% of requests`);
  }
  if (f.type === 'error') bits.push(`status ${f.status}`);
  if (f.type === 'latency') bits.push(`+${f.delay_ms}ms${f.jitter_ms ? ` ±${f.jitter_ms}ms` : ''}`);
  if (f.type === 'cpu_burn') bits.push(`${f.cores} core${f.cores > 1 ? 's' : ''} at ${(f.duty * 100).toFixed(0)}%`);
  if (f.path) bits.push(`path ${f.path}`);
  if (f.note) bits.push(`from “${f.note}”`);
  return bits.join(' · ');
}
