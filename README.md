# xray-installer

在 VPS 本机执行的 Go 命令行工具。

启动后会交互式询问：
- 域名
- FlClash 节点名称（默认 `[自建 1] 美国家宽-Reality`）

然后自动继续安装。

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
- 检查 443 端口占用情况并给出提示（不再前置拦截安装）
- 安装 Xray
- 生成 VLESS + Reality + Vision 服务端配置
- 启动并启用 `xray` systemd 服务
- 生成文章同款长版 FlClash 配置 `proxy.yaml`
- 支持 `uninstall`
- 支持 `--dry-run`
- 支持 GitHub Release 二进制一键安装脚本 `install.sh`

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
