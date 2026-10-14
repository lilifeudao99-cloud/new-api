#!/usr/bin/env python3
"""Poll an existing ailili.chat image batch. Never submits a generation request."""

import argparse
import json
import os
import re
import time
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


def get_json(url, api_key, deadline):
    """Retry only transient GET failures within the caller's overall deadline."""
    for attempt in range(6):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("查询超时，请保留原任务 ID，稍后继续查询，不要重新提交生图。")
        delay = min(5 * 2**attempt, 30)
        request = Request(url, headers={"Authorization": "Bearer " + api_key})
        try:
            with urlopen(request, timeout=min(30, remaining)) as response:
                return json.load(response)
        except HTTPError as error:
            status = error.code
            retry_after = error.headers.get("Retry-After", "")
            error.close()
            if status != 429 and not 500 <= status <= 599:
                raise RuntimeError(f"查询失败：HTTP {status}，请检查权限和任务 ID。") from None
            if retry_after.isdigit():
                delay = max(delay, int(retry_after))
        except (URLError, TimeoutError):
            pass
        if attempt == 5:
            raise RuntimeError("连续查询失败，请保留原任务 ID，稍后继续查询。")
        time.sleep(min(delay, max(0, deadline - time.monotonic())))


def poll_image_batch(task_id, api_key, timeout=900):
    if not re.fullmatch(r"task_[A-Za-z0-9_-]+", task_id):
        raise ValueError("请填写提交接口返回的完整 batch_id。")
    if not api_key or timeout <= 0:
        raise ValueError("请配置 AILILI_IMAGE_BATCH_API_KEY，并使用大于 0 的超时时间。")
    deadline = time.monotonic() + timeout
    url = "https://ailili.chat/v1/image-batches/" + quote(task_id, safe="")
    while True:
        batch = get_json(url, api_key, deadline)
        status = batch.get("status")
        if status in {"succeeded", "partially_succeeded", "failed", "canceled", "cancelled"}:
            break
        if status not in {"queued", "running"}:
            raise RuntimeError("接口返回未知任务状态，请保留任务 ID 并检查响应。")
        # Wait after the response, so requests never overlap.
        time.sleep(min(5, max(0, deadline - time.monotonic())))

    items = []
    cursor = ""
    while True:
        query = urlencode({"limit": 100, "cursor": cursor})
        page = get_json(url + "/items?" + query, api_key, deadline)
        items.extend(page.get("data", []))
        if not page.get("has_more"):
            return {"batch": batch, "items": items}
        next_cursor = page.get("next_cursor")
        if not next_cursor or next_cursor == cursor:
            raise RuntimeError("结果分页异常，请保留任务 ID 并检查响应。")
        cursor = next_cursor


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("task_id")
    parser.add_argument("--timeout", type=float, default=900)
    parser.add_argument("--output", default="image-batch-result.json")
    args = parser.parse_args()
    try:
        result = poll_image_batch(args.task_id, os.environ.get("AILILI_IMAGE_BATCH_API_KEY", ""), args.timeout)
        # Result links contain access credentials; create a private file and
        # refuse to overwrite an existing result.
        descriptor = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            json.dump(result, output, ensure_ascii=False, indent=2)
        print("任务状态：" + result["batch"]["status"] + "；结果已保存至 " + args.output)
    except (OSError, RuntimeError, ValueError) as error:
        parser.exit(1, str(error) + "\n")


if __name__ == "__main__":
    main()
