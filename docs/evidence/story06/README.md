# Story 06 Evidence

This directory stores repeatable local dev/test evidence for Story 06.

Run:

```bash
bash scripts/story06_smoke.sh
```

The script replaces and writes the latest evidence under
`docs/evidence/story06/latest/`.

Required evidence:

- health: `/health` response in `health.json`.
- debug dispatch: `/debug/dispatch` response in `debug-dispatch.json`.
- task run: `/debug/task/run` response in `task-run.json`.
- SQLite: message and turn queries in `sqlite-messages.txt` and `sqlite-turns.txt`.
- artifact cleanliness: `git status --ignored --short` output in `git-status-ignored.txt`.
- debug disabled: 404 check in `debug-disabled.txt`.

The script starts from `config.yaml.template`, explicitly enables debug only for
the smoke run, injects a local test token, and then verifies the same routes are
unavailable when debug is disabled.

This evidence is for the Story 06 mock scaffold. Real Feishu WebSocket, real SDK sender, real Codex app-server runtime, and production tracing remain follow-up story scope.
