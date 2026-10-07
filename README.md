# XGift

X (Twitter) Premium 礼品兑换平台。你生成兑换码发给用户；用户在兑换页输入兑换码与自己的 X 用户名，系统从账号池挑选 X 账号、经其绑定的 Shadowsocks 代理创建 **Stripe 付款链接**，用户自行付款后自动核对并显示兑换成功。服务端不接触银行卡，也不保存信用卡信息。

- **首页** `/`：落地页
- **兑换页** `/redeem/`：输入兑换码与用户名，实时显示生成进度与付款链接，付款后自动更新
- **管理后台**：兑换码、批次文件夹、统计、X 账号池与代理池、付款链接状态、手动生成链接、Stripe 公钥、站点公告、日志。路径每次安装随机生成，只在启动日志里输出
- **代理池**：后台每 5 分钟自动测活，检出失败（代理不可达、Cookie 失效、账号被封、被限流）会自动换下一个账号
- **长期运行**：记录保留策略 + 日志滚动 + 容器日志上限，磁盘占用在后台「设置」里可见

## Docker Compose 部署（推荐）

准备一台能运行 Docker 的 Linux 服务器、一个托管在 Cloudflare 的域名、至少一个 X 账号（`auth_token`、`ct0`）及其对应的 Shadowsocks 代理、以及 X 结账页使用的 `pk_live_` Stripe 公钥（可以稍后在后台「设置」里填）。容器只监听 `127.0.0.1`，不开放任何公网端口，对外由 Cloudflare Tunnel 回源（见下一节）。

```sh
git clone https://github.com/transflo/x_gift_bot.git
cd x_gift_bot
cp .env.example .env
# 编辑 .env：XGIFT_ORIGIN 填你的公网域名，XGIFT_ADMIN_PASSWORD 填 32 位以上随机密码
docker compose up -d
```

首次启动会建库，并写入内置的 X API 授权与默认套餐目录。

Stripe 公钥和站点公告都在后台「设置」里配置，保存即生效，不需要重启容器。

启动后看日志拿管理后台地址：

```sh
docker compose logs xgift | grep 管理后台
```

`https://你的域名/` 是首页，`/redeem/` 是兑换页。管理后台地址形如 `https://你的域名/1a2b3c4d5e6f7a8b9c0d/`，登录只用密码（无用户名）。

X 账号池在后台「账号」标签页添加，每个账号需要 `auth_token`、`ct0` 和一个 Shadowsocks 代理（**仅支持 Shadowsocks**）。

## 日常运维

```sh
docker compose logs -f xgift                    # 日志（含管理后台地址）

X="docker compose run --rm xgift"
V="--db /data/records.db"

$X status $V                                    # 校验账号池、API 授权、Stripe 公钥与套餐目录
$X accounts list $V                             # 查看账号池
$X accounts test $V                             # 立即测活全部账号
$X link alice --months 3 $V                     # 生成一次性付款链接（无需兑换码）
```

**备份**：所有状态都在 named volume `xgift-data`（`records.db`、`site.db`、`logs/`、`admin-path`）。定期备份整个卷。记录库是明文的，卷备份等同于备份全部凭据，请按敏感数据保存。

**更新**：`git pull && docker compose build && docker compose up -d`。数据库升级是增量式的，已有兑换码与订单会保留。

> 从「保管库」版本升级上来的注意：记录库换了文件（`vault.db` → 明文 `records.db`）且不再有密码文件，旧库不会被自动接管。全新启动后需要重新添加 X 账号池，并从「设置」里重填 Stripe 公钥。旧卷可以留作冷备。

## Cloudflare Tunnel

容器只绑定 `127.0.0.1:${XGIFT_PORT:-8787}`，不开放任何公网端口，宿主机上也不需要反向代理。装了 `cloudflared` 之后用 Tunnel 把它接到公网域名：

```sh
cloudflared tunnel login
cloudflared tunnel create xgift
cloudflared tunnel route dns xgift xp.example.com    # 换成你的域名
cloudflared tunnel run --url http://127.0.0.1:8787 xgift
```

跑通之后按 cloudflared 的文档把它装成 systemd 服务，开机自启。访客真实 IP 由 cloudflared 写入 `X-Forwarded-For`，本站按此取值，不需要额外配置。

**必须关掉 Rocket Loader**（Cloudflare 控制台 → Speed → Optimization）。它会把页面的 `<script src>` 改写成自己的异步加载器，而本站 CSP 只放行带 nonce 的脚本，加载器本身就被拦下，前端从此不会启动。症状很好认：后台页面一直转圈、连密码框都出不来，而服务器日志里什么都没有——请求根本没到容器。Auto Minify 也建议一并关掉。

## 环境变量

`docker compose` 从项目根目录的 `.env` 读取；裸机部署由 `site.env` 或 systemd `EnvironmentFile` 提供。

