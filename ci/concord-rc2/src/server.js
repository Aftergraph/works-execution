import { createServer } from "node:http";
import { timingSafeEqual } from "node:crypto";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { dirname, isAbsolute } from "node:path";
import { ConcordEngine } from "./engine.js";
import { JsonStore } from "./store.js";
import { TrustGatewayAuditAdapter } from "./adapters/trust-gateway.js";
import { HttpVerifierProviderAdapter } from "./adapters/verifier-provider.js";

const here = dirname(fileURLToPath(import.meta.url));
const root = dirname(here);
const publicDir = join(root, "public");
const host = process.env.CONCORD_HOST ?? "127.0.0.1";
const port = Number(process.env.CONCORD_PORT ?? 8787);
const configuredDataPath = process.env.CONCORD_DATA_PATH ?? ".data/concord.json";
const dataPath = isAbsolute(configuredDataPath) ? configuredDataPath : join(root, configuredDataPath);
const token = process.env.CONCORD_TOKEN ?? "";
const loopbackHosts = new Set(["127.0.0.1","localhost","::1"]);
if (!token && !loopbackHosts.has(host) && process.env.CONCORD_ALLOW_UNAUTHENTICATED_REMOTE !== "1") {
  throw new Error("remote Concord binding requires CONCORD_TOKEN or explicit CONCORD_ALLOW_UNAUTHENTICATED_REMOTE=1");
}
const signingKeyPath = process.env.CONCORD_SIGNING_KEY_PATH ?? "";
const receiptPrivateKeyPem = signingKeyPath ? await readFile(signingKeyPath, "utf8") : null;
const governanceMode = process.env.CONCORD_GOVERNANCE_MODE ?? "optional";
const verificationMode = process.env.CONCORD_VERIFICATION_MODE ?? "reference";

let authorityAdapter = null;
if (governanceMode === "live") {
  const baseUrl = process.env.CONCORD_TRUST_GATEWAY_URL ?? "";
  const gatewayToken = process.env.CONCORD_TRUST_GATEWAY_TOKEN ?? "";
  if (!baseUrl || !gatewayToken) throw new Error("live governance requires CONCORD_TRUST_GATEWAY_URL and CONCORD_TRUST_GATEWAY_TOKEN");
  authorityAdapter = new TrustGatewayAuditAdapter({
    baseUrl,
    token: gatewayToken,
    timeoutMs: Number(process.env.CONCORD_TRUST_GATEWAY_TIMEOUT_MS ?? 5000)
  });
}

let verifierAdapter = null;
if (verificationMode === "live") {
  const baseUrl = process.env.CONCORD_VERIFIER_URL ?? "";
  if (!baseUrl) throw new Error("live verification requires CONCORD_VERIFIER_URL");
  verifierAdapter = new HttpVerifierProviderAdapter({
    baseUrl,
    token: process.env.CONCORD_VERIFIER_TOKEN ?? "",
    path: process.env.CONCORD_VERIFIER_PATH ?? "/v1/verify",
    timeoutMs: Number(process.env.CONCORD_VERIFIER_TIMEOUT_MS ?? 10000)
  });
}

const engine = new ConcordEngine(new JsonStore(dataPath), () => new Date(), {
  receiptPrivateKeyPem,
  receiptKeyId: process.env.CONCORD_SIGNING_KEY_ID ?? null,
  governanceMode,
  verificationMode,
  authorityAdapter,
  verifierAdapter
});
const clients = new Set();
const startedAt = Date.now();
const counters = { requests:0, unauthorized:0, server_errors:0 };

engine.subscribe((event) => {
  const line = `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`;
  for (const res of clients) res.write(line);
});

function json(res, status, body) {
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    "x-content-type-options": "nosniff",
    "x-frame-options": "DENY",
    "referrer-policy": "no-referrer"
  });
  res.end(JSON.stringify(body, null, 2));
}

function safeEqual(left, right) {
  const a = Buffer.from(String(left), "utf8");
  const b = Buffer.from(String(right), "utf8");
  return a.length === b.length && timingSafeEqual(a, b);
}

function authorized(req) {
  if (!token) return true;
  return safeEqual(req.headers.authorization ?? "", `Bearer ${token}`);
}

async function body(req) {
  const parts = [];
  for await (const chunk of req) parts.push(chunk);
  const raw = Buffer.concat(parts).toString("utf8");
  if (!raw) return {};
  if (raw.length > 2_000_000) throw new Error("body too large");
  return JSON.parse(raw);
}

async function staticFile(pathname, res) {
  const requested = pathname === "/" ? "index.html" : pathname.replace(/^\/+/, "");
  const safe = normalize(requested).replace(/^([.][.][/\\])+/, "");
  const file = join(publicDir, safe);
  if (!file.startsWith(publicDir)) return false;
  try {
    const content = await readFile(file);
    const type = {
      ".html": "text/html; charset=utf-8",
      ".js": "text/javascript; charset=utf-8",
      ".css": "text/css; charset=utf-8"
    }[extname(file)] ?? "application/octet-stream";
    res.writeHead(200, {
      "content-type": type,
      "x-content-type-options": "nosniff",
      "x-frame-options": "DENY",
      "referrer-policy": "no-referrer",
      "content-security-policy": "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'"
    });
    res.end(content);
    return true;
  } catch {
    return false;
  }
}

