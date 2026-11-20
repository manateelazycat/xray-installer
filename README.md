# xray-installer

在 VPS 本机执行的 Go 命令行工具。

启动后会交互式询问：
- VPS 绑定的域名
- Reality 伪装目标域名（直接回车默认 `www.bing.com`）

安装器在同一个 VPS 上部署 VLESS + REALITY + Vision（TCP 443）和 Hysteria 2（UDP 443），并生成可导入 FlClash 和使用 Mihomo（Clash Meta）内核的 Clash 客户端的 `proxy.yaml`。

## 一键安装并立即执行

> `install.sh` 会从 GitHub Releases 下载预编译二进制，因此仓库需要先发布一个 tag/release。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/manateelazycat/xray-installer/main/install.sh) install
```

仅安装，不立即运行：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/manateelazycat/xray-installer/main/install.sh) install --no-run
```

安装后以 dry-run 方式立刻启动：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/manateelazycat/xray-installer/main/install.sh) install -- --dry-run
```

一键卸载：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/manateelazycat/xray-installer/main/install.sh) uninstall
```

```bash
xray-installer
```

它会自动：
- 检查运行环境（root / Linux / systemd / Debian/Ubuntu/Arch Linux）
- 检查 TCP 和 UDP 443 端口占用情况并给出提示
- 安装 Xray
- 生成 VLESS + REALITY + Vision 和 Hysteria 2 服务端配置
- 启动并启用 `xray` systemd 服务
- 生成带「自动切换」故障转移组的长版客户端配置 `proxy.yaml`
- 支持 `uninstall`
- 支持 `--dry-run`
- 支持 GitHub Release 二进制一键安装脚本 `install.sh`

生成的 FlClash 配置默认启用 `reality-opts.support-x25519mlkem768: true`，用于兼容新版 Xray 的 REALITY 握手要求。

## TCP 与 UDP 自动切换

- 在 VPS 防火墙和云安全组分别放行 **TCP 443** 和 **UDP 443**。两个节点使用同一个 VPS 域名。
- 在 FlClash 或 Mihomo（Clash Meta）客户端导入 `proxy.yaml`，在「节点选择」中使用「自动切换」。新导入的配置默认优先选这个策略。已有配置保存过手动选项时，导入后请确认当前选项。旧版原版 Clash 缺少所需协议支持。
- 「自动切换」优先使用 REALITY；健康检查判定它不可用后，新连接会走仍然可用的 Hysteria 2 节点。健康检查每 30 秒运行，已中断的连接需要重连。
- 安装器生成 Hysteria 2 的密码和五年有效期的自签名证书。客户端通过 SHA-256 指纹验证证书，无需申请公网证书。重新安装会生成新凭据，需要重新导入客户端配置。

## 当前边界

- 只支持 **Debian / Ubuntu / Arch Linux + systemd**
- 需要在 **VPS 本机**运行
- 域名 DNS 需要提前配好
- 如果用了 Cloudflare，需要保证是 **DNS Only**

## 构建

```bash
go build ./cmd/xray-installer
```

## 运行

```bash
sudo ./xray-installer
```

## 帮助

```bash
./xray-installer --help
./xray-installer --version
```

## 卸载

```bash
sudo ./xray-installer uninstall
```

## Dry run

```bash
./xray-installer --dry-run
```

生成文件：
- Xray 配置：`/usr/local/etc/xray/config.json`
- FlClash 配置：`/usr/local/etc/xray-installer/proxy.yaml`
- 安装记录：`/usr/local/etc/xray-installer/install-result.json`

安装完成后，可用下面命令直接获取 FlClash 配置：

```bash
sudo cat /usr/local/etc/xray-installer/proxy.yaml
```

## 验证

```bash
go test ./...
go build ./cmd/xray-installer
```

## 参考

- 文章：<https://manateelazycat.github.io/2026/04/09/best-proxy/>
- Xray 安装脚本：<https://github.com/XTLS/Xray-install>
- Xray 命令文档：<https://xtls.github.io/document/command>

## License

GPL-3.0
