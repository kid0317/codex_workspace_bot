# 附件落盘保留原始文件名

## 目标

让新上传的飞书文件在本地附件目录中保留经净化的原始文件名，替代固定的 `payload`，方便操作者直接识别文件内容。

## 范围

- 新上传的 image/file 附件落盘为其安全原始文件名。
- 保持既有 App、频道、session 与 attachment UUID 目录层级。
- 更新本地路径传递、持久化路径与到期清理的契约。
- 保持历史 `payload` 附件可读取、可清理；不迁移历史行或文件。

## 非目标

- 不改变飞书下载、文件大小限制、MIME 校验、附件 retention 或 App Server 输入类型。
- 不重命名历史附件。
- 不在文件名中增加随机后缀；同名文件由不同 attachment UUID 子目录隔离。

## 设计

新路径为：

```text
<attachment_root>/<app_hash>/<channel_hash>/<session_uuid>/<attachment_uuid>/<safe_original_name>
```

`safe_original_name` 继续使用当前 basename/控制字符过滤规则；空名或无效名回退为 `attachment`。同一 session 的 `outbox/` 位置保持不变。

附件完成时，MySQL `attachments.relative_path` 写入真实文件名的路径；普通文件的文本 manifest、图片的文字输入与 `localImage.path` 都读取该路径。

清理器支持旧叶子名 `payload` 与新叶子名。对新文件名只接受安全 basename，并确认其直接父目录为 attachment UUID、上一级为 session UUID，随后仅删除该 attachment UUID 目录；拒绝不满足该结构的记录，避免数据库异常路径扩大删除范围。

## 验收场景

1. 一个 `report.pdf` 上传后，其实际落盘路径以 `/report.pdf` 结尾，数据库与传入 Codex 的本地路径一致。
2. 一个图片 `photo.png` 上传后，其 `localImage.path` 以 `/photo.png` 结尾。
3. 两个同名 `report.pdf` 位于不同 attachment UUID 目录，均可保留。
4. 带路径分隔符或控制字符的文件名不能逃逸 UUID 目录；空名回退为 `attachment`。
5. 清理器能删除历史 `payload` 和新命名附件的各自 UUID 目录，但拒绝非法层级或不安全叶子名。

## 风险与缓解

文件名来自用户输入。仅使用净化后的 basename，且清理时重新验证路径结构；不信任数据库中的任意路径作为可递归删除的目录。

## 验证

遵循 RED → GREEN → REFACTOR：先增加聚焦的 attachment 单测并观察失败，再做最小实现。完成后运行目标包测试、`go vet ./...`、`./bot_controller.sh build`；运行时改动将用控制脚本重启并以新进程检查验证。
