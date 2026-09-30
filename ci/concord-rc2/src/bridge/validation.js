export const SHA_PATTERN = /^[0-9a-f]{40}$/;

export function validatePullRequest(pr, {
  repository = "Aftergraph/concord",
  authorizedActor = "JonasAbde",
  event,
  triggerLabel
} = {}) {
  if (!pr || pr.state !== "open") return {ok:false,reason:"closed_pr"};
  if (pr.head?.repo?.full_name !== repository) return {ok:false,reason:"foreign_head"};
  const sha = pr.head?.sha ?? "";
  if (!SHA_PATTERN.test(sha)) return {ok:false,reason:"malformed_sha"};
  if (!event || event.actor?.login !== authorizedActor) return {ok:false,reason:"unauthorized_actor"};
  if (event.label?.name !== triggerLabel) return {ok:false,reason:"wrong_label"};
  return {ok:true,sha,pr_number:pr.number};
}

export function latestMatchingLabelEvent(events, label) {
  return [...(events ?? [])]
    .filter((event)=>event?.event==="labeled" && event?.label?.name===label && event?.actor?.login)
    .sort((a,b)=>(b.id??0)-(a.id??0))[0] ?? null;
}
