package installer

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"xray-installer/internal/config"
)

const (
	DefaultNodeName  = "[自建 1] 美国家宽-Reality"
	HysteriaNodeName = "[自建 1] 美国家宽-Hysteria2"

	xrayBinary       = "/usr/local/bin/xray"
	xrayConfigDir    = "/usr/local/etc/xray"
	xrayConfigPath   = "/usr/local/etc/xray/config.json"
	outputDir        = "/usr/local/etc/xray-installer"
	proxyConfigPath  = "/usr/local/etc/xray-installer/proxy.yaml"
	metadataPath     = "/usr/local/etc/xray-installer/install-result.json"
	installScriptURL = "https://raw.githubusercontent.com/XTLS/Xray-install/main/install-release.sh"
	installLockPath  = "/run/xray-installer.lock"

	xrayConfigMode = 0o640
	privateMode    = 0o600
)

var domainRE = regexp.MustCompile(`^(?i)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

type Installer struct {
	stdout io.Writer
	stderr io.Writer
}

type InstallRequest struct {
	Domain   string
	DestHost string
	DryRun   bool
}

type Result struct {
	Domain          string    `json:"domain"`
	NodeName        string    `json:"node_name"`
	PublicIP        string    `json:"public_ip"`
	Port            int       `json:"port"`
	HysteriaNode    string    `json:"hysteria2_node_name"`
	HysteriaPort    int       `json:"hysteria2_udp_port"`
	UUID            string    `json:"uuid"`
	PublicKey       string    `json:"public_key"`
	ShortID         string    `json:"short_id"`
	DestHost        string    `json:"dest_host"`
	XrayConfigPath  string    `json:"xray_config_path"`
	ProxyConfigPath string    `json:"proxy_config_path"`
	DryRun          bool      `json:"dry_run"`
	InstalledAt     time.Time `json:"installed_at"`
}

func New(stdout, stderr io.Writer) *Installer {
	return &Installer{stdout: stdout, stderr: stderr}
}

func (i *Installer) Run(ctx context.Context, req InstallRequest) (*Result, error) {
	domain, err := normalizeDomain(req.Domain)
	if err != nil {
		return nil, err
	}
	destHost, err := normalizeDestHost(req.DestHost)
	if err != nil {
		return nil, err
	}

	nodeName := DefaultNodeName

	i.step("运行安装前检查")
	publicIP, err := i.preflight(ctx, !req.DryRun)
	if err != nil {
		return nil, err
	}

	if req.DryRun {
		i.step("Dry run：跳过 Xray 安装")
	} else {
		lock, err := lockInstallation()
		if err != nil {
			return nil, err
		}
		defer lock.Close()

		i.step("安装 Xray")
		if err := i.installXray(ctx); err != nil {
			return nil, err
		}
	}

	i.step("生成 VLESS / REALITY 和 Hysteria 2 配置")
	uuid, err := newUUID()
	if err != nil {
		return nil, fmt.Errorf("generate uuid: %w", err)
	}

	privateKey, publicKey, err := newX25519KeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate x25519 key pair: %w", err)
	}

	shortID, err := newShortID(8)
	if err != nil {
		return nil, fmt.Errorf("generate short id: %w", err)
	}

	now := time.Now().UTC()
	hysteria, err := newHysteriaParameters(domain, now)
	if err != nil {
		return nil, fmt.Errorf("generate Hysteria 2 credentials: %w", err)
	}

	params := config.Parameters{
		Domain:     domain,
		NodeName:   nodeName,
		UUID:       uuid,
		PrivateKey: privateKey,
		PublicKey:  publicKey,
		ShortID:    shortID,
		DestHost:   destHost,
		Port:       config.DefaultPort,
		Hysteria:   hysteria,
	}

	xrayConfig, err := config.RenderXray(params)
	if err != nil {
		return nil, err
	}

	flClashConfig, err := config.RenderFlClash(params)
	if err != nil {
		return nil, err
	}

	result := &Result{
		Domain:          domain,
		NodeName:        nodeName,
		PublicIP:        publicIP,
		Port:            config.DefaultPort,
		HysteriaNode:    hysteria.NodeName,
		HysteriaPort:    config.DefaultPort,
		UUID:            uuid,
		PublicKey:       publicKey,
		ShortID:         shortID,
		DestHost:        destHost,
		XrayConfigPath:  xrayConfigPath,
		ProxyConfigPath: proxyConfigPath,
		DryRun:          req.DryRun,
		InstalledAt:     now,
	}

	if req.DryRun {
		i.step("校验生成结果")
		if err := i.validateDryRun(ctx, xrayConfig); err != nil {
			return nil, err
		}
		i.printDryRunSummary(result)
		return result, nil
	}

	if err := os.MkdirAll(xrayConfigDir, 0o755); err != nil {
		return nil, fmt.Errorf("create xray config dir: %w", err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	if err := i.validateXrayConfig(ctx, xrayConfig); err != nil {
		return nil, err
	}

	group, err := xrayConfigGroup(ctx)
	if err != nil {
		return nil, err
	}
	meta, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal install result: %w", err)
	}
	meta = append(meta, '\n')
	files := []configurationFile{
		{path: xrayConfigPath, data: xrayConfig, mode: xrayConfigMode, group: group},
		{path: proxyConfigPath, data: flClashConfig, mode: privateMode, group: -1},
		{path: metadataPath, data: meta, mode: privateMode, group: -1},
	}
	_, statusErr := commandOutput(ctx, "systemctl", "is-active", "xray")
	err = replaceConfiguration(files, func() error {
		i.step("启动并验证 Xray 服务")
		return i.enableAndRestartXray(ctx)
	}, func() error {
		action := "stop"
		if statusErr == nil {
			action = "restart"
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		output, err := commandOutput(rollbackCtx, "systemctl", action, "xray")
		if err != nil {
			return fmt.Errorf("restore xray service: %w\n%s", err, strings.TrimSpace(output))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	i.printSummary(result)
	return result, nil
}

func (i *Installer) Uninstall(ctx context.Context, dryRun bool) error {
	if runtime.GOOS != "linux" {
		return errors.New("only Linux is supported")
	}
	if !dryRun && os.Geteuid() != 0 {
		return errors.New("please run uninstall as root")
	}
	if !dryRun {
		lock, err := lockInstallation()
		if err != nil {
			return err
		}
		defer lock.Close()
	}

	i.step("检查卸载目标")

	xrayInstalled := pathExists(xrayBinary) || pathExists("/etc/systemd/system/xray.service")
	paths := []string{xrayConfigPath, proxyConfigPath, metadataPath, outputDir}

	if dryRun {
		fmt.Fprintln(i.stdout, "Dry run：以下内容将被移除：")
		if xrayInstalled {
			fmt.Fprintf(i.stdout, "- Xray（通过官方卸载脚本 remove --purge）\n")
		} else {
			fmt.Fprintf(i.stdout, "- Xray 未检测到，跳过官方卸载脚本\n")
		}
		for _, path := range paths {
			if pathExists(path) {
				fmt.Fprintf(i.stdout, "- %s\n", path)
			}
		}
		if !pathExists(outputDir) && !xrayInstalled {
			fmt.Fprintln(i.stdout, "- 没有发现可移除内容")
		}
		return nil
	}

	if xrayInstalled {
		i.step("卸载 Xray")
		if err := i.removeXray(ctx); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(i.stdout, "未检测到已安装的 Xray，跳过官方卸载脚本。")
	}

	for _, path := range []string{xrayConfigPath, proxyConfigPath, metadataPath} {
		if err := removePathIfExists(path); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return fmt.Errorf("remove installer output dir: %w", err)
	}
	if err := removeDirIfEmpty(xrayConfigDir); err != nil {
		return err
	}

	fmt.Fprintln(i.stdout, "\n卸载完成。")
	return nil
}

func (i *Installer) preflight(ctx context.Context, requireRoot bool) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("only Linux is supported")
	}
	if requireRoot && os.Geteuid() != 0 {
		return "", errors.New("please run as root")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", errors.New("systemctl not found; systemd is required")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return "", errors.New("systemd does not appear to be active")
	}

	osID, osLike, err := readOSRelease()
	if err != nil {
		return "", err
	}
	if !supportsDistro(osID, osLike) {
		return "", fmt.Errorf("unsupported distro %q (ID_LIKE=%q); only Debian/Ubuntu/Arch Linux are supported in v1", osID, osLike)
	}

	publicIP, err := detectPublicIPv4(ctx)
	if err != nil {
		return "", err
	}
	i.warnIfPortInUse(ctx, "tcp", "-ltnpH")
	i.warnIfPortInUse(ctx, "udp", "-lunpH")

	return publicIP, nil
}

func (i *Installer) installXray(ctx context.Context) error {
	scriptPath, err := downloadInstallScript(ctx)
	if err != nil {
		return err
	}
	defer os.Remove(scriptPath)

	cmd := exec.CommandContext(ctx, "bash", scriptPath, "install", "--manually-restart-later")
	cmd.Stdout = i.stdout
	cmd.Stderr = i.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run xray install script: %w", err)
	}

	if _, err := os.Stat(xrayBinary); err != nil {
		return fmt.Errorf("xray binary not found after install: %w", err)
	}
	if _, err := os.Stat("/etc/systemd/system/xray.service"); err != nil {
		return fmt.Errorf("xray systemd service not found after install: %w", err)
	}
	return nil
}

func (i *Installer) removeXray(ctx context.Context) error {
	scriptPath, err := downloadInstallScript(ctx)
	if err != nil {
		return err
	}
	defer os.Remove(scriptPath)

	cmd := exec.CommandContext(ctx, "bash", scriptPath, "remove", "--purge")
	cmd.Stdout = i.stdout
	cmd.Stderr = i.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run xray remove script: %w", err)
	}

	return nil
}

func (i *Installer) validateDryRun(ctx context.Context, xrayConfig []byte) error {
	if !pathExists(xrayBinary) {
		fmt.Fprintf(i.stdout, "- dry-run: 本机尚未安装 xray，跳过 `xray run -test -config` 校验\n")
		return nil
	}

	if err := i.validateXrayConfig(ctx, xrayConfig); err != nil {
		return err
	}

	fmt.Fprintf(i.stdout, "- dry-run: 已用当前 xray 二进制校验生成的服务端配置\n")
	return nil
}

func (i *Installer) validateXrayConfig(ctx context.Context, configBytes []byte) error {
	tmpFile, err := os.CreateTemp("", "xray-config-*.json")
	if err != nil {
		return fmt.Errorf("create temp xray config: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	defer tmpFile.Close()

	if _, err := tmpFile.Write(configBytes); err != nil {
		return fmt.Errorf("write temp xray config: %w", err)
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temp xray config: %w", err)
	}

	output, err := commandOutput(ctx, xrayBinary, "run", "-test", "-config", tmpPath)
	if err != nil {
		return fmt.Errorf("xray config validation failed: %w\n%s", err, strings.TrimSpace(output))
	}
	return nil
}

func (i *Installer) enableAndRestartXray(ctx context.Context) error {
	for _, args := range [][]string{{"daemon-reload"}, {"restart", "xray"}} {
		if output, err := commandOutput(ctx, "systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s failed: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(output))
		}
	}

	if err := waitForXrayListeners(ctx); err != nil {
		logs, _ := commandOutput(ctx, "journalctl", "-u", "xray", "-n", "50", "--no-pager")
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(logs))
	}
	if status, err := commandOutput(ctx, "systemctl", "is-active", "xray"); err != nil || strings.TrimSpace(status) != "active" {
		logs, _ := commandOutput(ctx, "journalctl", "-u", "xray", "-n", "50", "--no-pager")
		return fmt.Errorf("xray service status is %q: %v\n%s", strings.TrimSpace(status), err, strings.TrimSpace(logs))
	}
	if output, err := commandOutput(ctx, "systemctl", "enable", "xray"); err != nil {
		return fmt.Errorf("systemctl enable xray failed: %w\n%s", err, strings.TrimSpace(output))
	}
	return nil
}

func waitForXrayListeners(ctx context.Context) error {
	if _, err := exec.LookPath("ss"); err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	portFilter := fmt.Sprintf("( sport = :%d )", config.DefaultPort)

	for {
		pid, err := commandOutput(ctx, "systemctl", "show", "--property=MainPID", "--value", "xray")
		if err != nil {
			return fmt.Errorf("read xray PID: %w", err)
		}
		listeners, err := commandOutput(ctx, "ss", "-ltnupH", portFilter)
		if err != nil {
			return fmt.Errorf("inspect xray listeners: %w", err)
		}
		owner := "pid=" + strings.TrimSpace(pid) + ","
		var tcp, udp bool
		for line := range strings.SplitSeq(listeners, "\n") {
			if strings.Contains(line, owner) {
				tcp = tcp || strings.HasPrefix(line, "tcp ")
				udp = udp || strings.HasPrefix(line, "udp ")
			}
		}
		if tcp && udp {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("xray did not listen on both TCP and UDP %d: %w", config.DefaultPort, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (i *Installer) step(message string) {
	fmt.Fprintf(i.stdout, "\n==> %s\n", message)
}

func (i *Installer) printSummary(result *Result) {
	fmt.Fprintln(i.stdout, "\n安装完成。")
	fmt.Fprintf(i.stdout, "- domain: %s\n", result.Domain)
	fmt.Fprintf(i.stdout, "- dest host: %s\n", result.DestHost)
	fmt.Fprintf(i.stdout, "- node name: %s\n", result.NodeName)
	fmt.Fprintf(i.stdout, "- public ip: %s\n", result.PublicIP)
	fmt.Fprintf(i.stdout, "- VLESS / REALITY: TCP %d\n", result.Port)
	fmt.Fprintf(i.stdout, "- Hysteria 2: UDP %d (%s)\n", result.HysteriaPort, result.HysteriaNode)
	fmt.Fprintf(i.stdout, "- uuid: %s\n", result.UUID)
	fmt.Fprintf(i.stdout, "- public key: %s\n", result.PublicKey)
	fmt.Fprintf(i.stdout, "- short id: %s\n", result.ShortID)
	fmt.Fprintf(i.stdout, "- xray config: %s\n", result.XrayConfigPath)
	fmt.Fprintf(i.stdout, "- flclash config: %s\n", result.ProxyConfigPath)
	fmt.Fprintf(i.stdout, "- install record: %s\n", metadataPath)
	fmt.Fprintf(i.stdout, "\n获取 FlClash 配置命令：\n  sudo cat %s\n", result.ProxyConfigPath)
}

func (i *Installer) printDryRunSummary(result *Result) {
	fmt.Fprintln(i.stdout, "\nDry run 完成，未对系统做任何修改。")
	fmt.Fprintf(i.stdout, "- domain: %s\n", result.Domain)
	fmt.Fprintf(i.stdout, "- dest host: %s\n", result.DestHost)
	fmt.Fprintf(i.stdout, "- node name: %s\n", result.NodeName)
	fmt.Fprintf(i.stdout, "- public ip: %s\n", result.PublicIP)
	fmt.Fprintf(i.stdout, "- VLESS / REALITY: TCP %d\n", result.Port)
	fmt.Fprintf(i.stdout, "- Hysteria 2: UDP %d (%s)\n", result.HysteriaPort, result.HysteriaNode)
	fmt.Fprintf(i.stdout, "- would write xray config to: %s\n", result.XrayConfigPath)
	fmt.Fprintf(i.stdout, "- would write flclash config to: %s\n", result.ProxyConfigPath)
	fmt.Fprintf(i.stdout, "- would save install record to: %s\n", metadataPath)
	fmt.Fprintf(i.stdout, "\n实际安装完成后可用以下命令获取 FlClash 配置：\n  sudo cat %s\n", result.ProxyConfigPath)
}

func normalizeDomain(input string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(strings.Trim(input, ".")))
	if domain == "" {
		return "", errors.New("domain is required")
	}
	if !domainRE.MatchString(domain) {
		return "", fmt.Errorf("invalid domain %q", input)
	}
	return domain, nil
}

func normalizeDestHost(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return config.DefaultDestHost, nil
	}
	return normalizeDomain(input)
}

func supportsDistro(id, like string) bool {
	id = strings.ToLower(id)
	like = strings.ToLower(like)
	if id == "ubuntu" || id == "debian" || id == "arch" || id == "archlinux" {
		return true
	}
	for _, part := range strings.FieldsFunc(like, func(r rune) bool { return r == ' ' || r == ',' }) {
		if part == "ubuntu" || part == "debian" || part == "arch" || part == "archlinux" {
			return true
		}
	}
	return false
}

func readOSRelease() (string, string, error) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", "", fmt.Errorf("read /etc/os-release: %w", err)
	}

	var id, like string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		key := parts[0]
		val := strings.Trim(parts[1], `"`)
		switch key {
		case "ID":
			id = val
		case "ID_LIKE":
			like = val
		}
	}
	if id == "" {
		return "", "", errors.New("unable to determine distro from /etc/os-release")
	}

	return id, like, nil
}

