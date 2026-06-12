# PICs Manager

PICs Manager 是一个基于 Go、React/TypeScript 和 MongoDB 的本地媒体分类与管理器。项目名保留 `PICs`，但当前版本已经从图片管理扩展为通用媒体管理：图片、视频、音频和文本类文件可以按各自独立的正则规则分类、归档和查询。

## 当前能力

- 支持 `full` 和 `classifyOnly` 两种扫描模式。
- 支持按媒体类型配置扩展名和独立分类正则。
- 支持图片损坏检测与隔离。
- 支持下载器常见的 `file (1).jpg` 补位语义。
- 支持同名文件的哈希分流：同名同哈希删除重复文件，同名不同哈希进入 `.same-name/<原文件名桶>/<sha256>/<原文件名>`。
- 支持持久化 run/journal：每次扫描都有 runId、阶段、计数、错误摘要和 JSONL 事件记录。
- 支持 NAS 运行控制：保守默认 worker、维护时间窗口、轻量 IO 节流和目录健康报告。
- 支持 MongoDB 入库、索引维护、文本搜索、以图搜图和缩略图懒加载。
- 提供 systemd、Docker Compose 和 logrotate 部署模板。
- 提供 Web UI、HTTP API、CLI 和验证工具。

## 文档

- [使用文档](docs/USAGE.md)
- [开发文档](docs/DEVELOPMENT.md)

## 快速启动

准备 MongoDB 后，修改 `config.yaml` 中的扫描路径和媒体规则，然后启动服务：

```bash
go run ./cmd/manager-server
```

启动前端开发服务：

```bash
cd web
npm install
npm run dev
```

执行一次只分类不入库的扫描：

```bash
go run ./cmd/cli -action scan -mode classifyOnly -scan-path /path/to/inbox -library-path /path/to/library
```

执行完整验证：

```bash
env GOCACHE=/tmp/pics-manager-gocache go test ./...
env GOCACHE=/tmp/pics-manager-gocache go vet ./...
cd web && npm run lint && npm run build
```
