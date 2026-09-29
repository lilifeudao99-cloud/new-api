function seedanceUsageProfile(models, resolutions) {
  const allResolutionLabels = {
    "480p": { en: "480p", zh: "480P" },
    "720p": { en: "720p", zh: "720P" },
    "1080p": { en: "1080p", zh: "1080P" },
    "4k": { en: "4K", zh: "4K" },
  };
  const resolutionLabels = {};
  for (const resolution of resolutions) resolutionLabels[resolution] = allResolutionLabels[resolution];
  return {
    models: models,
    schema: {
      seconds: {
        type: "number",
        unit: "second",
        description: { en: "Video generation unit price", zh: "视频生成单价" },
      },
      input_seconds: {
        type: "number",
        unit: "second",
        description: { en: "Reference video generation unit price", zh: "参考视频生成单价" },
      },
      resolution: {
        enum: resolutions,
        enumLabels: resolutionLabels,
        description: { en: "Output video resolution", zh: "输出视频分辨率" },
      },
      video_input: {
        enum: ["none", "video"],
        enumLabels: {
          none: { en: "No reference video", zh: "无参考视频" },
          video: { en: "With reference video", zh: "有参考视频" },
        },
        description: { en: "Reference video input", zh: "参考视频输入" },
      },
    },
    examples: [
      { label: resolutions[0] + " · 5s", facts: { seconds: 5, input_seconds: 0, resolution: resolutions[0], video_input: "none" } },
      { label: resolutions[Math.min(1, resolutions.length - 1)] + " · 5s + 3s reference", facts: { seconds: 5, input_seconds: 3, resolution: resolutions[Math.min(1, resolutions.length - 1)], video_input: "video" } },
    ],
  };
}

export const meta = {
  apiVersion: 1,
  key: "doubao",
  name: "Doubao Video",
  icon: "Doubao.Color",
  description: {
    en: "Doubao Seedance video generation with native Ark and OpenAI-compatible upstreams",
    zh: "豆包 Seedance 视频生成，支持原生 Ark 与 OpenAI 兼容上游",
  },
  version: "1.1.0",
  author: { name: "QuantumNous" },
  channelTypes: [54, 45], // VolcEngine-type channels serve Ark video models with the same wire format
  models: [
    "doubao-seedance-1-0-pro-250528",
    "doubao-seedance-1-0-lite-t2v",
    "doubao-seedance-1-0-lite-i2v",
    "doubao-seedance-1-5-pro-251215",
    "doubao-seedance-2-0-260128",
    "doubao-seedance-2-0-fast-260128",
    "doubao-seedance-2-0-mini-260615",
    "doubao-seedance-2-5-260628",
    "seedance-2.0",
    "seedance-2.0-fast",
    "seedance-2.0-mini",
    "seedance-2.5",
  ],
  fetchMode: "per_task",
  usageSchema: {
    // Upstream billing tokens (estimated at submit, actual on completion).
    tokens: {
      type: "number",
      unit: "token",
      description: { en: "Billing token unit price", zh: "计费 Token 单价" },
    },
    // Output video resolution; Seedance token unit price varies by resolution tier.
    resolution: {
      enum: ["480p", "720p", "1080p", "4k"],
      enumLabels: {
        "480p": { en: "480p", zh: "480p" },
        "720p": { en: "720p", zh: "720p" },
        "1080p": { en: "1080p", zh: "1080p" },
        "4k": { en: "4k", zh: "4k" },
      },
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
    // Whether the request includes reference video input; Seedance prices video-to-video tokens at a lower unit rate.
    video_input: {
      enum: ["none", "video"],
      enumLabels: { none: { en: "No reference video", zh: "无参考视频" }, video: { en: "With reference video", zh: "有参考视频" } },
      description: { en: "Reference video input", zh: "参考视频输入" },
    },
  },
  // Official Ark formula tokens = (input + output seconds) × W × H × 24 / 1024,
  // 16:9 max-pixel sizes, cross-checked against Volcengine price examples.
  usageExamples: [
    { label: "480p · 5s", facts: { tokens: 48038, resolution: "480p", video_input: "none" } },
    { label: "720p · 5s", facts: { tokens: 108000, resolution: "720p", video_input: "none" } },
    { label: "1080p · 5s", facts: { tokens: 243000, resolution: "1080p", video_input: "none" } },
    { label: "4k · 5s", facts: { tokens: 972000, resolution: "4k", video_input: "none" } },
    { label: "720p · 10s", facts: { tokens: 216000, resolution: "720p", video_input: "none" } },
    { label: "720p · 5s (+4s 输入视频)", facts: { tokens: 194400, resolution: "720p", video_input: "video" } },
  ],
  usageProfiles: [
    seedanceUsageProfile(["seedance-2.0"], ["480p", "720p", "1080p", "4k"]),
    seedanceUsageProfile(["seedance-2.0-fast"], ["720p"]),
    seedanceUsageProfile(["seedance-2.0-mini"], ["480p", "720p"]),
    seedanceUsageProfile(["seedance-2.5"], ["480p", "720p", "1080p"]),
  ],
  routes: [
    { method: "POST", path: "/doubao/api/v3/contents/generations/tasks", type: "submit", decode: "createTask", render: "taskCreated" },
    { method: "GET", path: "/doubao/api/v3/contents/generations/tasks/:task_id", type: "query", render: "taskStatus" },
  ],
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }, "openai_video"],
};

