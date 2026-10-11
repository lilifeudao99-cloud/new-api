// Asynchronous image batches for the OpenAI-compatible image upstream.
//
// The upstream batch API uses one batch id for one or more image items.  The
// New API task table keeps one durable task per submitted batch; polling reads
// the upstream items endpoint and projects its image URLs through the normal
// task-artifact proxy.
export const meta = {
  apiVersion: 1,
  key: "image-batch",
  name: "Image Batch",
  icon: "image",
  description: {
    en: "Asynchronous image generation and editing batches",
    zh: "异步文生图与图生图批次",
  },
  version: "1.0.1",
  author: { name: "New API" },
  models: ["gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "nano-banana-2", "nano-banana-2.1", "nano-banana-pro"],
  fetchMode: "per_task",
  usageSchema: {
    image_count: {
      type: "number",
      unit: "count",
      unitLabel: { en: "image", zh: "张" },
      description: { en: "Image generation unit price", zh: "图片生成单价" },
    },
  },
  routes: [
    { method: "POST", path: "/v1/image-batches/generations", type: "submit", action: "image_generation", decode: "decodeGeneration", render: "batchCreated" },
    { method: "POST", path: "/v1/image-batches/edits", type: "submit", action: "image_edit", decode: "decodeEdit", render: "batchCreated", bodyKinds: ["json", "multipart"] },
    { method: "GET", path: "/v1/image-batches/:batch_id", type: "query", taskIdParam: "batch_id", render: "batchStatus" },
    { method: "GET", path: "/v1/image-batches/:batch_id/items", type: "query", taskIdParam: "batch_id", render: "batchItems" },
  ],
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }],
};

const IMAGE_MODELS = new Set(meta.models);
const IMAGE_DIMENSION_MODELS = new Set(["nano-banana-2", "nano-banana-2.1", "nano-banana-pro", "gpt-image-2"]);

// The async batch API accepts an OpenAI-compatible WIDTHxHEIGHT `size`.
// Translate the public resolution/aspectRatio options using each model's
// documented dimensions. Nano Banana 2 and 2.1 use the Gemini 3.1 Flash Image
// table; Pro uses the Gemini 3 Pro Image table. Keep GPT Image's profile
// separate because its size contract is not the Gemini table.
const IMAGE_RESOLUTIONS = new Set(["1K", "2K", "4K"]);
const GPT_IMAGE_ASPECT_RATIOS = new Set(["21:9", "16:9", "3:2", "4:3", "5:4", "1:1", "4:5", "3:4", "2:3", "9:16"]);
const GPT_IMAGE_SIZE_PROFILES_4K = {
  "21:9": "3696x1584",
  "16:9": "3840x2160",
  "3:2": "3504x2336",
  "4:3": "3264x2448",
  "5:4": "3200x2560",
  "1:1": "2880x2880",
  "4:5": "2560x3200",
  "3:4": "2448x3264",
  "2:3": "2336x3504",
  "9:16": "2160x3840",
};

const NANO_BANANA_FLASH_SIZES = {
  "1:1": { "1K": "1024x1024", "2K": "2048x2048", "4K": "4096x4096" },
  "1:4": { "1K": "512x2048", "2K": "1024x4096", "4K": "2048x8192" },
  "1:8": { "1K": "384x3072", "2K": "768x6144", "4K": "1536x12288" },
  "2:3": { "1K": "848x1264", "2K": "1696x2528", "4K": "3392x5056" },
  "3:2": { "1K": "1264x848", "2K": "2528x1696", "4K": "5056x3392" },
  "3:4": { "1K": "896x1200", "2K": "1792x2400", "4K": "3584x4800" },
  "4:1": { "1K": "2048x512", "2K": "4096x1024", "4K": "8192x2048" },
  "4:3": { "1K": "1200x896", "2K": "2400x1792", "4K": "4800x3584" },
  "4:5": { "1K": "928x1152", "2K": "1856x2304", "4K": "3712x4608" },
  "5:4": { "1K": "1152x928", "2K": "2304x1856", "4K": "4608x3712" },
  "8:1": { "1K": "3072x384", "2K": "6144x768", "4K": "12288x1536" },
  "9:16": { "1K": "768x1376", "2K": "1536x2752", "4K": "3072x5504" },
  "16:9": { "1K": "1376x768", "2K": "2752x1536", "4K": "5504x3072" },
  "21:9": { "1K": "1584x672", "2K": "3168x1344", "4K": "6336x2688" },
};

