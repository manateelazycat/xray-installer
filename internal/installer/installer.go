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
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"xray-installer/internal/config"
)

const (
	DefaultNodeName = "[自建 1] 美国家宽-Reality"

	xrayBinary       = "/usr/local/bin/xray"
	xrayConfigDir    = "/usr/local/etc/xray"
	xrayConfigPath   = "/usr/local/etc/xray/config.json"
	outputDir        = "/usr/local/etc/xray-installer"
	proxyConfigPath  = "/usr/local/etc/xray-installer/proxy.yaml"
	metadataPath     = "/usr/local/etc/xray-installer/install-result.json"
	installScriptURL = "https://raw.githubusercontent.com/XTLS/Xray-install/main/install-release.sh"
)

var domainRE = regexp.MustCompile(`^(?i)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

type Installer struct {
	stdout io.Writer
	stderr io.Writer
}

type InstallRequest struct {
	Domain   string
	NodeName string
	DryRun   bool
}

type Result struct {
	Domain          string    `json:"domain"`
	NodeName        string    `json:"node_name"`
	PublicIP        string    `json:"public_ip"`
	Port            int       `json:"port"`
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

	nodeName, err := normalizeNodeName(req.NodeName)
	if err != nil {
		return nil, err
	}

	i.step("运行安装前检查")
	publicIP, err := i.preflight(ctx, !req.DryRun)
	if err != nil {
		return nil, err
	}

	if req.DryRun {
		i.step("Dry run：跳过 Xray 安装")
	} else {
		i.step("安装 Xray")
		if err := i.installXray(ctx); err != nil {
			return nil, err
		}
	}

	i.step("生成 Reality / VLESS 配置")
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

	params := config.Parameters{
		Domain:     domain,
		NodeName:   nodeName,
		UUID:       uuid,
		PrivateKey: privateKey,
		PublicKey:  publicKey,
		ShortID:    shortID,
		DestHost:   config.DefaultDestHost,
		Port:       config.DefaultPort,
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
		UUID:            uuid,
		PublicKey:       publicKey,
		ShortID:         shortID,
		DestHost:        config.DefaultDestHost,
		XrayConfigPath:  xrayConfigPath,
		ProxyConfigPath: proxyConfigPath,
		DryRun:          req.DryRun,
		InstalledAt:     time.Now().UTC(),
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

	if _, err := backupIfExists(xrayConfigPath); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(xrayConfigPath, xrayConfig, 0o600); err != nil {
		return nil, fmt.Errorf("write xray config: %w", err)
	}

	if _, err := backupIfExists(proxyConfigPath); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(proxyConfigPath, flClashConfig, 0o600); err != nil {
		return nil, fmt.Errorf("write flclash config: %w", err)
	}

	i.step("启动并验证 Xray 服务")
	if err := i.enableAndRestartXray(ctx); err != nil {
		return nil, err
	}

	if _, err := backupIfExists(metadataPath); err != nil {
		return nil, err
	}
	meta, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal install result: %w", err)
	}
	meta = append(meta, '\n')
	if err := writeFileAtomic(metadataPath, meta, 0o600); err != nil {
		return nil, fmt.Errorf("write install result: %w", err)
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
	if err := i.ensurePort443Available(ctx); err != nil {
		return "", err
	}

	return publicIP, nil
}

func (i *Installer) installXray(ctx context.Context) error {
	scriptPath, err := downloadInstallScript(ctx)
	if err != nil {
		return err
	}
	defer os.Remove(scriptPath)

	cmd := exec.CommandContext(ctx, "bash", scriptPath, "install")
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
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "xray"}, {"restart", "xray"}} {
		if output, err := commandOutput(ctx, "systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s failed: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(output))
		}
	}

	status, err := commandOutput(ctx, "systemctl", "is-active", "xray")
	if err != nil {
		logs, _ := commandOutput(ctx, "journalctl", "-u", "xray", "-n", "50", "--no-pager")
		return fmt.Errorf("xray service is not active: %w\n%s\n%s", err, strings.TrimSpace(status), strings.TrimSpace(logs))
	}
	if strings.TrimSpace(status) != "active" {
		logs, _ := commandOutput(ctx, "journalctl", "-u", "xray", "-n", "50", "--no-pager")
		return fmt.Errorf("xray service status is %q\n%s", strings.TrimSpace(status), strings.TrimSpace(logs))
	}
	return nil
}

func (i *Installer) step(message string) {
	fmt.Fprintf(i.stdout, "\n==> %s\n", message)
}

func (i *Installer) printSummary(result *Result) {
	fmt.Fprintln(i.stdout, "\n安装完成。")
	fmt.Fprintf(i.stdout, "- domain: %s\n", result.Domain)
	fmt.Fprintf(i.stdout, "- node name: %s\n", result.NodeName)
	fmt.Fprintf(i.stdout, "- public ip: %s\n", result.PublicIP)
	fmt.Fprintf(i.stdout, "- port: %d\n", result.Port)
	fmt.Fprintf(i.stdout, "- uuid: %s\n", result.UUID)
	fmt.Fprintf(i.stdout, "- public key: %s\n", result.PublicKey)
	fmt.Fprintf(i.stdout, "- short id: %s\n", result.ShortID)
	fmt.Fprintf(i.stdout, "- xray config: %s\n", result.XrayConfigPath)
	fmt.Fprintf(i.stdout, "- flclash config: %s\n", result.ProxyConfigPath)
	fmt.Fprintf(i.stdout, "- install record: %s\n", metadataPath)
}

func (i *Installer) printDryRunSummary(result *Result) {
	fmt.Fprintln(i.stdout, "\nDry run 完成，未对系统做任何修改。")
	fmt.Fprintf(i.stdout, "- domain: %s\n", result.Domain)
	fmt.Fprintf(i.stdout, "- node name: %s\n", result.NodeName)
	fmt.Fprintf(i.stdout, "- public ip: %s\n", result.PublicIP)
	fmt.Fprintf(i.stdout, "- would write xray config to: %s\n", result.XrayConfigPath)
	fmt.Fprintf(i.stdout, "- would write flclash config to: %s\n", result.ProxyConfigPath)
	fmt.Fprintf(i.stdout, "- would save install record to: %s\n", metadataPath)
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

func normalizeNodeName(input string) (string, error) {
	name := strings.TrimSpace(input)
	if name == "" {
		return DefaultNodeName, nil
	}
	if strings.ContainsAny(name, "\r\n") {
		return "", errors.New("node name cannot contain newlines")
	}
	return name, nil
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

func (i *Installer) ensurePort443Available(ctx context.Context) error {
	if output, err := commandOutput(ctx, "bash", "-lc", `ss -ltnpH '( sport = :443 )' 2>/dev/null || true`); err == nil {
		trimmed := strings.TrimSpace(output)
		if trimmed == "" {
			return nil
		}

		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "xray") {
			fmt.Fprintln(i.stdout, "- 检测到 tcp/443 已由现有 xray 监听，继续执行覆盖安装。")
			return nil
		}

		return fmt.Errorf("tcp/443 已被其他程序占用，请先释放该端口后再安装：\n%s", trimmed)
	}

	ln, err := net.Listen("tcp4", ":443")
	if err == nil {
		ln.Close()
		return nil
	}

	return fmt.Errorf("tcp/443 is unavailable: %w", err)
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

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func backupIfExists(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read existing file %s: %w", path, err)
	}

	backupPath := fmt.Sprintf("%s.bak.%s", path, time.Now().UTC().Format("20060102T150405Z"))
	mode := os.FileMode(0o600)
	if stat, err := os.Stat(path); err == nil {
		mode = stat.Mode().Perm()
	}
	if err := os.WriteFile(backupPath, data, mode); err != nil {
		return "", fmt.Errorf("write backup file %s: %w", backupPath, err)
	}
	return backupPath, nil
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
