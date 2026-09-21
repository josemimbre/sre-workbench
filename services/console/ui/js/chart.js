// A real chart: axes, grid, hover readout, a target line, and shaded bands marking when a
// fault was active. That last part is the whole point of the page — seeing the injection
// and the damage on the same time axis is what turns a number into an explanation.

import { STATUS_COLOR } from './format.js';

const MARGIN = { top: 12, right: 14, bottom: 24, left: 54 };

export class Chart {
  constructor(container, { height = 260, unit = 'ratio', target = null } = {}) {
    this.container = container;
    this.height = height;
    this.unit = unit;
    this.target = target;
    this.points = [];
    this.spans = [];
    this.status = 'plain';
    this.hoverIndex = null;

    this.container.classList.add('chart');
    this.container.innerHTML = '<div class="chart-empty">loading…</div>';

    this.onResize = debounce(() => this.render(), 120);
    window.addEventListener('resize', this.onResize);
  }

  destroy() {
    window.removeEventListener('resize', this.onResize);
  }

  setData({ points, spans, status, unit, target }) {
    this.points = (points || []).filter((p) => p[0] !== null);
    this.spans = spans || [];
    if (status) this.status = status;
    if (unit) this.unit = unit;
    this.target = target === undefined ? this.target : target;
    this.render();
  }

  render() {
    const width = Math.max(this.container.clientWidth, 320);
    const height = this.height;

    const usable = this.points.filter((p) => p[1] !== null);
    if (usable.length < 2) {
      this.container.innerHTML = '<div class="chart-empty">not enough samples in this window yet</div>';
      return;
    }

    const plotW = width - MARGIN.left - MARGIN.right;
    const plotH = height - MARGIN.top - MARGIN.bottom;

    const values = usable.map((p) => p[1]);
    let lo = Math.min(...values);
    let hi = Math.max(...values);
    if (this.target !== null && this.target !== undefined) {
      lo = Math.min(lo, this.target);
      hi = Math.max(hi, this.target);
    }
    if (hi - lo < 1e-9) hi = lo + Math.max(Math.abs(lo) * 0.02, 1e-6);
    const pad = (hi - lo) * 0.12;
    lo -= pad;
    hi += pad;
    // A ratio never usefully goes above 100%, and pretending otherwise wastes half the
    // chart on empty space above a flat line at 1.
    if (this.unit === 'ratio' && hi > 1) hi = 1 + (hi - 1) * 0.15;

    const t0 = this.points[0][0];
    const t1 = this.points[this.points.length - 1][0];

    const x = (t) => MARGIN.left + (t1 === t0 ? plotW : ((t - t0) / (t1 - t0)) * plotW);
    const y = (v) => MARGIN.top + plotH - ((v - lo) / (hi - lo)) * plotH;

    const color = STATUS_COLOR[this.status] || STATUS_COLOR.plain;
    const gradId = `cg-${Math.random().toString(36).slice(2, 8)}`;

    const yTicks = niceTicks(lo, hi, 4);
    const yStep = yTicks.length > 1 ? yTicks[1] - yTicks[0] : hi - lo;
    const gridY = yTicks.map((v) => `
      <line class="grid" x1="${MARGIN.left}" x2="${width - MARGIN.right}" y1="${y(v).toFixed(1)}" y2="${y(v).toFixed(1)}"/>
      <text class="axis" x="${MARGIN.left - 8}" y="${(y(v) + 3.5).toFixed(1)}" text-anchor="end">${axisLabel(v, this.unit, yStep)}</text>
    `).join('');

    const gridX = timeTicks(t0, t1).map((t) => `
      <line class="grid grid-x" x1="${x(t).toFixed(1)}" x2="${x(t).toFixed(1)}" y1="${MARGIN.top}" y2="${MARGIN.top + plotH}"/>
      <text class="axis" x="${x(t).toFixed(1)}" y="${height - 7}" text-anchor="middle">${timeLabel(t)}</text>
    `).join('');

    // Fault bands are drawn under the line so they never obscure the data they explain.
    const bands = this.spans.map((s) => {
      const bx = Math.max(x(s.start), MARGIN.left);
      const bw = Math.min(x(s.end), width - MARGIN.right) - bx;
      if (bw <= 0) return '';
      return `<rect class="band ${s.ongoing ? 'band-live' : ''}" x="${bx.toFixed(1)}" y="${MARGIN.top}"
                width="${bw.toFixed(1)}" height="${plotH}"/>`;
    }).join('');

    let line = '';
    let open = false;
    for (const [t, v] of this.points) {
      if (v === null) { open = false; continue; }
      line += `${open ? 'L' : 'M'}${x(t).toFixed(1)} ${y(v).toFixed(1)}`;
      open = true;
    }
    const area = `${line}L${x(t1).toFixed(1)} ${(MARGIN.top + plotH).toFixed(1)}L${x(t0).toFixed(1)} ${(MARGIN.top + plotH).toFixed(1)}Z`;

    const targetLine = (this.target !== null && this.target !== undefined) ? `
      <line class="target" x1="${MARGIN.left}" x2="${width - MARGIN.right}"
            y1="${y(this.target).toFixed(1)}" y2="${y(this.target).toFixed(1)}"/>
      <text class="axis target-label" x="${width - MARGIN.right}" y="${(y(this.target) - 5).toFixed(1)}" text-anchor="end">target</text>
    ` : '';

    this.container.innerHTML = `
      <svg class="chart-svg" width="${width}" height="${height}" role="img">
        <defs>
          <linearGradient id="${gradId}" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stop-color="${color}" stop-opacity="0.26"/>
            <stop offset="100%" stop-color="${color}" stop-opacity="0"/>
          </linearGradient>
        </defs>
        ${bands}
        ${gridY}
        ${gridX}
        ${targetLine}
        <path d="${area}" fill="url(#${gradId})"/>
        <path d="${line}" fill="none" stroke="${color}" stroke-width="1.8" stroke-linejoin="round" stroke-linecap="round"/>
        <g class="hover-layer" opacity="0">
          <line class="crosshair" y1="${MARGIN.top}" y2="${MARGIN.top + plotH}"/>
          <circle class="hover-dot" r="3.5" fill="${color}"/>
        </g>
        <rect class="hit" x="${MARGIN.left}" y="${MARGIN.top}" width="${plotW}" height="${plotH}" fill="transparent"/>
      </svg>
      <div class="chart-tip" hidden></div>`;

    this.attachHover({ x, y, t0, t1, width, plotW });
  }

