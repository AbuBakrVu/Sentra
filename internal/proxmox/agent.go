package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Заполнение дисков внутри ВМ знает только гостевая ОС. Proxmox передаёт
// вопрос гостевому агенту QEMU, если тот установлен в машине и включён в её
// настройках.

// FS — раздел внутри ВМ по данным гостевого агента.
type FS struct {
	Name       string `json:"name"`
	Mountpoint string `json:"mountpoint"`
	Type       string `json:"type"`
	Total      int64  `json:"total-bytes"`
	Used       int64  `json:"used-bytes"`
}

var (
	// ErrNoAgent: агент не включён в настройках ВМ или не запущен внутри неё.
	ErrNoAgent = errors.New("гостевой агент QEMU не отвечает")
	// ErrAgentForbidden: у токена нет права читать данные гостевого агента.
	ErrAgentForbidden = errors.New("у токена нет права на гостевой агент")
)

// AgentFSInfo спрашивает у гостевого агента разделы и их заполнение.
func (c *Client) AgentFSInfo(ctx context.Context, node string, vmid int) ([]FS, error) {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/get-fsinfo", url.PathEscape(node), vmid)
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api2/json"+path, nil)
	req.Header.Set("Authorization", c.auth)
	resp, err := c.http.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), ErrFingerprint.Error()) {
			return nil, ErrFingerprint
		}
		return nil, friendlyNetErr(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == 403:
		return nil, ErrAgentForbidden
	case resp.StatusCode == 401:
		return nil, errors.New("Proxmox не принял токен")
	case resp.StatusCode != 200:
		// «No QEMU guest agent configured», «QEMU guest agent is not running»,
		// тайм-аут агента: всё это значит, что спросить сейчас не у кого
		msg := strings.ToLower(string(body) + " " + resp.Status)
		if strings.Contains(msg, "agent") || strings.Contains(msg, "vm ") || strings.Contains(msg, "not running") {
			return nil, ErrNoAgent
		}
		return nil, fmt.Errorf("Proxmox ответил %s: %s", resp.Status, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	var env struct {
		Data struct {
			Result []FS `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("непонятный ответ гостевого агента: %w", err)
	}
	return Usable(env.Data.Result), nil
}

// Служебные файловые системы, которые не нужно показывать как диски.
var skipFS = map[string]bool{
	"squashfs": true, "tmpfs": true, "devtmpfs": true, "overlay": true, "iso9660": true, "udf": true,
	"cdfs": true, "nsfs": true, "proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "ramfs": true,
	"autofs": true, "efivarfs": true, "fuse.lxcfs": true, "fuse.snapfuse": true,
}

// Usable оставляет настоящие разделы: без служебных ФС, образов snap и
// повторных точек монтирования одного раздела (bind, подтома btrfs).
func Usable(in []FS) []FS {
	var out []FS
	seen := map[string]int{} // устройство и размер → индекс в out
	for _, f := range in {
		t := strings.ToLower(f.Type)
		mp := f.Mountpoint
		if f.Total <= 0 || f.Used < 0 || skipFS[t] || strings.HasPrefix(t, "fuse.") || mp == "" {
			continue
		}
		// Windows: «C:\», Linux: «/…». Тома без буквы («System Reserved») пропускаем.
		if !strings.HasPrefix(mp, "/") && !(len(mp) >= 2 && mp[1] == ':') {
			continue
		}
		if strings.HasPrefix(mp, "/snap/") || strings.HasPrefix(mp, "/var/lib/docker/") || strings.HasPrefix(mp, "/run/") {
			continue
		}
		if len(mp) > 3 && mp[1] == ':' {
			mp = strings.TrimRight(mp, `\`)
		} else if len(mp) == 3 && mp[1] == ':' {
			mp = mp[:2] // «C:\» → «C:»
		}
		f.Mountpoint = mp
		key := fmt.Sprintf("%s|%d", f.Name, f.Total)
		if i, ok := seen[key]; ok {
			if len(mp) < len(out[i].Mountpoint) {
				out[i] = f // один раздел смонтирован в нескольких местах: берём самый короткий путь
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, f)
	}
	return out
}