function trimmed(value) {
  return String(value || "").trim();
}

function draftTaskIds(content) {
  const ids = [];
  if (!Array.isArray(content)) return ids;
  for (const item of content) {
    if (!item || typeof item !== "object" || Array.isArray(item)) continue;
    if (item.type !== "draft_task") continue;
    const draft = item.draft_task;
    if (!draft || typeof draft !== "object" || Array.isArray(draft)) continue;
    const id = trimmed(draft.id);
    if (id) ids.push(id);
  }
  return ids;
}

function rewriteDraftTaskContent(content, originTasks) {
  if (!Array.isArray(content)) return content;
  return content.map(function (item) {
    if (!item || typeof item !== "object" || Array.isArray(item) || item.type !== "draft_task") return item;
    const draft = item.draft_task;
    if (!draft || typeof draft !== "object" || Array.isArray(draft) || !trimmed(draft.id)) return item;
    const publicId = trimmed(draft.id);
    let upstream = "";
    if (Array.isArray(originTasks)) {
      for (const task of originTasks) {
        if (task && task.taskId === publicId) {
          upstream = trimmed(task.upstreamTaskId);
          break;
        }
      }
    }
    if (!upstream) throw new Error("origin task is unavailable");
    return Object.assign({}, item, { draft_task: Object.assign({}, draft, { id: upstream }) });
  });
}

function normalizeResolution(value) {
  const raw = trimmed(value).toLowerCase();
  if (["480p", "720p", "1080p", "4k"].includes(raw)) return raw;
  const parts = raw.replace(/\*/g, "x").split("x");
  if (parts.length !== 2) return "720p";
  const max = Math.max(Number(parts[0]), Number(parts[1]));
  if (max >= 3840) return "4k";
  if (max >= 1920) return "1080p";
  if (max >= 1280) return "720p";
  return "480p";
}

function xinfengSupportedResolutions(model) {
  switch (trimmed(model)) {
    case "seedance-2.0-fast":
      return ["720p"];
    case "seedance-2.0-mini":
      return ["480p", "720p"];
    case "seedance-2.5":
      return ["480p", "720p", "1080p"];
    case "seedance-2.0":
      return ["480p", "720p", "1080p", "4k"];
    default:
      return [];
  }
}

function canonicalXinfengResolution(value) {
  const raw = trimmed(value).toLowerCase();
  if (["480p", "720p", "1080p", "4k"].includes(raw)) return raw;
  const parts = raw.replace(/\*/g, "x").split("x");
  if (parts.length !== 2 || parts.some((part) => !/^\d+(?:\.\d+)?$/.test(part) || Number(part) <= 0)) return "";
  return normalizeResolution(raw);
}