const server = createServer(async (req, res) => {
  counters.requests += 1;
  res.on("finish", () => {
    if (res.statusCode >= 500) counters.server_errors += 1;
  });
  const url = new URL(req.url, `http://${req.headers.host ?? "localhost"}`);
  try {
    if (req.method === "GET" && url.pathname === "/health") {
      const snap = await engine.snapshot();
      return json(res, 200, {
        ok:true,
        service:"concord",
        version:"1.0.0-rc.1",
        revision:snap.revision,
        uptime_s:Math.floor((Date.now() - startedAt) / 1000)
      });
    }

    if (req.method === "GET" && url.pathname === "/ready") {
      const snap = await engine.snapshot();
      return json(res, 200, {
        ok:true,
        version:"1.0.0-rc.1",
        revision:snap.revision,
        governance_mode:governanceMode,
        verification_mode:verificationMode
      });
    }

    if (req.method === "GET" && url.pathname === "/metrics") {
      if (token && !authorized(req)) {
        counters.unauthorized += 1;
        return json(res, 401, {ok:false,error:"unauthorized"});
      }
      const snap = await engine.snapshot();
      return json(res, 200, {
        service:"concord",
        version:"1.0.0-rc.1",
        requests_total:counters.requests,
        unauthorized_total:counters.unauthorized,
        server_errors_total:counters.server_errors,
        revision:snap.revision,
        sse_clients:clients.size,
        replicas:snap.replicas.length,
        assertions:snap.assertions.length,
        conflicts_hold:snap.conflicts.filter((c) => c.state === "HOLD").length
      });
    }

    if (url.pathname.startsWith("/v1/") && !authorized(req)) {
      counters.unauthorized += 1;
      return json(res, 401, { ok:false, error:"unauthorized" });
    }

    if (req.method === "GET" && url.pathname === "/v1/state") {
      return json(res, 200, await engine.snapshot());
    }

    if (req.method === "GET" && url.pathname === "/v1/export") {
      return json(res, 200, await engine.snapshot());
    }

    if (req.method === "GET" && url.pathname === "/v1/events") {
      res.writeHead(200, {
        "content-type": "text/event-stream",
        "cache-control": "no-cache",
        connection: "keep-alive"
      });
      res.write(": concord connected\n\n");
      clients.add(res);
      req.on("close", () => clients.delete(res));
      return;
    }

    if (req.method === "GET" && url.pathname === "/v1/sync/pull") {
      const replica_id = url.searchParams.get("replica_id");
      const since = Number(url.searchParams.get("since") ?? 0);
      const result = await engine.pull(replica_id, since);
      return json(res, result.status ?? 200, result);
    }

    if (req.method === "POST" && url.pathname === "/v1/replicas") {
      const result = await engine.registerReplica(await body(req));
      return json(res, result.ok ? 201 : (result.status ?? 400), result);
    }

    if (req.method === "POST" && url.pathname === "/v1/sync/push") {
      const result = await engine.push(await body(req));
      return json(res, result.status ?? 200, result);
    }

    if (req.method === "POST" && url.pathname === "/v1/sync/ack") {
      const result = await engine.acknowledgeCheckpoint(await body(req));
      return json(res, result.status ?? 200, result);
    }

    if (req.method === "POST" && url.pathname === "/v1/verification-links") {
      const payload = await body(req);
      const link = payload.link ?? payload;
      const result = await engine.addVerificationLink(link, payload.signature ?? null);
      return json(res, result.status ?? (result.ok ? 201 : 400), result);
    }

    const rotationMatch = url.pathname.match(/^\/v1\/replicas\/([^/]+)\/key-rotation$/);
    if (req.method === "POST" && rotationMatch) {
      const payload = await body(req);
      if (payload.rotation?.replica_id !== rotationMatch[1]) {
        return json(res, 400, { ok:false, errors:["rotation replica_id must match URL replica"] });
      }
      const result = await engine.rotateReplicaKey(payload);
      return json(res, result.status ?? (result.ok ? 200 : 400), result);
    }

    const conflictMatch = url.pathname.match(/^\/v1\/conflicts\/([^/]+)\/resolve$/);
    if (req.method === "POST" && conflictMatch) {
      const result = await engine.resolveConflict({ conflict_id: conflictMatch[1], ...(await body(req)) });
      return json(res, result.status ?? 200, result);
    }

    if (req.method === "GET" && await staticFile(url.pathname, res)) return;
    json(res, 404, { ok: false, error: "not_found" });
  } catch (error) {
    json(res, error instanceof SyntaxError ? 400 : 500, { ok: false, error: error.message });
  }
});

server.listen(port, host, () => {
  console.log(`Concord listening on http://${host}:${port}`);
});

let closing = false;
function gracefulClose(signal) {
  if (closing) return;
  closing = true;
  console.log(`Concord received ${signal}; closing`);
  for (const res of clients) {
    try { res.end(); } catch {}
  }
  server.close((error) => {
    if (error) {
      console.error(error);
      process.exitCode = 1;
    }
  });
}

process.on("SIGTERM", () => gracefulClose("SIGTERM"));
process.on("SIGINT", () => gracefulClose("SIGINT"));
