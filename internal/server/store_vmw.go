package server

import (
	"encoding/json"
	"strings"
	"time"
)

// ---------- VMware ----------
// Хосты и машины VMware живут в той же таблице hosts, что и Proxmox:
// source_id — id подключения, ext_id — host/<moref> или vm/<moref>.
// Поэтому UpsertPVEHost, PVEHostIDs и MarkPVEGone годятся и для них.

type VMWSource struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	URL         string         `json:"url"`
	Username    string         `json:"username"`
	Password    string         `json:"-"`
	Fingerprint string         `json:"fingerprint"`
	APIType     string         `json:"api_type"` // VirtualCenter или HostAgent (ESXi)
	LastOK      int64          `json:"last_ok"`
	LastError   string         `json:"last_error"`
	Summary     map[string]any `json:"summary"`
	CreatedAt   int64          `json:"created_at"`
}

const vmwCols = `id, name, url, username, password, fingerprint, api_type, last_ok, last_error, summary, created_at`

func scanVMW(sc interface{ Scan(...any) error }) (*VMWSource, error) {
	var p VMWSource
	var sum string
	if err := sc.Scan(&p.ID, &p.Name, &p.URL, &p.Username, &p.Password, &p.Fingerprint, &p.APIType,
		&p.LastOK, &p.LastError, &sum, &p.CreatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(sum), &p.Summary)
	return &p, nil
}

func (s *Store) AddVMW(name, url, user, pass, fp, apiType string) (string, error) {
	id := "vmw" + strings.ToLower(randToken(6))
	_, err := s.db.Exec(`INSERT INTO vmw_sources(id, name, url, username, password, fingerprint, api_type, created_at)
		VALUES(?,?,?,?,?,?,?,?)`, id, name, url, user, pass, fp, apiType, time.Now().Unix())
	return id, err
}

func (s *Store) VMWSources() ([]*VMWSource, error) {
	rows, err := s.db.Query(`SELECT ` + vmwCols + ` FROM vmw_sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VMWSource{}
	for rows.Next() {
		p, err := scanVMW(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) VMWSource(id string) (*VMWSource, error) {
	return scanVMW(s.db.QueryRow(`SELECT `+vmwCols+` FROM vmw_sources WHERE id=?`, id))
}

func (s *Store) VMWResult(id string, ok bool, errText string, summary map[string]any) {
	if ok {
		sj, _ := json.Marshal(summary)
		s.db.Exec(`UPDATE vmw_sources SET last_ok=?, last_error='', summary=? WHERE id=?`, time.Now().Unix(), string(sj), id)
		return
	}
	s.db.Exec(`UPDATE vmw_sources SET last_error=? WHERE id=?`, errText, id)
}

func (s *Store) SetVMWFingerprint(id, fp string) error {
	_, err := s.db.Exec(`UPDATE vmw_sources SET fingerprint=?, last_error='' WHERE id=?`, fp, id)
	return err
}

// DeleteVMW удаляет подключение вместе с его хостами и машинами.
func (s *Store) DeleteVMW(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	tx.Exec(`DELETE FROM problems WHERE host_id IN (SELECT id FROM hosts WHERE source_id=?)`, id)
	tx.Exec(`DELETE FROM hosts WHERE source_id=?`, id)
	tx.Exec(`DELETE FROM vmw_sources WHERE id=?`, id)
	return tx.Commit()
}