function validateXinfengResolution(req, model) {
  if (!isXinfengSeedanceModel(model)) return;
  for (const requested of [req.resolution, req.size]) {
    if (requested === undefined || trimmed(requested) === "") continue;
    const resolution = canonicalXinfengResolution(requested);
    if (!xinfengSupportedResolutions(model).includes(resolution)) {
      throw new Error("unsupported resolution for " + trimmed(model));
    }
  }
}

function isXinfengSeedanceModel(model) {
  return ["seedance-2.0", "seedance-2.0-fast", "seedance-2.0-mini", "seedance-2.5"].includes(trimmed(model));
}

function isVideoReferenceValue(value) {
  if (value && typeof value === "object" && !Array.isArray(value)) return isVideoReferenceValue(value.url || value.video_url || value.uri);
  const raw = trimmed(value).toLowerCase();
  return raw.startsWith("data:video/") || /\.(?:mp4|webm|mov|m4v)(?:$|[?#])/.test(raw);
}

function hasReferenceInput(req, ctx) {
  if (trimmed(req.input_reference) || trimmed(req.input_video) || trimmed(req.video)) return true;
  return (ctx && Array.isArray(ctx.files) && ctx.files.some((file) => file && ["input_reference", "input_video", "video"].includes(file.field))) || false;
}

function hasReferenceVideo(req, ctx) {
  if (trimmed(req.input_video) || trimmed(req.video)) return true;
  if (isVideoReferenceValue(req.input_reference)) return true;
  return (ctx && Array.isArray(ctx.files) && ctx.files.some((file) => {
    if (!file || !["input_reference", "input_video", "video"].includes(file.field)) return false;
    return String(file.mimeType || "").toLowerCase().startsWith("video/") || isVideoReferenceValue(file.filename);
  })) || false;
}

function referenceVideoSeconds(req, ctx) {
  for (const value of [req.input_seconds, req.reference_video_duration, req.input_reference_duration, ctx && ctx.inputVideoSeconds]) {
    const seconds = Number(value);
    if (Number.isFinite(seconds) && seconds > 0) return Math.min(seconds, 3600);
  }
  return 0;
}

function normalizeXinfengRequest(req, ctx, model) {
  const body = Object.assign({}, req || {});
  body.model = model;
  validateXinfengResolution(body, model);
  if (body.resolution !== undefined) body.size = normalizeResolution(body.resolution);
  if (body.size !== undefined) body.size = normalizeResolution(body.size);
  // These fields are gateway billing hints, not OpenAI video request fields.
  delete body.resolution;
  delete body.reference_video_duration;
  delete body.input_reference_duration;
  delete body.input_seconds;
  return body;
}

function hasVideo(content) {
  return Array.isArray(content) && content.some((item) => item && (item.type === "video_url" || Object.prototype.hasOwnProperty.call(item, "video_url")));
}

// Max-pixel 16:9 dimensions per resolution tier. Used when ratio is absent or
// adaptive so the submit-time estimate overestimates rather than underestimates.
// Official Ark formula: tokens = seconds × width × height × 24 / 1024.
// Video input duration is omitted; extractUsageOnComplete overlays the real bill.
function resolutionMaxPixels(resolution) {
  if (resolution === "480p") return [854, 480];
  if (resolution === "1080p") return [1920, 1080];
  if (resolution === "4k") return [3840, 2160];
  return [1280, 720];
}

function estimateTokens(seconds, resolution) {
  const dims = resolutionMaxPixels(resolution);
  return (seconds * dims[0] * dims[1] * 24) / 1024;
}

function videoInputRatio(model, resolution, content) {
  const video = hasVideo(content);
  const res = trimmed(resolution).toLowerCase();
  if (model === "doubao-seedance-2-5-260628") {
    if (res === "1080p") return video ? 7.0 / 10.7 : 11.7 / 10.7;
    return video ? 42 / 70 : 1;
  }
  if (model === "doubao-seedance-2-0-260128") {
    if (res === "1080p") return video ? 31 / 46 : 51 / 46;
    if (res === "4k") return video ? 16 / 46 : 26 / 46;
    return video ? 28 / 46 : 1;
  }
  if (model === "doubao-seedance-2-0-fast-260128") return video ? 22 / 37 : 1;
  if (model === "doubao-seedance-2-0-mini-260615") return video ? 14 / 23 : 1;
  return 1;
}

function responsesInput(req) {
  const texts = [],
    images = [];
  const input = req.input;
  if (typeof input === "string") texts.push(input);
  else if (Array.isArray(input)) {
    for (const item of input) {
      if (typeof item === "string") {
        texts.push(item);
        continue;
      }
      if (!item || typeof item !== "object" || Array.isArray(item)) continue;
      const content = item.content === undefined ? [item] : Array.isArray(item.content) ? item.content : [item.content];
      for (const part of content) {
        if (typeof part === "string") {
          texts.push(part);
          continue;
        }
        if (!part || typeof part !== "object" || Array.isArray(part)) continue;
        if (["input_text", "text"].includes(part.type) && typeof part.text === "string") texts.push(part.text);
        if (["input_image", "image_url"].includes(part.type)) {
          let image = part.image_url;
          if (image && typeof image === "object") image = image.url;
          if (trimmed(image)) images.push(trimmed(image));
        }
      }
    }
  }
  return {
    prompt: texts
      .filter(function (text) {
        return trimmed(text);
      })
      .join("\n"),
    images: images,
  };
}

function responsesVideoText(ctx) {
  const artifact = ctx && ctx.artifacts && ctx.artifacts.video;
  const url = trimmed(artifact && artifact.url);
  if (!url) throw new Error("video artifact is unavailable");
  const escaped = url.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return '<video controls src="' + escaped + '"></video>';
}

export const native = {
  createTask: function (ctx) {
    if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
    const body = ctx.body.value;
    if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("request body must be an object");
    const model = trimmed(body.model);
    if (!model) throw new Error("model is required");
    if (body.content !== undefined && !Array.isArray(body.content)) throw new Error("content must be an array");
    const content = Array.isArray(body.content) ? body.content : [];
    const texts = [];
    let hasReference = false;
    for (const item of content) {
      if (!item || typeof item !== "object" || Array.isArray(item)) continue;
      if (item.type === "text" && typeof item.text === "string") texts.push(item.text);
      else hasReference = true;
    }
    if (!texts.length && !hasReference) throw new Error("content is required");
    const requestBody = {
      model: model,
      prompt: texts
        .filter(function (text) {
          return trimmed(text);
        })
        .join("\n"),
      metadata: body,
    };
    const seconds = Number(body.duration);
    if (Number.isFinite(seconds) && seconds > 0) requestBody.seconds = seconds;
    const intent = { kind: "submit", model: model, action: hasReference ? "image_to_video" : "text_to_video", requestBody: requestBody };
    const originTaskIds = draftTaskIds(content);
    if (originTaskIds.length) intent.originTaskIds = originTaskIds;
    return intent;
  },
  taskCreated: function (ctx, task) {
    const data = task.data && typeof task.data === "object" && !Array.isArray(task.data) ? task.data : {};
    return Object.assign({}, data, { id: task.task_id });
  },
  taskStatus: function (ctx, task) {
    if (task.data && typeof task.data === "object" && !Array.isArray(task.data)) return Object.assign({}, task.data, { id: task.task_id });
    const statusMap = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "running", SUCCESS: "succeeded", FAILURE: "failed" };
    const output = { id: task.task_id, status: statusMap[task.status] || "queued" };
    if (task.fail_reason) output.error = { message: task.fail_reason };
    return output;
  },
  error: function (ctx, error) {
    return { error: { code: error.code, message: error.message } };
  },
};

export function buildSubmitRequest(ctx) {
  const req = ctx.requestBody;
  const model = ctx.upstreamModel || req.model || ctx.model;
  if (isXinfengSeedanceModel(model)) {
    const values = normalizeXinfengRequest(req, ctx, model);
    const headers = { Authorization: "Bearer " + ctx.apiKey };
    if ((ctx.files || []).length) {
      const parts = [];
      for (const key of Object.keys(values)) {
        if (key === "metadata" || values[key] === undefined || values[key] === null || typeof values[key] === "object") continue;
        parts.push({ name: key, value: values[key] });
      }
      if (values.metadata && typeof values.metadata === "object" && !Array.isArray(values.metadata)) {
        parts.push({ name: "metadata", value: JSON.stringify(values.metadata) });
      }
      for (const file of ctx.files) parts.push({ name: file.field, fileRef: file.ref, filename: file.filename });
      return {
        url: ctx.baseUrl + "/v1/videos",
        method: "POST",
        headers,
        bodyType: "multipart",
        parts,
        action: hasReferenceInput(values, ctx) ? "image_to_video" : "text_to_video",
      };
    }
    headers["Content-Type"] = "application/json";
    return {
      url: ctx.baseUrl + "/v1/videos",
      method: "POST",
      headers,
      body: values,
      action: hasReferenceInput(values, ctx) ? "image_to_video" : "text_to_video",
    };
  }
  const metadata = req.metadata || {};
  const body = Object.assign({ model: req.model || "", content: [] }, metadata);
  const imageContent = [];
  const images = Array.isArray(req.images) ? req.images : [];
  for (const url of images) imageContent.push({ type: "image_url", image_url: { url: url } });
  const metadataContent = Array.isArray(body.content) ? body.content : [];
  body.content = imageContent.concat(metadataContent).filter((item) => item && item.type !== "text");
  const hasReference = body.content.length > 0;
  if (trimmed(req.prompt) || !hasReference) body.content.push({ type: "text", text: req.prompt || "" });
  if (Array.isArray(body.content)) body.content = rewriteDraftTaskContent(body.content, ctx.originTasks);
  const seconds = Number.parseInt(req.seconds || "", 10);
  if (seconds > 0) body.duration = seconds;
  body.model = ctx.upstreamModel || body.model;
  return {
    url: ctx.baseUrl + "/api/v3/contents/generations/tasks",
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
    body: body,
    action: hasReference ? "image_to_video" : "text_to_video",
    rewriteModel: body.model,
  };
}

export function parseSubmitResponse(ctx, resp) {
  if (isXinfengSeedanceModel(ctx.upstreamModel || ctx.model)) {
    const body = resp.body || {};
    const taskId = body.id || body.task_id;
    if (!taskId) throw new Error("task_id is empty");
    return { taskId, taskData: body };
  }
  if (!resp.body || !resp.body.id) throw new Error("task_id is empty");
  return { taskId: resp.body.id, taskData: resp.body };
}

export function extractUsage(ctx) {
  const req = ctx.requestBody || {};
  const model = ctx.upstreamModel || ctx.model;
  if (isXinfengSeedanceModel(model)) {
    let seconds = Number(req.seconds || req.duration || 4);
    if (!Number.isFinite(seconds) || seconds <= 0) seconds = 4;
    const videoInput = hasReferenceVideo(req, ctx);
    return {
      seconds: Math.min(seconds, 3600),
      input_seconds: videoInput ? referenceVideoSeconds(req, ctx) : 0,
      resolution: normalizeResolution(req.resolution || req.size || "720p"),
      video_input: videoInput ? "video" : "none",
    };
  }
  const metadata = req.metadata || {};
  if (ctx.usagePurpose === "billing_ratios") {
    const ratio = videoInputRatio(ctx.upstreamModel || ctx.model, metadata.resolution, metadata.content);
    return ratio === 1 ? null : { video_input_ratio: ratio };
  }
  let seconds = Number(req.seconds || req.duration || metadata.duration || 0);
  if (!Number.isFinite(seconds) || seconds <= 0) {
    const frames = Number(metadata.frames);
    seconds = Number.isFinite(frames) && frames > 0 ? Math.floor(frames / 24) : 15;
  }
  if (seconds <= 0) seconds = 5;
  seconds = Math.min(seconds, 3600);
  const rawResolution = metadata.resolution || req.size;
  const raw = trimmed(rawResolution).toLowerCase();
  const recognized = ["480p", "720p", "1080p", "4k"].includes(raw) || raw.replace("*", "x").split("x").length === 2;
  const resolution = recognized ? normalizeResolution(rawResolution) : "1080p";
  return {
    tokens: estimateTokens(seconds, resolution),
    resolution: resolution,
    video_input: hasVideo(metadata.content) ? "video" : "none",
  };
}

export function buildQueryRequest(ctx) {
  if (isXinfengSeedanceModel(ctx.upstreamModel || ctx.model)) {
    return { url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId), method: "GET", headers: { Authorization: "Bearer " + ctx.apiKey } };
  }
  return {
    url: ctx.baseUrl + "/api/v3/contents/generations/tasks/" + ctx.taskId,
    method: "GET",
    headers: { Accept: "application/json", "Content-Type": "application/json", Authorization: "Bearer " + ctx.apiKey },
  };
}

