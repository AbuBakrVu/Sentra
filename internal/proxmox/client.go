// Package proxmox опрашивает Proxmox VE через его API: узлы, виртуальные
// машины (QEMU), контейнеры (LXC) и хранилища. Нужен API-токен с ролью
// PVEAuditor (только чтение).
package proxmox

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Num принимает и число, и строку: часть полей API Proxmox приходит строками.
type Num float64

func (n *Num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil // неизвестный формат не должен ронять весь опрос
	}
	*n = Num(f)
	return nil
}

// Resource — элемент /cluster/resources.
type Resource struct {
	ID       string `json:"id"`   // qemu/100, lxc/101, node/pve1, storage/pve1/local
	Type     string `json:"type"` // qemu, lxc, node, storage
	Node     string `json:"node"`
	VMID     Num    `json:"vmid"`
	Name     string `json:"name"`
	Status   string `json:"status"` // running, stopped, online, offline, available
	Template Num    `json:"template"`
	CPU      Num    `json:"cpu"` // доля от maxcpu, 0..1
	MaxCPU   Num    `json:"maxcpu"`
	Mem      Num    `json:"mem"`
	MaxMem   Num    `json:"maxmem"`
	Disk     Num    `json:"disk"`
	MaxDisk  Num    `json:"maxdisk"`
	NetIn    Num    `json:"netin"` // байты с запуска
	NetOut   Num    `json:"netout"`
	Uptime   Num    `json:"uptime"`
	Storage  string `json:"storage"`
	PlugType string `json:"plugintype"`
	Shared   Num    `json:"shared"`
	Tags     string `json:"tags"`
}

// NodeStatus — /nodes/{node}/status.
type NodeStatus struct {
	LoadAvg []Num `json:"loadavg"`
	Memory  struct {
		Total Num `json:"total"`
		Used  Num `json:"used"`
	} `json:"memory"`
	Swap struct {
		Total Num `json:"total"`
		Used  Num `json:"used"`
	} `json:"swap"`
	RootFS struct {
		Total Num `json:"total"`
		Used  Num `json:"used"`
		Avail Num `json:"avail"`
	} `json:"rootfs"`
	PVEVersion string `json:"pveversion"`
	KVersion   string `json:"kversion"`
	CPUInfo    struct {
		Model string `json:"model"`
		CPUs  Num    `json:"cpus"`
	} `json:"cpuinfo"`
	Uptime Num `json:"uptime"`
}

type Client struct {
	base string // https://host:8006
	auth string
	http *http.Client
	pin  string // sha256 сертификата, AA:BB:..., как показывает Proxmox
}

// NormalizeURL приводит адрес к виду https://host:8006.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("укажите адрес Proxmox")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("адрес Proxmox указан неверно")
	}
	if u.Scheme != "https" {
		return "", errors.New("Proxmox работает только по https")
	}
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(strings.Trim(host, "[]"), "8006")
	}
	return "https://" + host, nil
}

// Fingerprint — отпечаток в формате, который показывает Proxmox (Узел › Сертификаты).
func Fingerprint(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.Raw)
	parts := make([]string, len(h))
	for i, b := range h {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// New создаёт клиент. Если pin пустой, сертификат не проверяется: так делаем
// только при первом подключении, чтобы узнать и запомнить отпечаток.
func New(base, tokenID, secret, pin string) *Client {
	c := &Client{base: strings.TrimRight(base, "/"), pin: strings.ToUpper(strings.TrimSpace(pin)),
		auth: "PVEAPIToken=" + strings.TrimSpace(tokenID) + "=" + strings.TrimSpace(secret)}
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, // проверяем сами: по отпечатку
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if c.pin == "" {
				return nil
			}
			if len(raw) == 0 {
				return errors.New("Proxmox не прислал сертификат")
			}
			cert, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			if Fingerprint(cert) != c.pin {
				return ErrFingerprint
			}
			return nil
		},
	}
	tr := &http.Transport{
		TLSClientConfig:     tlsCfg,
		Proxy:               nil, // Proxmox в локальной сети, прокси не нужен
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 4,
	}
	c.http = &http.Client{Transport: tr, Timeout: 15 * time.Second}
	return c
}

var ErrFingerprint = errors.New("сертификат Proxmox сменился: отпечаток не совпадает с сохранённым")

// FetchFingerprint подключается и возвращает отпечаток сертификата сервера.
func FetchFingerprint(ctx context.Context, base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true}}
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return "", friendlyNetErr(err)
	}
	defer conn.Close()
	st := conn.(*tls.Conn).ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return "", errors.New("Proxmox не прислал сертификат")
	}
	return Fingerprint(st.PeerCertificates[0]), nil
}

func friendlyNetErr(err error) error {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return errors.New("Proxmox не отвечает: проверьте адрес и что порт 8006 доступен с коробки")
	case strings.Contains(err.Error(), "connection refused"):
		return errors.New("соединение отклонено: на этом адресе не работает веб-интерфейс Proxmox (порт 8006)")
	case strings.Contains(err.Error(), "no such host"):
		return errors.New("имя Proxmox не найдено в DNS, укажите IP-адрес")
	}
	return err
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api2/json"+path, nil)
	req.Header.Set("Authorization", c.auth)
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, ErrFingerprint) || strings.Contains(err.Error(), ErrFingerprint.Error()) {
			return ErrFingerprint
		}
		return friendlyNetErr(err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
	case 401:
		return errors.New("Proxmox не принял токен: проверьте ID токена и секрет")
	case 403:
		return errors.New("у токена нет прав: выдайте пользователю роль PVEAuditor на «/»")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("Proxmox ответил %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&env); err != nil {
		return fmt.Errorf("непонятный ответ Proxmox: %w", err)
	}
	return json.Unmarshal(env.Data, out)
}

func (c *Client) Resources(ctx context.Context) ([]Resource, error) {
	var r []Resource
	err := c.get(ctx, "/cluster/resources", &r)
	return r, err
}

func (c *Client) NodeStatus(ctx context.Context, node string) (*NodeStatus, error) {
	var s NodeStatus
	err := c.get(ctx, "/nodes/"+url.PathEscape(node)+"/status", &s)
	return &s, err
}

// Version — /version, удобно как проверка токена.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
		Release string `json:"release"`
	}
	err := c.get(ctx, "/version", &v)
	return v.Version, err
}