func detectPublicIPv4(ctx context.Context) (string, error) {
	services := []string{
		"https://api.ipify.org",
		"https://ipv4.icanhazip.com",
		"https://ifconfig.me/ip",
	}
	client := &http.Client{Timeout: 8 * time.Second}

	for _, url := range services {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}

		ip := strings.TrimSpace(string(body))
		parsed := net.ParseIP(ip)
		if parsed != nil && parsed.To4() != nil {
			return parsed.String(), nil
		}
	}

	return "", errors.New("unable to detect public IPv4 from external services")
}

func (i *Installer) warnIfPortInUse(ctx context.Context, network, ssFlags string) {
	port := config.DefaultPort
	output, err := commandOutput(ctx, "ss", ssFlags, fmt.Sprintf("( sport = :%d )", port))
	if err != nil {
		fmt.Fprintf(i.stdout, "- 警告：无法检查 %s/%d 是否可用：%v\n", network, port, err)
		return
	}
	listeners := strings.TrimSpace(output)
	if listeners == "" {
		return
	}
	for _, listener := range strings.Split(listeners, "\n") {
		if !strings.Contains(listener, `(("xray",`) {
			fmt.Fprintf(i.stdout, "- 警告：%s/%d 存在其他监听程序，可能导致 Xray 启动失败：\n%s\n", network, port, listeners)
			return
		}
	}
	fmt.Fprintf(i.stdout, "- 提示：%s/%d 已由现有 Xray 监听，将继续覆盖安装。\n", network, port)
}