export function parseTaskResult(ctx, body) {
  if (isXinfengSeedanceModel(ctx.upstreamModel || ctx.model)) {
    const statuses = { queued: "QUEUED", pending: "QUEUED", processing: "IN_PROGRESS", in_progress: "IN_PROGRESS", completed: "SUCCESS", succeeded: "SUCCESS", failed: "FAILURE", cancelled: "FAILURE" };
    const rawStatus = trimmed(body.status).toLowerCase();
    const mapped = statuses[rawStatus];
    const result = { status: mapped || "UNKNOWN" };
    if (result.status === "SUCCESS") {
      const content = body.content || {};
      result.url = body.video_url || content.video_url || content.url || body.url || "";
    }
    if (!mapped) result.reason = "unrecognized status: " + String(body.status || "");
    if (result.status === "FAILURE") result.reason = body.error && body.error.message ? body.error.message : "task failed";
    const progress = Number(String(body.progress === undefined ? "" : body.progress).replace("%", ""));
    if (Number.isFinite(progress) && progress > 0 && progress < 100) result.progress = progress + "%";
    return result;
  }
  if (body.status === "pending" || body.status === "queued") return { status: "QUEUED", progress: "10%" };
  if (body.status === "processing" || body.status === "running") return { status: "IN_PROGRESS", progress: "50%" };
  if (body.status === "succeeded") {
    const result = { status: "SUCCESS", progress: "100%", url: body.content && body.content.video_url ? body.content.video_url : "" };
    const usage = body.usage || {};
    const completionTokens = Number(usage.completion_tokens || 0);
    const totalTokens = Number(usage.total_tokens || 0);
    if (Number.isFinite(completionTokens) && completionTokens > 0) result.completionTokens = completionTokens;
    if (Number.isFinite(totalTokens) && totalTokens > 0) result.totalTokens = totalTokens;
    return result;
  }
  if (body.status === "failed" || body.status === "expired" || body.status === "cancelled") {
    const reason = body.error && body.error.message ? body.error.message : body.status;
    return { status: "FAILURE", progress: "100%", reason: reason };
  }
  return { status: "UNKNOWN", reason: "unrecognized status: " + String(body.status || "") };
}