const NANO_BANANA_PRO_SIZES = {
  "1:1": { "1K": "1024x1024", "2K": "2048x2048", "4K": "4096x4096" },
  "2:3": { "1K": "848x1264", "2K": "1696x2528", "4K": "3392x5056" },
  "3:2": { "1K": "1264x848", "2K": "2528x1696", "4K": "5056x3392" },
  "3:4": { "1K": "896x1200", "2K": "1792x2400", "4K": "3584x4800" },
  "4:3": { "1K": "1200x896", "2K": "2400x1792", "4K": "4800x3584" },
  "4:5": { "1K": "928x1152", "2K": "1856x2304", "4K": "3712x4608" },
  "5:4": { "1K": "1152x928", "2K": "2304x1856", "4K": "4608x3712" },
  "9:16": { "1K": "768x1376", "2K": "1536x2752", "4K": "3072x5504" },
  "16:9": { "1K": "1376x768", "2K": "2752x1536", "4K": "5504x3072" },
  "21:9": { "1K": "1584x672", "2K": "3168x1344", "4K": "6336x2688" },
};

function normalizeResolution(value) {
  const resolution = trimmed(value).toUpperCase();
  if (!IMAGE_RESOLUTIONS.has(resolution)) throw new Error("resolution must be 1K, 2K, or 4K");
  return resolution;
}

function normalizeAspectRatio(value, model) {
  const aspectRatio = trimmed(value);
  const profile = model === "nano-banana-pro" ? NANO_BANANA_PRO_SIZES : NANO_BANANA_FLASH_SIZES;
  const supported = model === "gpt-image-2" ? GPT_IMAGE_ASPECT_RATIOS.has(aspectRatio) : !!profile[aspectRatio];
  if (!supported) throw new Error("aspectRatio is not supported by the selected image model");
  return aspectRatio;
}

function imageSizeForOptions(model, resolution, aspectRatio) {
  if (model === "nano-banana-2" || model === "nano-banana-2.1") {
    const dimensions = NANO_BANANA_FLASH_SIZES[aspectRatio];
    if (!dimensions) throw new Error("aspectRatio is not supported by the selected image model");
    return dimensions[resolution];
  }
  if (model === "nano-banana-pro") {
    const dimensions = NANO_BANANA_PRO_SIZES[aspectRatio];
    if (!dimensions) throw new Error("aspectRatio is not supported by the selected image model");
    return dimensions[resolution];
  }
  const base = GPT_IMAGE_SIZE_PROFILES_4K[aspectRatio];
  if (!base) throw new Error("unsupported aspect ratio");
  if (resolution === "4K") return base;
  const match = /^(\d+)x(\d+)$/.exec(base);
  const width = Number(match[1]);
  const height = Number(match[2]);
  const targetLongEdge = resolution === "2K" ? 2048 : 1024;
  const scale = targetLongEdge / Math.max(width, height);
  return Math.round(width * scale) + "x" + Math.round(height * scale);
}

function normalizeImageOptions(body, modelHint) {
  const normalized = Object.assign({}, body || {});
  // The asynchronous upstream batch API does not accept the OpenAI-style
  // quality field.  Keep accepting it from older clients for compatibility,
  // but never persist or forward it to the upstream request.
  delete normalized.quality;
  const hasResolution = normalized.resolution !== undefined;
  const hasAspectRatio = normalized.aspectRatio !== undefined;
  if (!hasResolution && !hasAspectRatio) return normalized;

  const model = trimmed(normalized.model || modelHint);
  if (!IMAGE_DIMENSION_MODELS.has(model)) throw new Error("resolution and aspectRatio are supported only for nano-banana-2, nano-banana-2.1, nano-banana-pro, and gpt-image-2");

  const resolution = hasResolution ? normalizeResolution(normalized.resolution) : "1K";
  const aspectRatio = hasAspectRatio ? normalizeAspectRatio(normalized.aspectRatio, model) : "1:1";
  const derivedSize = imageSizeForOptions(model, resolution, aspectRatio);
  if (normalized.size !== undefined && normalized.size !== derivedSize) {
    throw new Error("size conflicts with resolution/aspectRatio; provide either size or resolution plus aspectRatio");
  }
  normalized.size = derivedSize;
  delete normalized.resolution;
  delete normalized.aspectRatio;
  return normalized;
}

function trimmed(value) {
  return String(value || "").trim();
}

