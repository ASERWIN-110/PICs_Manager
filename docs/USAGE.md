# 使用文档

本文面向运行和使用 PICs Manager 的用户。

## 组件

- `manager-server`：HTTP API 服务，供前端和外部系统调用。
- `web`：React/TypeScript 前端。
- `pics-cli`：命令行入口，支持扫描、备份、查询和统计。
- `verify-scan`：验证工具，用于在隔离工作目录中验证分类结果、损坏文件处理、非媒体文件跳过和数据库同步。

Release 包中会包含三个 Go 可执行文件、`config.yaml` 和已经构建好的 `web-dist`。

## 环境要求

- Go 1.24 或兼容版本。
- Node.js 和 npm，用于开发或重新构建前端。
- MongoDB。`classifyOnly` 模式不需要连接数据库，`full` 模式和查询功能需要 MongoDB。

如果 MongoDB 使用专用应用账号，推荐通过环境变量提供凭据：

```dotenv
MONGO_APP_USERNAME=dev_user
MONGO_APP_PASSWORD=your_password
MONGO_APP_AUTH_SOURCE=admin
```

程序会读取 `PIC_MANAGER_ENV_FILE` 指向的 `.env` 文件；如果未设置，会尝试读取 `/home/darkman/dev/mongodb/config/.env`。使用应用账号时，最终连接串必须带上 `authSource=admin`，程序会在有用户名和密码时自动补齐该参数。

## 配置文件

主配置文件是仓库根目录的 `config.yaml`。

关键字段：

```yaml
server:
  port: ":8080"
  timeout: 30s
  maintenanceToken: ""

database:
  uri: "mongodb://localhost:27017"
  name: "media_manager"

scanner:
  mode: "full"
  scanPath: "/path/to/inbox"
  stagingPath: "/path/to/staging"
  finalLibraryPath: "/path/to/library"
  backupPath: "/path/to/backups"
  quarantinePath: "/path/to/quarantine"
  workerCount: 0
  batchSize: 100
  ioThrottleMs: 0
  maintenanceWindow: ""
  maxFilesPerDir: 5000
  followSymlinks: false

security:
  enabled: false
  storePath: ""
  defaultPairingTTL: "24h"
  requireViewerForRead: true
  allowLocalAdmin: true
  corsAllowedOrigins: []

scheduler:
  enabled: false
  interval: "6h"
  mode: "full"
  runOnStartup: false

runRetention:
  maxRuns: 200
  maxAgeDays: 90
```

`scanner.mode` 可选值：

- `full`：分类、归档并同步数据库。
- `classifyOnly`：只分类和归档，不写入数据库。

路径含义：

- `scanPath`：待处理文件入口。
- `stagingPath`：分类临时目录。
- `finalLibraryPath`：最终媒体库目录。
- `backupPath`：清单和数据库备份输出目录。
- `quarantinePath`：损坏文件或无法安全合并的目录。

NAS 长期运行相关字段：

- `workerCount`：`0` 表示使用保守默认值，最多 4 个 worker。历史导入时可以手动调高，但不建议默认用满 CPU。
- `ioThrottleMs`：每次分类移动文件前等待的毫秒数。机械盘或低端 NAS 可以设置为 `5`、`10` 这类小值降低 IO 峰值。
- `maintenanceWindow`：维护时间窗口，格式 `HH:MM-HH:MM`，例如 `01:00-06:00`。留空表示任何时间都允许运行。
- `maxFilesPerDir`：目录健康报告阈值。单目录文件数超过该值会写入报告 warnings。
- `followSymlinks`：默认 `false`，扫描和下载都会跳过 symlink。设为 `true` 后会解析真实路径，并要求真实路径仍在扫描根或最终库内。

`server.maintenanceToken` 是旧版兼容 token。新部署建议使用 `security` 设备绑定。

`scheduler` 可让后端按固定间隔自动触发扫描。调度器和手工 CLI/API 共用 runstate 维护锁，如果已有任务运行，本轮调度会跳过。

`runRetention` 会清理已结束的旧 run 和 journal。`maxRuns: 0` 表示不按数量清理，`maxAgeDays: 0` 表示不按天数清理；未结束的 run 不会被清理。

## 设备绑定和远程访问

开启设备绑定：

```yaml
security:
  enabled: true
  requireViewerForRead: true
```

