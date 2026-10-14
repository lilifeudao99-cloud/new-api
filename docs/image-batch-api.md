# ailili.chat 异步图片生成 API

本文介绍如何通过 ailili.chat 调用异步文生图和图生图接口。上游受理后返回 ailili.chat 任务 ID，客户端使用该 ID 查询进度；任务完成后可取得上游 OSS 图片链接。此变更版本部署后，新生成的图片直接使用上游存储，不再下载并转存到 TOS。

## 接入信息

- API Base URL：`https://ailili.chat/v1`
- 认证：`Authorization: Bearer <AILILI_API_KEY>`，使用 ailili.chat 后台创建的 API Key。
- 支持模型：`gpt-image-2`、`gpt-image-2.5-flare`、`gpt-image-2.5-sunburst`、`nano-banana-2`、`nano-banana-2.1`、`nano-banana-pro`。
- 当前文档使用 `https://ailili.chat` 域名。若客户网络或代理对其他 API 域名有访问限制，请使用此 Base URL。

请将 API Key 保存在服务端环境变量或密钥管理服务中，不要放在网页前端、移动端应用、公开代码仓库或 URL 查询参数中。

## 文生图：创建任务

```http
POST https://ailili.chat/v1/image-batches/generations
Authorization: Bearer <AILILI_API_KEY>
Content-Type: application/json
Idempotency-Key: <本次请求的唯一值>
```

请求示例：

```bash
curl -X POST 'https://ailili.chat/v1/image-batches/generations' \
  -H "Authorization: Bearer $AILILI_API_KEY" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{
    "model": "nano-banana-2",
    "prompt": "清晨柔和自然光下的现代办公楼建筑摄影",
    "n": 1,
    "resolution": "2K",
    "aspectRatio": "4:3"
  }'
```

成功时返回任务 ID 和初始状态，示例：

```json
{
  "batch_id": "<task_id>",
  "status": "queued",
  "total": 1,
  "queued": 1,
  "running": 0,
  "succeeded": 0,
  "failed": 0,
  "poll_url": "/v1/image-batches/<task_id>"
}
```

`Idempotency-Key` 必须为 8–128 个字符。每个新的生成请求应使用新的随机值（例如 UUID）；在网络超时且无法确认请求是否已受理时，不要立即换一个 Key 重发，以免重复创建任务或产生重复费用。

## 图生图：上传本地参考图

图生图使用 `multipart/form-data`。图片文件直接上传到 ailili.chat，不需要客户先把图片放到公网。服务端会将输入保存到私有对象存储，再为上游生成限时读取地址；限时地址和对象存储内部信息不会作为接口结果返回给客户。

```http
POST https://ailili.chat/v1/image-batches/edits
Authorization: Bearer <AILILI_API_KEY>
Content-Type: multipart/form-data
Idempotency-Key: <本次请求的唯一值>
```

请求示例：

```bash
curl -X POST 'https://ailili.chat/v1/image-batches/edits' \
  -H "Authorization: Bearer $AILILI_API_KEY" \
  -H "Idempotency-Key: $(uuidgen)" \
  -F 'model=nano-banana-2' \
  -F 'prompt=保持参考图建筑主体不变，优化为清晨自然光下的真实建筑摄影' \
  -F 'resolution=2K' \
  -F 'aspectRatio=4:3' \
  -F 'image[]=@./reference.png;type=image/png'
```

表单字段：

| 字段 | 必填 | 说明 |
|---|---|---|
| `model` | 是 | 上述支持的模型之一 |
| `prompt` | 是 | 描述期望的图片内容或编辑要求 |
| `image[]` | 图生图必填 | PNG、JPEG、GIF 或 WebP 参考图；可上传 1–10 张 |
| `n` | 否 | 生成数量，1–64，默认 1 |
| `size` | 否 | `WIDTHxHEIGHT` 格式，例如 `2048x1536`；实际可用尺寸受所选上游模型支持范围影响 |
| `resolution` | 否 | `1K`、`2K` 或 `4K`；与 `aspectRatio` 一起使用时由 ailili.chat 转换为上游 `size` |
| `aspectRatio` | 否 | 可用比例按模型区分；Nano Banana 支持范围见下文，`gpt-image-2` 支持 `21:9`、`16:9`、`3:2`、`4:3`、`5:4`、`1:1`、`4:5`、`3:4`、`2:3`、`9:16` |

`resolution` 和 `aspectRatio` 目前仅开放给 `nano-banana-2`、`nano-banana-2.1`、`nano-banana-pro`、`gpt-image-2`。`gpt-image-2.5-flare` 和 `gpt-image-2.5-sunburst` 继续使用原有 `size` 参数。

