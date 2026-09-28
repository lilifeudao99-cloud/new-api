export type VideoSampleLang = 'curl' | 'python' | 'typescript' | 'javascript'

export type VideoSampleContext = {
  baseUrl: string
  apiKeyEnv: string
  modelName: string
  endpointType: string
  endpointPath: string
}

export function buildVideoSample(
  lang: VideoSampleLang,
  ctx: VideoSampleContext
): string {
  const url = `${ctx.baseUrl}${ctx.endpointPath}`
  const payload = {
    model: ctx.modelName,
    prompt: 'A paper crane unfolds and flies over a quiet lake at sunset.',
    seconds: 5,
    size: '720p',
  }
  const body = JSON.stringify(payload, null, 2)

  if (lang === 'curl') {
    return [
      `response=$(curl -fsS ${url} \\`,
      `  -H "Authorization: Bearer $${ctx.apiKeyEnv}" \\`,
      `  -H "Content-Type: application/json" \\`,
      `  -d '${body.replaceAll('\n', '\n     ')}')`,
      '',
      "task_id=$(printf '%s' \"$response\" | jq -r '.id')",
      'for attempt in $(seq 1 120); do',
      `  task=$(curl -fsS "${url}/$task_id" -H "Authorization: Bearer $${ctx.apiKeyEnv}")`,
      "  status=$(printf '%s' \"$task\" | jq -r '.status')",
      '  printf \'%s\\n\' "$status"',
      '  case "$status" in',
      '    completed) printf \'%s\\n\' "$task" | jq .; break ;;',
      '    failed|cancelled) printf \'%s\\n\' "$task" | jq . >&2; exit 1 ;;',
      '  esac',
      '  if [ "$attempt" -eq 120 ]; then echo "Timed out waiting for video task" >&2; exit 1; fi',
      '  sleep 5',
      'done',
    ].join('\n')
  }

  if (lang === 'python') {
    return [
      'import os',
      'import requests',
      'import time',
      '',
      `url = "${url}"`,
      'headers = {',
      `    "Authorization": f"Bearer {os.environ['${ctx.apiKeyEnv}']}",`,
      '    "Content-Type": "application/json",',
      '}',
      `payload = ${JSON.stringify(payload)}`,
      'task = requests.post(url, headers=headers, json=payload)',
      'task.raise_for_status()',
      'task = task.json()',
      '',
      'for attempt in range(120):',
      '    result = requests.get(f"{url}/{task[\'id\']}", headers=headers)',
      '    result.raise_for_status()',
      '    task = result.json()',
      '    print(task["status"])',
      '    if task["status"] == "completed":',
      '        break',
      '    if task["status"] in {"failed", "cancelled"}:',
      '        raise RuntimeError(task)',
      '    if attempt == 119:',
      '        raise TimeoutError("Timed out waiting for video task")',
      '    time.sleep(5)',
      'print(task)',
    ].join('\n')
  }

  const jsBody = body.replaceAll('\n', '\n  ')
  return [
    `import { setTimeout as sleep } from 'node:timers/promises'`,
    '',
    `const url = '${url}'`,
    `const headers = { Authorization: \`Bearer \${process.env.${ctx.apiKeyEnv}}\`, 'Content-Type': 'application/json' }`,
    '',
    'const taskResponse = await fetch(url, {',
    "  method: 'POST',",
    '  headers,',
    `  body: JSON.stringify(${jsBody}),`,
    '})',
    'if (!taskResponse.ok) throw new Error(await taskResponse.text())',
    'const task = await taskResponse.json()',
    'let result = task',
    'for (let attempt = 0; attempt < 120; attempt += 1) {',
    '  const resultResponse = await fetch(`${url}/${task.id}`, { headers })',
    '  if (!resultResponse.ok) throw new Error(await resultResponse.text())',
    '  result = await resultResponse.json()',
    '  console.log(result.status)',
    '  if (result.status === "completed") break',
    '  if (["failed", "cancelled"].includes(result.status)) throw new Error(JSON.stringify(result))',
    '  if (attempt === 119) throw new Error("Timed out waiting for video task")',
    '  await sleep(5000)',
    '}',
    'console.log(result)',
  ].join('\n')
}
