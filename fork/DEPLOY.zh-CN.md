# 在服务器部署二开的 New API

## 为什么 GitHub 曾经看不到分支

之前创建的是本地分支，没有执行 `git push`，所以 GitHub 只显示 `main`。现在 `feat/custom-development` 已推送到 `qqcxz/new-api`。在仓库页面的分支下拉框选择它即可；GitHub 默认仍可能显示 `main`。

以后的流程是：本地在固定分支开发 → 提交 → 推送到这个分支 → 服务器拉取同一分支 → 重新构建并替换应用容器。只修改本地代码或只推送 GitHub，都不会自动更新服务器。

## 适用范围

本文按 **Linux 服务器、新建独立实例、Docker Engine + Docker Compose** 编写。需要 Git、OpenSSL、Docker/Compose，以及下载构建依赖的网络。服务器不必单独安装 Go 或 Bun，根目录 Dockerfile 会在构建阶段处理。

专用配置是 `fork/compose.production.yml`，从当前源码构建 `new-api-custom:local`，同时启动 PostgreSQL 15 和 Redis 7。项目名固定为 `new-api-custom`，有独立的数据库和持久化数据卷，数据库与 Redis 不开放宿主机端口。

**始终带上本文的 `--env-file` 和 `-f` 参数。根目录原有 `docker-compose.yml` 使用官方镜像；直接运行不带参数的 `docker compose up -d` 不会部署你的二开源码。**

这不是旧实例迁移脚本。如果服务器已运行官方实例且需要保留账户、余额、渠道等数据，先备份原数据库、数据目录、配置和密钥，确认原数据库类型后另做迁移；不要把这里的新数据库当成原数据库。

## 首次部署

### 1. 获取固定开发分支

在服务器上选择存放代码的目录，然后执行：

```bash
git clone --single-branch --branch feat/custom-development https://github.com/qqcxz/new-api.git new-api-custom
cd new-api-custom
git branch --show-current
docker --version
docker compose version
```

若 Docker 尚未安装，按服务器发行版使用 Docker 官方安装说明。本文使用 `docker compose` 子命令，不使用旧的 `docker-compose` 程序。

### 2. 创建服务器私密配置

仅首次执行复制；不要在更新时覆盖已经保存的配置：

```bash
cp fork/deploy.env.example fork/deploy.env
chmod 600 fork/deploy.env
openssl rand -hex 32
```

运行 `openssl rand -hex 32` 四次，生成四个不同的随机值。使用编辑器填写：

```bash
nano fork/deploy.env
```

将值分别填到 `POSTGRES_PASSWORD`、`REDIS_PASSWORD`、`SESSION_SECRET`、`CRYPTO_SECRET`。使用十六进制密码，避免数据库/Redis URL 中的特殊字符编码问题。其他项目暂时保留默认值。

`fork/deploy.env` 已被 Git 和 Docker 构建上下文忽略，不会随提交上传或被复制进镜像构建目录。仍应在安全位置备份这份文件，不要发到聊天或贴进工单。后续更新保留这些值，尤其不要随意更换加密密钥；数据库已初始化后，修改环境文件也不会自动修改数据库内已有用户密码。

### 3. 校验、构建、启动

以下命令都在仓库根目录执行：

```bash
# 检查配置而不输出解析后的密码
docker compose --env-file fork/deploy.env -f fork/compose.production.yml config --quiet

# 从本分支源码构建应用镜像；首次构建需要下载前后端依赖
docker compose --env-file fork/deploy.env -f fork/compose.production.yml build --pull new-api

# 启动应用、数据库与 Redis
docker compose --env-file fork/deploy.env -f fork/compose.production.yml up -d --no-build

# 查看状态与应用日志
docker compose --env-file fork/deploy.env -f fork/compose.production.yml ps
docker compose --env-file fork/deploy.env -f fork/compose.production.yml logs --tail=100 new-api
curl -fsS http://127.0.0.1:3001/api/status
```

应用默认只绑定服务器的 `127.0.0.1:3001`，并不开放公网访问。镜像是服务器本地构建的，不依赖发布到 Docker Hub，也不会自动推送镜像。