`resolution` 和 `aspectRatio` 是异步接口的便捷输入。两者至少提供一个时，网关会补齐缺省值（缺省分辨率为 `1K`，缺省比例为 `1:1`），再按所选模型的官方尺寸表转换成上游批次接口使用的 `size`（`WIDTHxHEIGHT`）。Nano Banana 模型按各自的模型规格映射；`nano-banana-2` 与 `nano-banana-2.1` 使用 Gemini 3.1 Flash Image 的尺寸表，`nano-banana-pro` 使用 Gemini 3 Pro Image 的尺寸表。`gpt-image-2` 使用单独的尺寸配置。若同时传入 `size`，必须与换算结果完全一致，否则提交会返回 `400`。如果客户已经根据上游能力确定了精确像素尺寸，也可以只传 `size`。

Nano Banana 不会通过缩放 4K 尺寸推算 1K/2K，而是逐项使用 Google 官方文档列出的像素尺寸。Nano Banana 2 与 2.1 支持 `1:1`、`1:4`、`1:8`、`2:3`、`3:2`、`3:4`、`4:1`、`4:3`、`4:5`、`5:4`、`8:1`、`9:16`、`16:9`、`21:9`；Nano Banana Pro 支持 `1:1`、`2:3`、`3:2`、`3:4`、`4:3`、`4:5`、`5:4`、`9:16`、`16:9`、`21:9`。Pro 不支持 `1:4`、`1:8`、`4:1`、`8:1`，提交这些比例会被拒绝。

