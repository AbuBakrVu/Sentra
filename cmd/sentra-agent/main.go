package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sentra/internal/agent"
)

var version = "0.1.0-dev"

const defaultCfg = "/etc/sentra/agent.json"

func usage() {
	fmt.Fprintf(os.Stderr, `sentra-agent %s

Команды:
  enroll --server URL --token TOKEN --pin PIN   зарегистрировать агент на сервере
  run                                           запустить сбор и отправку метрик
  collect                                       один раз собрать и вывести метрики (отладка)
  version

Общий флаг: --config (по умолчанию %s)
`, version, defaultCfg)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", defaultCfg, "файл конфигурации")
	server := fs.String("server", "", "адрес сервера, например https://192.168.1.10")
	token := fs.String("token", "", "токен подключения")
	pin := fs.String("pin", "", "отпечаток ключа сервера (sha256, base64)")
	fs.Parse(os.Args[2:])

	switch cmd {
	case "enroll":
		if *server == "" || *token == "" || *pin == "" {
			log.Fatal("нужны --server, --token и --pin")
		}
		if err := agent.Enroll(*server, *token, *pin, version, *cfgPath); err != nil {
			log.Fatalf("регистрация не удалась: %v", err)
		}
		fmt.Println("Агент зарегистрирован, конфигурация:", *cfgPath)
	case "run":
		cfg, err := agent.LoadConfig(*cfgPath)
		if err != nil {
			log.Fatalf("нет конфигурации (%v). Сначала выполните enroll.", err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := agent.Run(ctx, cfg, version); err != nil {
			log.Fatal(err)
		}
	case "collect":
		c := agent.NewCollector()
		ctxSleep()
		for _, s := range c.Collect() {
			fmt.Printf("%-24s %v %v\n", s.Name, s.Labels, s.Value)
		}
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
}

func ctxSleep() { time.Sleep(2 * time.Second) }
