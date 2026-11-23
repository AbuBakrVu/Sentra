package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"sentra/internal/proto"
	"sentra/internal/proxmox"
)

const kindPBS = "pbs"

type pbsSource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Datastore string `json:"datastore"`
	Namespace string `json:"namespace"`
	TokenID   string `json:"token_id"`
	WarnHours int    `json:"warn_hours"`
	LastSeen  int64  `json:"last_seen"`
	LastError string `json:"last_error"`
}

type pbsConfig struct {
	pbsSource
	Secret      string `json:"secret"`
	Fingerprint string `json:"fingerprint"`
}

func (s *Store) pbsSources() ([]pbsConfig, error) {
	rows, err := s.db.Query(`SELECT h.id, h.name, h.last_seen, p.url, p.datastore, p.namespace,
		p.token_id, p.secret, p.fingerprint, p.warn_hours, p.last_error
		FROM pbs_sources p JOIN hosts h ON h.id=p.id ORDER BY h.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := []pbsConfig{}
	for rows.Next() {
		var p pbsConfig
		if err := rows.Scan(&p.ID, &p.Name, &p.LastSeen, &p.URL, &p.Datastore, &p.Namespace,
			&p.TokenID, &p.Secret, &p.Fingerprint, &p.WarnHours, &p.LastError); err != nil {
			return nil, err
		}
		sources = append(sources, p)
	}
	return sources, rows.Err()
}

type pbsObservation struct {
	at      time.Time
	meta    map[string]any
	samples []proto.Sample
}

func (p pbsConfig) observe(ctx context.Context) (pbsObservation, error) {
	client, err := proxmox.NewBackup(p.URL, p.TokenID, p.Secret, p.Fingerprint)
	if err != nil {
		return pbsObservation{}, err
	}
	defer client.Close()
	now := time.Now()
	data, err := client.BackupStorage(ctx, p.Datastore, p.Namespace, now)
	if err != nil {
		return pbsObservation{}, err
	}
	return summarizePBS(p, data, now), nil
}

func summarizePBS(p pbsConfig, data proxmox.BackupDatastore, now time.Time) pbsObservation {
	cutoff := now.Add(-time.Duration(p.WarnHours) * time.Hour).Unix()
	stale := 0
	for _, group := range data.Groups {
		if group.Latest == 0 || group.Latest < cutoff {
			stale++
		}
	}
	o := pbsObservation{at: now, meta: map[string]any{
		"url": p.URL, "datastore": p.Datastore, "namespace": p.Namespace, "warn_hours": p.WarnHours,
		"backup_groups": data.Groups, "stale_groups": stale, "group_count": len(data.Groups),
	}}
	if data.Total > 0 {
		labels := map[string]string{"mount": p.Datastore}
		o.samples = []proto.Sample{
			{Name: proto.MFsUsedPct, Value: pct(float64(data.Used), float64(data.Total)), Labels: labels},
			{Name: proto.MFsTotal, Value: float64(data.Total), Labels: labels},
			{Name: proto.MFsFree, Value: float64(data.Avail), Labels: labels},
		}
		o.meta["space_used"] = data.Used
		o.meta["space_total"] = data.Total
		o.meta["space_available"] = data.Avail
	}
	return o
}

func pbsProblems(h *Host, want map[string]desired) {
	if count, ok := h.Meta["group_count"].(float64); ok && count == 0 {
		want["backup_empty"] = desired{"degraded", 1, "В PBS нет групп резервных копий", 0}
	}
	if stale, _ := h.Meta["stale_groups"].(float64); stale > 0 {
		want["backup_stale"] = desired{"degraded", 1, fmt.Sprintf("Группы PBS без свежей копии: %d", int(stale)), 0}
	}
}

func (s *Server) startPBS(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			sources, err := s.st.pbsSources()
			if err != nil {
				log.Printf("PBS: список подключений: %v", err)
			}
			var wg sync.WaitGroup
			slots := make(chan struct{}, 8)
			for _, p := range sources {
				if ctx.Err() != nil {
					break
				}
				slots <- struct{}{}
				wg.Add(1)
				go func() {
					defer func() { <-slots; wg.Done() }()
					pollCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
					defer cancel()
					s.pollPBS(pollCtx, p)
				}()
			}
			wg.Wait()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Server) pollPBS(ctx context.Context, p pbsConfig) {
	o, err := p.observe(ctx)
	if err != nil {
		if ctx.Err() != context.Canceled {
			s.st.db.Exec(`UPDATE pbs_sources SET last_error=? WHERE id=?`, err.Error(), p.ID)
		}
		return
	}
	meta, _ := json.Marshal(o.meta)
	metrics, _ := json.Marshal(summarize(o.samples))
	_, err = s.st.db.Exec(`UPDATE hosts SET meta=?, last_seen=?, last_metrics=? WHERE id=? AND last_seen<=?`,
		string(meta), o.at.Unix(), string(metrics), p.ID, o.at.Unix())
	if err != nil {
		log.Printf("PBS %s: данные не сохранены: %v", p.Name, err)
		return
	}
	s.st.db.Exec(`UPDATE pbs_sources SET last_error='' WHERE id=?`, p.ID)
	s.recordPBS(ctx, p.ID, o)
}

func (s *Server) recordPBS(ctx context.Context, id string, o pbsObservation) {
	if h, err := s.st.Host(id); err == nil {
		s.eng.Eval(h, o.at)
	}
	if len(o.samples) > 0 {
		if err := s.vm.Import(ctx, id, o.at.UnixMilli(), o.samples); err != nil {
			log.Printf("PBS %s: метрики не сохранены: %v", id, err)
		}
	}
}

func (s *Server) listPBS(w http.ResponseWriter, _ *http.Request, _ *User) {
	sources, err := s.st.pbsSources()
	if err != nil {
		httpErr(w, 500, "не удалось загрузить подключения PBS")
		return
	}
	public := make([]pbsSource, 0, len(sources))
	for _, p := range sources {
		public = append(public, p.pbsSource)
	}
	writeJSON(w, 200, public)
}

var backupTokenID = regexp.MustCompile(`^[^@!:\s]+@[^@!:\s]+![^@!:\s]+$`)
var backupStorePath = regexp.MustCompile(`^[[:alnum:]_][[:alnum:]_.-]*(/[[:alnum:]_][[:alnum:]_.-]*)*$`)

func (s *Server) addPBS(w http.ResponseWriter, r *http.Request, u *User) {
	if u.Role != "admin" {
		httpErr(w, 403, "подключение PBS доступно администратору")
		return
	}
	var p pbsConfig
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&p) != nil {
		httpErr(w, 400, "неверные параметры PBS")
		return
	}
	var err error
	p.URL, err = proxmox.NormalizeBackupURL(p.URL)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	p.Name, p.Datastore, p.Namespace = strings.TrimSpace(p.Name), strings.TrimSpace(p.Datastore), strings.TrimSpace(p.Namespace)
	p.TokenID, p.Secret, p.Fingerprint = strings.TrimSpace(p.TokenID), strings.TrimSpace(p.Secret), strings.TrimSpace(p.Fingerprint)
	if p.WarnHours == 0 {
		p.WarnHours = 36
	}
	if !backupTokenID.MatchString(p.TokenID) || p.Secret == "" || !backupStorePath.MatchString(p.Datastore) ||
		strings.Contains(p.Datastore, "/") || (p.Namespace != "" && !backupStorePath.MatchString(p.Namespace)) ||
		p.WarnHours < 1 || p.WarnHours > 8760 {
		httpErr(w, 400, "проверьте datastore, пространство имён, токен и срок свежести копий (1–8760 ч)")
		return
	}
	if p.Name == "" {
		p.Name = p.Datastore
		if p.Namespace != "" {
			p.Name += " / " + p.Namespace
		}
	}
	p.ID = "pbs" + strings.ToLower(randToken(9))
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	o, err := p.observe(ctx)
	if err != nil {
		if errors.Is(err, proxmox.ErrBackupFingerprintRequired) {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": err.Error(),
				"code":  "pbs_fingerprint_required",
			})
		} else {
			httpErr(w, 502, err.Error())
		}
		return
	}
	if err := s.st.addPBS(p, o); err != nil {
		log.Printf("PBS: подключение не сохранено: %v", err)
		httpErr(w, 409, "не удалось сохранить подключение. Проверьте, подключено ли уже это хранилище и пространство имён")
		return
	}
	s.recordPBS(ctx, p.ID, o)
	p.LastSeen = o.at.Unix()
	writeJSON(w, 201, p.pbsSource)
}

func (s *Store) addPBS(p pbsConfig, o pbsObservation) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	meta, _ := json.Marshal(o.meta)
	metrics, _ := json.Marshal(summarize(o.samples))
	_, err = tx.Exec(`INSERT INTO hosts(id, name, key_hash, kind, meta, last_seen, last_metrics, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, "-", kindPBS, string(meta), o.at.Unix(), string(metrics), o.at.Unix())
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO pbs_sources(id, url, datastore, namespace, token_id, secret, fingerprint, warn_hours) VALUES(?,?,?,?,?,?,?,?)`,
		p.ID, p.URL, p.Datastore, p.Namespace, p.TokenID, p.Secret, p.Fingerprint, p.WarnHours)
	if err != nil {
		return err
	}
	return tx.Commit()
}