官方参考：[Google Gemini 图片生成文档](https://ai.google.dev/gemini-api/docs/image-generation?hl=zh-cn#python_17)。官方 Gemini 原生接口使用 `aspect_ratio`/`image_size`；本异步上游批次 API 使用 `size` 像素字符串，因此 ailili.chat 将公开的 `aspectRatio`/`resolution` 映射为官方尺寸表对应的 `size`。官方接口没有 `quality` 参数，异步请求也不会发送该字段。`resolution` 不改变计费规则，费用仍按模型广场当前配置和任务数量结算。

常用组合示例：

| 模型 | 分辨率 | 比例 | 转换后的 `size` |
|---|---|---|---|
| `nano-banana-2` / `nano-banana-2.1` | `1K` | `4:3` | `1200x896` |
| `nano-banana-2` / `nano-banana-2.1` | `2K` | `4:3` | `2400x1792` |
| `nano-banana-2` / `nano-banana-2.1` | `4K` | `16:9` | `5504x3072` |
| `nano-banana-2.1` | `4K` | `1:8` | `1536x12288` |
| `nano-banana-pro` | `4K` | `4:3` | `4800x3584` |
| `gpt-image-2` | `4K` | `16:9` | `3840x2160` |

不要把 `1K`、`2K`、`4K` 或 `16:9` 直接填写到 `size`；`size` 仍必须是 `WIDTHxHEIGHT`。同一个请求中不要混用互相冲突的 `size`、`resolution` 和 `aspectRatio`。

异步批次请求不使用 `quality` 字段；即使旧客户端携带该字段，网关也不会将其转发给上游。

图片字段兼容 `image`、`image[]`、`image[0]`、`images`、`images[]` 和 `images[0]` 等常见 multipart 写法。建议统一使用重复的 `image[]` 字段上传多张图。上传文件需满足 ailili.chat 的请求体限制及上游模型的文件大小限制；超过上游限制时任务可能失败。

如使用 JSON 提交图生图，`images` 必须是上游可公开读取的 HTTP(S) 图片 URL 数组。异步上游不接受原始文件或 `data:` URL。涉及客户私有图片时，建议使用 multipart 上传。

## 查询任务进度

使用提交响应中的 `batch_id` 查询：

```bash
curl 'https://ailili.chat/v1/image-batches/<task_id>' \
  -H "Authorization: Bearer $AILILI_API_KEY"
```

响应中的 `status` 可能为：

| 状态 | 含义 |
|---|---|
| `queued` | 已受理，等待处理 |
| `running` | 正在处理 |
| `succeeded` | 任务成功完成；检查 `succeeded` 数量或查询 items 获取结果 |
| `partially_succeeded` | 批次中至少一个结果成功，同时存在失败或未知项 |
| `failed` | 任务失败 |

客户端建议每 **5 秒**查询一次：等待上一次查询结束后再计时，避免重叠请求。遇到 `succeeded`、`partially_succeeded` 或 `failed` 等终态时停止轮询，并查询 items，同时展示成功结果和失败原因。若 `total` 大于 1，使用 `succeeded`、`failed`、`queued` 和 `running` 等计数跟踪批次进度。

`GET` 查询读取网关已保存的状态，不会重新发起生图。图片轮询优化版本部署后，服务端会以 5 秒目标间隔调度上游查询；上一轮仍在查询时不会重叠执行。上游生成和查询耗时仍会影响实际完成时间，5 秒不是完成时限。客户自己的程序需要采用上述查询间隔。

可直接使用 [Python 查询客户端](../bin/poll-image-batch.py)（仅标准库，无需安装依赖）：

```bash
# 预先通过环境变量设置 AILILI_IMAGE_BATCH_API_KEY，不要将 Key 写入脚本。
python3 bin/poll-image-batch.py '<task_id>' --output image-batch-result.json
```

该客户端只查询已有任务，每 5 秒查询一次，终态后读取全部分页结果，将任务状态及每项错误/下载链接保存到权限为 `0600` 的新文件。临时网络故障、HTTP 429/5xx 按 5、10、20、30 秒退避重试，遵守数字形式的 `Retry-After`；其他 HTTP 错误立即报告。默认等待最多 15 分钟，可使用 `--timeout` 修改；超时后保留原任务 ID 继续查询，**不要重新提交 POST 生图请求**。结果文件含访问凭据，请勿公开。

## 获取生成结果

使用 items 接口获取每个生成项的状态和结果链接：

```bash
curl 'https://ailili.chat/v1/image-batches/<task_id>/items' \
  -H "Authorization: Bearer $AILILI_API_KEY"
```

响应示例：

```json
{
  "object": "list",
  "data": [
    {
      "item_id": "<item_id>",
      "status": "succeeded",
      "output_url": "https://<upstream-image-host>/images/<image>.png"
    }
  ],
  "next_cursor": "",
  "has_more": false
}
```

新任务的 `output_url` 保留上游返回的完整 OSS 图片 URL；ailili.chat 不再提前下载图片，也不向 TOS 写入生成结果。请将它作为不透明 URL 使用，不要解析或修改查询参数。链接可能包含访问凭据，不要写入公开日志。链接有效期和图片保留期限由上游决定，本接口不承诺永久下载；需要长期保存时，请在任务完成后下载到客户自己的存储。重新查询任务只会读取已保存的 URL，不能保证刷新上游已过期的链接。

已有任务中保存的 ailili.chat/TOS 下载链接继续按原方式访问。输入参考图与生成结果分开处理：上游异步图生图只接受公网图片 URL，所以本地 multipart 参考图仍需通过 TOS 上传并转换为限时 URL；如果客户已提供公网图片 URL，则不需要这次输入上传。不要关闭 TOS 配置或删除桶、密钥及历史对象，否则本地上传和已有结果下载会受影响。

也可使用 New API 通用任务详情接口：

```http
GET https://ailili.chat/v1/tasks/<task_id>
GET https://ailili.chat/v1/tasks/<task_id>/artifacts
```

这两个通用任务接口使用同一 API Key 认证。完成态下，`/artifacts` 返回 ailili.chat 内容代理链接；新任务的图片由该入口从上游读取，历史 TOS 任务继续从 TOS 下载。入口路径稳定不代表上游图片永久有效。

## 计费说明

费用按 ailili.chat 模型广场中所选模型当前配置的计价规则结算。`nano-banana-2` 和 `nano-banana-pro` 当前按每次 API 调用固定 `$0.15` 计费（本次异步调用对应 75,000 quota）；不要将其误解为每张图片 `$0.15`。其他模型不应套用这两个模型的固定价格。

提交请求会创建计费任务。请勿因客户端轮询超时而重复提交；先用原 `batch_id` 查询任务。任务项级别的成功/失败和实际账单状态以 ailili.chat 任务详情、用量记录及账户账单为准。

## 常见错误与排查

| HTTP 状态 | 常见原因 | 建议 |
|---|---|---|
| `400` | 模型、字段、图片格式、数量或图片输入不符合要求 | 检查必填字段、图片字段命名及模型支持的参数 |
| `401` | API Key 缺失、无效或已撤销 | 在 ailili.chat 后台检查 Key 状态，并使用 `Authorization: Bearer` 传递 |
| `403` | 账号/Key 权限不足，或请求被网络安全层拦截 | 确认 Key 所属账号权限；若响应来自 CDN/安全代理，检查域名和请求来源，不要无限重试 |
| `404` | 任务 ID 不存在，或当前 Key 无权读取该任务 | 确保使用创建任务的账号/Key，并检查任务 ID 是否完整 |
| `413` | 上传请求体过大 | 压缩或缩小参考图片后再提交 |
| `429` | 触发请求频率或并发限制 | 降低提交/轮询频率，按 `Retry-After`（若存在）退避 |
| `5xx` | ailili.chat 或上游临时故障 | 查询原任务状态；仅在明确确认任务未受理后才重新提交 |

如果任务已创建但上游无法下载参考图，检查图片大小、文件类型和上游限制。使用 multipart 上传可避免依赖客户自有图片 URL 的公网可达性。

## 安全建议

- API Key 只放在可信后端，使用环境变量或密钥管理系统加载。
- 不要把 API Key 放进浏览器代码、App 包、URL 或客户可查看的日志。
- 不要公开分享含 `access` 参数的完整结果下载链接；它是临时访问凭据。
- 使用 HTTPS，并为每次新提交生成唯一的 `Idempotency-Key`。
- 将任务 ID、模型、HTTP 状态和脱敏错误信息用于排障；不要记录 Authorization 请求头、完整签名 URL 或图片内容。
