import { check, sleep } from 'k6';
import {
  ERA_LEGACY,
  ERA_MODERN,
  callTool,
  closeSession,
  listTools,
  openSession,
} from './lib/mcp.js';

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:8765/mcp';
const ACCESS_TOKEN = __ENV.AGENTCY_HTTP_TOKEN || '';
const PROFILE = __ENV.K6_PROFILE || 'smoke';
const ERA_FILTER = __ENV.K6_ERA || 'both';
const SUMMARY_EXPORT = __ENV.K6_SUMMARY_EXPORT || '';

const WORKER_POOL_SIZE = 10;

function validateConfig() {
  if (!ACCESS_TOKEN || ACCESS_TOKEN.length < 32) {
    throw new Error('AGENTCY_HTTP_TOKEN must be set to a secret of at least 32 characters');
  }
  if (PROFILE !== 'smoke' && PROFILE !== 'load') {
    throw new Error(`Unsupported K6_PROFILE: ${PROFILE} (expected smoke or load)`);
  }
  if (ERA_FILTER !== 'both' && ERA_FILTER !== ERA_MODERN && ERA_FILTER !== ERA_LEGACY) {
    throw new Error(`Unsupported K6_ERA: ${ERA_FILTER} (expected both, ${ERA_MODERN}, or ${ERA_LEGACY})`);
  }
}

function buildScenarios() {
  const isLoad = PROFILE === 'load';
  const scenarios = {};
  const steadyConfig = isLoad
    ? {
        executor: 'ramping-vus',
        startVUs: 0,
        stages: [
          { duration: '5s', target: 10 },
          { duration: '30s', target: 10 },
          { duration: '5s', target: 0 },
        ],
        gracefulRampDown: '5s',
      }
    : {
        executor: 'constant-vus',
        vus: 2,
        duration: '10s',
      };

  if (ERA_FILTER === 'both' || ERA_FILTER === ERA_MODERN) {
    scenarios.modern_steady = Object.assign({ exec: 'modernSteady' }, steadyConfig);
  }
  if (ERA_FILTER === 'both' || ERA_FILTER === ERA_LEGACY) {
    scenarios.legacy_steady = Object.assign({ exec: 'legacySteady' }, steadyConfig);
  }
  scenarios.lifecycle = isLoad
    ? { executor: 'constant-vus', exec: 'lifecycleFlow', vus: 2, duration: '35s' }
    : { executor: 'constant-vus', exec: 'lifecycleFlow', vus: 1, duration: '10s' };
  return scenarios;
}

function buildThresholds() {
  const thresholds = {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
    'mcp_tool_duration{operation:read_messages}': ['p(95)<100'],
    'mcp_tool_duration{operation:conversation_status}': ['p(95)<100'],
    'mcp_tool_duration{operation:post_message}': ['p(95)<100'],
    'mcp_tool_duration{operation:list_closed_conversations}': ['p(95)<100'],
    'mcp_tool_duration{operation:create_conversation}': ['p(95)<150'],
    'mcp_tool_duration{operation:join_conversation}': ['p(95)<150'],
  };
  if (ERA_FILTER === 'both' || ERA_FILTER === ERA_MODERN) {
    thresholds[`mcp_tool_duration{era:${ERA_MODERN}}`] = ['p(95)<100'];
  }
  if (ERA_FILTER === 'both' || ERA_FILTER === ERA_LEGACY) {
    thresholds[`mcp_tool_duration{era:${ERA_LEGACY}}`] = ['p(95)<100'];
  }
  return thresholds;
}

validateConfig();

export const options = {
  scenarios: buildScenarios(),
  thresholds: buildThresholds(),
};

function seedSteadyRoom(session, label) {
  const invitees = [];
  for (let i = 1; i <= WORKER_POOL_SIZE; i += 1) {
    invitees.push(`W${i}`);
  }
  const created = callTool(session, 'create_conversation', {
    brief: `k6 steady benchmark (${label})`,
    your_name: 'Lead',
    invitees,
  });
  if (!created || !created.invitations) {
    throw new Error(`Failed to create steady room for ${label}`);
  }
  const tokens = [];
  for (const name of invitees) {
    const joined = callTool(session, 'join_conversation', {
      code: created.invitations[name],
    });
    if (!joined || !joined.token) {
      throw new Error(`Failed to join worker ${name} in ${label}`);
    }
    tokens.push(joined.token);
  }
  const seedPost = callTool(session, 'post_message', {
    token: created.token,
    submission_id: `seed-${label}-1`,
    body: `Initial seed message for ${label}`,
    kind: 'note',
  });
  return {
    conversation: created.conversation,
    leadToken: created.token,
    workerTokens: tokens,
    initialCursor: seedPost && seedPost.message_id ? seedPost.message_id : 0,
  };
}

function seedClosedRoom(session) {
  const created = callTool(session, 'create_conversation', {
    brief: 'k6 closed history seed',
    your_name: 'Lead',
    invitees: ['Worker'],
  });
  if (!created || !created.token) {
    throw new Error('Failed to create closed seed conversation');
  }
  callTool(session, 'post_message', {
    token: created.token,
    submission_id: 'seed-closed-1',
    body: 'Completed task with verified evidence.',
    kind: 'note',
  });
  callTool(session, 'close_conversation', {
    token: created.token,
    summary: 'Seeded closed conversation for k6 read benchmarks.',
  });
}

