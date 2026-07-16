const MAX_OBJECT_BYTES = 64 * 1024 * 1024;
const SHA256_PATTERN = /^[0-9a-f]{64}$/;
const KEY_PATTERN = /^(payload|manifests)\/LosAngeles\/(\d{4})\/(\d{2})\/(\d{2})\/(\d{8}-\d{12}Z-[0-9a-f]{8})\/(.+)$/;

function jsonResponse(body, status) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

function hexToBytes(value) {
  const output = new Uint8Array(value.length / 2);
  for (let index = 0; index < output.length; index += 1) {
    output[index] = Number.parseInt(value.slice(index * 2, index * 2 + 2), 16);
  }
  return output;
}

async function tokenMatches(provided, expected) {
  const encoder = new TextEncoder();
  const [providedHash, expectedHash] = await Promise.all([
    crypto.subtle.digest("SHA-256", encoder.encode(provided)),
    crypto.subtle.digest("SHA-256", encoder.encode(expected)),
  ]);
  const left = new Uint8Array(providedHash);
  const right = new Uint8Array(expectedHash);
  let difference = left.length ^ right.length;
  for (let index = 0; index < Math.max(left.length, right.length); index += 1) {
    difference |= (left[index] ?? 0) ^ (right[index] ?? 0);
  }
  return difference === 0;
}

function validateKey(key) {
  const match = KEY_PATTERN.exec(key);
  if (!match) return false;
  const [kind, year, month, day, archiveId, filename] = match.slice(1);
  const archiveDay = archiveId.slice(0, 8);
  if (archiveDay !== `${year}${month}${day}`) return false;
  if (kind === "payload") return /^payload\.tar\.gz\.part-\d{5}$/.test(filename);
  return filename === "manifest.json" || filename === "manifest.json.sha256";
}

async function handlePut(request, env, key) {
  if (!env.INGEST_TOKEN || !env.ARCHIVE_BUCKET) {
    return jsonResponse({ error: "archive ingest is not configured" }, 503);
  }
  const authorization = request.headers.get("authorization") ?? "";
  const providedToken = authorization.startsWith("Bearer ") ? authorization.slice(7) : "";
  if (!providedToken || !(await tokenMatches(providedToken, env.INGEST_TOKEN))) {
    return jsonResponse({ error: "unauthorized" }, 401);
  }
  if (!validateKey(key)) {
    return jsonResponse({ error: "invalid immutable archive key" }, 400);
  }

  const contentLength = Number.parseInt(request.headers.get("content-length") ?? "", 10);
  const contentSha256 = (request.headers.get("x-content-sha256") ?? "").toLowerCase();
  if (!Number.isSafeInteger(contentLength) || contentLength <= 0 || contentLength > MAX_OBJECT_BYTES) {
    return jsonResponse({ error: "invalid content length" }, 413);
  }
  if (!SHA256_PATTERN.test(contentSha256) || request.body === null) {
    return jsonResponse({ error: "valid x-content-sha256 and body are required" }, 400);
  }

  const stored = await env.ARCHIVE_BUCKET.put(key, request.body, {
    onlyIf: new Headers({ "if-none-match": "*" }),
    sha256: hexToBytes(contentSha256).buffer,
    httpMetadata: {
      contentType: request.headers.get("content-type") ?? "application/octet-stream",
    },
    customMetadata: {
      sha256: contentSha256,
      immutable: "true",
    },
  });
  if (stored === null) {
    return jsonResponse({ error: "archive object already exists" }, 409);
  }
  return jsonResponse({ key, sha256: contentSha256, stored: true }, 201);
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (request.method === "GET" && url.pathname === "/health") {
      return jsonResponse({ status: "ok" }, 200);
    }
    if (request.method !== "PUT") {
      return jsonResponse({ error: "method not allowed" }, 405);
    }
    const prefix = "/v1/archive/";
    if (!url.pathname.startsWith(prefix)) {
      return jsonResponse({ error: "not found" }, 404);
    }
    let key;
    try {
      key = decodeURIComponent(url.pathname.slice(prefix.length));
    } catch {
      return jsonResponse({ error: "invalid path encoding" }, 400);
    }
    return handlePut(request, env, key);
  },
};

export { MAX_OBJECT_BYTES, validateKey };
