// Every call to the console's own API. The console proxies Prometheus and the services'
// admin APIs, so this file never talks to anything else.

async function request(path, options = {}) {
  const res = await fetch(path, options);
  const text = await res.text();
  let body = null;
  if (text) {
    try { body = JSON.parse(text); } catch { body = { error: text }; }
  }
  if (!res.ok) throw new Error((body && body.error) || `${res.status} ${res.statusText}`);
  return body;
}

const qs = (params) => new URLSearchParams(params).toString();

export const api = {
  catalog: () => request('/api/catalog'),

  summary: (job) => request(`/api/summary?${qs({ job })}`),

  series: (key, job, minutes) => request(`/api/series?${qs({ key, job, minutes })}`),

  // The stretches of time a fault was active, read from the service's own gauge so that
  // injections made outside this page still show up on the charts.
  annotations: (job, minutes) => request(`/api/annotations?${qs({ job, minutes })}`),

  faults: (service) => request(`/api/faults?${qs({ service })}`),

  injectFault: (service, fault) => request(`/api/faults?${qs({ service })}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(fault),
  }),

  removeFault: (service, id) =>
    request(`/api/faults/${encodeURIComponent(id)}?${qs({ service })}`, { method: 'DELETE' }),

  clearFaults: (service) => request(`/api/faults?${qs({ service })}`, { method: 'DELETE' }),

  runScenario: (service, id) =>
    request(`/api/scenarios/${encodeURIComponent(id)}/run?${qs({ service })}`, { method: 'POST' }),

  crash: (service) => request(`/api/crash?${qs({ service })}`, { method: 'POST' }),
};
