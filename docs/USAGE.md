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

# 备份数据库
go run ./cmd/cli -action dump-database

# 查看统计和索引状态
go run ./cmd/cli -action stats
```

查询命令：

```bash
# 列出系列
go run ./cmd/cli -action list-series -limit 50

# 使用 nextCursor 取下一页
go run ./cmd/cli -action list-series -limit 50 -cursor '<nextCursor>'

# 搜索系列
go run ./cmd/cli -action search -query keyword -limit 50

# 列出某个系列下的媒体
go run ./cmd/cli -action list-media -series-id '<seriesObjectId>' -limit 50
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
GET  /api/v1/series?limit=20&cursor=
GET  /api/v1/series/{seriesId}/images?limit=20&cursor=
GET  /api/v1/series/{seriesId}/thumbnail
GET  /api/v1/images/{imageId}/thumbnail
GET  /api/v1/search/text?q=keyword&limit=20&cursor=
POST /api/v1/search/image
GET  /api/v1/config
PUT  /api/v1/config
```

启动扫描任务：

```bash
curl -X POST http://localhost:8080/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"path":"/path/to/inbox","mode":"classifyOnly"}'
```

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
tar -xzf PICs_Manager_v0.1.0_linux_amd64.tar.gz
cd PICs_Manager_v0.1.0_linux_amd64
```

修改 `config.yaml`，然后运行：

```bash
./manager-server
./pics-cli -action scan -mode classifyOnly
./verify-scan -source /path/to/dataset -mode classifyOnly
```

Windows 包中可执行文件后缀为 `.exe`。
