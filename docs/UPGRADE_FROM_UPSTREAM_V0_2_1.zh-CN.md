# 官方 v0.2.1 升级到我们的 v0.2.14：Docker 操作指南

适用范围：已经通过 Docker Compose 部署官方 `Wei-Shaw/sub2api v0.2.1`，现在切换到 `jhupo/sub2api v0.2.14`，继续使用原 PostgreSQL、Redis 和应用数据目录。

- 发布地址：https://github.com/jhupo/sub2api/releases/tag/v0.2.14
- 目标镜像：`ghcr.io/jhupo/sub2api:0.2.14`
- 已验证镜像摘要：`sha256:df9f03e04ba58b74689ed1b0e3fa7bc95138db388503b7602db1273e15d63b85`
- 本文命令在服务器 Bash 中执行，不是在 Windows PowerShell 中执行。

## 1. 先看结论

**配置层面通常只需要修改应用的 `image`，但操作流程不能省掉停写备份和迁移验收。**

升级不会主动清空用户、余额、历史记录或 Redis。新程序会执行必要的数据库迁移，回填旧订阅和 API Key 的支付来源，因此不是“数据库完全不发生变化”。

保留原来的以下配置，不要用新仓库的整个 Compose 模板覆盖线上 YAML：

- Compose 项目名、网络、端口和 Caddy 转发目标。
- PostgreSQL、Redis 服务及其镜像、账号密码、数据库名称、Redis DB 编号。
- 所有命名卷和目录挂载，尤其是 `/app/data`、PostgreSQL 数据、Redis 数据。
- `JWT_SECRET`、`TOTP_ENCRYPTION_KEY`、配置文件和已有功能开关。
- 原有资源限制、代理和安全设置，不要照抄测试环境的放宽安全配置。

`UPDATE_STRATEGY=binary` 保持原状即可。本次用 Docker 镜像升级，不需要切换为其他更新策略。

**禁止执行：** `docker compose down -v`、`docker system prune --volumes`、Redis `FLUSHDB` / `FLUSHALL`，或删除数据库迁移记录。

## 2. 确认要升级的是哪一套

同一台机器有多个 Sub2API 时，不要仅凭镜像名或域名猜测目标。先只读查看：

```bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Ports}}'
docker compose ls
```

结合现有 Caddy 配置，确认目标域名对应的端口或容器。**以下变量中的容器名必须替换为确认后的目标，不要直接复制占位符。**

```bash
APP_CONTAINER='需要升级的应用容器名'

docker inspect "$APP_CONTAINER" --format '{{ index .Config.Labels "com.docker.compose.project" }}'
docker inspect "$APP_CONTAINER" --format '{{ index .Config.Labels "com.docker.compose.service" }}'
docker inspect "$APP_CONTAINER" --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}'
docker inspect "$APP_CONTAINER" --format '{{ index .Config.Labels "com.docker.compose.project.config_files" }}'
docker inspect "$APP_CONTAINER" --format '{{json .Mounts}}'
docker inspect "$APP_CONTAINER" --format '{{json .Config.Entrypoint}} {{json .Config.Cmd}}'
docker exec "$APP_CONTAINER" /app/sub2api -version
```

最后一条适用于默认二进制路径。如果实际启动命令使用了其他程序路径，应检查实际运行的二进制，不能用镜像标签代替程序版本。

如果没有 Compose 标签，或实际版本不是官方 v0.2.1，先停在这里核实部署方式，不能生搬后续命令。

进入上述 `working_dir`，按实际输出填写：

```bash
cd /opt/你的实际部署目录

PROJECT='上述实际Compose项目名'
COMPOSE_FILE='/opt/你的实际部署目录/docker-compose.yml'
APP_SERVICE='sub2api'
PG_SERVICE='postgres'
PG_USER='sub2api'
PG_DATABASE='sub2api'

dc() { docker compose -p "$PROJECT" -f "$COMPOSE_FILE" "$@"; }

dc config --services
dc ps
```