| 变量 | 说明 |
|---|---|
| `XGIFT_ORIGIN` | 站点完整域名，必须 `https://` 开头，用于 Origin 校验。不能带尾斜杠 |
| `XGIFT_ADMIN_PASSWORD` | 后台管理员密码，至少 32 字符。生成：`openssl rand -base64 24` |
| `XGIFT_STRIPE_KEY` | 可选的 `pk_live_` 公钥（公开密钥）。**仅在记录为空时写入**，之后请改后台「设置」 |
| `XGIFT_PORT` | 宿主机监听端口，默认 8787 |
| `XGIFT_PAYMENTS_ENABLED` | `true` 开放前台兑换，`false` 暂停（不消耗兑换码） |
| `XGIFT_TURNSTILE_SITE_KEY` / `XGIFT_TURNSTILE_SECRET_FILE` | 可选 Cloudflare Turnstile 人机验证，两者必须同时配置 |

以下仅在裸机部署或需要覆盖默认值时使用：`XGIFT_LISTEN`（默认 `127.0.0.1:8787`）、`XGIFT_ALLOW_PUBLIC_LISTEN`（监听非回环地址必须设为 `true`）、`XGIFT_DATA_DIR`（必填）、`XGIFT_ADMIN_PASSWORD_FILE`（`XGIFT_ADMIN_PASSWORD` 的替代，密码放 0600 文件而不进环境变量）。

`/livez` 是存活探针（进程与数据库可用即 200，Docker 健康检查用它）；`/healthz` 是就绪探针，账号池为空时返回 503。

## 后台「设置」

- **Stripe 公钥**：校验格式后写入记录库，可用于替换 `XGIFT_STRIPE_KEY` 的初始值。同页显示套餐目录解析出的商户、币种与金额，用来核对配置是否生效。
- **站点公告**：开启后在首页与兑换页顶部显示一条横幅，可选普通／提醒／重要三种语气，保存即生效，不需要重新构建镜像。
- **存储占用**：所在分区剩余空间、各数据库与日志文件体积、每一类记录的条数与占用。

## 日志

后台「日志」标签页展示最近的访问与事件记录，并可按级别筛选、下载完整的 JSONL 文件。日志写在数据卷的 `logs/` 下，单文件上限 8 MB、保留 4 个归档，总量不会超过 32 MB；`docker-compose.yml` 另外给容器日志设了 10 MB × 3 的上限。

记录内容包括请求方法、路径、状态码、耗时、User-Agent、Referer，以及**访问者真实 IP**，还有订单事件（链接生成、付款确认、生成失败）和管理员操作。

IP 取自 `X-Real-IP`，其次 `X-Forwarded-For`，都没有时才退回连接来源——在容器里那会是代理的内网地址（通常形如 `172.x.x.x`）。Cloudflare Tunnel 会写入 `X-Forwarded-For`；如果用的是自建反向代理，记得让它传递真实 IP，否则所有访客会共用一个限流桶。

请求 URL 中的查询参数不写入日志，所以下载下来的文件可以直接交给他人查看。

## 存储与清理

系统每天自动清理一次（启动 5 分钟后执行第一次，也可以在「设置」里点「立即清理」）：

| 记录 | 保留 |
|---|---|
| 付款会话快照 `checkout-verification:` | 30 天 |
| Stripe 错误诊断 `stripe-error:` | 30 天 |
| 付款完成凭证 `checkout-completion:` | 90 天 |
| 换链接历史 `checkout-history:` | 90 天 |

兑换码原文、订单绑定、账号池、API 授权与套餐目录**不会**被删除——它们是业务数据，不是运行痕迹。清理后如果碎片占比明显会执行一次 `VACUUM`，并截断 SQLite 的 WAL 文件。

## 工作方式

1. 用户提交兑换码与 X 用户名；服务端校验后立即绑定，并在后台开始生成链接。
2. 选账号时优先沿用该订单已绑定的账号，否则选等待时间最短的账号。每个账号有 15 秒创建间隔与 3 分钟未付款窗口，天然形成账号级负载均衡。
3. 服务端经该账号的 Shadowsocks 代理调用 X GraphQL API 创建赠送订单，并只读访问 Stripe 校验商户、商品、金额、收件人与 `Unpaid` 状态；全部通过后才把链接推给前台。
4. 用户自行在 Stripe 付款；后台定期只读轮询会话状态，成功后把兑换码标记为已完成。
5. 链接过期或被支付机构拒绝时，用户可对同一兑换码手动重新生成一次，之后需联系管理员。

后台「兑换码」列表的付款链接列显示每笔订单的链接状态——待付款（带剩余时间倒计时）、已过期、已拒绝、已付款，点开可以查看链接全文、有效期、绑定的 X 账号与订单消息。

