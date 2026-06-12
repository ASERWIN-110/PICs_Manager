# 开发文档

本文面向继续维护 PICs Manager 的开发者。

## 代码结构

```text
cmd/
  cli/              命令行入口
  manager-server/   HTTP 服务入口
  verify-scan/      扫描结果验证工具
config/             配置读取、环境变量覆盖、配置校验
internal/api/       HTTP handlers 和 routes
internal/models/    MongoDB/API 模型
internal/task/      扫描任务管理
pkg/database/       数据库存储接口
pkg/database/mongo/ MongoDB 实现和索引维护
pkg/hasher/         SHA-256、pHash 和 pHash buckets
pkg/maintenance/    文件清单和数据库备份
pkg/runstate/       run 状态、JSONL journal 和维护锁
pkg/scanner/        预处理、分类、聚合、入库协调
pkg/thumbnailer/    图片缩略图生成
web/                React/TypeScript 前端
```

## 核心流程

扫描由 `pkg/scanner/orchestrator.go` 串起来：

1. 预处理：处理下载器编号副本和基础文件损坏补位。
2. 媒体过滤：按 `scanner.mediaTypes` 过滤支持的扩展名。
3. 损坏图片隔离：可解码图片保留，损坏图片移动到 `quarantine/corrupted`。
4. 分类：按媒体类型对应正则提取系列名，移动到 staging。
5. 聚合归档：按目录聚合规则把 staging 归入最终媒体库。
6. 入库：`full` 模式同步 MongoDB；`classifyOnly` 模式跳过。

`classifyOnly` 不要求数据库连接。CLI 的 `scanRequiresDatabase` 和 Orchestrator 的 mode 校验必须保持这个约束。

## 运行模型

无人值守运行依赖 `pkg/runstate`：

- run 状态文件：`<logger.path>/runs/<runId>.json`
- journal 文件：`<logger.path>/runs/<runId>.journal.jsonl`
- 维护锁：`<logger.path>/runs/active.lock`

`internal/task.Manager` 在启动后台扫描前创建 run 并占用维护锁，任务结束后释放锁。CLI 的 `scan` 也使用同一个锁，避免 server 和 SSH 手工命令并发整理同一批文件。

后端启动时必须调用 `RecoverUnfinishedRuns`。它会把上次遗留的 `pending/running` run 标记为 `interrupted`，并清理旧锁文件。当前策略是标记中断，不自动重放；后续断点恢复应基于 journal checkpoint 扩展。

`DELETE /api/v1/tasks/{taskId}` 会调用 `StopTask`，取消扫描 context，并把 run 标记为 `stopped`。`POST /api/v1/tasks/{taskId}/pause` 会调用 `PauseTask`，取消扫描 context，并把 run 标记为 `paused`。`stopped/paused` 都表示人为控制且可用最终库和 journal 做恢复判断，不应按普通失败处理。

Orchestrator 通过 context 携带 `runstate.Recorder`。新增阶段时应同步写：

- `recorder.Phase`：阶段和计数 checkpoint。
- `recorder.Event`：文件级或关键操作事件。

预处理、分类、聚合归档和入库阶段已经记录关键 before/after 事件。新增实际移动、删除、批量写入或修复文件的阶段时，也应记录操作前后事件。

## 不要随意改动的兼容点

- 图片分类正则公式不要改。历史数据和下载器命名依赖这些规则。
- Mongo 物理集合和最终库物理目录都必须按媒体类型拆分：图片使用 `images`，视频使用 `videos`，音频使用 `audios`，文本使用 `texts`，其他自定义类型使用 `media_<type>`。API 中的 `images`、`Image`、`imageCount` 等历史命名仅作为兼容层，不能把非图片重新混入 `images` 集合或图片目录。
- 缩略图正文仍存 MongoDB，但列表接口只能返回 `thumbnailUrl`，不能把 base64 放回列表 JSON。
- 深分页必须走 cursor/keyset。`page > 1` 且没有 `cursor` 时 API 应返回错误。
- 以图搜图必须使用 pHash bucket 缩小候选集，不能回退到全量读取所有 pHash 后在 Go 中扫描。
- 同名不同哈希文件必须进入 `.same-name/<bucket>/<sha256>/<filename>`，不要改成简单追加 `-1`、`-2`。
- CLI 和 server 都必须走 runstate 维护锁，不能新增绕过锁的扫描入口。