function objectValue(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function requestModel(ctx, body) {
  const model = trimmed((body && body.model) || (ctx && ctx.model));
  if (!IMAGE_MODELS.has(model)) throw new Error("unsupported image model");
  return model;
}

function validateCommon(body) {
  if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("request body must be an object");
  if (!trimmed(body.prompt)) throw new Error("prompt is required");
  if (body.n !== undefined && (!Number.isInteger(body.n) || body.n < 1 || body.n > 64)) throw new Error("n must be an integer between 1 and 64");
  if (body.response_format !== undefined && body.response_format !== "url") throw new Error("response_format must be url for asynchronous image batches");
  if (body.size !== undefined && (typeof body.size !== "string" || !/^\d{2,5}x\d{2,5}$/.test(body.size))) throw new Error("size must use WIDTHxHEIGHT");
}

function decodeSubmit(ctx, action) {
  if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
  const model = requestModel(ctx, ctx.body.value);
  const body = normalizeImageOptions(ctx.body.value, model);
  validateCommon(body);
  return { kind: "submit", model: model, action: action, requestBody: Object.assign({}, body, { model: model }) };
}

function validateImages(images) {
  if (!Array.isArray(images) || images.length < 1 || images.length > 10) throw new Error("images must contain between 1 and 10 public URLs");
  for (const image of images) {
    const fileRef = image && typeof image === "object" && typeof image.__fileRef === "string" && image.encoding === "tos_url";
    if (!fileRef && (typeof image !== "string" || !/^https?:\/\//i.test(image) || image.length > 4096)) throw new Error("images must contain public http(s) URLs or uploaded image files");
  }
}

function isImageUploadField(field) {
  // Accept common multipart encodings: repeated `image`, `image[]`, indexed
  // `image[0]`/`image[1]`, and the plural `images` equivalents.
  return /^images?(?:\[\]|\[\d+\])?$/.test(String(field || ""));
}

function decodeEditBody(ctx) {
  if (!ctx.body) throw new Error("request body required");
  if (ctx.body.kind === "json") return ctx.body.value;
  if (ctx.body.kind !== "multipart") throw new Error("JSON or multipart body required");
  const fields = objectValue(ctx.body.fields);
  const first = function (name) { const value = fields[name]; return Array.isArray(value) ? value[0] : value; };
  const body = { prompt: first("prompt"), model: first("model") || ctx.model };
  for (const key of ["size", "resolution", "aspectRatio", "response_format"]) if (first(key) !== undefined) body[key] = first(key);
  if (first("n") !== undefined && String(first("n")) !== "") body.n = Number(first("n"));
  // RouteRequestContext keeps multipart file references under body.files;
  // older direct hook callers may still provide the legacy top-level field.
  const files = Array.isArray(ctx.body.files) ? ctx.body.files : (Array.isArray(ctx.files) ? ctx.files : []);
  const images = files.filter(function (file) { return file && isImageUploadField(file.field); });
  if (images.length) body.images = images.map(function (file) { return { __fileRef: file.ref, encoding: "tos_url", mimeType: file.mimeType || "application/octet-stream" }; });
  return body;
}

export const native = {
  decodeGeneration: function (ctx) {
    return decodeSubmit(ctx, "image_generation");
  },
  decodeEdit: function (ctx) {
    const rawBody = decodeEditBody(ctx);
    const model = requestModel(ctx, rawBody);
    const body = normalizeImageOptions(rawBody, model);
    validateCommon(body);
    const hasImages = Array.isArray(body.images);
    const hasTasks = Array.isArray(body.tasks);
    if (hasImages === hasTasks) throw new Error("exactly one of images or tasks is required");
    if (hasImages) validateImages(body.images);
    if (hasTasks) {
      if (body.tasks.length < 1 || body.tasks.length > 64) throw new Error("tasks must contain between 1 and 64 items");
      for (const task of body.tasks) validateImages(task && task.images);
    }
    return { kind: "submit", model: model, action: "image_edit", requestBody: Object.assign({}, body, { model: model }) };
  },
  batchCreated: function (_ctx, task) {
    const data = objectValue(task.data);
    return {
      batch_id: task.task_id,
      status: "queued",
      model: data.model || "",
      total: Number(data.total || data.n || 1),
      queued: Number(data.total || data.n || 1),
      running: 0,
      succeeded: 0,
      failed: 0,
      unknown: 0,
      canceled: 0,
      cancel_requested: false,
      created_at: task.created_at || 0,
      updated_at: task.updated_at || task.created_at || 0,
      poll_url: "/v1/image-batches/" + encodeURIComponent(task.task_id),
    };
  },
  batchStatus: function (_ctx, task) {
    return batchSummary(task);
  },
  batchItems: function (_ctx, task) {
    const items = itemList(task);
    return { object: "list", data: items, next_cursor: "", has_more: false };
  },
  error: function (_ctx, error) {
    return { error: { message: error.message, type: "invalid_request_error", code: error.code } };
  },
};

function itemList(task) {
  const data = objectValue(task && task.data);
  if (Array.isArray(data.data)) return data.data;
  return [];
}

function initialCount(task) {
  const data = objectValue(task && task.data);
  const total = Number(data.total || data.n || 1);
  return Number.isFinite(total) && total > 0 ? Math.floor(total) : 1;
}

function statusCounts(task) {
  const items = itemList(task);
  const counts = { total: items.length || initialCount(task), queued: 0, running: 0, succeeded: 0, failed: 0, unknown: 0, canceled: 0 };
  for (const item of items) {
    const status = trimmed(item && item.status).toLowerCase();
    if (status === "succeeded") counts.succeeded++;
    else if (status === "failed") counts.failed++;
    else if (status === "canceled") counts.canceled++;
    else if (status === "unknown") counts.unknown++;
    else if (status === "running") counts.running++;
    else counts.queued++;
  }
  return counts;
}

function batchStatusForTask(task, counts) {
  if (task.status === "FAILURE") return "failed";
  if (task.status === "SUCCESS") return counts.failed || counts.unknown || counts.canceled ? (counts.succeeded ? "partially_succeeded" : "failed") : "succeeded";
  if (task.status === "IN_PROGRESS") return "running";
  return "queued";
}

function batchSummary(task) {
  const counts = statusCounts(task);
  return {
    batch_id: task.task_id,
    status: batchStatusForTask(task, counts),
    total: counts.total,
    queued: counts.queued,
    running: counts.running,
    succeeded: counts.succeeded,
    failed: counts.failed,
    unknown: counts.unknown,
    canceled: counts.canceled,
    cancel_requested: false,
    created_at: task.created_at || 0,
    updated_at: task.updated_at || task.created_at || 0,
    poll_url: "/v1/image-batches/" + encodeURIComponent(task.task_id),
  };
}

function firstOutputURL(ctx) {
  const data = objectValue(ctx && ctx.data);
  const items = Array.isArray(data.data) ? data.data : [];
  for (const item of items) {
    const url = trimmed(item && item.output_url);
    if (url) return url;
  }
  return "";
}

function imageText(ctx) {
  const artifact = ctx && ctx.artifacts && ctx.artifacts.image;
  const url = trimmed(artifact && artifact.url) || firstOutputURL(ctx);
  if (!url) throw new Error("image artifact is unavailable");
  const escaped = url.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return '<img src="' + escaped + '" />';
}

export function buildSubmitRequest(ctx) {
  const body = normalizeImageOptions(ctx.requestBody || {}, ctx.model);
  const incoming = ctx.requestHeaders || {};
  const idempotencyKey = trimmed(incoming["Idempotency-Key"] || incoming["Idempotency-key"]);
  if (idempotencyKey.length < 8 || idempotencyKey.length > 128) throw new Error("Idempotency-Key length must be between 8 and 128 characters");
  const isEdit = ctx.action === "image_edit";
  if (isEdit) {
    if (Array.isArray(body.images)) validateImages(body.images);
    if (Array.isArray(body.tasks)) for (const task of body.tasks) validateImages(task && task.images);
  } else if (body.images !== undefined || body.tasks !== undefined) {
    throw new Error("images and tasks are not allowed for image generation");
  }
  body.response_format = "url";
  const headers = { Authorization: "Bearer " + ctx.apiKey, "Content-Type": "application/json", "Idempotency-Key": idempotencyKey, Accept: "application/json" };
  return { url: ctx.baseUrl + (isEdit ? "/v1/image-batches/edits" : "/v1/image-batches/generations"), method: "POST", headers: headers, body: body, action: ctx.action };
}

export function parseSubmitResponse(_ctx, resp) {
  const body = objectValue(resp.body);
  if (resp.statusCode < 200 || resp.statusCode >= 300) throw new Error(String(body.message || (body.error && body.error.message) || "image batch submission failed"));
  const batchID = trimmed(body.batch_id);
  if (!batchID) throw new Error("missing batch_id");
  return { taskId: batchID, taskData: body };
}

export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  const body = objectValue(ctx.requestBody);
  const imageCount = Array.isArray(body.tasks) ? body.tasks.length : (Number.isInteger(body.n) && body.n > 0 ? body.n : 1);
  // Keep the request-body `images` array separate from the numeric billing
  // fact.  The host validates declared usage fields recursively, so sharing
  // that name would make multipart image edits look like a non-numeric usage
  // value before the task is submitted.
  return { image_count: imageCount };
}

export function buildQueryRequest(ctx) {
  const cursor = trimmed(ctx.cursor);
  return {
    url: ctx.baseUrl + "/v1/image-batches/" + encodeURIComponent(ctx.taskId) + "/items?limit=100" + (cursor ? "&cursor=" + encodeURIComponent(cursor) : ""),
    method: "GET",
    headers: { Authorization: "Bearer " + ctx.apiKey, Accept: "application/json" },
  };
}

export function parseTaskResult(ctx, body) {
  const items = Array.isArray(body && body.data) ? body.data : [];
  let succeeded = 0,
    running = 0,
    queued = 0,
    failed = 0,
    unknown = 0,
    canceled = 0,
    reason = "";
  for (const item of items) {
    const status = trimmed(item && item.status).toLowerCase();
    if (status === "succeeded") succeeded++;
    else if (status === "running") running++;
    else if (status === "failed") {
      failed++;
      reason = reason || trimmed(item.error_message);
    } else if (status === "unknown") unknown++;
    else if (status === "canceled") canceled++;
    else queued++;
  }
  const total = items.length;
  let status = "QUEUED";
  if (succeeded + failed + unknown + canceled === total && total > 0) status = succeeded > 0 ? "SUCCESS" : "FAILURE";
  else if (running > 0) status = "IN_PROGRESS";
  let url = "";
  for (const item of items) {
    const candidate = trimmed(item && item.output_url);
    if (candidate) {
      url = candidate;
      break;
    }
  }
  return { taskId: ctx.publicTaskId, status: status, reason: reason, url: url };
}

function responsesInput(req) {
  const texts = [],
    images = [];
  if (typeof req.input === "string") texts.push(req.input);
  else if (Array.isArray(req.input)) {
    for (const item of req.input) {
      if (typeof item === "string") texts.push(item);
      else if (item && typeof item === "object") {
        const parts = Array.isArray(item.content) ? item.content : [item];
        for (const part of parts) {
          if (!part || typeof part !== "object") continue;
          if ((part.type === "input_text" || part.type === "text") && typeof part.text === "string") texts.push(part.text);
          if (part.type === "input_image" || part.type === "image_url") {
            const value = typeof part.image_url === "object" ? part.image_url.url : part.image_url;
            if (trimmed(value)) images.push(trimmed(value));
          }
        }
      }
    }
  }
  if (trimmed(req.prompt)) texts.push(trimmed(req.prompt));
  return { prompt: texts.filter(trimmed).join("\n"), images: images };
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value || {};
      const input = responsesInput(req);
      if (!input.prompt && input.images.length === 0) throw new Error("input is required");
      const body = { model: ctx.model, prompt: input.prompt, n: 1, response_format: "url" };
      if (input.images.length) body.images = input.images;
      return { kind: "submit", model: ctx.model, action: input.images.length ? "image_edit" : "image_generation", requestBody: body };
    },
    renderEvents: function (_ctx, task, previousState) {
      const status = String(task.status || "QUEUED");
      const state = { status: status };
      if (status === "SUCCESS") return { events: previousState && previousState.status === status ? [] : [{ type: "output", data: imageText(task) }], state: state, done: true };
      if (status === "FAILURE") return { events: [{ type: "error", code: "task_failed", message: task.fail_reason || "image task failed" }], state: state, done: true };
      if (previousState && previousState.status === status) return { events: [], state: state, done: false };
      return { events: [{ type: "progress", message: status.toLowerCase() }], state: state, done: false };
    },
    renderFinal: function (ctx, task) {
      return { output: [{ type: "message", status: "completed", role: "assistant", content: [{ type: "output_text", text: imageText(Object.assign({}, ctx, task)), annotations: [], logprobs: [] }] }], metadata: { vendor: "image-batch" } };
    },
  },
};

export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  return itemList(task).map(function (item, index) {
    return trimmed(item && item.output_url) ? { key: "image-" + index, type: "image", mimeType: "image/*" } : null;
  }).filter(Boolean);
}

export function buildContentRequest(ctx) {
  const match = /^image-(\d+)$/.exec(String(ctx.artifactKey || ""));
  if (!match) throw new Error("artifact_not_found");
  const item = itemList(ctx)[Number(match[1])];
  const url = trimmed(item && item.output_url);
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export function extractUsageOnComplete(_task, _result, body) {
  const items = Array.isArray(body && body.data) ? body.data : [];
  const count = items.filter(function (item) {
    return trimmed(item && item.status).toLowerCase() === "succeeded";
  }).length;
  return count > 0 ? { images: count } : null;
}
