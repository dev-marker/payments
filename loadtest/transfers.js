// A load test of transfers, through the gateway, the way clients call it.
// setup() signs up the users. Then transfers arrive at a set rate, however
// slowly we answer: an open model, like real traffic.
import http from 'k6/http';
import { check, fail } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
// The gateway allows a user 120 requests a minute: at 40 transfers a
// second, 50 users send about 50 each.
const USERS = 50;
const PASSWORD = 'correct horse battery staple';

export const options = {
  setupTimeout: '2m',
  scenarios: {
    transfers: {
      executor: 'ramping-arrival-rate',
      startRate: 5,
      timeUnit: '1s',
      preAllocatedVUs: 20,
      maxVUs: 100,
      stages: [
        { duration: '30s', target: 40 }, // up to 40 transfers a second
        { duration: '45s', target: 40 }, // hold it there
        { duration: '15s', target: 0 }, // and back down
      ],
    },
  },
  // If any of these fails, so does the run, with an exit code CI can see.
  thresholds: {
    'http_req_failed{name:transfer}': ['rate<0.01'],
    'http_req_duration{name:transfer}': ['p(95)<200', 'p(99)<500'],
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['med', 'p(95)', 'p(99)', 'max'],
};

// Every request sends JSON, a token if there is one, a new Idempotency-Key,
// and a name, so its numbers get a line of their own.
function params(token, name) {
  const headers = { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() };
  if (token) headers.Authorization = `Bearer ${token}`;
  return { headers, tags: { name } };
}

// post sends a request in setup, where anything unexpected stops the test.
function post(path, body, token, name, status) {
  const res = http.post(`${BASE_URL}${path}`, JSON.stringify(body), params(token, name));
  if (!check(res, { [`${name}: ${status}`]: (r) => r.status === status })) {
    fail(`${name}: got ${res.status}, want ${status}: ${res.body}`);
  }
  return res;
}

// Each user registers, logs in, opens a wallet, and deposits $1,000.
export function setup() {
  const run = Date.now();
  const users = [];
  for (let i = 0; i < USERS; i++) {
    const email = `load-${run}-${i}@example.com`;
    post('/v1/auth/register', { email, password: PASSWORD }, null, 'register', 201);
    const token = post('/v1/auth/login', { email, password: PASSWORD }, null, 'login', 200)
      .json('access_token');
    const wallet = post('/v1/wallets', { currency: 'USD' }, token, 'wallet', 201).json('id');
    post(`/v1/wallets/${wallet}/deposits`, { amount: 100000, currency: 'USD' }, token, 'deposit', 201);
    users.push({ token, wallet });
  }
  return users;
}

// One iteration is one transfer of a dollar, to someone else.
export default function (users) {
  const i = Math.floor(Math.random() * users.length);
  const j = (i + 1 + Math.floor(Math.random() * (users.length - 1))) % users.length;
  const res = http.post(`${BASE_URL}/v1/transfers`, JSON.stringify({
    from: users[i].wallet, to: users[j].wallet, amount: 100, currency: 'USD',
  }), params(users[i].token, 'transfer'));
  check(res, { 'transfer: 202': (r) => r.status === 202 });
}

// k6's own summary is long. This one fits a terminal: the transfers, their
// latency, and every threshold, passed or failed.
export function handleSummary(data) {
  const m = data.metrics;
  const ms = (v) => `${v.toFixed(0)} ms`;
  const d = m['http_req_duration{name:transfer}'].values;
  const failed = m['http_req_failed{name:transfer}'].values.rate * 100;
  const lines = [
    '',
    `transfers  ${m.iterations.values.count}, ${failed.toFixed(2)}% failed`,
    `latency    med ${ms(d.med)}, p95 ${ms(d['p(95)'])}, p99 ${ms(d['p(99)'])}, max ${ms(d.max)}`,
    '',
  ];
  for (const [name, metric] of Object.entries(m)) {
    for (const [rule, { ok }] of Object.entries(metric.thresholds || {})) {
      lines.push(`${ok ? '✓' : '✗'} ${name}: ${rule}`);
    }
  }
  return { stdout: lines.join('\n') + '\n' };
}
