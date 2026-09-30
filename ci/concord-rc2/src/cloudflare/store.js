const EMPTY = {
  schema:"concord-state/1",
  revision:0,
  replicas:{},
  assertions:{},
  conflicts:{},
  receipts:{},
  verification_links:{},
  events:[]
};

export class DurableObjectStore {
  constructor(storage) {
    this.storage = storage;
  }

  async read() {
    const state = await this.storage.get("state");
    return state ? structuredClone(state) : structuredClone(EMPTY);
  }

  async transact(fn) {
    return this.storage.transaction(async (txn) => {
      const existing = await txn.get("state");
      const state = existing ? structuredClone(existing) : structuredClone(EMPTY);
      const result = await fn(state);
      await txn.put("state", state);
      return structuredClone(result);
    });
  }
}