export function setup() {
  const admin = openSession(BASE_URL, ACCESS_TOKEN, ERA_MODERN);
  seedClosedRoom(admin);
  const data = { rooms: {} };
  if (ERA_FILTER === 'both' || ERA_FILTER === ERA_MODERN) {
    data.rooms[ERA_MODERN] = seedSteadyRoom(admin, ERA_MODERN);
  }
  if (ERA_FILTER === 'both' || ERA_FILTER === ERA_LEGACY) {
    data.rooms[ERA_LEGACY] = seedSteadyRoom(admin, ERA_LEGACY);
  }
  return data;
}

let modernSession = null;
let modernCursor = 0;

let legacySession = null;
let legacyCursor = 0;

function runSteadyIteration(session, room, cursor) {
  const token = room.workerTokens[(__VU - 1) % room.workerTokens.length];
  const subId = `k6-${session.era}-vu${__VU}-it${__ITER}`;
  const body = `Steady note from vu=${__VU} iter=${__ITER} era=${session.era}`;

  callTool(session, 'post_message', {
    token,
    submission_id: subId,
    body,
    kind: 'note',
  });

  const page = callTool(session, 'read_messages', {
    token,
    after: cursor,
    wait_seconds: 0,
  });
  let nextCursor = cursor;
  if (page && Array.isArray(page.messages) && page.messages.length > 0) {
    const lastMsg = page.messages[page.messages.length - 1];
    if (lastMsg && typeof lastMsg.id === 'number' && lastMsg.id > nextCursor) {
      nextCursor = lastMsg.id;
    }
  }

  callTool(session, 'conversation_status', { token });
  callTool(session, 'list_closed_conversations', {});

  if (__ITER % 5 === 0) {
    listTools(session);
  }
  return nextCursor;
}

export function modernSteady(data) {
  if (!modernSession) {
    modernSession = openSession(BASE_URL, ACCESS_TOKEN, ERA_MODERN);
    modernCursor = data.rooms[ERA_MODERN].initialCursor;
  }
  modernCursor = runSteadyIteration(modernSession, data.rooms[ERA_MODERN], modernCursor);
  sleep(0.05);
}

export function legacySteady(data) {
  if (!legacySession) {
    legacySession = openSession(BASE_URL, ACCESS_TOKEN, ERA_LEGACY);
    legacyCursor = data.rooms[ERA_LEGACY].initialCursor;
  }
  legacyCursor = runSteadyIteration(legacySession, data.rooms[ERA_LEGACY], legacyCursor);
  sleep(0.05);
}

function selectLifecycleEra() {
  if (ERA_FILTER === ERA_MODERN || ERA_FILTER === ERA_LEGACY) {
    return ERA_FILTER;
  }
  return __ITER % 2 === 0 ? ERA_MODERN : ERA_LEGACY;
}

export function lifecycleFlow() {
  const era = selectLifecycleEra();
  const session = openSession(BASE_URL, ACCESS_TOKEN, era);
  try {
    const created = callTool(session, 'create_conversation', {
      brief: `k6 lifecycle vu=${__VU} iter=${__ITER}`,
      your_name: 'Lead',
      invitees: ['Worker'],
    });
    if (!created || !created.token || !created.invitations || !created.invitations.Worker) {
      return;
    }
    const joined = callTool(session, 'join_conversation', {
      code: created.invitations.Worker,
    });
    if (!joined || !joined.token) {
      return;
    }
    const posted = callTool(session, 'post_message', {
      token: joined.token,
      submission_id: `lc-${era}-vu${__VU}-it${__ITER}`,
      body: `Lifecycle finding from vu=${__VU} iter=${__ITER}`,
      kind: 'note',
    });
    const read = callTool(session, 'read_messages', {
      token: created.token,
      after: 0,
      wait_seconds: 0,
    });
    check(read, {
      'lifecycle lead reads worker message': (r) =>
        Boolean(r && Array.isArray(r.messages) && posted && r.messages.some((m) => m.id === posted.message_id)),
    });
    callTool(session, 'conversation_status', { token: created.token });
    callTool(session, 'close_conversation', {
      token: created.token,
      summary: `Lifecycle complete vu=${__VU} iter=${__ITER}`,
    });
  } finally {
    closeSession(session);
  }
  sleep(0.1);
}

function formatMetricRow(name, metric) {
  if (!metric || !metric.values) {
    return '';
  }
  const v = metric.values;
  const p50 = v.med !== undefined ? v.med.toFixed(2) : '-';
  const p95 = v['p(95)'] !== undefined ? v['p(95)'].toFixed(2) : '-';
  const max = v.max !== undefined ? v.max.toFixed(2) : '-';
  return `  ${name.padEnd(56)} p50=${p50.padStart(6)}ms  p95=${p95.padStart(6)}ms  max=${max.padStart(7)}ms`;
}

export function handleSummary(data) {
  const lines = [
    '',
    `agentcy HTTP MCP k6 summary (profile=${PROFILE}, era=${ERA_FILTER})`,
    '------------------------------------------------------------------------',
  ];
  const keys = Object.keys(data.metrics)
    .filter((k) => k.startsWith('mcp_tool_duration'))
    .sort();
  for (const key of keys) {
    const row = formatMetricRow(key, data.metrics[key]);
    if (row) {
      lines.push(row);
    }
  }
  const failed = data.metrics.http_req_failed;
  const reqs = data.metrics.http_reqs;
  if (reqs && reqs.values) {
    lines.push(
      `  total_requests=${reqs.values.count}  rps=${reqs.values.rate.toFixed(1)}  fail_rate=${
        failed ? (failed.values.rate * 100).toFixed(2) : '0.00'
      }%`
    );
  }
  lines.push('');

  const result = { stdout: `${lines.join('\n')}\n` };
  if (SUMMARY_EXPORT) {
    result[SUMMARY_EXPORT] = JSON.stringify(data, null, 2);
  }
  return result;
}