生成一次性配对码：

```bash
go run ./cmd/cli -action create-pairing-code -device-name family-viewer -scope viewer
go run ./cmd/cli -action create-pairing-code -device-name admin-laptop -scope admin -ttl 2h
```

打开前端后输入配对码，前端会保存一次性换回的设备 token。服务端只保存 token 哈希，不保存明文 token。

权限级别：

- `viewer`：查看系列、搜索、缩略图和下载。
- `maintainer`：包含 viewer，并可启动/停止/暂停扫描、查看 run/journal。
- `admin`：包含 maintainer，并可读取和更新配置。

设备管理：

```bash
go run ./cmd/cli -action list-devices
go run ./cmd/cli -action revoke-device -device-id '<deviceId>'
```

远程安全限制：

- 非本机请求即使带 admin token，也不能修改数据库、日志目录、服务端端口和扫描关键路径。
- 非本机请求启动扫描时，只能使用配置中的 `scanner.scanPath`，请求体里的任意 path 会被忽略。
- 本机请求在 `security.allowLocalAdmin: true` 时可用于完整维护配置，适合 SSH 到 NAS 后操作。

## 媒体类型和分类规则

每种媒体类型有自己的扩展名和分类正则：

```yaml
scanner:
  mediaTypes:
    - type: "image"
      extensions: [".jpg", ".jpeg", ".png", ".gif", ".webp"]
      filePatterns:
        - '^(.*?)_(\d+)_p(\d+)_(\d+)(\.[a-zA-Z0-9_]+)?$'
    - type: "video"
      extensions: [".mp4", ".mkv", ".mov", ".avi", ".wmv", ".webm", ".m4v"]
      filePatterns:
        - '^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$'
```

`image` 会兼容历史 `scanner.filePatterns`。不要为了“美化”文件名改动已有图片正则，分类稳定性依赖这些公式。

不在 `mediaTypes.extensions` 中的文件会被跳过，不会入库，也不会出现在最终媒体库中。

最终库物理目录和 MongoDB 物理集合都会按媒体类型拆分：图片使用 `images`，视频使用 `videos`，音频使用 `audios`，文本使用 `texts`，自定义类型使用 `media_<type>`。例如视频文件会落到 `<finalLibraryPath>/videos/V/Video/Video_1.mp4`，并写入 MongoDB 的 `videos` 集合。查看 API 和前端也按媒体类型分页查询，不会把图片、视频、音频、文本混在同一个列表里。

Web 管理页的“分类规则”区域会按媒体类型显示独立规则卡片。每张卡片可以单独编辑：

- `type`：媒体类型名，例如 `image`、`video`、`audio`、`text`。
- `extensions`：该类型支持的扩展名。
- `filePatterns`：该类型自己的分类正则，一行一个。

保存后会写回 `scanner.mediaTypes`。后端仍会在保存时做配置校验；正则公式不会被前端自动改写。

## 同名文件处理

同一个系列目录下如果出现同名文件：

- 同名且 SHA-256 相同：删除新进入的重复文件，保留已有文件。
- 同名但 SHA-256 不同：不会加 `-1`、`-2` 后缀，而是写入确定性分流目录：

```text
<series>/.same-name/<原文件名去扩展名>/<sha256>/<原文件名>
```

这种结构由文件内容哈希决定。相同输入再次分类后会形成同一棵目录树，避免因为处理顺序不同导致目录结构漂移。

对于下载器生成的 `name (1).jpg`、`name (2).jpg`：

- 如果基础文件损坏，会按编号顺序寻找健康副本补位，并保留损坏原件。
- 如果基础文件健康，编号副本会按同名哈希规则处理。

## 损坏图片和非媒体文件

图片会尝试解码验证，支持 `jpg/jpeg/png/gif/webp`。损坏图片会移动到：

```text
<quarantinePath>/corrupted/
```

视频、音频和文本类文件不做媒体内容解码，只按扩展名和正则分类。

## CLI 使用

从源码运行：

```bash
go run ./cmd/cli -action scan
```

Release 包中可执行文件名是 `pics-cli`。

常用命令：

