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

```bash
cd deploy
PIC_MANAGER_MAINTENANCE_TOKEN=change-me docker compose up -d
```

compose 示例把后端和 MongoDB 都绑定到 `127.0.0.1`。前端查看端可以单独部署，通过反向代理访问后端只读 API；维护 API 设置 `PIC_MANAGER_MAINTENANCE_TOKEN` 后需要 token。

## 日志滚动

如果使用 systemd，优先依赖 journald。如果还有模块文件日志，可以复制 `deploy/logrotate/pics-manager` 到 `/etc/logrotate.d/` 并按实际日志目录调整路径。
