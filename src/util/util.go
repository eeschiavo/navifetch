package util

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/GerardPolloRebozado/navifetch/src/model"
)

// HTTPGet esegue una richiesta HTTP GET con timeout e header personalizzati.
func HTTPGet(ctx context.Context, url string, headers map[string]string) (body []byte, status int, contentType string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header.Get("Content-Type"), err
	}
	return b, resp.StatusCode, resp.Header.Get("Content-Type"), nil
}

// CleanExternalSuffix rimuove il marcatore "(external)" (sia prefisso che suffisso) e l'eventuale suffisso "[...]" aggiunto per Amperfy.
func CleanExternalSuffix(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "(external) ")
	s = strings.TrimPrefix(s, "(external)")
	s = strings.TrimSuffix(s, " (external)")
	s = strings.TrimSuffix(s, "(external)")
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "]") {
		if idx := strings.LastIndex(s, " ["); idx != -1 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}

// GetTrackPath costruisce il percorso su disco del brano secondo la struttura Artista/Album/Titolo.mp3.
func GetTrackPath(libPath, artist, album, title string, permanent bool) string {
	safeArtist := SanitizeFilename(CleanExternalSuffix(artist))
	if safeArtist == "" {
		safeArtist = "Unknown"
	}
	safeAlbum := SanitizeFilename(CleanExternalSuffix(album))
	if safeAlbum == "" {
		safeAlbum = "Unknown"
	}
	safeTitle := SanitizeFilename(CleanExternalSuffix(title))
	if safeTitle == "" {
		safeTitle = "Unknown"
	}

	filename := fmt.Sprintf("%s.mp3", safeTitle)
	if permanent {
		return filepath.Join(libPath, safeArtist, safeAlbum, filename)
	}
	return filepath.Join(libPath, "cached", safeArtist, safeAlbum, filename)
}

// SanitizeFilename sostituisce i caratteri non validi per il filesystem con un trattino.
func SanitizeFilename(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "*", "-")
	s = strings.ReplaceAll(s, "?", "-")
	s = strings.ReplaceAll(s, "\"", "-")
	s = strings.ReplaceAll(s, "<", "-")
	s = strings.ReplaceAll(s, ">", "-")
	s = strings.ReplaceAll(s, "|", "-")
	return strings.TrimSpace(s)
}

// IsSongInSubsonicSongList verifica se un titolo è già presente nella lista di brani Subsonic.
func IsSongInSubsonicSongList(name string, subsonicList []model.SubsonicSong) bool {
	cleanName := strings.ToLower(CleanExternalSuffix(name))
	for _, song := range subsonicList {
		if cleanName == strings.ToLower(CleanExternalSuffix(song.Title)) {
			return true
		}
	}
	return false
}
