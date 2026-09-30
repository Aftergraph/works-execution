import { randomBytes, createHash } from "node:crypto";

export function id(prefix) {
  return `${prefix}_${randomBytes(16).toString("hex")}`;
}

export function stableStringify(value) {
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(",")}]`;
  if (value && typeof value === "object") {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableStringify(value[key])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

export function digest(value) {
  return createHash("sha256").update(stableStringify(value)).digest("hex");
}