X 请求不读取环境代理，全部走账号绑定的 Shadowsocks 出口；Stripe 只读校验复用同一出口，保证地区价格一致。

### 代理测活与故障转移

后台每 5 分钟探测所有启用账号（经其代理 HEAD `x.com`）。**连续 3 次失败**的账号会被排到选号队列末尾，恢复后自动回到正常。

检出过程中遇到账号侧问题会**自动换下一个账号**重试：代理不可达、Cookie 失效（401/403）、账号不存在（404）、被限流（429）。X 自身的 5xx 不算账号问题，仍走原来的退避重试，不浪费账号池。

后台账号列表的「测活」列显示每个账号最近一次探测结果，点「测试」可立即探测。

## 命令行参考

```
xgift setup                      交互式首次配置（可选：自定义套餐目录或预装账号池时才需要）
xgift init                       从 stdin 读取 JSON 记录建库（accounts/api-auth/stripe-key/catalog）
xgift status                     校验账号池、API 授权、Stripe 公钥与套餐目录
xgift put --name <name>          覆盖单个记录
xgift accounts list|add|remove|enable|disable|test   管理 X 账号池
xgift check                      测试全部账号代理到 X 的连通性
xgift link <username> --months N 生成或复用付款链接并打印 URL
```

`accounts add` 从 stdin 读取账号 JSON 数组（`id` 可省略，系统自动生成）：

```json
[{ "label": "主账号", "auth_token": "....", "ct0": "....", "enabled": true,
   "proxy": { "type": "shadowsocks", "server": "1.2.3.4", "server_port": 8388,
              "method": "aes-256-gcm", "password": "...." } }]
```

每个账号必须绑定一个 Shadowsocks 代理，其他代理类型会被拒绝。

## 裸机部署

需要 Node.js 20.9+ 与 Go 1.27+：

```sh
npm --prefix web ci
npm --prefix web run build                    # 静态导出并复制到 internal/site/web
CGO_ENABLED=1 go build -tags with_quic,with_utls -o bin/xgift     ./cmd/xgift
CGO_ENABLED=1 go build -tags with_quic,with_utls -o bin/xgift-web ./cmd/xgift-web
```

按 `deploy/site.env.example` 准备 `/etc/xgift/site.env`（0600），填好 `XGIFT_ORIGIN`、`XGIFT_ADMIN_PASSWORD`（`XGIFT_STRIPE_KEY` 可选），首次启动会自动建库。按需修改 `deploy/xgift.service` 后：

```sh
sudo systemctl link $PWD/deploy/xgift.service
sudo systemctl enable --now xgift
```

对外访问同样走 Cloudflare Tunnel，把它指向 `127.0.0.1:8787` 即可。

需要自定义套餐目录或预装账号池时，再运行 `./bin/xgift setup` 交互式向导。

## 安全

- 敏感记录（X Cookie、代理密码、Stripe 公钥、兑换码明文）**明文**存在 SQLite 记录库 `records.db` 里。保护它的是文件权限（0600）与数据目录（0700）——拿到这个文件就等于拿到全部凭据，卷备份请按敏感数据对待。
- 后台路径每次安装随机生成（`admin-path`，0600），只在启动日志输出；公开页面不含任何指向它的链接。
- 后台使用 Basic Auth（仅密码），管理员密码下限 32 字符；`/api/*` 与后台 API 按 IP 限流，超出返回 429。密码建议用 `openssl rand -base64 24` 生成。
- 每个响应使用独立 CSP nonce，禁止内联脚本与外部资源；静态资源由 Go 二进制内嵌提供。**因此不要在 Cloudflare 上开 Rocket Loader**，见上文。
- 服务端不包含任何银行卡数据，也不会向 Stripe 提交付款。

> 随机路径只是让登录页躲开自动扫描，真正的防线是密码。`XGIFT_ADMIN_PASSWORD` 请用 `openssl rand -base64 24` 生成。

## 开发

```sh
# 前端（Next.js 16 + shadcn/ui）
cd web
npm ci
npm run dev        # 开发服务器
npm run typecheck
npm run build      # 静态导出 + 复制到 ../internal/site/web

# 后端
go test ./...
go build ./cmd/...
```

Docker 构建依次执行前端导出与 Go 编译，最终镜像只包含 `xgift` 与 `xgift-web` 两个二进制和运行时依赖。

首页是一屏内容：一句标题、一个按钮，背景是一块由 CSS 画出的柔光。这一页不含任何图片或视频，也不下发客户端 JS——`web/app/page.tsx` 是服务端组件，改背景只需改 `Backdrop` 里那个径向渐变。

`web/public/` 下的文件会被 `go:embed` 一并打进二进制，所以不再使用的素材要删掉：放过一个 4 MB 的视频，就同时重了首屏和二进制。