```bash
# 完整扫描并同步数据库
go run ./cmd/cli -action scan -mode full

# 只分类不入库
go run ./cmd/cli -action scan -mode classifyOnly \
  -scan-path /path/to/inbox \
  -library-path /path/to/library

# 生成文件清单
go run ./cmd/cli -action create-manifest

# 只生成目录健康报告，不触发整理
go run ./cmd/cli -action health-report

# 备份数据库
go run ./cmd/cli -action dump-database

# 从最终库补齐或重建数据库记录
go run ./cmd/cli -action rebuild-database

# 查看统计和索引状态
go run ./cmd/cli -action stats

# 查看最近运行记录
go run ./cmd/cli -action list-runs -limit 20

# 查看单次运行摘要
go run ./cmd/cli -action show-run -run-id '<runId>'

# 查看单次运行 journal
go run ./cmd/cli -action run-journal -run-id '<runId>'

# 校验某次中断/停止/暂停运行是否具备恢复依据
go run ./cmd/cli -action verify-run -run-id '<runId>'
```

查询命令：

```bash
# 列出系列
go run ./cmd/cli -action list-series -limit 50

# 使用 nextCursor 取下一页
go run ./cmd/cli -action list-series -limit 50 -cursor '<nextCursor>'

# 搜索系列
go run ./cmd/cli -action search -query keyword -limit 50

# 按媒体类型列出某个系列下的媒体
go run ./cmd/cli -action list-media -series-id '<seriesObjectId>' -media-type image -limit 50
go run ./cmd/cli -action list-media -series-id '<seriesObjectId>' -media-type video -limit 50
```

分页优先使用 `cursor`。`page > 1` 会通过游标逐页走到目标页，只适合人工排查；大量数据下不要依赖深分页。

## Web 和 API

启动后端：

```bash
go run ./cmd/manager-server
```

启动前端开发服务：

```bash
cd web
npm install
npm run dev
```

默认 API 地址：

```text
http://localhost:8080/api/v1
```

主要接口：

```text
POST /api/v1/tasks
GET  /api/v1/tasks/{taskId}
DELETE /api/v1/tasks/{taskId}
POST /api/v1/tasks/{taskId}/pause
GET  /api/v1/runs
GET  /api/v1/runs/{runId}
GET  /api/v1/runs/{runId}/journal
GET  /api/v1/series?limit=20&cursor=
GET  /api/v1/series/{seriesId}/media/{mediaType}?limit=20&cursor=
GET  /api/v1/series/{seriesId}/images?limit=20&cursor=      # 兼容接口，只返回图片
GET  /api/v1/series/{seriesId}/thumbnail
GET  /api/v1/images/{imageId}/thumbnail
GET  /api/v1/series/{seriesId}/download
GET  /api/v1/media/{mediaId}/download
GET  /api/v1/search/text?q=keyword&limit=20&cursor=
POST /api/v1/search/image
GET  /api/v1/auth/status
POST /api/v1/auth/claim
GET  /api/v1/config
PUT  /api/v1/config
```

启动扫描任务：

```bash
curl -X POST http://localhost:8080/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"path":"/path/to/inbox","mode":"classifyOnly"}'
```

设备绑定开启后：

```bash
curl -X POST http://localhost:8080/api/v1/tasks \
  -H 'Authorization: Bearer <device-token>' \
  -H 'Content-Type: application/json' \
  -d '{"mode":"classifyOnly"}'
```

下载接口只按数据库 ID 下载，不接受任意路径。单文件下载支持 HTTP Range；系列下载会流式生成 zip。

列表接口返回统一 envelope：

```json
{
  "data": [],
  "meta": {
    "pagination": {
      "currentPage": 1,
      "totalPages": 10,
      "totalItems": 200,
      "limit": 20,
      "nextCursor": "..."
    }
  }
}
```

缩略图不会内联在 JSON 中。列表只返回 `thumbnailUrl`，前端按需请求缩略图 URL。

## 运行记录和 journal

每次扫描会在日志目录下写入持久运行记录：

```text
<logger.path>/runs/<runId>.json
<logger.path>/runs/<runId>.journal.jsonl
```

run JSON 包含：

- `status`：`pending`、`running`、`stopping`、`stopped`、`pausing`、`paused`、`completed`、`failed`、`interrupted`。
- `phase`：当前或最后阶段，例如 `preprocess`、`classify_done`、`archive_done`、`database_sync_done`。
- `counts`：阶段计数，例如 `preprocessedFiles`、`unsupportedFiles`、`classifiedFiles`、`sameNameFiles`。
- `errorSummary`：失败或启动恢复摘要。

