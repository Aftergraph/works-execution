import { DurableObject } from "cloudflare:workers";
import { ConcordEngine } from "../engine.js";
import { DurableObjectStore } from "./store.js";
import { TrustGatewayAuditAdapter } from "../adapters/trust-gateway.js";
import { HttpVerifierProviderAdapter } from "../adapters/verifier-provider.js";

function json(status, body, extra = {}) {
  return new Response(JSON.stringify(body, null, 2), {
    status,
    headers:{
      "content-type":"application/json; charset=utf-8",
      "cache-control":"no-store",
      "x-content-type-options":"nosniff",
      ...extra
    }
  });
}

async function requestJson(request) {
  const text = await request.text();
  if (!text) return {};
  return JSON.parse(text);
}

export class ConcordState extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    this.ctx = ctx;
    this.env = env;
    this.startedAt = Date.now();
    this.clients = new Set();
    this.counters = {requests:0, errors:0};

    const governanceMode = env.CONCORD_GOVERNANCE_MODE ?? "required";
    const verificationMode = env.CONCORD_VERIFICATION_MODE ?? "registered-signed";

    let authorityAdapter = null;
    if (governanceMode === "live" && env.CONCORD_TRUST_GATEWAY_URL) {
      authorityAdapter = new TrustGatewayAuditAdapter({
        baseUrl:env.CONCORD_TRUST_GATEWAY_URL,
        token:env.CONCORD_TRUST_GATEWAY_TOKEN ?? "",
        timeoutMs:Number(env.CONCORD_TRUST_GATEWAY_TIMEOUT_MS ?? 5000)
      });
    }

    let verifierAdapter = null;
    if (verificationMode === "live" && env.CONCORD_VERIFIER_URL) {
      verifierAdapter = new HttpVerifierProviderAdapter({
        baseUrl:env.CONCORD_VERIFIER_URL,
        token:env.CONCORD_VERIFIER_TOKEN ?? "",
        path:env.CONCORD_VERIFIER_PATH ?? "/v1/verify",
        timeoutMs:Number(env.CONCORD_VERIFIER_TIMEOUT_MS ?? 10000)
      });
    }

    this.governanceMode = governanceMode;
    this.verificationMode = verificationMode;
    this.engine = new ConcordEngine(new DurableObjectStore(ctx.storage), () => new Date(), {
      receiptPrivateKeyPem:env.CONCORD_SIGNING_KEY_PEM ?? null,
      receiptKeyId:env.CONCORD_SIGNING_KEY_ID ?? null,
      governanceMode,
      verificationMode,
      authorityAdapter,
      verifierAdapter
    });

    this.engine.subscribe((event) => {
      const frame = `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`;
      for (const writer of this.clients) {
        writer.write(frame).catch(() => this.clients.delete(writer));
      }
    });
  }

  async fetch(request) {
    this.counters.requests += 1;
    const url = new URL(request.url);
    try {
      if (request.method === "GET" && url.pathname === "/health") {
        const snap = await this.engine.snapshot();
        return json(200,{
          ok:true,
          service:"concord",
          runtime:"cloudflare-durable-object",
          version:"1.0.0-rc.2",
          revision:snap.revision,
          uptime_s:Math.floor((Date.now()-this.startedAt)/1000)
        });
      }

      if (request.method === "GET" && url.pathname === "/ready") {
        const snap = await this.engine.snapshot();
        return json(200,{
          ok:true,
          version:"1.0.0-rc.2",
          revision:snap.revision,
          governance_mode:this.governanceMode,
          verification_mode:this.verificationMode,
          storage:"durable-object"
        });
      }

      if (request.method === "GET" && url.pathname === "/metrics") {
        const snap = await this.engine.snapshot();
        return json(200,{
          service:"concord",
          runtime:"cloudflare-durable-object",
          version:"1.0.0-rc.2",
          requests_total:this.counters.requests,
          errors_total:this.counters.errors,
          revision:snap.revision,
          sse_clients:this.clients.size,
          replicas:snap.replicas.length,
          assertions:snap.assertions.length,
          conflicts_hold:snap.conflicts.filter((c)=>c.state==="HOLD").length
        });
      }

      if (request.method === "GET" && url.pathname === "/v1/state") {
        return json(200,await this.engine.snapshot());
      }

      if (request.method === "GET" && url.pathname === "/v1/export") {
        return json(200,await this.engine.snapshot());
      }

      if (request.method === "GET" && url.pathname === "/v1/events") {
        const {readable,writable}=new TransformStream();
        const writer=writable.getWriter();
        await writer.write(new TextEncoder().encode(": concord connected\n\n"));
        const textWriter={
          write:(text)=>writer.write(new TextEncoder().encode(text))
        };
        this.clients.add(textWriter);
        request.signal.addEventListener("abort",()=>{
          this.clients.delete(textWriter);
          writer.close().catch(()=>{});
        });
        return new Response(readable,{
          headers:{
            "content-type":"text/event-stream",
            "cache-control":"no-cache, no-store",
            "connection":"keep-alive"
          }
        });
      }

      if (request.method === "GET" && url.pathname === "/v1/sync/pull") {
        const result=await this.engine.pull(url.searchParams.get("replica_id"),Number(url.searchParams.get("since")??0));
        return json(result.status??200,result);
      }

      if (request.method === "POST" && url.pathname === "/v1/replicas") {
        const result=await this.engine.registerReplica(await requestJson(request));
        return json(result.ok?201:(result.status??400),result);
      }

      if (request.method === "POST" && url.pathname === "/v1/sync/push") {
        const result=await this.engine.push(await requestJson(request));
        return json(result.status??200,result);
      }

      if (request.method === "POST" && url.pathname === "/v1/sync/ack") {
        const result=await this.engine.acknowledgeCheckpoint(await requestJson(request));
        return json(result.status??200,result);
      }

      if (request.method === "POST" && url.pathname === "/v1/verification-links") {
        const payload=await requestJson(request);
        const link=payload.link??payload;
        const result=await this.engine.addVerificationLink(link,payload.signature??null);
        return json(result.status??(result.ok?201:400),result);
      }

      const rotation=url.pathname.match(/^\/v1\/replicas\/([^/]+)\/key-rotation$/);
      if (request.method==="POST" && rotation) {
        const payload=await requestJson(request);
        if (payload.rotation?.replica_id!==rotation[1]) {
          return json(400,{ok:false,errors:["rotation replica_id must match URL replica"]});
        }
        const result=await this.engine.rotateReplicaKey(payload);
        return json(result.status??(result.ok?200:400),result);
      }

      const conflict=url.pathname.match(/^\/v1\/conflicts\/([^/]+)\/resolve$/);
      if (request.method==="POST" && conflict) {
        const result=await this.engine.resolveConflict({conflict_id:conflict[1],...(await requestJson(request))});
        return json(result.status??200,result);
      }

      return json(404,{ok:false,error:"not_found"});
    } catch (error) {
      this.counters.errors += 1;
      return json(error instanceof SyntaxError?400:500,{ok:false,error:error.message});
    }
  }
}
