// Formatting and colour rules, in one place so a number looks the same everywhere it
// appears — in a tile, on an axis and inside a tooltip.

export function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

// formatValue keeps a fixed number of decimals per unit. Digits that change width make a
// live number look like it is flickering even when it is barely moving.
export function formatValue(v, unit, { compact = false } = {}) {
  if (v === null || v === undefined || Number.isNaN(v)) return { text: '—', unit: '' };
  switch (unit) {
    case 'ratio': return { text: (v * 100).toFixed(compact ? 1 : 2), unit: '%' };
    // A budget is a balance, not a remainder. Spending more than the allowance is a
    // real state with a magnitude worth reading, so it is shown as an overdraft rather
    // than clamped at zero — slightly over and over by triple are different problems.
    case 'budget': return v < 0
      ? { text: (Math.abs(v) * 100).toFixed(0), unit: '% over' }
      : { text: (v * 100).toFixed(1), unit: '% left' };
    case 'rps': return { text: v.toFixed(1), unit: 'req/s' };
    case 'rate': return { text: v.toFixed(2), unit: '× budget' };
    case 'seconds': return v < 1
      ? { text: (v * 1000).toFixed(0), unit: 'ms' }
      : { text: v.toFixed(2), unit: 's' };
    case 'count': return { text: Math.round(v).toString(), unit: '' };
    default: return { text: v.toFixed(2), unit: '' };
  }
}

export function formatCompact(v, unit) {
  const { text, unit: u } = formatValue(v, unit, { compact: true });
  return u ? `${text}${u === '%' ? '%' : ` ${u}`}` : text;
}

// statusOf encodes the SLO as a colour. The thresholds are not decoration: 6 is the SRE
// Workbook's second paging burn rate, and a quarter of the budget left is the point where
// a window is in trouble even if nothing is failing at this instant.
export function statusOf(sig, v) {
  if (v === null || v === undefined || Number.isNaN(v)) return 'none';
  if (sig.unit === 'budget') {
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

export const STATUS_COLOR = {
  good: 'var(--good)',
  warn: 'var(--warn)',
  bad: 'var(--bad)',
  plain: 'var(--accent)',
  none: 'var(--text-faint)',
};

export function humanSeconds(s) {
  if (s === null || s === undefined) return '—';
  if (s <= 0) return '0s';
  if (s < 60) return `${Math.ceil(s)}s`;
  const m = Math.floor(s / 60);
  const rest = Math.round(s % 60);
  if (m < 60) return rest ? `${m}m ${rest}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

export function clockTime(ms) {
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function shortClock(ms) {
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}