function artifactData(ctx) {
  const data = (ctx && ctx.data) || {};
  if (data.data && typeof data.data === "object" && data.data.task_id && Object.prototype.hasOwnProperty.call(data.data, "data")) return data.data.data || {};
  return data;
}

export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  const data = task.data && typeof task.data === "object" && !Array.isArray(task.data) ? task.data : {};
  const model = trimmed(task.upstreamModel || task.model || data.model || data.upstream_model || data.upstreamModel);
  if (isXinfengSeedanceModel(model)) return [{ key: "video", type: "video" }];
  const content = artifactData(task).content || {};
  const artifacts = [];
  if (trimmed(content.video_url)) artifacts.push({ key: "video", type: "video" });
  if (trimmed(content.last_frame_url)) artifacts.push({ key: "last_frame", type: "image", mimeType: "image/png" });
  return artifacts;
}

export function buildContentRequest(ctx) {
  if (isXinfengSeedanceModel(ctx.upstreamModel || ctx.model)) {
    if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
    return { url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.upstreamTaskId) + "/content", method: ctx.clientRequest.method, headers: { Authorization: "Bearer " + ctx.apiKey } };
  }
  const content = artifactData(ctx).content || {};
  const urls = { video: content.video_url, last_frame: content.last_frame_url };
  const url = trimmed(urls[ctx.artifactKey]);
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export function extractUsageOnComplete(task, taskResult, body) {
  const model = trimmed(task && (task.upstreamModel || task.model));
  if (isXinfengSeedanceModel(model)) {
    const source = body || {};
    const facts = {};
    const usage = source.usage && typeof source.usage === "object" ? source.usage : {};
    const seconds = Number(source.seconds || source.duration || source.output_seconds || usage.seconds || usage.output_seconds || 0);
    if (Number.isFinite(seconds) && seconds > 0) facts.seconds = Math.min(seconds, 3600);
    if (source.resolution !== undefined || source.size !== undefined) facts.resolution = normalizeResolution(source.resolution || source.size);
    const inputSeconds = Number(source.input_seconds === undefined ? usage.input_seconds : source.input_seconds);
    if (Number.isFinite(inputSeconds) && inputSeconds > 0) {
      facts.input_seconds = Math.min(inputSeconds, 3600);
      facts.video_input = "video";
    }
    return facts;
  }
  if (!body || body.status !== "succeeded") return {};
  const facts = {};
  const usage = body.usage || {};
  let tokens = Number(usage.completion_tokens);
  if (!Number.isFinite(tokens) || tokens <= 0) tokens = Number(usage.total_tokens);
  if (Number.isFinite(tokens) && tokens > 0) facts.tokens = tokens;
  const content = body.content || {};
  const resolution = trimmed(content.resolution || body.resolution).toLowerCase();
  if (["480p", "720p", "1080p", "4k"].includes(resolution)) facts.resolution = resolution;
  return facts;
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      const model = trimmed(req.model);
      if (!model) throw new Error("model is required");
      if (req.input !== undefined && typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      if (req.images !== undefined && !Array.isArray(req.images)) throw new Error("images must be an array");
      if (req.metadata !== undefined && (!req.metadata || typeof req.metadata !== "object" || Array.isArray(req.metadata)))
        throw new Error("metadata must be an object");
      validateXinfengResolution(req, model);
      const input = responsesInput(req);
      const prompt = input.prompt || trimmed(req.prompt);
      const images = [];
      for (const image of [req.image, req.input_reference].concat(req.images || [], input.images)) {
        if (trimmed(image) && !images.includes(trimmed(image))) images.push(trimmed(image));
      }
      if (!prompt && images.length === 0) throw new Error("input is required");
      const metadata = Object.assign({}, req.metadata || {});
      if (Object.prototype.hasOwnProperty.call(req, "resolution")) metadata.resolution = req.resolution;
      else if (req.size && !metadata.resolution) metadata.resolution = normalizeResolution(req.size);
      const requestBody = { model: model, prompt: prompt, metadata: metadata };
      if (images.length) requestBody.images = images;
      if (Object.prototype.hasOwnProperty.call(req, "seconds")) requestBody.seconds = req.seconds;
      else if (Object.prototype.hasOwnProperty.call(req, "duration")) requestBody.seconds = req.duration;
      if (Object.prototype.hasOwnProperty.call(req, "size")) requestBody.size = req.size;
      if (Object.prototype.hasOwnProperty.call(req, "resolution")) requestBody.resolution = req.resolution;
      if (Object.prototype.hasOwnProperty.call(req, "reference_video_duration")) requestBody.reference_video_duration = req.reference_video_duration;
      if (Object.prototype.hasOwnProperty.call(req, "input_seconds")) requestBody.input_seconds = req.input_seconds;
      const intent = { kind: "submit", model: model, action: images.length ? "image_to_video" : "text_to_video", requestBody: requestBody };
      const originTaskIds = draftTaskIds(metadata.content);
      if (originTaskIds.length) intent.originTaskIds = originTaskIds;
      return intent;
    },
    renderEvents: function (ctx, task, previousState) {
      const status = String(task.status || "UNKNOWN").toUpperCase();
      const value = Number(String(task.progress || "").replace("%", ""));
      const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
      const state = { status: status, progress: progress };
      if (status === "SUCCESS") {
        const text = responsesVideoText(ctx);
        const events = previousState && previousState.status === status ? [] : [{ type: "output", data: text }];
        return { events: events, state: state, done: true };
      }
      if (status === "FAILURE")
        return { events: [{ type: "error", code: "task_failed", message: task.fail_reason || "task failed" }], state: state, done: true };
      if (previousState && previousState.status === status && previousState.progress === progress) return { events: [], state: state, done: false };
      const event = { type: "progress", message: status.toLowerCase() };
      if (progress !== null) event.progress = progress;
      return { events: [event], state: state, done: false };
    },
    renderFinal: function (ctx, _task) {
      return {
        output: [
          {
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", text: responsesVideoText(ctx), annotations: [], logprobs: [] }],
          },
        ],
        metadata: { vendor: "doubao" },
      };
    },
  },
};

