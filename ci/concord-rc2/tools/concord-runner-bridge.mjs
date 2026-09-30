#!/usr/bin/env node
import { spawn } from "node:child_process";
import { mkdir, rm } from "node:fs/promises";
import { join } from "node:path";
import { latestMatchingLabelEvent, validatePullRequest } from "../src/bridge/validation.js";

const cfg = {
  token: process.env.CONCORD_BRIDGE_GITHUB_TOKEN ?? process.env.GH_TOKEN ?? "",
  repository: process.env.CONCORD_BRIDGE_REPOSITORY ?? "Aftergraph/concord",
  actor: process.env.CONCORD_BRIDGE_AUTHORIZED_ACTOR ?? "JonasAbde",
  repoRoot: process.env.CONCORD_BRIDGE_REPO_ROOT ?? "/opt/concord-controller",
  stateDir: process.env.CONCORD_BRIDGE_STATE_DIR ?? "/var/lib/concord-runner-bridge",
  pollMs: Number(process.env.CONCORD_BRIDGE_POLL_MS ?? 30000),
  ciLabel: process.env.CONCORD_BRIDGE_CI_LABEL ?? "concord-ci:run",
  deployLabel: process.env.CONCORD_BRIDGE_DEPLOY_LABEL ?? "concord-deploy:run"
};

if (!cfg.token) throw new Error("CONCORD_BRIDGE_GITHUB_TOKEN or GH_TOKEN is required");
if (!Number.isFinite(cfg.pollMs) || cfg.pollMs < 15000) throw new Error("CONCORD_BRIDGE_POLL_MS must be >= 15000");

const apiRoot = `https://api.github.com/repos/${cfg.repository}`;
const headers = {
  accept:"application/vnd.github+json",
  authorization:`Bearer ${cfg.token}`,
  "x-github-api-version":"2022-11-28",
  "user-agent":"aftergraph-concord-runner-bridge/1"
};

async function api(path, init = {}) {
  const response = await fetch(apiRoot + path, {
    ...init,
    headers:{...headers,...(init.body ? {"content-type":"application/json"} : {})},
    signal:AbortSignal.timeout(30000)
  });
  if (!response.ok) throw new Error(`github ${init.method ?? "GET"} ${path}: ${response.status}`);
  if (response.status === 204) return null;
  return response.json();
}

async function listTriggered(label) {
  const issues = await api(`/issues?state=open&labels=${encodeURIComponent(label)}&per_page=100`);
  return issues.filter((issue)=>issue.pull_request).map((issue)=>issue.number);
}

async function labelEvents(number) {
  return api(`/issues/${number}/events?per_page=100`);
}

async function getPr(number) {
  return api(`/pulls/${number}`);
}

async function addLabels(number, labels) {
  await api(`/issues/${number}/labels`, {method:"POST",body:JSON.stringify({labels})});
}

async function removeLabel(number, label) {
  const response = await fetch(`${apiRoot}/issues/${number}/labels/${encodeURIComponent(label)}`, {
    method:"DELETE",headers,signal:AbortSignal.timeout(30000)
  });
  if (response.status !== 404 && !response.ok) throw new Error(`github DELETE label: ${response.status}`);
}

async function postStatus(sha, context, state, description) {
  await api(`/statuses/${sha}`, {
    method:"POST",
    body:JSON.stringify({
      state,
      context,
      description:description.slice(0,140),
      target_url:`https://github.com/${cfg.repository}/commit/${sha}`
    })
  });
}

function run(argv, options = {}) {
  return new Promise((resolve, reject)=>{
    const child=spawn(argv[0],argv.slice(1),{
      cwd:options.cwd ?? cfg.repoRoot,
      env:{...process.env,...(options.env ?? {})},
      stdio:"inherit",
      shell:false
    });
    child.on("error",reject);
    child.on("close",(code,signal)=>{
      if (code===0) resolve();
      else reject(new Error(`${argv.join(" ")} failed code=${code} signal=${signal ?? ""}`));
    });
  });
}

