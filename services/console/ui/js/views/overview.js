// The map of the workbench: what is running, how one request becomes a number, where each
// fault bites, and the reasoning behind every query. This is the view to read first.
//
// All the markup lives in <template> elements in index.html; this file only decides what
// goes in which slot.

import { clone, list, paragraphs } from '../dom.js';

export function renderOverview(root, catalog) {
  const o = catalog.overview;
  const signals = [...catalog.signals, ...catalog.budgets];

  const view = clone('tpl-overview', {
    '[data-slot="title"]': o.title,
    '[data-slot="lede"]': paragraphs(o.lede),
    '[data-slot="components"]': list('tpl-component', o.components, component),
    '[data-slot="journey"]': list('tpl-journey-step', o.journey, (s) => journeyStep(s, o)),
    '[data-slot="facts"]': list('tpl-fact', o.baseline, fact),
    '[data-slot="terms"]': list('tpl-term', o.vocabulary, term),
    '[data-slot="injections"]': list('tpl-injection', o.injection_points, injection),
    '[data-slot="topics"]': list('tpl-topic', catalog.topics, topic),
    '[data-slot="queries"]': list('tpl-query-item', signals, query),
  });

  root.replaceChildren(view);
  wireDiagram(root);
}

// Clicking a box in the diagram opens the matching component and scrolls to it.
function wireDiagram(root) {
  root.querySelectorAll('[data-node]').forEach((node) => {
    const open = () => {
      const target = root.querySelector(`details[data-component="${node.dataset.node}"]`);
      if (!target) return;
      target.open = true;
      target.scrollIntoView({ behavior: 'smooth', block: 'center' });
      target.classList.add('flash');
      setTimeout(() => target.classList.remove('flash'), 1200);
    };
    node.addEventListener('click', open);
    node.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(); } });
  });
}

function component(c) {
  const emits = c.emits || [];
  return {
    '': { data: { component: c.id } },
    '.dot': { class: `dot-${c.kind}` },
    '.component-name': c.name,
    '.component-role': c.role,
    '.component-image': c.image + (c.port ? ` · :${c.port}` : ''),
    '[data-slot="detail"]': paragraphs(c.detail),
    '.emits': { hidden: emits.length === 0 },
    '[data-slot="metrics"]': emits.map((m) => clone('tpl-code', { '': m })),
  };
}

function journeyStep(s, o) {
  const where = o.components.find((c) => c.id === s.where);
  return {
    '.journey-n': s.n,
    '.journey-title': s.title,
    '.journey-where': s.where,
    '.journey-head .dot': { class: `dot-${where ? where.kind : 'infra'}` },
    '.journey-detail': s.detail,
  };
}

const fact = (f) => ({
  '.fact-label': f.label,
  '.fact-value': f.value,
  '.fact-note': f.note,
});

const term = (t) => ({
  '.term-name': t.term,
  '.term-plain': t.plain,
  '.term-here-text': t.here,
});

const injection = (p) => ({
  '.injection-head': p.fault,
  '.injection-where': p.where,
  '.injection-effect': p.effect,
  '[data-slot="visible"]': list('tpl-li', p.visible_in, (v) => ({ '': v })),
  '[data-slot="invisible"]': list('tpl-li', p.invisible_in, (v) => ({ '': v })),
});

const topic = (t) => ({
  summary: t.title,
  '[data-slot="body"]': paragraphs(t.body),
});

const query = (sig) => ({
  '.query-name': sig.name,
  '.query-target': {
    hidden: !sig.target,
    text: sig.target ? `target ${(sig.target * 100).toFixed(sig.target >= 0.99 ? 1 : 0)}%` : '',
  },
  '.query-spec': sig.spec,
  '.query-why': sig.why,
  '.promql': sig.query,
  '.query-derivation': { hidden: !sig.derivation, text: sig.derivation || '' },
});