这里的 `APP_SERVICE`、`PG_SERVICE` 是 YAML 中 `services:` 下的服务名，不是 `container_name`。`PG_USER`、`PG_DATABASE` 必须与应用实际连接的数据库一致。若使用外部 PostgreSQL，改用对应连接的备份工具，不要备份本机另一套数据库。

如果原部署使用多个 `-f`、指定 `--env-file` 或依赖额外环境变量，`dc` 函数必须保留这些原始参数。不要换目录、换项目名重新创建一套空数据卷。

## 3. 备份配置并提前下载镜像

先保证有足够磁盘空间，备份大小取决于数据库和应用数据量。不要在空间不足时直接清理线上镜像或数据卷。

```bash
df -h
umask 077
BACKUP_DIR="/opt/sub2api-upgrade-backups/${PROJECT}-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$BACKUP_DIR"

cp -- "$COMPOSE_FILE" "$BACKUP_DIR/compose-before.yml"
if [ -f .env ]; then cp -- .env "$BACKUP_DIR/env-before"; fi
docker inspect "$APP_CONTAINER" > "$BACKUP_DIR/app-before.json"
dc config > "$BACKUP_DIR/compose-resolved-before.yml"
docker exec "$APP_CONTAINER" /app/sub2api -version > "$BACKUP_DIR/version-before.txt" 2>&1

docker pull ghcr.io/jhupo/sub2api:0.2.14
docker image inspect ghcr.io/jhupo/sub2api:0.2.14 --format '{{json .RepoDigests}}'
```

逐条确认成功再继续。额外的 Compose 覆盖文件、指定的环境文件、外部挂载配置也必须备份。`inspect` 和展开后的配置可能包含密码，备份目录不要公开或上传到 Git。

下载镜像不会切换正在运行的应用。

## 4. 进入维护窗口并停止应用写入

1. 在现有入口暂时阻止该站点的新业务请求，保留管理员验收通道；不要影响另一域名。
2. 等待正在进行的 HTTP / SSE / WS 请求结束，尤其是长推理和压缩。
3. 确认没有另一个应用实例、定时任务或共享数据库的后台服务继续写入目标数据库。
4. 只停止目标应用，不停止数据库、Redis 或 Caddy。

```bash
dc stop -t 610 "$APP_SERVICE"
dc ps -a
```

`610` 是 Docker 停止等待时间，不保证应用一定等待这么久；应用自身也可能配置更短的退出超时。所以必须先排空请求，不能靠延长这个数字解决所有在途请求问题。

保留旧应用数据和当前二进制：

```bash
docker cp "$APP_CONTAINER":/app/data "$BACKUP_DIR/app-data"
docker cp "$APP_CONTAINER":/app/sub2api "$BACKUP_DIR/old-sub2api"
```

按实际挂载位置补齐其他数据。默认 `/app/data` 不包含 PostgreSQL 和 Redis 数据，不能替代下一节的数据库备份。

**此前用过后台在线更新的部署：** 当前容器内的二进制可能比旧镜像新，换回旧镜像未必等于回到升级前版本。应保留实际二进制、版本和配置；也可以在应用停止后给该应用容器制作一个本地回退镜像：

```bash
# 可选。仅保存目标应用容器的可写层，不包括挂载卷。
ROLLBACK_IMAGE="local/sub2api-rollback:$(date +%Y%m%d-%H%M%S)"
docker commit "$APP_CONTAINER" "$ROLLBACK_IMAGE"
```

该镜像可能含部署信息，不要推送到公共镜像仓库。它也不能撤销数据库迁移。

## 5. 备份数据库并记录业务基线

在所有应用写入已停止之后执行：

```bash
dc exec -T "$PG_SERVICE" pg_dump \
  -U "$PG_USER" -d "$PG_DATABASE" -Fc \
  > "$BACKUP_DIR/database.dump"
```

**必须确认上条命令退出成功，且备份不是空文件。** 认证失败、磁盘不足或连接错误时停止升级，不要跳过备份。

