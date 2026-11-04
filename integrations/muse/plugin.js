// Installed through the task-plugin management API; no gateway image upgrade.
export const meta = {
  apiVersion: 1, key: "muse", name: "Muse Video", version: "1.0.1",
  author: { name: "ailili.chat" }, icon: "text:Muse",
  description: { en: "Asynchronous Muse video generation", zh: "Muse 异步视频生成" },
  baseUrl: "https://yuanai.uk", fetchMode: "per_task",
  models: ["muse-video", "muse-video-20s", "muse-video-30s", "muse-video-40s", "muse-video-50s", "muse-video-60s"],
  protocols: ["openai_video"],
  // The deployed host limits shared /v1/videos creation to built-in models.
  // A native create route uses the same driver, billing, polling and artifacts.
  routes: [{ method: "POST", path: "/muse/v1/videos", type: "submit", decode: "createVideo", render: "videoCreated", bodyKinds: ["json", "multipart"] }],
  usageSchema: { video_count: { type: "number", unit: "count", description: { en: "Video generation unit price", zh: "视频生成单价" } } },
};

function normalizeRequest(req, model) {
  if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("JSON object required");
  if (!String(req.prompt || "").trim()) throw new Error("prompt is required");
  const body = Object.assign({}, req, { model });
  const seconds = req.seconds === undefined ? req.duration : req.seconds;
  if (seconds !== undefined) {
    if ((typeof seconds !== "string" && typeof seconds !== "number") || !Number.isInteger(Number(seconds)) || Number(seconds) < 1 || Number(seconds) > 60)
      throw new Error("seconds must be an integer between 1 and 60");
    if (model === "muse-video" && Number(seconds) > 10) throw new Error("muse-video supports at most 10 seconds");
    body.seconds = String(Number(seconds));
    delete body.duration;
  }
  if (model === "muse-video" && (req.video_url || req.input_video || req.video)) throw new Error("muse-video does not support reference video");
  for (const field of ["images", "reference_images"]) {
    if (req[field] !== undefined && (!Array.isArray(req[field]) || req[field].length > 4)) throw new Error(field + " must contain at most 4 references");
  }
  return body;
}

export function buildSubmitRequest(ctx) {
  const body = normalizeRequest(ctx.requestBody, ctx.upstreamModel || ctx.model);
  const request = { url: ctx.baseUrl + "/v1/videos", method: "POST", headers: { Authorization: "Bearer " + ctx.apiKey } };
  if ((ctx.files || []).length) {
    request.bodyType = "multipart";
    request.parts = [];
    for (const key of Object.keys(body)) {
      if (body[key] !== undefined && body[key] !== null) request.parts.push({ name: key, value: typeof body[key] === "object" ? JSON.stringify(body[key]) : String(body[key]) });
    }
    for (const file of ctx.files) request.parts.push({ name: file.field, fileRef: file.ref, filename: file.filename });
  } else {
    request.headers["Content-Type"] = "application/json";
    request.body = body;
  }
  return request;
}

export function parseSubmitResponse(ctx, response) {
  const body = response.body || {};
  const taskId = body.id || body.task_id;
  if (typeof taskId !== "string" || !taskId) throw new Error("upstream did not return a task ID");
  return { taskId, taskData: body };
}

// Each accepted generation costs exactly one call, irrespective of duration,
// trimming, reference media, or client/upstream usage hints.
export function extractUsage() { return { video_count: 1 }; }
export function extractUsageOnComplete() { return { video_count: 1 }; }

export function buildQueryRequest(ctx) {
  return { url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId), method: "GET", headers: { Authorization: "Bearer " + ctx.apiKey } };
}

function videoURL(body) {
  const value = body.video_url || body.url || (Array.isArray(body.data) && body.data[0] && body.data[0].url) || "";
  return typeof value === "string" && /^https:\/\//i.test(value) ? value : "";
}

export function parseTaskResult(ctx, body) {
  const statuses = { queued: "QUEUED", pending: "QUEUED", processing: "IN_PROGRESS", in_progress: "IN_PROGRESS", running: "IN_PROGRESS", completed: "SUCCESS", succeeded: "SUCCESS", success: "SUCCESS", failed: "FAILURE", cancelled: "FAILURE", canceled: "FAILURE" };
  const status = statuses[body.status] || "UNKNOWN";
  const result = { status };
  if (status === "UNKNOWN") result.reason = "unrecognized upstream task status";
  if (status === "FAILURE") result.reason = body.error && body.error.message ? body.error.message : "video generation failed";
  const progress = Number(body.progress);
  if (Number.isFinite(progress) && progress >= 0 && progress <= 100) result.progress = progress + "%";
  const url = videoURL(body);
  if (url) result.url = url;
  return result;
}

export function listArtifacts(task) { return task.status === "SUCCESS" ? [{ key: "video", type: "video" }] : []; }

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  return { url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.upstreamTaskId) + "/content", method: ctx.clientRequest.method, headers: { Authorization: "Bearer " + ctx.apiKey } };
}

export const protocols = {
  openai_video: {
    decodeRequest(ctx) {
      if (!ctx.body || !["json", "multipart"].includes(ctx.body.kind)) throw new Error("JSON or multipart body required");
      let req = ctx.body.value;
      if (ctx.body.kind === "multipart") {
        req = {};
        for (const key of Object.keys(ctx.body.fields || {})) {
          const values = ctx.body.fields[key];
          if (values.length !== 1) throw new Error(key + " must be provided once");
          req[key] = values[0];
        }
        for (const key of ["images", "reference_images"]) if (typeof req[key] === "string") req[key] = JSON.parse(req[key]);
      }
      const body = normalizeRequest(req, ctx.model);
      return { kind: "submit", model: ctx.model, action: "generate", requestBody: body };
    },
    render(ctx, task) {
      const output = {};
      const body = task.data || {};
      const url = videoURL(body);
      if (task.status === "SUCCESS" && url) { output.video_url = url; output.url = url; }
      for (const key of ["seconds", "size", "resolution"]) if (body[key] !== undefined) output[key] = body[key];
      if (task.status === "FAILURE") output.error = { code: "video_generation_failed", message: task.fail_reason || "video generation failed" };
      return output;
    },
  },
};

export const native = {
  createVideo(ctx) {
    const request = ctx.body && (ctx.body.kind === "json" ? ctx.body.value : ctx.body.fields);
    const model = request && (Array.isArray(request.model) ? request.model[0] : request.model);
    if (!meta.models.includes(model)) throw new Error("unsupported Muse model");
    return protocols.openai_video.decodeRequest(Object.assign({}, ctx, { model }));
  },
  videoCreated(ctx, task) {
    const properties = task.properties || {};
    const data = task.data || {};
    return {
      id: task.task_id, object: "video", model: properties.origin_model_name || data.model || "",
      status: "queued", progress: 0, created_at: task.created_at || 0,
    };
  },
};