### 4. 访问与配置域名

无域名时，可以在自己的电脑上建立 SSH 隧道，用于首次初始化：

```bash
ssh -L 3001:127.0.0.1:3001 你的SSH用户@服务器IP
```

保持 SSH 连接，在电脑浏览器打开 `http://localhost:3001`，按照安装向导设置管理员。

对外服务时，在宿主机 Nginx 或其他 HTTPS 反向代理上，把实际域名转发到服务器的 `http://127.0.0.1:3001`，配置好证书。流式响应需要代理关闭响应缓冲并配置适当超时。如果反向代理本身运行在另一容器，需使用该容器能访问的宿主机地址或专门的网络连接，不能把另一个容器的 localhost 当作宿主机。

HTTPS 域名启用后，在 `fork/deploy.env` 设置以下两项，替换成真实、准确的域名 Origin，不带路径：

```dotenv
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_TRUSTED_URL=https://你的实际域名
```

然后重新创建应用容器，使环境变量生效：

```bash
docker compose --env-file fork/deploy.env -f fork/compose.production.yml up -d --no-build --no-deps new-api
```

## 后续更新二开功能

先确认开发电脑上的功能已经提交并推送到 `origin/feat/custom-development`。每次升级前，备份当前数据库、应用数据卷、日志和 `fork/deploy.env`，记下当前部署提交号。数据库可能随应用启动自动迁移，因此仅切回旧代码不等于恢复数据库。

在服务器仓库目录执行：

```bash
git status --short
git branch --show-current
git rev-parse --short HEAD

# 工作区干净、且位于固定开发分支时再继续
git pull --ff-only origin feat/custom-development

# 先构建新镜像；构建失败时旧应用容器仍可继续运行
docker compose --env-file fork/deploy.env -f fork/compose.production.yml build --pull new-api

# 只替换应用，不主动替换 PostgreSQL 或 Redis
docker compose --env-file fork/deploy.env -f fork/compose.production.yml up -d --no-build --no-deps new-api

docker compose --env-file fork/deploy.env -f fork/compose.production.yml ps
docker compose --env-file fork/deploy.env -f fork/compose.production.yml logs --tail=100 new-api
curl -fsS http://127.0.0.1:3001/api/status
```

这是单实例更新，替换应用容器时会有短暂中断。不要在服务器直接开发或用 `git reset --hard` 覆盖修改。不要用 `docker compose down -v` 更新；`-v` 会删除本配置使用的数据卷。

## 数据与备份

固定 Compose 项目名下的数据卷包括：

- `postgres_data`：账户、余额、渠道等数据库记录。
- `app_data`：应用数据目录 `/data`。
- `app_logs`：应用日志目录 `/app/logs`。
- `redis_data`：Redis 持久化目录。

正常重新构建和替换应用容器会继续使用同一项目的数据卷；不要随意更改项目名、删除卷或重新生成密码。可用 `docker compose --env-file fork/deploy.env -f fork/compose.production.yml volumes` 查看实际卷名。

PostgreSQL 逻辑备份示例（只备份数据库，不等于备份全部数据）：

```bash
mkdir -p fork/backups
chmod 700 fork/backups
docker compose --env-file fork/deploy.env -f fork/compose.production.yml exec -T postgres pg_dump -U newapi -d newapi -Fc > "fork/backups/database-$(date +%Y%m%d-%H%M%S).dump"
```

使用前确认命令成功，定期将备份移到服务器之外并测试恢复；应用数据卷和私密配置另行备份。`fork/backups/` 已排除在 Git 与 Docker 构建上下文之外。上线已有业务之前应确定数据库与文件的协调备份、恢复方式。

## 参考

- New API 官方 Docker Compose 安装说明：`https://docs.newapi.ai/zh/docs/installation/deployment-methods/docker-compose-installation`
- Docker Compose 源码构建规范：`https://docs.docker.com/reference/compose-file/build/`
- Docker 安装说明：`https://docs.docker.com/engine/install/`

本文提供了仓库配置和操作步骤；尚未在你的服务器上执行镜像构建或启动验证。
