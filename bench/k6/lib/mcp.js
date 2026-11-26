import http from 'k6/http';
import { check } from 'k6';
import { Trend } from 'k6/metrics';

export const ERA_MODERN = '2026-07-28';
export const ERA_LEGACY = '2025-11-25';

const CLIENT_INFO = { name: 'agentcy-k6', version: '0.1.0' };
const ACCEPT_HEADER = 'application/json, text/event-stream';

export const mcpToolDuration = new Trend('mcp_tool_duration', true);

function getHeader(headers, targetName) {
  if (!headers) {
    return '';
  }
  const lower = targetName.toLowerCase();
  for (const key of Object.keys(headers)) {
    if (key.toLowerCase() === lower) {
      return headers[key] || '';
    }
  }
  return '';
}

function buildHeaders(session, method, toolName) {
  const headers = {
    Authorization: `Bearer ${session.token}`,
    Accept: ACCEPT_HEADER,
    'Content-Type': 'application/json',
  };
  if (session.era === ERA_MODERN) {
    headers['MCP-Protocol-Version'] = ERA_MODERN;
    if (method) {
      headers['MCP-Method'] = method;
    }
    if (toolName) {
      headers['MCP-Name'] = toolName;
    }
    return headers;
  }
  if (session.sessionId) {
    headers['Mcp-Session-Id'] = session.sessionId;
    headers['MCP-Protocol-Version'] = ERA_LEGACY;
  }
  return headers;
}

function parseSseBody(body) {
  let parsed = null;
  const lines = body.split('\n');
  for (const rawLine of lines) {
    const line = rawLine.replace(/\r$/, '');
    if (!line.startsWith('data:')) {
      continue;
    }
    const payload = line.slice(5).trim();
    if (!payload) {
      continue;
    }
    try {
      const msg = JSON.parse(payload);
      if (msg && (msg.result !== undefined || msg.error !== undefined)) {
        parsed = msg;
      }
    } catch (_err) {
      // Ignore non-JSON SSE data frames.
    }
  }
  return parsed;
}

export function parseRpcResponse(res) {
  if (!res || !res.body) {
    return null;
  }
  const contentType = getHeader(res.headers, 'content-type').toLowerCase();
  if (contentType.startsWith('text/event-stream')) {
    return parseSseBody(res.body);
  }
  try {
    return JSON.parse(res.body);
  } catch (_err) {
    return null;
  }
}

function parseToolPayload(rpc) {
  if (!rpc || rpc.error || !rpc.result || rpc.result.isError) {
    return null;
  }
  const content = rpc.result.content;
  if (!Array.isArray(content) || content.length === 0 || !content[0].text) {
    return null;
  }
  try {
    return JSON.parse(content[0].text);
  } catch (_err) {
    return null;
  }
}

function nextRequestId(session) {
  const id = session.nextId;
  session.nextId += 1;
  return id;
}

function modernMeta() {
  return {
    'io.modelcontextprotocol/protocolVersion': ERA_MODERN,
    'io.modelcontextprotocol/clientInfo': CLIENT_INFO,
    'io.modelcontextprotocol/clientCapabilities': {},
  };
}

export function openSession(baseUrl, token, era) {
  const session = {
    baseUrl,
    token,
    era,
    sessionId: null,
    nextId: 1,
  };
  if (era === ERA_MODERN) {
    return session;
  }

  const initBody = JSON.stringify({
    jsonrpc: '2.0',
    id: nextRequestId(session),
    method: 'initialize',
    params: {
      protocolVersion: ERA_LEGACY,
      capabilities: {},
      clientInfo: CLIENT_INFO,
    },
  });
  const initRes = http.post(baseUrl, initBody, {
    headers: buildHeaders(session, null, null),
    tags: { era, operation: 'initialize' },
  });
  const sessionId = getHeader(initRes.headers, 'mcp-session-id');
  const initRpc = parseRpcResponse(initRes);
  check(initRes, {
    'initialize status 200': (r) => r.status === 200,
    'initialize returns session id': () => sessionId.length > 0,
    'initialize rpc ok': () => Boolean(initRpc && initRpc.result && !initRpc.error),
  });
  session.sessionId = sessionId;

  const notifyBody = JSON.stringify({
    jsonrpc: '2.0',
    method: 'notifications/initialized',
  });
  const notifyRes = http.post(baseUrl, notifyBody, {
    headers: buildHeaders(session, null, null),
    tags: { era, operation: 'notifications/initialized' },
  });
  check(notifyRes, {
    'notifications/initialized accepted': (r) => r.status === 202 || r.status === 200,
  });

  return session;
}

export function closeSession(session) {
  if (!session || session.era !== ERA_LEGACY || !session.sessionId) {
    return;
  }
  http.del(session.baseUrl, null, {
    headers: buildHeaders(session, null, null),
    tags: { era: session.era, operation: 'session_close' },
  });
  session.sessionId = null;
}

export function listTools(session) {
  const params = session.era === ERA_MODERN ? { _meta: modernMeta() } : {};
  const body = JSON.stringify({
    jsonrpc: '2.0',
    id: nextRequestId(session),
    method: 'tools/list',
    params,
  });
  const tags = { era: session.era, operation: 'tools/list' };
  const res = http.post(session.baseUrl, body, {
    headers: buildHeaders(session, 'tools/list', null),
    tags,
  });
  mcpToolDuration.add(res.timings.duration, tags);
  const rpc = parseRpcResponse(res);
  check(res, {
    'tools/list status 200': (r) => r.status === 200,
    'tools/list returns tools': () => Boolean(rpc && rpc.result && Array.isArray(rpc.result.tools)),
  });
  return rpc;
}

export function callTool(session, name, args) {
  const rpcParams = {
    name,
    arguments: args || {},
  };
  if (session.era === ERA_MODERN) {
    rpcParams._meta = modernMeta();
  }
  const body = JSON.stringify({
    jsonrpc: '2.0',
    id: nextRequestId(session),
    method: 'tools/call',
    params: rpcParams,
  });
  const tags = { era: session.era, operation: name };
  const res = http.post(session.baseUrl, body, {
    headers: buildHeaders(session, 'tools/call', name),
    tags,
  });
  mcpToolDuration.add(res.timings.duration, tags);
  const rpc = parseRpcResponse(res);
  const payload = parseToolPayload(rpc);
  check(res, {
    [`${name} status 200`]: (r) => r.status === 200,
    [`${name} payload ok`]: () => payload !== null,
  });
  return payload;
}