## 配置和环境变量

`config.LoadConfig(".")` 读取 `config.yaml`，然后应用环境变量覆盖。

支持的覆盖：

- `SERVER_PORT` 或 `PIC_MANAGER_SERVER_PORT`
- `DATABASE_URI` 或 `MONGO_URI`
- `DATABASE_NAME` 或 `MONGO_DATABASE`
- `MONGO_APP_USERNAME`
- `MONGO_APP_PASSWORD`
- `MONGO_APP_AUTH_SOURCE` 或 `MONGO_AUTH_SOURCE`

`PIC_MANAGER_ENV_FILE` 可指定 `.env` 文件路径。未指定时，会尝试读取 `/home/darkman/dev/mongodb/config/.env`；该默认路径读取失败不会报错，显式指定的路径读取失败会报错。

当存在 `MONGO_APP_USERNAME` 和 `MONGO_APP_PASSWORD` 时，程序会把凭据写入 Mongo URI，并在缺少 `authSource` 时补齐 `authSource=admin`。

NAS 运行控制字段：

- `scanner.workerCount`：`0` 使用保守默认 worker，最多 4。显式配置值不被限制。
- `scanner.ioThrottleMs`：分类移动文件前的轻量 sleep，用于降低 IO 峰值。
- `scanner.maintenanceWindow`：`HH:MM-HH:MM`，启动扫描时如果不在窗口内会拒绝运行。
- `scanner.maxFilesPerDir`：目录健康报告阈值。
- `server.maintenanceToken`：可选维护 API token。设置后，维护接口要求 `Authorization: Bearer` 或 `X-Maintenance-Token`。

## 数据库索引

MongoDB 索引由 `pkg/database/mongo/store.go` 的 `EnsureIndexes` 维护。它同时负责补齐部分派生字段：

- `hasThumbnail`
- `pHashBuckets`

新增查询能力时优先扩展 Store 接口并补测试，避免 handler 直接依赖 MongoDB driver。

## 同名文件策略

入口函数：

- `resolveSameNameTarget`
- `moveToSameNameTarget`
- `sameFileHash`

规则：

- 目标不存在：直接移动。
- 目标存在且 SHA-256 相同：删除源文件。
- 目标存在但 SHA-256 不同：移动到 `.same-name/<原文件名桶>/<sha256>/<原文件名>`。
- `.same-name` 内目标已存在且哈希相同：删除源文件。
- `.same-name` 内目标已存在但内容不同：返回错误，避免覆盖。

这个设计的目标是让同一批文件无论处理顺序如何，最终目录树都稳定。

## API 约定

所有 JSON 响应使用 envelope：

```json
{
  "data": {},
  "meta": {},
  "error": {
    "code": "error_code",
    "message": "human readable message"
  }
}
```

列表接口使用 cursor：

- 第一页允许不传 `cursor`。
- 后续页必须传上一页返回的 `meta.pagination.nextCursor`。
- `limit` 在 API 中最大 100。

缩略图接口直接返回图片二进制，列表接口只返回缩略图 URL。

运行记录只读 API：

```text
GET /api/v1/runs
GET /api/v1/runs/{runId}
GET /api/v1/runs/{runId}/journal
```

维护 API：

```text
POST   /api/v1/tasks
DELETE /api/v1/tasks/{taskId}
POST   /api/v1/tasks/{taskId}/pause
PUT    /api/v1/config
```

设置 `server.maintenanceToken` 后，上述维护 API 必须带 token；查看 API 不需要 token。

## 前端约定

前端在 `web/`：

```bash
cd web
npm install
npm run dev
npm run lint
npm run build
npm run e2e:smoke
```

