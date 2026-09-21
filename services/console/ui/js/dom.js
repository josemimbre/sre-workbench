// A very small rendering helper, so the markup can live in <template> elements in the
// HTML instead of inside JavaScript strings.
//
// Two things follow from that and both matter here. Values are written with textContent,
// so nothing has to be escaped by hand and no catalog text can ever be interpreted as
// markup. And updating a value touches one text node instead of rebuilding a subtree,
// which is what stops a number that refreshes twice a second from looking like it is
// flickering.

// clone stamps out a template and fills it in.
//
//   clone('tpl-tile', {
//     '.tile-name': sig.name,                        // textContent
//     '.tile-value': { text: v, class: 'is-good' },  // text plus attributes
//     '.remove':     { on: { click: handler } },     // event listeners
//     '':            { data: { status: 'warn' } },   // '' targets the root node
//   })
export function clone(templateId, bindings = {}) {
  const tpl = document.getElementById(templateId);
  if (!tpl) throw new Error(`missing template #${templateId}`);
  const node = tpl.content.firstElementChild.cloneNode(true);
  return apply(node, bindings);
}

export function apply(node, bindings = {}) {
  for (const [selector, value] of Object.entries(bindings)) {
    const target = selector === '' ? node : node.querySelector(selector);
    if (target) bind(target, value);
  }
  return node;
}

function bind(target, value) {
  if (value === null || value === undefined) return;

  if (value instanceof Node) { target.replaceChildren(value); return; }
  if (Array.isArray(value)) { target.replaceChildren(...value); return; }

  if (typeof value === 'object') {
    for (const [key, v] of Object.entries(value)) {
      switch (key) {
        case 'text': target.textContent = v; break;
        // `html` is only ever handed markup this code built itself, never catalog text.
        case 'html': target.innerHTML = v; break;
        case 'children': target.replaceChildren(...v); break;
        case 'on': for (const [event, fn] of Object.entries(v)) target.addEventListener(event, fn); break;
        case 'data': Object.assign(target.dataset, v); break;
        case 'class': target.classList.add(...[].concat(v)); break;
        case 'hidden': target.hidden = Boolean(v); break;
        case 'style': Object.assign(target.style, v); break;
        default:
          if (v === false || v === null || v === undefined) target.removeAttribute(key);
          else target.setAttribute(key, v === true ? '' : v);
      }
    }
    return;
  }

  target.textContent = value;
}

// list maps items to cloned nodes, ready to hand to replaceChildren.
export function list(templateId, items, fill) {
  return items.map((item, i) => clone(templateId, fill(item, i)));
}

// paragraphs turns an array of strings into <p> nodes.
export function paragraphs(texts, className = '') {
  return texts.map((t) => {
    const p = document.createElement('p');
    p.textContent = t;
    if (className) p.className = className;
    return p;
  });
}

export function el(tag, props = {}) {
  return apply(document.createElement(tag), { '': props });
}

export function mount(host, nodes) {
  host.replaceChildren(...[].concat(nodes));
  return host;
}
