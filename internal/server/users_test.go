package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateMonitoringUser(t *testing.T) {
	st := testStore(t)
	if err := st.CreateUser("admin", "fixture-admin-password", "admin"); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, loginFails: map[string][]time.Time{}}
	h := s.Handler()
	request := func(method, path, body string, session *http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if session != nil {
			req.AddCookie(session)
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		return res
	}
	login := func(body, wantLogin, wantRole string) *http.Cookie {
		t.Helper()
		res := request(http.MethodPost, "/api/login", body, nil, http.StatusOK)
		var user User
		if err := json.Unmarshal(res.Body.Bytes(), &user); err != nil || user.Login != wantLogin || user.Role != wantRole {
			t.Fatalf("unexpected login response: %s", res.Body)
		}
		for _, cookie := range res.Result().Cookies() {
			if cookie.Name == cookieName {
				return cookie
			}
		}
		t.Fatal("session cookie missing")
		return nil
	}
	admin := login(`{"login":"admin","password":"fixture-admin-password"}`, "admin", "admin")
	const create = `{"login":" noc.viewer ","password":"fixture-viewer-password","role":"admin"}`
	request(http.MethodPost, "/api/users", create, nil, http.StatusUnauthorized)
	request(http.MethodPost, "/api/users", `{"login":"noc","password":"short"}`, admin, http.StatusBadRequest)
	created := request(http.MethodPost, "/api/users", create, admin, http.StatusCreated)
	if strings.Contains(created.Body.String(), "fixture-viewer-password") {
		t.Fatal("creation response exposed the password")
	}
	request(http.MethodPost, "/api/users", `{"login":"noc.viewer","password":"fixture-other-password"}`, admin, http.StatusConflict)
	viewer := login(`{"login":"noc.viewer","password":"fixture-viewer-password"}`, "noc.viewer", "viewer")
	request(http.MethodGet, "/api/hosts", "", viewer, http.StatusOK)
	request(http.MethodPost, "/api/users", create, viewer, http.StatusForbidden)
	request(http.MethodPost, "/api/enroll", "{}", viewer, http.StatusForbidden)
	request(http.MethodDelete, "/api/hosts/example", "", viewer, http.StatusForbidden)
	request(http.MethodGet, "/api/snmp", "", viewer, http.StatusForbidden)
	request(http.MethodPost, "/api/password", `{"old":"fixture-viewer-password","new":"fixture-updated-password"}`, viewer, http.StatusOK)
}
