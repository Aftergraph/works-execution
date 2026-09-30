import { chmod, mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

const EMPTY = { schema: "concord-state/1", revision: 0, replicas: {}, assertions: {}, conflicts: {}, receipts: {}, verification_links: {}, key_rotations: {}, events: [] };

export class JsonStore {
  constructor(path) {
    this.path = path;
    this.queue = Promise.resolve();
  }

  async read() {
    try {
      const parsed = JSON.parse(await readFile(this.path, "utf8"));
      return { ...structuredClone(EMPTY), ...parsed };
    } catch (error) {
      if (error.code === "ENOENT") return structuredClone(EMPTY);
      throw error;
    }
  }

  async transact(fn) {
    const task = this.queue.then(async () => {
      const state = await this.read();
      const result = await fn(state);
      const dir = dirname(this.path);
      await mkdir(dir, { recursive: true, mode:0o700 });
      try { await chmod(dir, 0o700); } catch {}
      const temp = `${this.path}.tmp`;
      await writeFile(temp, JSON.stringify(state, null, 2) + "\n", {encoding:"utf8",mode:0o600});
      await chmod(temp, 0o600);
      await rename(temp, this.path);
      try { await chmod(this.path, 0o600); } catch {};
      return result;
    });
    this.queue = task.catch(() => {});
    return task;
  }
}
