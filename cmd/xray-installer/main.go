package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"xray-installer/internal/config"
	"xray-installer/internal/installer"
)

const (
	commandInstall   = "install"
	commandUninstall = "uninstall"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

type cliOptions struct {
	command string
	dryRun  bool
	help    bool
	version bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, err := parseCLI(args)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n\n", err)
		printUsage(stderr)
		return 2
	}

	if options.help {
		printUsage(stdout)
		return 0
	}
	if options.version {
		printVersion(stdout)
		return 0
	}

	inst := installer.New(stdout, stderr)

	switch options.command {
	case commandInstall:
		req, err := promptInstallRequest(stdin, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		req.DryRun = options.dryRun

		if _, err := inst.Run(context.Background(), req); err != nil {
			fmt.Fprintf(stderr, "\nError: %v\n", err)
			return 1
		}
	case commandUninstall:
		if err := inst.Uninstall(context.Background(), options.dryRun); err != nil {
			fmt.Fprintf(stderr, "\nError: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "Error: unsupported command %q\n", options.command)
		return 2
	}

	return 0
}

func parseCLI(args []string) (cliOptions, error) {
	options := cliOptions{command: commandInstall}

	if len(args) > 0 && args[0] == commandUninstall {
		options.command = commandUninstall
		args = args[1:]
	}

	fs := flag.NewFlagSet("xray-installer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&options.dryRun, "dry-run", false, "preview actions without making changes")
	fs.BoolVar(&options.help, "help", false, "show help")
	fs.BoolVar(&options.help, "h", false, "show help")
	fs.BoolVar(&options.version, "version", false, "show version")

	if err := fs.Parse(args); err != nil {
		return options, err
	}

	if fs.NArg() > 0 {
		return options, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	return options, nil
}

func promptInstallRequest(stdin io.Reader, stdout io.Writer) (installer.InstallRequest, error) {
	reader := bufio.NewReader(stdin)

	domain, err := promptRequired(reader, stdout, "请输入 VPS 绑定的域名")
	if err != nil {
		return installer.InstallRequest{}, err
	}

	destHost, err := promptDefault(reader, stdout, "请输入伪装目标域名", config.DefaultDestHost)
	if err != nil {
		return installer.InstallRequest{}, err
	}

	return installer.InstallRequest{
		Domain:   domain,
		DestHost: destHost,
	}, nil
}

func promptRequired(reader *bufio.Reader, stdout io.Writer, label string) (string, error) {
	for {
		fmt.Fprintf(stdout, "%s: ", label)
		value, err := readLine(reader)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
		fmt.Fprintln(stdout, "输入不能为空，请重试。")
	}
}

func promptDefault(reader *bufio.Reader, stdout io.Writer, label, defaultValue string) (string, error) {
	fmt.Fprintf(stdout, "%s（直接回车默认 %s）: ", label, defaultValue)
	value, err := readLine(reader)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}

	if errors.Is(err, io.EOF) && len(line) == 0 {
		return "", io.EOF
	}

	return strings.TrimSpace(line), nil
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `xray-installer - 在 VPS 部署 Xray REALITY 和 Hysteria 2，并生成 FlClash / Clash Meta 配置

用法:
  xray-installer [--dry-run]
  xray-installer uninstall [--dry-run]
  xray-installer --version
  xray-installer --help

说明:
  1. 启动安装后询问 VPS 绑定域名和 Reality 伪装目标域名
  2. 生成 REALITY 主用和 Hysteria 2 备用节点
  3. 自动部署 TCP 443 和 UDP 443，客户端默认使用「自动切换」策略组

命令:
  uninstall    卸载 Xray，并删除 xray-installer 生成的文件

选项:
  --dry-run    只预览，不实际安装 / 卸载 / 写文件 / 重启服务
  --version    输出版本信息
  -h, --help   输出帮助

节点名称:
  %s
  %s

示例:
  xray-installer
  xray-installer --dry-run
  xray-installer --version
  sudo xray-installer uninstall
`, installer.DefaultNodeName, installer.HysteriaNodeName)
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "xray-installer %s\ncommit: %s\nbuild date: %s\n", version, commit, buildDate)
}
