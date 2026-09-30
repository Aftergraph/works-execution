import { timingSafeEqual } from "node:crypto";
import { ConcordState } from "./durable.js";

export { ConcordState };

function safeEqual(left,right) {
  const a=Buffer.from(String(left),"utf8");
  const b=Buffer.from(String(right),"utf8");
  return a.length===b.length && timingSafeEqual(a,b);
}

function authorized(request,env) {
  const token=env.CONCORD_TOKEN??"";
  if (!token) return false;
  return safeEqual(request.headers.get("authorization")??"",`Bearer ${token}`);
}

function securityHeaders(headers=new Headers()) {
  headers.set("x-content-type-options","nosniff");
  headers.set("x-frame-options","DENY");
  headers.set("referrer-policy","no-referrer");
  headers.set("permissions-policy","camera=(), microphone=(), geolocation=()");
  return headers;
}

function json(status,body) {
  return new Response(JSON.stringify(body,null,2),{
    status,
    headers:securityHeaders(new Headers({
      "content-type":"application/json; charset=utf-8",
      "cache-control":"no-store"
    }))
  });
}

async function stateStub(env) {
  const id=env.CONCORD_STATE.idFromName("canonical");
  return env.CONCORD_STATE.get(id);
}

export default {
  async fetch(request,env) {
    const url=new URL(request.url);
    const apiPath=url.pathname.startsWith("/v1/") || url.pathname==="/metrics";
    if (apiPath && !authorized(request,env)) return json(401,{ok:false,error:"unauthorized"});

    if (url.pathname==="/health" || url.pathname==="/ready" || apiPath) {
      const stub=await stateStub(env);
      const response=await stub.fetch(request);
      const headers=securityHeaders(new Headers(response.headers));
      return new Response(response.body,{status:response.status,statusText:response.statusText,headers});
    }

    if (env.ASSETS) {
      const response=await env.ASSETS.fetch(request);
      const headers=securityHeaders(new Headers(response.headers));
      if ((headers.get("content-type")??"").includes("text/html")) {
        headers.set("content-security-policy","default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'");
      }
      return new Response(response.body,{status:response.status,statusText:response.statusText,headers});
    }

    return json(404,{ok:false,error:"not_found"});
  }
};
