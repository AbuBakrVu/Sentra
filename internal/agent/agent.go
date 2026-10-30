package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dozor/internal/proto"
)

// Config хранится в /etc/dozor/agent.json (права 0600).
type Config struct {
	Server   string `json:"server"` // https://192.168.1.10
	Pin      string `json:"pin"`    // sha256 SPKI сертификата сервера, base64
	HostID   string `json:"host_id"`
	Key      string `json:"key"`
	Interval int    `json:"interval"` // секунды
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Interval <= 0 {
		c.Interval = 30
	}
	return &c, nil
}

func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

// SPKIPin считает отпечаток публичного ключа в формате curl --pinnedpubkey.
func SPKIPin(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(h[:])
}

// NewClient возвращает HTTP-клиент, который доверяет только серверу с заданным ключом.
// Сертификат самоподписанный, поэтому цепочку не проверяем, а сверяем отпечаток.
func NewClient(pin string) *http.Client {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, // проверка ниже, по отпечатку ключа
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("сервер не прислал сертификат")
			}
			cert, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			if got := SPKIPin(cert); got != pin {
				return fmt.Errorf("отпечаток сертификата сервера не совпадает: %s", got)
			}
			return nil
		},
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	tr.Proxy = nil // агент ходит на сервер в локальной сети напрямую
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}

// Enroll регистрирует агент на сервере по токену подключения.
func Enroll(server, token, pin, version, cfgPath string) error {
	server = strings.TrimRight(server, "/")
	cl := NewClient(pin)
	body, _ := json.Marshal(proto.RegisterRequest{Token: token, Inventory: Inventory(version)})
	resp, err := cl.Post(server+"/api/agent/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("сервер отказал в регистрации: %s %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var rr proto.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return err
	}
	cfg := &Config{Server: server, Pin: pin, HostID: rr.HostID, Key: rr.Key, Interval: 30}
	return cfg.Save(cfgPath)
}

// Run — основной цикл: собрать метрики, отправить, подождать.
func Run(ctx context.Context, cfg *Config, version string) error {
	cl := NewClient(cfg.Pin)
	col := NewCollector()
	interval := time.Duration(cfg.Interval) * time.Second
	log.Printf("dozor-agent %s: сервер %s, интервал %s", version, cfg.Server, interval)

	// первый сбор через 5 секунд, чтобы скорости CPU и сети посчитались
	timer := time.NewTimer(5 * time.Second)
	cycle := 0
	var lastErr string
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		batch := proto.MetricsBatch{Timestamp: time.Now().UnixMilli(), Samples: col.Collect()}
		if cycle%20 == 0 { // опись раз в ~10 минут
			inv := Inventory(version)
			batch.Inventory = &inv
		}
		err := push(ctx, cl, cfg, &batch)
		if err != nil {
			if err.Error() != lastErr {
				log.Printf("ошибка отправки: %v", err)
			}
			lastErr = err.Error()
		} else {
			if lastErr != "" {
				log.Printf("связь с сервером восстановлена")
			}
			lastErr = ""
		}
		cycle++
		timer.Reset(interval)
	}
}

func push(ctx context.Context, cl *http.Client, cfg *Config, b *proto.MetricsBatch) error {
	body, _ := json.Marshal(b)
	req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Server+"/api/agent/metrics", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.HostID+":"+cfg.Key)
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		return fmt.Errorf("сервер ответил %s", resp.Status)
	}
	return nil
}
