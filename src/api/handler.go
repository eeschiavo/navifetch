package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GerardPolloRebozado/navifetch/src/config"
	"github.com/GerardPolloRebozado/navifetch/src/metadata"
	"github.com/GerardPolloRebozado/navifetch/src/service"
	"github.com/GerardPolloRebozado/navifetch/src/util"
)

type Handler struct {
	cfg           *config.Config
	rp            *service.SubsonicReverseProxy
	metadata      metadata.Provider
	albumService  *service.AlbumService
	searchService *service.SearchService
	songService   *service.SongService
	streamService *service.StreamService
}

func NewHandler(cfg *config.Config, rp *service.SubsonicReverseProxy) *Handler {
	p, err := metadata.NewProvider(cfg.MetadataProvider, cfg.Country, cfg.Limit)
	if err != nil {
		log.Fatalf("Failed to initialize metadata provider: %v", err)
	}
	return &Handler{
		cfg: cfg, rp: rp, metadata: p,
		albumService:  service.NewAlbumService(rp, p),
		searchService: service.NewSearchService(rp, p),
		songService:   service.NewSongService(rp, p),
		streamService: service.NewStreamService(cfg, p),
	}
}

func (h *Handler) Healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// SmartSearch gestisce le ricerche Subsonic unendo brani locali ed esterni, ignorando le query per soli artisti/album.
func (h *Handler) SmartSearch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("songCount") == "0" {
		h.rp.ServeHTTP(w, r)
		return
	}
	query := extractSearchQuery(r, "query", "q", "any", "song", "title", "artist")
	if query == "\"\"" || query == "" {
		h.rp.ServeHTTP(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	log.Printf("Search query: %s", query)
	body, contentType, err := h.searchService.SmartSearch(ctx, query, r.URL.Path, r.URL.RawQuery)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", service.ContentTypeOrJSON(contentType))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func extractSearchQuery(r *http.Request, keys ...string) string {
	q := r.URL.Query()
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// ProxyMetadata restituisce i metadati di un brano esterno in XML o JSON, oppure inoltra a Navidrome.
func (h *Handler) ProxyMetadata(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !strings.HasPrefix(id, "external-") {
		h.rp.ServeHTTP(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	res, err := h.songService.GetSong(ctx, strings.TrimPrefix(id, "external-"))
	if err != nil {
		http.Error(w, "Song not found", http.StatusNotFound)
		return
	}
	if f := r.URL.Query().Get("f"); f == "" || f == "xml" {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(service.WrapSingleSongXML(res))
		return
	}
	resp := map[string]any{
		"subsonic-response": map[string]any{"status": "ok", "version": "1.16.1", "song": res},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

// NativeApiSong intercetta l'API nativa /api/song di Navidrome per arricchire le ricerche con brani esterni.
func (h *Handler) NativeApiSong(w http.ResponseWriter, r *http.Request) {
	h.TranslateExternalIDs(r)
	query := extractSearchQuery(r, "title", "q", "query", "search", "artist")
	if query == "" || query == "\"\"" {
		h.rp.ServeHTTP(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	body, _, _, err := h.rp.SendNavidromeRequestWithHeaders(ctx, r.URL.Path, r.URL.RawQuery, r.Header)
	var localSongs []map[string]any
	if err == nil && body != nil {
		_ = json.Unmarshal(body, &localSongs)
	}
	externalSongs, _ := h.metadata.SearchSongs(ctx, query)
	allSongs := append([]map[string]any{}, localSongs...)
	for _, s := range externalSongs {
		allSongs = append(allSongs, map[string]any{
			"id": s.ID, "title": s.Title, "artist": s.Artist, "artistId": s.ArtistID,
			"album": s.Album, "albumId": s.AlbumID, "duration": s.Duration, "genre": s.Genre,
			"coverArt": s.CoverArt, "hasCoverArt": true, "size": s.Size, "isDir": false, "path": s.Title + ".mp3",
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Total-Count", fmt.Sprintf("%d", len(allSongs)))
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(allSongs)
}

// ProxyStream scarica e serve direttamente i file MP3 per gli ID esterni (supportando Range HTTP per iOS/Amperfy).
func (h *Handler) ProxyStream(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" && strings.HasPrefix(r.URL.Path, "/api/stream/") {
		id = strings.TrimPrefix(r.URL.Path, "/api/stream/")
	}
	if strings.HasPrefix(id, "external-") {
		cleanID := strings.TrimPrefix(id, "external-")
		_, targetPath, err := h.streamService.DownloadTrack(cleanID, r.URL.Query().Get("u"), true)
		if err != nil {
			http.Error(w, "Download failed", http.StatusBadGateway)
			return
		}
		go h.rp.SendNavidromeRequest(context.Background(), "/rest/startScan.view", r.URL.RawQuery)
		w.Header().Set("Content-Type", "audio/mpeg")
		http.ServeFile(w, r, targetPath)
		return
	}
	h.TranslateExternalIDs(r)
	h.rp.ServeHTTP(w, r)
}

func (h *Handler) ProxyPlaylist(w http.ResponseWriter, r *http.Request) {
	h.TranslateExternalIDs(r)
	h.rp.ServeHTTP(w, r)
}
func (h *Handler) ProxyIDTranslation(w http.ResponseWriter, r *http.Request) {
	h.ProxyPlaylist(w, r)
}

// resolveExternalID scarica un brano esterno e ne risolve l'ID effettivo assegnato da Navidrome.
func (h *Handler) resolveExternalID(extID string, r *http.Request) (string, bool) {
	cleanID := strings.TrimPrefix(extID, "external-")
	username := r.URL.Query().Get("u")
	songMetadata, targetPath, err := h.streamService.DownloadTrack(cleanID, username, true)
	if err != nil {
		return "", false
	}
	title := util.CleanExternalSuffix(songMetadata.Title)
	artist := util.CleanExternalSuffix(songMetadata.Artist)
	mbid := songMetadata.MusicBrainzId
	if mbid == "" {
		mbid = cleanID
	}
	libPath := strings.TrimRight(h.cfg.LibraryPathForUser(username), "/")
	relativePath := strings.TrimPrefix(targetPath, libPath+"/")
	foundSong, err := h.rp.FindNavidromeSongID(h.cfg, relativePath, artist, title, mbid, r)
	if err != nil {
		return "", false
	}
	return foundSong.ID, true
}

// TranslateExternalIDs converte gli ID "external-" presenti in query, path o body nei rispettivi ID di Navidrome.
func (h *Handler) TranslateExternalIDs(r *http.Request) {
	q := r.URL.Query()
	modifiedQuery := false
	for key, values := range q {
		for i, val := range values {
			if strings.HasPrefix(val, "external-") {
				if resolvedID, ok := h.resolveExternalID(val, r); ok {
					values[i] = resolvedID
					modifiedQuery = true
				}
			}
		}
		q[key] = values
	}
	if modifiedQuery {
		r.URL.RawQuery = q.Encode()
	}
	extIDRegex := regexp.MustCompile(`external-[a-zA-Z0-9_\-]+`)
	if strings.Contains(r.URL.Path, "external-") {
		for _, extID := range extIDRegex.FindAllString(r.URL.Path, -1) {
			if resolvedID, ok := h.resolveExternalID(extID, r); ok {
				r.URL.Path = strings.Replace(r.URL.Path, extID, resolvedID, 1)
			}
		}
	}
	h.translateBodyExternalIDs(r, extIDRegex)
}

func (h *Handler) translateBodyExternalIDs(r *http.Request, extIDRegex *regexp.Regexp) {
	if r.Body == nil || (r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch) {
		return
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil || len(bodyBytes) == 0 {
		return
	}
	bodyStr := string(bodyBytes)
	if !strings.Contains(bodyStr, "external-") {
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		return
	}
	uniqueIDs := make(map[string]bool)
	for _, match := range extIDRegex.FindAllString(bodyStr, -1) {
		uniqueIDs[match] = true
	}
	for extID := range uniqueIDs {
		if resolvedID, ok := h.resolveExternalID(extID, r); ok {
			bodyStr = strings.ReplaceAll(bodyStr, extID, resolvedID)
		}
	}
	newBodyBytes := []byte(bodyStr)
	r.Body = io.NopCloser(bytes.NewReader(newBodyBytes))
	r.ContentLength = int64(len(newBodyBytes))
	r.Header.Set("Content-Length", strconv.Itoa(len(newBodyBytes)))
}

func (h *Handler) ProxyCoverArt(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		id = strings.TrimPrefix(r.URL.Path, "/api/coverArt/")
	}
	if !strings.HasPrefix(id, "external-") {
		h.rp.ServeHTTP(w, r)
		return
	}
	sizeInt, err := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
	if err != nil || sizeInt <= 0 {
		sizeInt = 250
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	image, contentType, err := h.songService.GetCoverArt(ctx, strings.TrimPrefix(id, "external-"), sizeInt)
	if err != nil {
		http.Error(w, "Failed to fetch cover", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(image)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(image)
}

func (h *Handler) GetAlbum(w http.ResponseWriter, r *http.Request) {
	albumId := r.URL.Query().Get("id")
	if !strings.HasPrefix(albumId, "external-") {
		h.rp.ServeHTTP(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	resp, err := h.albumService.GetAlbum(ctx, albumId, r.URL.Path, r.URL.RawQuery)
	if err != nil {
		http.Error(w, "Failed to fetch album", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) CatchAll(w http.ResponseWriter, r *http.Request) { h.rp.ServeHTTP(w, r) }