func lockInstallation() (*os.File, error) {
	lock, err := os.OpenFile(installLockPath, os.O_CREATE|os.O_RDWR, privateMode)
	if err != nil {
		return nil, fmt.Errorf("open installer lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("an Xray install or uninstall is already running")
		}
		return nil, fmt.Errorf("lock installer: %w", err)
	}
	return lock, nil
}

func xrayConfigGroup(ctx context.Context) (int, error) {
	output, err := commandOutput(ctx, "systemctl", "show", "--property=User,Group", "xray.service")
	if err != nil {
		return 0, fmt.Errorf("read xray service credentials: %w\n%s", err, strings.TrimSpace(output))
	}

	var serviceUser, serviceGroup string
	for line := range strings.SplitSeq(output, "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "User":
			serviceUser = value
		case "Group":
			serviceGroup = value
		}
	}
	if serviceGroup != "" {
		if gid, err := strconv.Atoi(serviceGroup); err == nil && gid >= 0 {
			return gid, nil
		}
		group, err := user.LookupGroup(serviceGroup)
		if err != nil {
			return 0, fmt.Errorf("look up xray service group: %w", err)
		}
		return strconv.Atoi(group.Gid)
	}
	if serviceUser == "" || serviceUser == "root" || serviceUser == "0" {
		return 0, nil
	}

	lookup := user.Lookup
	if _, err := strconv.Atoi(serviceUser); err == nil {
		lookup = user.LookupId
	}
	account, err := lookup(serviceUser)
	if err != nil {
		return 0, fmt.Errorf("look up xray service user: %w", err)
	}
	return strconv.Atoi(account.Gid)
}

func downloadInstallScript(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, installScriptURL, nil)
	if err != nil {
		return "", fmt.Errorf("build install script request: %w", err)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download xray install script: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download xray install script: unexpected status %s", resp.Status)
	}

	f, err := os.CreateTemp("", "xray-install-*.sh")
	if err != nil {
		return "", fmt.Errorf("create temp install script: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("save xray install script: %w", err)
	}
	if err := f.Chmod(0o700); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("chmod xray install script: %w", err)
	}
	return f.Name(), nil
}

func newUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

func newX25519KeyPair() (string, string, error) {
	curve := ecdh.X25519()
	privateKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	publicKey := privateKey.PublicKey()
	return base64.RawURLEncoding.EncodeToString(privateKey.Bytes()), base64.RawURLEncoding.EncodeToString(publicKey.Bytes()), nil
}

func newShortID(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func removePathIfExists(path string) error {
	if !pathExists(path) {
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func removeDirIfEmpty(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read dir %s: %w", path, err)
	}
	if len(entries) != 0 {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove empty dir %s: %w", path, err)
	}
	return nil
}
