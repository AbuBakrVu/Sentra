package server

import (
	"encoding/json"
	"strings"
	"time"

	"sentra/internal/snmp"
)

// ---------- SNMP ----------
// Каждое SNMP-устройство — одна запись в hosts: source_id = id устройства,
// ext_id = "dev", kind = "snmp". Параметры доступа лежат в snmp_devices.

type SNMPDevice struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Addr      string    `json:"addr"`
	Auth      snmp.Auth `json:"auth"`
	LastOK    int64     `json:"last_ok"`
	LastError string    `json:"last_error"`
	CreatedAt int64     `json:"created_at"`
}

const snmpCols = `id, name, addr, version, community, username, auth_proto, auth_pass, priv_proto, priv_pass, last_ok, last_error, created_at`

func scanSNMP(sc interface{ Scan(...any) error }) (*SNMPDevice, error) {
	var d SNMPDevice
	a := &d.Auth
	err := sc.Scan(&d.ID, &d.Name, &d.Addr, &a.Version, &a.Community, &a.User, &a.AuthProto, &a.AuthPass,
		&a.PrivProto, &a.PrivPass, &d.LastOK, &d.LastError, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) AddSNMP(name, addr string, a snmp.Auth) (string, error) {
	id := "snmp" + strings.ToLower(randToken(6))
	_, err := s.db.Exec(`INSERT INTO snmp_devices(id, name, addr, version, community, username, auth_proto, auth_pass,
		priv_proto, priv_pass, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, name, addr, a.Version, a.Community,
		a.User, a.AuthProto, a.AuthPass, a.PrivProto, a.PrivPass, time.Now().Unix())
	return id, err
}

func (s *Store) SNMPDevices() ([]*SNMPDevice, error) {
	rows, err := s.db.Query(`SELECT ` + snmpCols + ` FROM snmp_devices ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SNMPDevice{}
	for rows.Next() {
		d, err := scanSNMP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) SNMPDevice(id string) (*SNMPDevice, error) {
	return scanSNMP(s.db.QueryRow(`SELECT `+snmpCols+` FROM snmp_devices WHERE id=?`, id))
}

func (s *Store) SNMPResult(id string, ok bool, errText string) {
	if ok {
		s.db.Exec(`UPDATE snmp_devices SET last_ok=?, last_error='' WHERE id=?`, time.Now().Unix(), id)
		return
	}
	s.db.Exec(`UPDATE snmp_devices SET last_error=? WHERE id=?`, errText, id)
}

// UpdateSNMPAuth меняет адрес и доступ (например, сменили пароль на устройстве).
func (s *Store) UpdateSNMPAuth(id, addr string, a snmp.Auth) error {
	_, err := s.db.Exec(`UPDATE snmp_devices SET addr=?, version=?, community=?, username=?, auth_proto=?, auth_pass=?,
		priv_proto=?, priv_pass=?, last_error='' WHERE id=?`, addr, a.Version, a.Community, a.User, a.AuthProto,
		a.AuthPass, a.PrivProto, a.PrivPass, id)
	return err
}

// DeleteSNMP удаляет устройство вместе с его записью в hosts и проблемами.
func (s *Store) DeleteSNMP(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	tx.Exec(`DELETE FROM problems WHERE host_id IN (SELECT id FROM hosts WHERE source_id=?)`, id)
	tx.Exec(`DELETE FROM hosts WHERE source_id=?`, id)
	tx.Exec(`DELETE FROM snmp_devices WHERE id=?`, id)
	return tx.Commit()
}

// SNMPHostMeta — meta записи устройства в hosts (для данных между опросами).
func (s *Store) SNMPHostMeta(deviceID string) (map[string]any, error) {
	var mj string
	if err := s.db.QueryRow(`SELECT meta FROM hosts WHERE source_id=? AND ext_id='dev'`, deviceID).Scan(&mj); err != nil {
		return map[string]any{}, err
	}
	m := map[string]any{}
	json.Unmarshal([]byte(mj), &m)
	return m, nil
}
