import test from "node:test";
import assert from "node:assert/strict";
import { latestMatchingLabelEvent, validatePullRequest } from "../src/bridge/validation.js";

const validPr = {
  number:7,
  state:"open",
  base:{ref:"frontier/v0.4.0-live-adapters"},
  head:{
    sha:"a".repeat(40),
    repo:{full_name:"Aftergraph/concord"}
  }
};

const validEvent = {
  id:42,
  event:"labeled",
  actor:{login:"JonasAbde"},
  label:{name:"concord-ci:run"}
};

test("accepts authorized same-repo exact-head trigger", () => {
  const result=validatePullRequest(validPr,{
    repository:"Aftergraph/concord",
    authorizedActor:"JonasAbde",
    event:validEvent,
    triggerLabel:"concord-ci:run"
  });
  assert.deepEqual(result,{ok:true,sha:"a".repeat(40),pr_number:7});
});

test("rejects foreign repo, actor, label, closed PR and malformed SHA", () => {
  const cases=[
    [{...validPr,head:{...validPr.head,repo:{full_name:"Mallory/fork"}}},validEvent,"foreign_head"],
    [validPr,{...validEvent,actor:{login:"Mallory"}},"unauthorized_actor"],
    [validPr,{...validEvent,label:{name:"other"}},"wrong_label"],
    [{...validPr,state:"closed"},validEvent,"closed_pr"],
    [{...validPr,head:{...validPr.head,sha:"main"}},validEvent,"malformed_sha"]
  ];
  for (const [pr,event,reason] of cases) {
    const result=validatePullRequest(pr,{
      repository:"Aftergraph/concord",
      authorizedActor:"JonasAbde",
      event,
      triggerLabel:"concord-ci:run"
    });
    assert.equal(result.ok,false);
    assert.equal(result.reason,reason);
  }
});

test("uses newest matching label event only", () => {
  const event=latestMatchingLabelEvent([
    {...validEvent,id:4},
    {...validEvent,id:9},
    {...validEvent,id:8,actor:{login:"Other"}},
    {id:10,event:"unlabeled",actor:{login:"JonasAbde"},label:{name:"concord-ci:run"}}
  ],"concord-ci:run");
  assert.equal(event.id,9);
});
