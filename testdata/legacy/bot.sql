INSERT INTO channels (channel_key, app_id, chat_type, chat_id, thread_id, created_at)
VALUES ('p2p:oc_legacy:legacy-app', 'legacy-app', 'p2p', 'oc_legacy', '', '2026-06-01T00:00:00Z');

INSERT INTO sessions (id, channel_key, claude_session_id, status, created_by, created_at, updated_at)
VALUES
  ('legacy-active', 'p2p:oc_legacy:legacy-app', 'legacy-thread-1', 'active', 'ou_legacy', '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z'),
  ('legacy-archived', 'p2p:oc_legacy:legacy-app', 'legacy-thread-0', 'archived', 'ou_legacy', '2026-05-31T00:00:00Z', '2026-05-31T00:00:00Z');

INSERT INTO messages (id, session_id, sender_id, role, content, feishu_msg_id, created_at)
VALUES
  ('legacy-message-user', 'legacy-active', 'ou_legacy', 'user', 'legacy hello', 'om_legacy_1', '2026-06-01T00:00:01Z'),
  ('legacy-message-assistant', 'legacy-active', '', 'assistant', 'legacy reply', '', '2026-06-01T00:00:02Z');

INSERT INTO tasks (id, app_id, name, cron_expr, target_type, target_id, prompt, enabled, send_output, post_archive, created_by, created_at)
VALUES
  ('legacy-app/daily', 'legacy-app', 'Daily', '0 9 * * *', 'p2p', 'ou_legacy', 'daily prompt', true, true, false, 'system', '2026-06-01T00:00:00Z'),
  ('legacy-app/disabled', 'legacy-app', 'Disabled', '', '', '', 'disabled prompt', false, false, false, 'system', '2026-06-01T00:00:00Z');