journal 是 JSONL，每行一个事件。预处理、分类、聚合归档和入库阶段都会记录关键 before/after 事件，阶段结束会记录 checkpoint。后端启动时会把上次未结束的 `pending/running` run 标记为 `interrupted`，避免无人值守时误以为任务仍在运行。

同一时刻只允许一个维护任务。CLI 和 server 共用 `<logger.path>/runs/active.lock`，因此不会同时整理同一批文件。

停止后台任务：

```bash
curl -X DELETE http://localhost:8080/api/v1/tasks/<taskId> \
  -H 'X-Maintenance-Token: <token>'
```

停止会取消当前扫描 context，并把 run 标记为 `stopped`。下一次可以通过最终库、健康报告和 journal 判断恢复点；数据库侧可以用 `rebuild-database` 从最终库补齐。

暂停后台任务：

```bash
curl -X POST http://localhost:8080/api/v1/tasks/<taskId>/pause \
  -H 'X-Maintenance-Token: <token>'
```

暂停同样会取消当前扫描 context，但 run 会标记为 `paused`。它表达的是人为暂停，可用于 NAS 维护窗口结束、需要释放 IO 或准备后续恢复检查的场景。

恢复校验：

```bash
go run ./cmd/cli -action verify-run -run-id '<runId>'
```

该命令会读取 run+journal，确认存在 checkpoint，并基于当前最终库生成一份 `verify-<runId>.health.json`。它不会自动移动文件；它用于判断是否可以通过重新扫描或 `rebuild-database` 继续恢复。

`verify-run` 会输出 `recoveryStatus`：

- `complete`：运行已完成，当前校验未发现需要关注的错误。
- `recoverable_with_review`：运行被中断、停止、暂停或失败，但存在 checkpoint，可结合最终库和 journal 继续检查。
- `needs_attention`：journal 中存在失败事件或健康报告 warnings，恢复前需要人工查看。

每次归档后会输出目录健康报告：

```text
<backupPath>/reports/<runId>.health.json
```

报告包含最终库目录数、文件数、`.same-name` 文件数、隔离区文件数、非媒体跳过数、单目录最大文件数和阈值 warnings。`health-report` 的非媒体计数基于当前 `scanPath` 和媒体扩展配置做只读统计，不移动文件。

## 验证扫描结果

`verify-scan` 会复制或硬链接源数据到工作目录，运行扫描，然后比较输入、输出、隔离区和数据库数量。

只验证分类：

```bash
go run ./cmd/verify-scan -source ./test_img -mode classifyOnly -workers 4
```

验证完整入库：

```bash
go run ./cmd/verify-scan \
  -source ./test_img \
  -mode full \
  -mongo-uri 'mongodb://dev_user:password@localhost:27017/?authSource=admin' \
  -reset-db
```

参数：

- `-source`：源数据目录，必填。
- `-workdir`：验证工作目录，默认在系统临时目录创建。
- `-mode`：`classifyOnly` 或 `full`。
- `-copy-mode`：`hardlink` 或 `copy`。
- `-reset-db`：full 模式前清理验证数据库集合。
- `-keep`：保留工作目录，便于检查目录树。

## Release 包使用

下载对应平台包后解压：

```bash
tar -xzf PICs_Manager_v0.1.3_linux_amd64.tar.gz
cd PICs_Manager_v0.1.3_linux_amd64
```

修改 `config.yaml`，然后运行：

```bash
./manager-server
./pics-cli -action scan -mode classifyOnly
./verify-scan -source /path/to/dataset -mode classifyOnly
```

Windows 包中可执行文件后缀为 `.exe`。

## NAS 部署

部署模板在 `deploy/`：

- `deploy/systemd/pics-manager.service`
- `deploy/systemd/pics-manager-health-report.service`
- `deploy/systemd/pics-manager-health-report.timer`
- `deploy/logrotate/pics-manager`
- `deploy/docker-compose.yml`
- `Dockerfile`

systemd 模板默认把服务限制在指定媒体目录和运行目录内写入。Docker Compose 示例默认把后端和 MongoDB 绑定到 `127.0.0.1`，并通过 `/health` 做容器健康检查；前端查看端可以单独部署。
