#!/usr/bin/env python3
"""Render paired S03 raw/event JSONL files as a locally viewable HTML table."""

from __future__ import annotations

import argparse
import html
import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class Row:
    observed_at: str
    event_type: str
    raw_json: str
    seq: int


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Render paired raw/event JSONL as an Event-type then time sorted HTML table."
    )
    parser.add_argument("--raw", type=Path, required=True, help="appserver-raw-*.ndjson")
    parser.add_argument("--event", type=Path, required=True, help="appserver-event-*.jsonl")
    parser.add_argument("--output", type=Path, required=True, help="Output HTML file")
    return parser.parse_args()


def read_lines(path: Path) -> list[str]:
    try:
        with path.open(encoding="utf-8") as handle:
            return [line.rstrip("\n") for line in handle if line.strip()]
    except OSError as error:
        raise ValueError(f"cannot read {path}: {error}") from error


def event_type(raw_message: dict[str, Any], event: dict[str, Any]) -> str:
    method = raw_message.get("method")
    if isinstance(method, str) and method:
        return method
    message_class = event.get("message_class")
    if isinstance(message_class, str) and message_class:
        return f"<{message_class}>"
    return "<unknown>"


def load_rows(raw_path: Path, event_path: Path) -> list[Row]:
    raw_lines = read_lines(raw_path)
    event_lines = read_lines(event_path)
    if len(raw_lines) != len(event_lines):
        raise ValueError(
            f"raw/event row count mismatch: {len(raw_lines)} != {len(event_lines)}"
        )

    rows: list[Row] = []
    for line_number, (raw_line, event_line) in enumerate(
        zip(raw_lines, event_lines, strict=True), start=1
    ):
        try:
            raw_message = json.loads(raw_line)
            event = json.loads(event_line)
        except json.JSONDecodeError as error:
            raise ValueError(f"invalid JSON at paired line {line_number}: {error}") from error
        if not isinstance(raw_message, dict) or not isinstance(event, dict):
            raise ValueError(f"paired line {line_number} is not a JSON object")
        observed_at = event.get("observed_at")
        seq = event.get("seq")
        if not isinstance(observed_at, str) or not isinstance(seq, int):
            raise ValueError(f"event line {line_number} is missing observed_at or integer seq")
        if seq != line_number:
            raise ValueError(f"event line {line_number} has seq={seq}, expected {line_number}")
        rows.append(Row(observed_at, event_type(raw_message, event), raw_line, seq))
    return sorted(rows, key=lambda row: (row.event_type, row.observed_at, row.seq))


def render(rows: list[Row], raw_path: Path, event_path: Path) -> str:
    body = "\n".join(
        "<tr>"
        f"<td>{html.escape(row.observed_at)}</td>"
        f"<td>{html.escape(row.event_type)}</td>"
        f"<td><pre>{html.escape(row.raw_json)}</pre></td>"
        "</tr>"
        for row in rows
    )
    return f"""<!doctype html>
<html lang=\"zh-CN\">
<head>
  <meta charset=\"utf-8\">
  <title>S03 原始 Event 表</title>
  <style>
    body {{ font-family: system-ui, sans-serif; margin: 24px; color: #18212f; }}
    table {{ border-collapse: collapse; width: 100%; table-layout: fixed; }}
    th, td {{ border: 1px solid #cbd5e1; padding: 8px; vertical-align: top; text-align: left; }}
    th {{ background: #e2e8f0; position: sticky; top: 0; }}
    th:nth-child(1), td:nth-child(1) {{ width: 15%; white-space: nowrap; }}
    th:nth-child(2), td:nth-child(2) {{ width: 20%; word-break: break-all; }}
    pre {{ margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; font: 12px/1.45 ui-monospace, monospace; }}
  </style>
</head>
<body>
  <h1>S03 原始 App Server Event</h1>
  <p>排序：Event 类型升序；同类型按收到时间升序。共 {len(rows)} 条。原始 JSON 逐行来自 raw 文件，未重序列化。</p>
  <p>raw: <code>{html.escape(str(raw_path))}</code><br>event index: <code>{html.escape(str(event_path))}</code></p>
  <table>
    <thead><tr><th>时间（UTC）</th><th>Event 类型</th><th>原始 JSON</th></tr></thead>
    <tbody>
{body}
    </tbody>
  </table>
</body>
</html>
"""


def main() -> None:
    args = parse_args()
    rows = load_rows(args.raw, args.event)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(render(rows, args.raw, args.event), encoding="utf-8")
    print(f"wrote {len(rows)} rows to {args.output}")


if __name__ == "__main__":
    main()
