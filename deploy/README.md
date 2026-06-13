# NAS 部署模板

本目录提供长期无人值守部署参考。

## systemd

1. 解压 release 到 `/opt/pics-manager`。
2. 创建运行用户：

```bash
sudo useradd --system --home /var/lib/pics-manager --create-home pics-manager
```

3. 将配置放到 `/opt/pics-manager/config.yaml`，将敏感环境变量放到 `/etc/pics-manager/pics-manager.env`。
4. 复制 service：

```bash
sudo cp deploy/systemd/pics-manager.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now pics-manager
```

service 默认只给 `/srv/media`、`/var/lib/pics-manager` 和 `/var/log/pics-manager` 写权限。按 NAS 实际路径调整 `ReadWritePaths`。

## Docker Compose

Docker Compose 是 NAS 推荐部署方式。模板包含：

- `pics-manager`：Go API、扫描器和调度器。
- `pics-web`：nginx 静态前端查看器。
- `mongodb`：仅在 compose 内部网络暴露的 MongoDB。

准备配置：

```bash
cd deploy
cp .env.example .env
```

编辑 `.env`：

- `PICS_MEDIA_ROOT` 指向 NAS 上的媒体根目录。该目录下需要有 `inbox/staging/library/quarantine` 四个子目录。
- `PICS_DATA_ROOT` 指向应用数据目录，用于 MongoDB、日志、备份和设备绑定 token hash。
- `PUID/PGID` 设置为拥有媒体目录读写权限的 NAS 用户。
- `PICS_WEB_API_BASE_URL` 必须是浏览器能访问到的 API 地址，例如 `http://nas-tailnet-name:8080/api/v1`。

编辑 `nas/config.yaml`：

- 默认容器内路径是 `/media/inbox`、`/media/staging`、`/media/library`、`/media/quarantine` 和 `/data/*`。
- 默认开启 `security.enabled` 和 `scheduler.enabled`，首次使用需要进入容器生成配对码。
- 如果通过 Tailscale 域名访问前端，补充 `security.corsAllowedOrigins`。

启动：

```bash
cd deploy
docker compose --env-file .env up -d --build
```

首次生成配对码：

```bash
docker compose exec pics-manager pics-cli -action create-pairing-code -device-name nas-browser -scope admin
```

然后打开前端，把配对码输入登录页。需要给其他设备只读访问时，把 `-scope admin` 换成 `-scope viewer`。

常用维护命令：

```bash
docker compose exec pics-manager pics-cli -action scan -mode full
docker compose exec pics-manager pics-cli -action health-report
docker compose exec pics-manager pics-cli -action list-runs
docker compose logs -f pics-manager
```

默认后端和前端都绑定到 `127.0.0.1`，适合由 NAS 自带反向代理或 Tailscale 入口转发。MongoDB 不映射到宿主端口，只给 compose 内部访问。

配置文件 `deploy/nas/config.yaml` 是可写挂载，admin 页面保存配置会写回该文件。长期运行前建议确认它和 `PICS_DATA_ROOT`、`PICS_MEDIA_ROOT` 都由 `PUID/PGID` 对应用户拥有。

## 日志滚动

如果使用 systemd，优先依赖 journald。如果还有模块文件日志，可以复制 `deploy/logrotate/pics-manager` 到 `/etc/logrotate.d/` 并按实际日志目录调整路径。