  attachHover({ x, y, t0, t1, width, plotW }) {
    const svg = this.container.querySelector('.chart-svg');
    const hit = this.container.querySelector('.hit');
    const layer = this.container.querySelector('.hover-layer');
    const crosshair = this.container.querySelector('.crosshair');
    const dot = this.container.querySelector('.hover-dot');
    const tip = this.container.querySelector('.chart-tip');
    if (!hit) return;

    const move = (event) => {
      const rect = svg.getBoundingClientRect();
      const px = event.clientX - rect.left;
      const t = t0 + ((px - MARGIN.left) / plotW) * (t1 - t0);

      let best = null;
      let bestDist = Infinity;
      for (const p of this.points) {
        if (p[1] === null) continue;
        const d = Math.abs(p[0] - t);
        if (d < bestDist) { bestDist = d; best = p; }
      }
      if (!best) return;

      const bx = x(best[0]);
      const by = y(best[1]);
      layer.setAttribute('opacity', '1');
      crosshair.setAttribute('x1', bx);
      crosshair.setAttribute('x2', bx);
      dot.setAttribute('cx', bx);
      dot.setAttribute('cy', by);

      const duringFault = this.spans.some((s) => best[0] >= s.start && best[0] <= s.end);
      tip.hidden = false;
      tip.innerHTML = `
        <div class="chart-tip-value">${axisLabel(best[1], this.unit, Math.abs(best[1]) / 100 || 0.01)}</div>
        <div class="chart-tip-time">${new Date(best[0] * 1000).toLocaleTimeString()}</div>
        ${duringFault ? '<div class="chart-tip-fault">fault active</div>' : ''}`;

      const tipW = tip.offsetWidth;
      const left = Math.min(Math.max(bx - tipW / 2, 4), width - tipW - 4);
      tip.style.left = `${left}px`;
      tip.style.top = `${Math.max(by - 58, 2)}px`;
    };

    hit.addEventListener('pointermove', move);
    hit.addEventListener('pointerleave', () => {
      layer.setAttribute('opacity', '0');
      tip.hidden = true;
    });
  }
}

// niceTicks picks round numbers for the axis instead of dividing the range evenly, which
// is what stops an axis reading 99.3847%.
function niceTicks(lo, hi, count) {
  const span = hi - lo;
  if (span <= 0) return [lo];
  const raw = span / count;
  const magnitude = 10 ** Math.floor(Math.log10(raw));
  const candidates = [1, 2, 2.5, 5, 10].map((m) => m * magnitude);
  const step = candidates.find((c) => c >= raw) || candidates[candidates.length - 1];

  const first = Math.ceil(lo / step) * step;
  const out = [];
  for (let v = first; v <= hi + step * 1e-6; v += step) out.push(Number(v.toFixed(10)));
  return out;
}

function timeTicks(t0, t1) {
  const span = t1 - t0;
  const steps = [60, 120, 300, 600, 900, 1800, 3600, 7200];
  const step = steps.find((s) => span / s <= 6) || steps[steps.length - 1];
  const first = Math.ceil(t0 / step) * step;
  const out = [];
  for (let t = first; t <= t1; t += step) out.push(t);
  return out;
}

function timeLabel(t) {
  return new Date(t * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function axisLabel(v, unit, step) {
  switch (unit) {
    case 'ratio': {
      const decimals = Math.max(0, Math.min(3, Math.ceil(-Math.log10(Math.abs(step) * 100 || 1))));
      return `${(v * 100).toFixed(decimals)}%`;
    }
    case 'seconds': return v < 1 ? `${(v * 1000).toFixed(0)}ms` : `${v.toFixed(2)}s`;
    case 'rate': return `${v.toFixed(v < 10 ? 1 : 0)}×`;
    case 'rps': return v.toFixed(v < 10 ? 1 : 0);
    case 'count': return v.toFixed(0);
    default: return v.toFixed(2);
  }
}

function debounce(fn, ms) {
  let handle;
  return (...args) => {
    clearTimeout(handle);
    handle = setTimeout(() => fn(...args), ms);
  };
}
