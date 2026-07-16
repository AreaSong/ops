import assert from "node:assert/strict";
import test from "node:test";

import worker, { validateKey } from "../src/index.js";

const TOKEN = "test-ingest-token";
const KEY = "payload/LosAngeles/2026/07/15/20260715-003500000000Z-0123abcd/payload.tar.gz.part-00000";

class FakeBucket {
  constructor() {
    this.objects = new Map();
  }

  async put(key, body, options) {
    if (this.objects.has(key)) return null;
    this.objects.set(key, { body, options });
    return { key };
  }
}

function request(method, key = KEY, token = TOKEN) {
  const body = method === "PUT" ? "archive-data" : undefined;
  return new Request(`https://example.test/v1/archive/${key}`, {
    method,
    body,
    headers: {
      authorization: `Bearer ${token}`,
      "content-length": String(Buffer.byteLength(body ?? "")),
      "content-type": "application/octet-stream",
      "x-content-sha256": "0".repeat(64),
    },
  });
}

test("archive keys are strictly scoped and date-bound", () => {
  assert.equal(validateKey(KEY), true);
  assert.equal(
    validateKey("manifests/LosAngeles/2026/07/15/20260715-003500000000Z-0123abcd/manifest.json"),
    true,
  );
  assert.equal(validateKey(KEY.replace("2026/07/15", "2026/07/14")), false);
  assert.equal(validateKey(KEY.replace("payload.tar.gz.part-00000", "../../secret")), false);
});

test("worker accepts one immutable object and rejects overwrite", async () => {
  const env = { ARCHIVE_BUCKET: new FakeBucket(), INGEST_TOKEN: TOKEN };
  const first = await worker.fetch(request("PUT"), env);
  const second = await worker.fetch(request("PUT"), env);
  assert.equal(first.status, 201);
  assert.equal(second.status, 409);
  assert.equal(env.ARCHIVE_BUCKET.objects.size, 1);
  const stored = env.ARCHIVE_BUCKET.objects.get(KEY);
  assert.equal(stored.options.customMetadata.immutable, "true");
  assert.equal(stored.options.onlyIf.get("if-none-match"), "*");
});

test("worker rejects unauthorized and destructive methods", async () => {
  const env = { ARCHIVE_BUCKET: new FakeBucket(), INGEST_TOKEN: TOKEN };
  assert.equal((await worker.fetch(request("PUT", KEY, "wrong"), env)).status, 401);
  assert.equal((await worker.fetch(request("DELETE"), env)).status, 405);
  assert.equal(env.ARCHIVE_BUCKET.objects.size, 0);
});
