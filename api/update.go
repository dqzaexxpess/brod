package api

import "net/http"

// Обновление приложения (только владелец): состояние, проверка сейчас, скачивание с проверкой, установка (Linux и Windows).

func (s *Server) updateGet(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	writeJSON(w, 200, s.core().UpdateInfo())
}

// updateCheck всегда отвечает состоянием: причина неудачи проверки лежит в поле error, а не в коде ответа (экран показывает её рядом с версией).
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	info, _ := s.core().CheckUpdate(r.Context(), caller(r))
	writeJSON(w, 200, info)
}

func (s *Server) updateDownload(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		AllowMetered bool `json:"allow_metered"`
	}
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	info, err := s.core().StartUpdateDownload(caller(r), b.AllowMetered)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, info)
}

// updateInstall запускает установку скачанного обновления (Linux, Windows): ядро проверяет и распаковывает, запускает помощника и
// завершает демон. Тело необязательно: app_pid — процесс приложения, выхода которого нужно дождаться.
func (s *Server) updateInstall(w http.ResponseWriter, r *http.Request) {
	if !onlyUser(w, r) {
		return
	}
	var b struct {
		AppPID int `json:"app_pid"`
	}
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	info, err := s.core().InstallUpdate(caller(r), b.AppPID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, info)
}
