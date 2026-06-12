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

## 不要随意改动的兼容点

- 图片分类正则公式不要改。历史数据和下载器命名依赖这些规则。
- Mongo 集合和 API 中仍保留 `images`、`Image`、`imageCount` 等历史命名。当前语义已经是媒体文件，但字段名用于兼容旧数据和前端。
- 缩略图正文仍存 MongoDB，但列表接口只能返回 `thumbnailUrl`，不能把 base64 放回列表 JSON。
- 深分页必须走 cursor/keyset。`page > 1` 且没有 `cursor` 时 API 应返回错误。
- 以图搜图必须使用 pHash bucket 缩小候选集，不能回退到全量读取所有 pHash 后在 Go 中扫描。
- 同名不同哈希文件必须进入 `.same-name/<bucket>/<sha256>/<filename>`，不要改成简单追加 `-1`、`-2`。

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

## 交叉编译和发布

前端先构建：

```bash
cd web
npm run build
```

Go 命令推荐静态交叉编译：

```bash
env GOCACHE=/tmp/pics-manager-gocache CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o dist/PICs_Manager_linux_amd64/manager-server ./cmd/manager-server
```

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
