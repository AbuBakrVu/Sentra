package server

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct{ Login, Password string }
	if err := readJSON(r, &in, 4096); err != nil {
		httpErr(w, http.StatusBadRequest, "неверные данные пользователя")
		return
	}
	login := strings.TrimSpace(in.Login)
	if login == "" || utf8.RuneCountInString(login) > 64 || strings.ContainsFunc(login, func(c rune) bool {
		return !unicode.IsGraphic(c) || unicode.IsSpace(c)
	}) {
		httpErr(w, http.StatusBadRequest, "введите логин до 64 символов без пробелов")
		return
	}
	if length := utf8.RuneCountInString(in.Password); length < 8 || length > 128 {
		httpErr(w, http.StatusBadRequest, "пароль должен содержать от 8 до 128 символов")
		return
	}
	const role = "viewer"
	switch err := s.st.CreateUser(login, in.Password, role); {
	case errors.Is(err, errLoginTaken):
		httpErr(w, http.StatusConflict, errLoginTaken.Error())
	case err != nil:
		httpErr(w, http.StatusInternalServerError, "не удалось создать пользователя")
	default:
		writeJSON(w, http.StatusCreated, map[string]string{"login": login, "role": role})
	}
}