```bash
test -s "$BACKUP_DIR/database.dump"
dc exec -T "$PG_SERVICE" pg_restore --list \
  < "$BACKUP_DIR/database.dump" \
  > "$BACKUP_DIR/database-restore-list.txt"
sha256sum "$BACKUP_DIR/database.dump" > "$BACKUP_DIR/database.dump.sha256"
```

`pg_restore --list` 只是结构可读检查，不等于完整恢复验证。重要站点应先将此备份实际恢复到独立测试数据库，并验证用户及计费数据；不要将恢复命令指向线上数据库。备份还应复制到受保护的另一台机器或备份存储。

同一 Bash 会话中定义只读基线检查：

```bash
db_audit() {
  dc exec -T "$PG_SERVICE" psql -X -v ON_ERROR_STOP=1 \
    -U "$PG_USER" -d "$PG_DATABASE" <<'SQL'
SELECT count(*) AS users, sum(balance) AS balance_total FROM users;
SELECT status, count(*) AS keys FROM api_keys GROUP BY status ORDER BY status;
SELECT count(*) AS accounts FROM accounts;
SELECT count(*) AS subscriptions,
       sum(daily_usage_usd) AS daily_used,
       sum(weekly_usage_usd) AS weekly_used,
       sum(monthly_usage_usd) AS monthly_used
FROM user_subscriptions;
SELECT count(*) AS usage_records, sum(actual_cost) AS billed_total FROM usage_logs;
SQL
}
db_audit > "$BACKUP_DIR/business-before.txt"
```

合计只是快速核对，还应在演练中逐项检查套餐有效期、额度、用户归属和密钥支付来源。

**Redis 保持原服务、原密码、原 DB 和原挂载，不清空、不重建。** 如需备份持久化文件，应采用现有 Redis 的一致性备份方法；不要直接复制正在改写的 AOF 文件冒充完整备份，也不要操作另一套业务共享的 Redis。

## 6. 只修改应用镜像

编辑原 YAML 中目标应用服务的 `image`，其他配置保留：

```yaml
services:
  sub2api:
    image: ghcr.io/jhupo/sub2api:0.2.14
    # 其余现有配置原样保留，不要用本片段覆盖整个服务。
```

如原来的 `image` 使用环境变量，可改该变量或直接固定上述值。最终必须确认生效的是 `0.2.14`，不要用 `latest`。

```bash
dc config --quiet
dc config --images
```

如果原配置直接挂载了 `/app/sub2api`、启动命令指向另一份旧程序，或某个启动脚本会覆盖二进制，必须先处理该覆盖关系；否则单改 `image` 可能仍然运行旧程序。默认部署没有这种额外挂载时不需要新增配置。

## 7. 显式迁移，再启动新版

逐条执行，每条成功后再执行下一条：

```bash
dc run --rm --no-deps --entrypoint /app/sub2api "$APP_SERVICE" -migrate-only
```

应看到 `Database migrations applied successfully`。接着检查：

```bash
dc run --rm --no-deps --entrypoint /app/sub2api "$APP_SERVICE" -check-migrations
```

应看到 `Database migrations are current`。出现 checksum、SQL、约束或连接错误时停止，不要删除迁移记录、修改校验值或者直接启动旧版。

```bash
dc up -d --no-deps --force-recreate "$APP_SERVICE"
dc ps
dc exec -T "$APP_SERVICE" /app/sub2api -version
dc logs --since 10m --tail 200 "$APP_SERVICE"
```

版本应为 `0.2.14`，提交为 `58c13df6bdd26075c0b8c80c6e09e6b1af7b02d5`。

`--no-deps` 表示不连带启动或重建依赖服务；`--rm` 只移除这次临时迁移容器，不删除原命名数据卷。迁移命令会修改目标数据库，不是只读检查。

使用实际映射端口检查健康状态。例如原端口为 8080 时：

```bash
curl -fsS http://127.0.0.1:8080/health
db_audit > "$BACKUP_DIR/business-after.txt"
diff -u "$BACKUP_DIR/business-before.txt" "$BACKUP_DIR/business-after.txt"
```

如果原端口不是 8080，请替换为目标应用端口。健康接口应返回 `{"status":"ok"}`。在未恢复业务、未开始测试扣费的前提下比较基线；如有差异先查清原因，不能只看容器是 `Up` 就放行。

