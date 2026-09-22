// Client-side view of the SLI. Everything this script measures is what the user would
// experience; everything /metrics measures is what the server believes happened. Both are
// deliberately flat at rest, so any divergence between them is caused by an injected
// fault rather than by the generator.
import http from 'k6/http';
import { check } from 'k6';

const TARGET = __ENV.TARGET || 'http://localhost:8080';
const RATE = Number(__ENV.RATE || 20);
const DURATION = __ENV.DURATION || '10m';
// A slice of the traffic is genuinely invalid input. It produces 4xx, which must never
// show up as a bad event in the availability SLI — that is the point of sending it.
const INVALID_RATE = Number(__ENV.INVALID_RATE || 0.02);

export const options = {
  scenarios: {
    steady: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: Math.max(10, RATE),
      maxVUs: Math.max(50, RATE * 10),
    },
  },
  // No thresholds: this generator must never fail the run, it has to keep pushing
  // traffic while the service is deliberately broken.
  thresholds: {},
};

export default function () {
  const invalid = Math.random() < INVALID_RATE;
  const body = invalid
    ? { item_id: '', qty: 0 }
    : { item_id: `sku-${Math.floor(Math.random() * 50)}`, qty: 1 + Math.floor(Math.random() * 3) };

  const res = http.post(`${TARGET}/checkout`, JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
    tags: { endpoint: 'checkout', expected: invalid ? 'client_error' : 'ok' },
  });

  check(res, { 'no 5xx': (r) => r.status > 0 && r.status < 500 });
}
