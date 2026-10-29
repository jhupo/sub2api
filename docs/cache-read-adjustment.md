# 缓存用量调整

入口：账号管理 → 更多操作 → 缓存用量调整。

- 默认关闭，默认比例为 10%，支持 0–100 的整数百分比。
- 仅对勾选的上游账号生效；可以加入账号列表中已选中的账号。
- 转换量为 `floor(缓存读取 token × 比例 / 100)`，小数部分舍去。
- 缓存为 0、比例为 0、总开关关闭或账号未选中时，不转换用量。
- 仅调整响应中的用量口径，上游实际的缓存行为不变。客户端 usage、使用日志和本地计费使用调整后的数值。
- 输出 token、缓存写入 token 和总 token 不变。

例如，普通输入 1,000、缓存读取 8,000，比例 10%，调整后普通输入为 1,800、缓存读取为 7,200。

## 协议差异

Anthropic 的 `input_tokens` 不包含缓存读取，因此转换时增加 `input_tokens`，减少 `cache_read_input_tokens`。

OpenAI 的 `input_tokens` / `prompt_tokens` 和 Gemini 的 `promptTokenCount` 已包含缓存读取，因此这些总输入字段保持不变，只减少缓存子项。现有计费代码会从总输入中减去缓存，得到增加后的普通输入量。

覆盖 JSON 普通响应、SSE、OpenAI Responses WebSocket，以及 Bedrock 解码后的流式事件。未知字段和错误响应保留原样。超出已有上游响应读取限制的内容保留原样，继续交给原有的大小限制逻辑处理。

## 实现位置

- `backend/internal/pkg/usageadjustment/`：协议字段转换和响应体读取器。
- `backend/internal/repository/http_upstream.go`：响应解压之后、业务解析之前执行 HTTP 转换。
- `backend/internal/service/openai_ws_*`、`bedrock_stream.go`：二进制流和 WebSocket 在解码后、用量提取之前转换。
- `backend/internal/service/setting_cache_read_adjustment.go`：配置校验、账号匹配和缓存。
- `frontend/src/components/admin/CacheReadAdjustmentModal.vue`：管理弹窗。

配置复用现有 `settings` 表的 `cache_read_adjustment` 键，无需数据库迁移。保存后当前节点立即更新；其他节点最长约 5 秒刷新。已开始的响应继续使用开始处理时的比例，新的响应使用新配置。

## 管理接口

`GET /api/v1/admin/settings/cache-read-adjustment`

`PUT /api/v1/admin/settings/cache-read-adjustment`

```json
{
  "enabled": true,
  "percent": 10,
  "account_ids": [7, 8]
}
```

接口沿用现有管理端认证。比例越界、非整数或非正数账号 ID 会返回 400。

## 发布

功能版本：`v0.2.39`，基于 `v0.2.38`，使用正式版本号以兼容现有更新检查与升级脚本。

推送带注释的 tag 会触发现有 Release 工作流，生成 Linux amd64/arm64 安装包和多架构 Docker 镜像。本次发布使用完整发布流程；从 `0.2.38` 可通过管理后台检测并升级到 `0.2.39`。功能默认关闭，升级后在账号管理中手动开启。

```bash
docker pull ghcr.io/jhupo/sub2api:0.2.39
```

Docker Compose 部署时将 `.env` 中的 `SUB2API_VERSION` 设置为 `0.2.39` 后重新拉取并启动。二进制部署使用 Release 中匹配服务器架构的 `sub2api_0.2.39_linux_amd64.tar.gz` 或 `sub2api_0.2.39_linux_arm64.tar.gz`，并用 `checksums.txt` 校验。

预发布 tag 强制使用完整发布流程，只推送对应版本的镜像标签，不覆盖 `latest`、主版本或次版本标签，也不向默认分支同步 VERSION。现有自动升级仅支持正式版本，故 `v0.2.38-cache.1` 不作为本次升级版本使用。
