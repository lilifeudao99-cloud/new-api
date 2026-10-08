# 异步图片批次 API

图片异步插件为 `gpt-image-2`、`gpt-image-2.5-flare`、`gpt-image-2.5-sunburst`、`nano-banana-2` 和 `nano-banana-pro` 提供 OpenAI 风格任务接口。

所有接口使用 New API token：`Authorization: Bearer <AILILI_API_KEY>`。

## 文生图

```http
POST /v1/image-batches/generations
Content-Type: application/json
Idempotency-Key: 生成一个 8–128 字符的唯一值
```

```json
{
  "model": "nano-banana-pro",
  "prompt": "清晨自然光下的现代办公楼建筑摄影",
  "n": 1,
  "size": "2048x1536",
  "quality": "high"
}
```

## 图生图（本地 multipart 上传）

```http
POST /v1/image-batches/edits
Content-Type: multipart/form-data
Idempotency-Key: 生成一个 8–128 字符的唯一值
```

表单字段：`model`、`prompt`（必填）；`image` 可重复上传 1–10 张 PNG、JPEG、GIF 或 WebP 文件；`n`、`size`、`quality` 可选。文件先上传到私有 TOS，服务端向上游提交限时读取 URL，不会向客户返回该 URL。

也可在 JSON 请求中用 `images` 数组提交公开 HTTP(S) 图片地址；异步上游不接受原始文件或 data URL。

## 查询任务和图片

提交成功返回 `batch_id`、`status` 与 `poll_url`。使用 `GET /v1/image-batches/{batch_id}` 查询批次状态；使用 `GET /v1/image-batches/{batch_id}/items` 取得全部任务项。上游分页由服务端自动收齐。成功项的 `output_url` 是 ailili.chat 稳定下载链接；访问时由服务端使用短时 TOS 签名地址读取私有对象。原始上游临时 URL 和 TOS 对象 key 不对外暴露。

结果图片保存在配置的私有对象存储中。图生图输入的上游读取 URL 默认有效 24 小时；内部下载 TOS URL 默认有效 15 分钟，可通过 `TASK_ARTIFACT_STORE_S3_INPUT_PRESIGN_TTL` 与 `TASK_ARTIFACT_STORE_S3_PRESIGN_TTL` 调整（上限 7 天）。
