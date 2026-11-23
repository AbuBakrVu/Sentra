package proxmox

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrBackupFingerprintRequired = errors.New("укажите SHA-256 отпечаток из PBS")

type BackupClient struct {
	base string
	auth string
	http *http.Client
}

type BackupGroup struct {
	Name   string `json:"name"`
	Latest int64  `json:"latest"`
	Copies int    `json:"copies"`
}

type BackupDatastore struct {
	Total  uint64        `json:"total"`
	Used   uint64        `json:"used"`
	Avail  uint64        `json:"avail"`
	Groups []BackupGroup `json:"groups"`
}

func NormalizeBackupURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		if ip := net.ParseIP(raw); ip != nil && strings.Contains(raw, ":") {
			raw = "[" + raw + "]"
		}
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("укажите HTTPS-адрес PBS без пути, например https://pbs.example.com:8007")
	}
	port := u.Port()
	if port == "" {
		port = "8007"
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return "", errors.New("неверный порт PBS")
	}
	return "https://" + net.JoinHostPort(u.Hostname(), port), nil
}

func NewBackup(base, token, secret, fingerprint string) (*BackupClient, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if fingerprint != "" {
		pin, err := hex.DecodeString(strings.ReplaceAll(fingerprint, ":", ""))
		if err != nil || len(pin) != sha256.Size {
			return nil, errors.New("укажите SHA-256 отпечаток сертификата PBS")
		}
		var expected [sha256.Size]byte
		copy(expected[:], pin)
		tlsConfig.InsecureSkipVerify = true // вместо системного CA проверяем точный отпечаток
		tlsConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || sha256.Sum256(state.PeerCertificates[0].Raw) != expected {
				return errors.New("отпечаток сертификата PBS не совпадает")
			}
			cert := state.PeerCertificates[0]
			if now := time.Now(); now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return errors.New("сертификат PBS ещё не действует или уже истёк")
			}
			return nil
		}
	}
	return &BackupClient{
		base: base, auth: "PBSAPIToken " + token + ":" + url.PathEscape(secret),
		http: &http.Client{
			Transport:     &http.Transport{TLSClientConfig: tlsConfig, TLSHandshakeTimeout: 5 * time.Second},
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *BackupClient) Close() { c.http.CloseIdleConnections() }

func (c *BackupClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api2/json"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.auth)
	res, err := c.http.Do(req)
	if err != nil {
		var hostname x509.HostnameError
		if errors.As(err, &hostname) {
			return fmt.Errorf("адрес PBS не указан в сертификате, %w", ErrBackupFingerprintRequired)
		}
		var authority x509.UnknownAuthorityError
		if errors.As(err, &authority) {
			return fmt.Errorf("сертификат PBS не доверен, %w", ErrBackupFingerprintRequired)
		}
		return fmt.Errorf("запрос к PBS: %w", err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return errors.New("PBS не принял ID токена или секрет")
	case http.StatusForbidden:
		return errors.New("нет доступа: проверьте роль DatastoreAudit у пользователя и токена")
	default:
		return fmt.Errorf("PBS вернул HTTP %d: проверьте имя datastore и пространство имён", res.StatusCode)
	}
	var env struct{ Data json.RawMessage }
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&env); err != nil {
		return fmt.Errorf("не удалось прочитать ответ PBS: %w", err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return errors.New("PBS не вернул данные")
	}
	return json.Unmarshal(env.Data, out)
}

// BackupStorage читает хранилище и последний завершённый снимок каждой группы.
func (c *BackupClient) BackupStorage(ctx context.Context, store, namespace string, now time.Time) (BackupDatastore, error) {
	path := "/admin/datastore/" + url.PathEscape(store)
	var data BackupDatastore
	if err := c.get(ctx, path+"/status", &data); err != nil {
		return data, err
	}
	var snapshots []struct {
		Type string  `json:"backup-type"`
		ID   string  `json:"backup-id"`
		Time int64   `json:"backup-time"`
		Size *uint64 `json:"size"` // PBS заполняет size после чтения манифеста
	}
	query := ""
	if namespace != "" {
		query = "?" + url.Values{"ns": {namespace}}.Encode()
	}
	if err := c.get(ctx, path+"/snapshots"+query, &snapshots); err != nil {
		return data, err
	}
	groups := make(map[string]BackupGroup)
	for _, snap := range snapshots {
		if snap.Type == "" || snap.ID == "" {
			continue
		}
		name := snap.Type + "/" + snap.ID
		group := groups[name]
		group.Name = name
		if snap.Size != nil && snap.Time > 0 && snap.Time <= now.Add(5*time.Minute).Unix() {
			group.Copies++
			group.Latest = max(group.Latest, snap.Time)
		}
		groups[name] = group
	}
	data.Groups = make([]BackupGroup, 0, len(groups))
	for _, group := range groups {
		data.Groups = append(data.Groups, group)
	}
	sort.Slice(data.Groups, func(i, j int) bool {
		a, b := data.Groups[i], data.Groups[j]
		if a.Latest == b.Latest {
			return a.Name < b.Name
		}
		return a.Latest < b.Latest
	})
	return data, nil
}
