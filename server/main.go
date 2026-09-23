// 虎符 M0 —— 自建群聊消息服务
//
// 一条命令起服务：
//
//	tigertally serve
//
// 设计文档：docs/DESIGN-M0.md
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		if err := cmdServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "错误: "+err.Error())
			os.Exit(1)
		}
	case "version", "-v", "--version":
		fmt.Println("tigertally " + version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `虎符 tigertally —— 自托管群聊消息服务

用法:
  tigertally serve [选项]     启动服务
  tigertally version          打印版本
  tigertally help             显示本帮助

选项（也可用环境变量，命令行优先）:
  --addr string             监听地址             (TIGERTALLY_ADDR，默认 127.0.0.1:8787)
  --db string               数据库文件           (TIGERTALLY_DB，默认 ./tigertally.db)
  --bootstrap-token string  建群所需的口令，留空则允许任何人建群
                                                  (TIGERTALLY_BOOTSTRAP_TOKEN)
  --allow-origin string     允许跨域的来源，可重复 (TIGERTALLY_ALLOW_ORIGINS，逗号分隔)
                            默认只允许同源与 file:// 页面；填 * 表示不检查

例:
  tigertally serve --addr 0.0.0.0:8787 --db /var/lib/tigertally/data.db
`)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", envOr("TIGERTALLY_ADDR", "127.0.0.1:8787"), "监听地址")
	dbPath := fs.String("db", envOr("TIGERTALLY_DB", "./tigertally.db"), "数据库文件路径")
	bootstrap := fs.String("bootstrap-token", os.Getenv("TIGERTALLY_BOOTSTRAP_TOKEN"), "建群口令，留空表示允许任何人建群")
	var origins stringSliceFlag
	fs.Var(&origins, "allow-origin", "允许跨域的来源，可重复；* 表示不检查")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if v := os.Getenv("TIGERTALLY_ALLOW_ORIGINS"); v != "" && len(origins) == 0 {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
	}

	cfg := Config{
		Addr:           *addr,
		DBPath:         *dbPath,
		BootstrapToken: *bootstrap,
		AllowOrigins:   origins,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, cfg, os.Stdout)
}

type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