## 8. 放量前验收

- 后台可登录；用户数、旧余额、账号和历史使用记录正确。
- 旧 API Key 原值仍能使用，停用 Key 仍被拒绝。
- 余额 Key 走余额；旧套餐 Key 正确绑定原用户的迁移套餐，不意外变成余额支付。
- 套餐到期时间、日/周/月限额和已用额度正确，多套餐用户没有串套餐。
- 使用专用测试用户验证管理员分配套餐、旧余额兑换码及旧套餐兑换码。验证会产生真实业务写入，不要误兑换用户正在使用的码。
- 用低成本测试请求验证流式/非流式返回及实际计费；核对模型单价、分组倍率、使用记录和扣费来源。
- Codex 验证多轮 WS、切换模型、客户端取消后重试；请求结束后没有残留并发槽或长期冻结。
- 查看应用日志，确认没有数据库迁移、套餐分配、资金结算相关错误，再恢复该站点流量。

新鉴权缓存使用 `jhupo:apikey:auth:` 命名空间及结构版本 24。旧缓存不会被当成新支付结构直接使用，未命中时从数据库加载，**不需要清空 Redis**。保留 Redis 不等于所有旧缓存都必须继续命中。

请求预授权默认关闭，关闭时余额和套餐都在请求完成后按实际用量结算；开启后才会在转发前预留预计金额或套餐额度。此次升级不要顺便调整风控、调度和预扣开关，先验收原配置下的行为，再单独测试新开关。

升级前产生的 WS `previous_response_id` 如缺少可验证的历史输入额度记录，启用预授权时可能需要客户端携带完整历史重新发起；不能按零历史成本放行。不要因此清空用户数据或缓存。

## 9. 出问题如何回退

| 所处阶段 | 处理方式 |
| --- | --- |
| 尚未执行迁移 | 保持维护状态，恢复旧配置/镜像或原停用容器；核实实际二进制版本后启动。 |
| 已开始迁移，包括中途报错 | 保持维护状态，停止应用写入。部分迁移可能已提交，不能假定全库自动回滚；先检查日志和迁移状态。 |
| 迁移后，新业务尚未放行 | 优先修复前进；如确需回退，使用验证过的升级前数据库备份、对应旧程序及配置恢复到隔离的回退部署，校验后再切入口。 |
| 新版已接收请求、充值或兑换 | 不能直接覆盖恢复旧备份，否则丢失升级后的交易。应停写并对账，优先向前修复；回退必须处理新增交易及缓存一致性。 |

**套餐迁移后，仅将 `image` 换回官方 v0.2.1 不是受支持的回退方式。** 迁移修改了部分约束和支付关系，保留旧字段不代表旧程序仍能安全写入。

数据库恢复后的 Redis 缓存需要按实际业务键做一致性处理；不提供全库清空作为通用回退步骤。旧镜像、配置、数据库备份在升级验收和观察期结束前都应保留。

## 10. 本次已经验证到什么程度

已在 `192.168.2.150` 的独立 Compose 项目中完成官方 `v0.2.1` 到本发布镜像的演练：

- 创建 4 个测试用户（另含管理员）、3 个业务分组、3 份订阅、4 把密钥、余额/套餐兑换码以及调用记录。
- 停止应用后备份数据库，并实际恢复到另一个测试数据库验证。
- 只替换应用容器；数据库和 Redis 容器 ID、挂载及 Redis 测试标记保留。
- 旧余额、套餐有效期及用量、密钥归属和旧使用记录核对通过。
- 两条 HTTP 协议路径、流式/非流式、1.5/2/0.75 倍率及余额预扣开启后的专项计费验证通过。
- 旧兑换码兑换、新套餐分配通过，最终没有待处理余额结算和套餐残留冻结。

这不是你的生产数据库副本验证，也不是使用真实 OAuth 账号的压力测试。WS 逐轮资金与并发场景另有本版单元/竞态测试覆盖。正式线上仍应做自己的停写备份和验收。