const legacyRenderers = {
  openai_video: function (task) {
    const data = task.data || {};
    const statusMap = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
    const output = {
      id: task.task_id,
      object: "video",
      model: task.properties ? task.properties.origin_model_name || "" : "",
      status: statusMap[task.status] || "unknown",
      progress: Number(String(task.progress || "0").replace("%", "")),
      created_at: task.created_at,
      completed_at: task.updated_at,
    };
    if (data.status === "failed") output.error = { message: data.error ? data.error.message || "" : "", code: data.error ? data.error.code || "" : "" };
    return output;
  },
};

protocols.openai_video = {
  decodeRequest: function (ctx) {
    if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) throw new Error("JSON or multipart body required");
    if (ctx.body.kind === "json") {
      if (!ctx.body.value || Array.isArray(ctx.body.value)) throw new Error("JSON object required");
      const req = ctx.body.value;
      validateXinfengResolution(req, ctx.model);
      const seconds = req.seconds === undefined ? req.duration : req.seconds;
      if (seconds !== undefined && (!Number.isFinite(Number(seconds)) || Number(seconds) <= 0 || Number(seconds) > 3600))
        throw new Error("seconds must be between 1 and 3600");
      for (const name of ["reference_video_duration", "input_seconds"]) {
        if (req[name] !== undefined && (!Number.isFinite(Number(req[name])) || Number(req[name]) < 0 || Number(req[name]) > 3600))
          throw new Error(name + " must be between 0 and 3600");
      }
      return {
        kind: "submit",
        model: ctx.model,
        action: req.input_reference || req.input_video || req.video || req.image ? "image_to_video" : "text_to_video",
        requestBody: Object.assign({}, req, { model: ctx.model }),
      };
    }
    const first = function (name) {
      const values = (ctx.body.fields || {})[name] || [];
      if (values.length > 1) throw new Error(name + " must be provided once");
      return values[0];
    };
    const req = {};
    const fields = ctx.body.fields || {};
    for (const name of Object.keys(fields)) {
      req[name] = first(name);
    }
    if (req.metadata !== undefined) {
      let parsed;
      try {
        parsed = JSON.parse(req.metadata);
      } catch (e) {
        throw new Error("metadata must be a JSON object string");
      }
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("metadata must be a JSON object string");
      req.metadata = parsed;
    }
    if ((ctx.body.files || []).length && !isXinfengSeedanceModel(ctx.model)) throw new Error("Doubao requires image and video references to be URLs inside metadata.content");
    if (req.seconds !== undefined) req.seconds = Number(req.seconds);
    else if (req.duration !== undefined) req.seconds = Number(req.duration);
    for (const name of ["reference_video_duration", "input_seconds"]) {
      if (req[name] !== undefined) req[name] = Number(req[name]);
      if (req[name] !== undefined && (!Number.isFinite(req[name]) || req[name] < 0 || req[name] > 3600)) throw new Error(name + " must be between 0 and 3600");
    }
    validateXinfengResolution(req, ctx.model);
    const seconds = req.seconds === undefined ? req.duration : req.seconds;
    if (seconds !== undefined && (!Number.isFinite(Number(seconds)) || Number(seconds) <= 0 || Number(seconds) > 3600))
      throw new Error("seconds must be between 1 and 3600");
    return {
      kind: "submit",
      model: ctx.model,
      action: (ctx.body.files || []).length || req.input_reference || req.input_video || req.video || req.image ? "image_to_video" : "text_to_video",
      requestBody: Object.assign({}, req, { model: ctx.model }),
    };
  },
  render: function (ctx, task) {
    return legacyRenderers.openai_video(task);
  },
};