前端通过 `web/src/services/api.ts` 访问 API。新增接口时优先更新：

- `web/src/types/`
- `web/src/services/api.ts`
- 对应页面或组件
- `web/e2e/smoke.cjs`

## 本地验证

推荐命令：

```bash
env GOCACHE=/tmp/pics-manager-gocache go test ./...
env GOCACHE=/tmp/pics-manager-gocache go vet ./...
go run golang.org/x/tools/cmd/deadcode@latest ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
env GOCACHE=/tmp/pics-manager-gocache go test -race ./config ./cmd/cli ./cmd/verify-scan ./internal/api ./internal/task ./pkg/database/mongo ./pkg/hasher ./pkg/logger ./pkg/maintenance ./pkg/scanner
cd web && npm run lint
cd web && npm run build
cd web && npx -y knip --no-progress
cd web && npm run e2e:smoke
```

如果 Go 默认 cache 在只读目录，使用 `GOCACHE=/tmp/pics-manager-gocache`。

## 验证工具

`cmd/verify-scan` 用于验证真实数据集：

```bash
go run ./cmd/verify-scan -source ./test_img -mode classifyOnly -workers 4
```

full 模式会连接 MongoDB：

```bash
go run ./cmd/verify-scan \
  -source ./test_img \
  -mode full \
  -mongo-uri 'mongodb://dev_user:password@localhost:27017/?authSource=admin' \
  -reset-db
```

它会检查：

- 损坏图片是否进入隔离区。
- 非媒体文件是否被跳过。
- 同名同哈希文件是否去重。
- 同名不同哈希文件是否进入 `.same-name`。
- classifyOnly 是否不入库。
- full 模式下输出数量和数据库计数是否一致。

数据库补齐：

```bash
go run ./cmd/cli -action rebuild-database
```

该命令扫描 `scanner.finalLibraryPath` 中实际包含媒体文件的系列目录，upsert series/media，更新缩略图和数量，并删除数据库中已不存在的媒体记录。它用于 full 模式中断后从最终库重建或补齐 MongoDB。

恢复校验：

```bash
go run ./cmd/cli -action verify-run -run-id '<runId>'
```

该命令读取 run+journal，要求至少存在一个 checkpoint，并基于最终库生成健康报告。它是断点恢复前的人工/自动检查入口；真正重放策略应继续基于 journal 事件扩展。

`verify-run` 会输出 `recoveryStatus`：

- `complete`
- `recoverable_with_review`
- `needs_attention`

## 交叉编译和发布

前端先构建：

```bash
cd web
npm run build
```

Go 命令推荐静态交叉编译。当前发布矩阵：

- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`
- `windows/amd64`
- `windows/arm64`

单平台示例：

```bash
env GOCACHE=/tmp/pics-manager-gocache CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o dist/PICs_Manager_linux_amd64/manager-server ./cmd/manager-server
```

每个平台需要构建：

- `./cmd/manager-server`
- `./cmd/cli`，发布文件名为 `pics-cli`
- `./cmd/verify-scan`

当前 release 包包含：

- `manager-server`
- `pics-cli`
- `verify-scan`
- `config.yaml`
- `web-dist`

发布前确认 `.gitignore` 仍排除：

- `Pictures/`
- `test_img/`
- `timetable.png`
- `dist/`
- `web/dist/`
- `node_modules/`

不要把测试数据、下载数据、构建产物或本地日志提交进仓库。

## 部署模板

`deploy/` 包含：

- `systemd/pics-manager.service`
- `systemd/pics-manager-health-report.service`
- `systemd/pics-manager-health-report.timer`
- `logrotate/pics-manager`
- `docker-compose.yml`

systemd service 使用 `ReadWritePaths` 限制可写范围。health-report timer 默认每天 03:30 运行 `pics-cli -action health-report`，用于不触发整理的每日目录健康报告，报告内包含最终库、隔离区、`.same-name` 和非媒体跳过数。Docker Compose 通过 `/health` 做后端容器健康检查。