async function withWorktree(sha, fn) {
  const worktrees=join(cfg.stateDir,"worktrees");
  const path=join(worktrees,sha);
  await mkdir(worktrees,{recursive:true,mode:0o700});
  await rm(path,{recursive:true,force:true});
  await run(["git","-C",cfg.repoRoot,"fetch","--no-tags","origin",sha]);
  await run(["git","-C",cfg.repoRoot,"worktree","add","--detach",path,sha]);
  try {
    const actual=await new Promise((resolve,reject)=>{
      const child=spawn("git",["rev-parse","HEAD"],{cwd:path,stdio:["ignore","pipe","inherit"]});
      const chunks=[];
      child.stdout.on("data",(chunk)=>chunks.push(chunk));
      child.on("error",reject);
      child.on("close",(code)=>code===0?resolve(Buffer.concat(chunks).toString("utf8").trim()):reject(new Error("git rev-parse failed")));
    });
    if (actual!==sha) throw new Error(`worktree head mismatch expected=${sha} actual=${actual}`);
    return await fn(path);
  } finally {
    await run(["git","-C",cfg.repoRoot,"worktree","remove","--force",path]).catch(()=>{});
    await rm(path,{recursive:true,force:true});
  }
}

async function claim(number, trigger, running) {
  await addLabels(number,[running]);
  await removeLabel(number,trigger);
}

async function finish(number, running, terminal) {
  await removeLabel(number,running).catch(()=>{});
  await addLabels(number,[terminal]).catch(()=>{});
}

async function processCi(number, pr, sha) {
  const running="concord-ci:running";
  await claim(number,cfg.ciLabel,running);
  await postStatus(sha,"concord/ci-local","pending","Concord exact-head verification in progress");
  try {
    await withWorktree(sha,(path)=>run(["bash","tools/verify-release.sh",sha],{
      cwd:path,
      env:{GITHUB_REPOSITORY:cfg.repository,CONCORD_BRIDGE_CI_VERIFIED:"1"}
    }));
    await postStatus(sha,"concord/ci-local","success","Concord exact-head verification passed");
    await finish(number,running,"concord-ci:passed");
  } catch (error) {
    await postStatus(sha,"concord/ci-local","failure","Concord exact-head verification failed").catch(()=>{});
    await finish(number,running,"concord-ci:failed");
    throw error;
  }
}

async function latestStatus(sha, context) {
  const statuses=await api(`/commits/${sha}/statuses?per_page=100`);
  return statuses.filter((s)=>s.context===context).sort((a,b)=>String(b.updated_at).localeCompare(String(a.updated_at)))[0]?.state ?? "missing";
}

async function processDeploy(number, pr, sha) {
  const ci=await latestStatus(sha,"concord/ci-local");
  if (ci!=="success") throw new Error(`deploy blocked: concord/ci-local=${ci}`);
  const running="concord-deploy:running";
  await claim(number,cfg.deployLabel,running);
  await postStatus(sha,"concord/deploy/cloudflare","pending","Concord Cloudflare deploy in progress");
  try {
    await withWorktree(sha,(path)=>run(["bash","tools/cloudflare-deploy.sh",sha],{
      cwd:path,
      env:{
        GITHUB_REPOSITORY:cfg.repository,
        CONCORD_DEPLOY_CI_VERIFIED:"1"
      }
    }));
    await postStatus(sha,"concord/deploy/cloudflare","success","Concord Cloudflare deployment passed");
    await finish(number,running,"concord-deploy:passed");
  } catch (error) {
    await postStatus(sha,"concord/deploy/cloudflare","failure","Concord Cloudflare deployment failed").catch(()=>{});
    await finish(number,running,"concord-deploy:failed");
    throw error;
  }
}

async function processLabel(label, handler) {
  const numbers=await listTriggered(label);
  for (const number of numbers) {
    const pr=await getPr(number);
    const events=await labelEvents(number);
    const event=latestMatchingLabelEvent(events,label);
    const validation=validatePullRequest(pr,{
      repository:cfg.repository,
      authorizedActor:cfg.actor,
      event,
      triggerLabel:label
    });
    if (!validation.ok) {
      console.error(`skip PR #${number}: ${validation.reason}`);
      continue;
    }
    try {
      await handler(number,pr,validation.sha);
    } catch (error) {
      console.error(`PR #${number} ${label} failed:`,error);
    }
  }
}

let stopping=false;
process.on("SIGTERM",()=>{stopping=true;});
process.on("SIGINT",()=>{stopping=true;});

await mkdir(cfg.stateDir,{recursive:true,mode:0o700});
console.log(`Concord Runner Bridge polling ${cfg.repository} every ${cfg.pollMs}ms`);

while (!stopping) {
  try {
    await processLabel(cfg.ciLabel,processCi);
    await processLabel(cfg.deployLabel,processDeploy);
  } catch (error) {
    console.error("bridge poll failed:",error);
  }
  if (!stopping) await new Promise((resolve)=>setTimeout(resolve,cfg.pollMs));
}
