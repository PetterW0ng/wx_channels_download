# 微信视频号下载器

体积小、使用简单、支持 macOS 和 Windows 系统。

## 使用说明

下载[构建包](https://github.com/ltaoo/wx_channels_download/releases)，**以管理员身份运行**，首次打开会自动安装证书，然后启动服务。

当终端提示「代理服务启动成功」就说明可以使用了。

![正常使用](./docs/assets/app_screenshot1.png)

> 已安装证书会跳过安装证书步骤。

打开微信 PC 端，点击需要下载的视频，在视频下方的操作按钮一栏，会多出一个下载按钮，如下所示

![视频下载按钮](./docs/assets/screenshot1.png)

如果没有，在页面侧边或底部会有悬浮按钮，拥有相同的功能

| 首页推荐 | 视频详情页 |
| --- | --- |
| ![首页推荐](docs/assets/fixed_btn1.jpg) | ![视频详情页](docs/assets/fixed_btn2.jpg) |


等待视频开始播放，然后暂停视频，点击下载按扭即可下载视频。下载成功后，会在上方显示已下载的文件，下载文件名最后面会标志该视频质量。

![视频下载成功](./docs/assets/screenshot2.png)

下载按钮默认会下载视频号默认质量的视频（即当前播放的视频，一般都是体积最小的），可以在下拉菜单下载其他质量的视频


## 登录认证

打开下载器页面后，需要使用 go-crm 账号登录。登录成功才会加载应用页面；业务 API、文件接口和 WebSocket 握手也会校验本地会话。侧边栏可退出登录，退出或会话过期会关闭该会话的 WebSocket。

启动顺序为：检查会话 → 登录并确认会话 → 创建下载与抓取模型 → 同时连接 `/ws/v1/download_task` 和 `/ws/scraper` → 加载页面业务请求。核心连接不依赖当前打开的页面；每次重连前重新检查会话，401、退出或到期会停止核心服务及后续网络请求。HTTP 和 WebSocket 使用登录页面的同一服务地址，避免 `localhost` 与 `127.0.0.1` 混用。更新后需刷新原先打开的页面，让旧连接加载新的认证流程。

在运行目录的 `config.yaml` 中配置（修改后重启）：

```yaml
auth:
  baseURL: "http://101.201.150.118:3001/api"
  platformType: 3
```

`baseURL` 包含 `/api`，程序向其追加 `/auth/login`。`platformType` 由服务端配置决定；请求中可省略，提供时必须与配置一致。默认值来自所提供的 go-crm 接口示例。线上传输账号密码时应将地址配置为支持 HTTPS 的认证服务。

本地接口：

| 接口 | 用途 |
| --- | --- |
| `POST /auth/login` 或 `POST /api/auth/login` | JSON 参数 `identity`、`password`；兼容 go-crm 的 `status/message/data` 响应，`data` 为 CRM Token |
| `GET /api/auth/session` | 查询当前用户名及会话到期时间；未登录返回 401 |
| `POST /api/auth/logout` | 撤销本地会话并清除 Cookie |

管理页面和普通 API 调用者使用登录响应设置的 HttpOnly Cookie，不接受 CRM Token 作为本地会话。API 调用者需保存并回传 Cookie。调用示例见 [api.http](./api.http)。

微信视频号页面运行在独立的浏览器环境，不能共享管理页面 Cookie。登录后，本地拦截器会为 `https://channels.weixin.qq.com` 注入脚本提供绑定最近一次管理登录的受限凭据，仅允许下载和视频号相关接口。HTTP 请求通过 Authorization 头传递凭据，WebSocket 通过握手子协议传递，凭据不会写入 URL。未登录时不启动业务请求；管理页面退出或会话到期后，凭据失效并关闭相关 WebSocket。其他来源不能使用该凭据。

升级认证功能后，请重启下载器、在管理页面登录，并关闭后重新打开微信视频号窗口，以加载新的注入脚本。

密码不落盘，会话仅保存在进程内，最长 72 小时且不超过 CRM Token 到期时间；重启服务后需重新登录。当前只接入登录接口，不调用 CRM 会员资料接口，因此不会同步该接口特有的会员到期或异地登录检查。

## 开发说明

先以 管理员身份 启动终端，然后 `go run main.go` 即可。

## 打包

参考 `build/build.sh` 脚本。

## 感谢

前端解密部分参考自
<br>
https://github.com/kanadeblisst00/WechatVideoSniffer2.0
<br>

后端解密代码来自
<br>
https://github.com/Hanson/WechatSphDecrypt


## ⚠️ 免责声明

```text
本项目为开源项目
仅用于技术交流学习和研究的目的
请遵守法律法规,请勿用作任何非法用途
否则造成一切后果自负
若您下载并使用即视为您知晓并同意
```
