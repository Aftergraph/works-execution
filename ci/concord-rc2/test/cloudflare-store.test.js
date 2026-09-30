import test from "node:test";
import assert from "node:assert/strict";
import { DurableObjectStore } from "../src/cloudflare/store.js";

class FakeTxn {
  constructor(map){ this.map=map; }
  async get(key){ return structuredClone(this.map.get(key)); }
  async put(key,value){ this.map.set(key,structuredClone(value)); }
}

class FakeStorage {
  constructor(){ this.map=new Map(); }
  async get(key){ return structuredClone(this.map.get(key)); }
  async transaction(fn){ return fn(new FakeTxn(this.map)); }
}

test("DurableObjectStore persists Concord state transactionally", async () => {
  const storage=new FakeStorage();
  const store=new DurableObjectStore(storage);

  const initial=await store.read();
  assert.equal(initial.revision,0);
  assert.deepEqual(initial.replicas,{});

  const result=await store.transact(async (state)=>{
    state.revision=3;
    state.replicas.test={name:"Friday"};
    return {ok:true,revision:state.revision};
  });

  assert.deepEqual(result,{ok:true,revision:3});
  const persisted=await store.read();
  assert.equal(persisted.revision,3);
  assert.equal(persisted.replicas.test.name,"Friday");
});

test("DurableObjectStore returns isolated state copies", async () => {
  const storage=new FakeStorage();
  const store=new DurableObjectStore(storage);
  await store.transact(async (state)=>{
    state.revision=1;
    return true;
  });

  const one=await store.read();
  one.revision=999;
  const two=await store.read();
  assert.equal(two.revision,1);
});
