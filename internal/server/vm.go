package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"dozor/internal/proto"
)

// VM — клиент VictoriaMetrics (single-node).
type VM struct {
	base string
	cl   *http.Client
}

func NewVM(base string) *VM {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &VM{base: strings.TrimRight(base, "/"), cl: &http.Client{Transport: tr, Timeout: 15 * time.Second}}
}

const metricPrefix = "dozor_"

func escLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// Import пишет пакет метрик в формате Prometheus text exposition.
func (v *VM) Import(ctx context.Context, hostID string, ts int64, samples []proto.Sample) error {
	var b bytes.Buffer
	for _, s := range samples {
		if !validName(s.Name) {
			continue
		}
		b.WriteString(metricPrefix + s.Name + `{host="` + escLabel(hostID) + `"`)
		keys := make([]string, 0, len(s.Labels))
		for k := range s.Labels {
			if validName(k) && k != "host" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(`,` + k + `="` + escLabel(s.Labels[k]) + `"`)
		}
		b.WriteString("} " + strconv.FormatFloat(s.Value, 'g', -1, 64) + " " + strconv.FormatInt(ts, 10) + "\n")
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", v.base+"/api/v1/import/prometheus", &b)
	resp, err := v.cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("victoriametrics: %s %s", resp.Status, msg)
	}
	return nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

type Series struct {
	Label  string     `json:"label"`
	Values []*float64 `json:"values"`
}

type Chart struct {
	Timestamps []int64  `json:"timestamps"` // unix seconds
	Series     []Series `json:"series"`
	Unit       string   `json:"unit"`
}

// QueryRange выполняет запрос и выравнивает все ряды на общую сетку времени,
// чтобы фронтенд мог сразу отдать их в uPlot. legend — метка, которая подпишет ряд.
func (v *VM) QueryRange(ctx context.Context, q string, start, end time.Time, step time.Duration, legend func(map[string]string) string) (*Chart, error) {
	p := url.Values{}
	p.Set("query", q)
	p.Set("start", strconv.FormatInt(start.Unix(), 10))
	p.Set("end", strconv.FormatInt(end.Unix(), 10))
	p.Set("step", strconv.Itoa(int(step.Seconds()))+"s")
	req, _ := http.NewRequestWithContext(ctx, "GET", v.base+"/api/v1/query_range?"+p.Encode(), nil)
	resp, err := v.cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]any          `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("victoriametrics: %s", r.Error)
	}
	st := int64(step.Seconds())
	first := start.Unix() / st * st
	n := int((end.Unix()-first)/st) + 1
	ch := &Chart{Timestamps: make([]int64, n)}
	for i := range ch.Timestamps {
		ch.Timestamps[i] = first + int64(i)*st
	}
	for _, res := range r.Data.Result {
		s := Series{Label: legend(res.Metric), Values: make([]*float64, n)}
		for _, pt := range res.Values {
			tsF, _ := pt[0].(float64)
			idx := int((int64(tsF) - first) / st)
			if idx < 0 || idx >= n {
				continue
			}
			str, _ := pt[1].(string)
			if f, err := strconv.ParseFloat(str, 64); err == nil {
				f := f
				s.Values[idx] = &f
			}
		}
		ch.Series = append(ch.Series, s)
	}
	sort.Slice(ch.Series, func(i, j int) bool { return ch.Series[i].Label < ch.Series[j].Label })
	return ch, nil
}
