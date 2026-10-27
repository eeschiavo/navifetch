package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GerardPolloRebozado/navifetch/src/model"
	"github.com/GerardPolloRebozado/navifetch/src/service"
)

// GetAlbum restituisce i dettagli e le tracce di un album (da cache classifiche, iTunes o Navidrome) in XML o JSON.
func (h *Handler) GetAlbum(w http.ResponseWriter, r *http.Request) {
	albumID := r.URL.Query().Get("id")
	if !strings.HasPrefix(albumID, "external-") {
		h.rp.ServeHTTP(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	cleanID := strings.TrimPrefix(albumID, "external-")
	album, ok := h.chartsService.GetExternalAlbum(ctx, cleanID)
	if !ok {
		resp, err := h.albumService.GetAlbum(ctx, albumID, r.URL.Path, r.URL.RawQuery)
		if err != nil || resp == nil || resp.Subsonic.Album == nil {
			http.Error(w, "Failed to fetch album", http.StatusInternalServerError)
			return
		}
		album = resp.Subsonic.Album
	}

	format := r.URL.Query().Get("f")
	if format == "" || format == "xml" {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(service.WrapAlbumDetailXML(album))
		return
	}

	resp := model.SubsonicAlbumResponse{}
	resp.Subsonic.Status = "ok"
	resp.Subsonic.Version = "1.16.1"
	resp.Subsonic.Album = album
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// GetAlbumList intercetta la richiesta "newest" (Ultimi Album) arricchendola con le nuove uscite Apple Music Italia.
func (h *Handler) GetAlbumList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset := q.Get("offset")
	if q.Get("type") != "newest" || (offset != "" && offset != "0") {
		h.rp.ServeHTTP(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	localAlbums := h.fetchLocalNewestAlbums(ctx, r)
	extAlbums, _ := h.chartsService.GetNewestAlbums(ctx)

	limit := 25
	if s, err := strconv.Atoi(q.Get("size")); err == nil && s > limit {
		limit = s
	}
	combined := make([]model.SubsonicAlbum, 0, len(extAlbums)+len(localAlbums))
	combined = append(combined, extAlbums...)
	combined = append(combined, localAlbums...)
	if len(combined) > limit {
		combined = combined[:limit]
	}

	if q.Get("f") == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(service.WrapAlbumListJSON(combined))
		return
	}

	isV2 := strings.Contains(r.URL.Path, "2")
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(service.WrapAlbumListXML(combined, isV2))
}

func (h *Handler) fetchLocalNewestAlbums(ctx context.Context, r *http.Request) []model.SubsonicAlbum {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil
	}
	q.Set("f", "json")
	body, _, _, err := h.rp.SendNavidromeRequestWithHeaders(ctx, r.URL.Path, q.Encode(), r.Header)
	if err != nil || len(body) == 0 {
		return nil
	}
	var parsed struct {
		Subsonic struct {
			AlbumList2 struct {
				Album []model.SubsonicAlbum `json:"album"`
			} `json:"albumList2"`
			AlbumList struct {
				Album []model.SubsonicAlbum `json:"album"`
			} `json:"albumList"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Printf("Failed to parse local newest albums: %v", err)
		return nil
	}
	if len(parsed.Subsonic.AlbumList2.Album) > 0 {
		return parsed.Subsonic.AlbumList2.Album
	}
	return parsed.Subsonic.AlbumList.Album
}

// GetPlaylists inietta le playlist virtuali Apple Music (Top 50 Italia e Nuove Uscite) nella lista di Navidrome.
func (h *Handler) GetPlaylists(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	body, _, _, err := h.rp.SendNavidromeRequestWithHeaders(ctx, r.URL.Path, r.URL.RawQuery, r.Header)
	if err != nil {
		body = nil
	}
	vps := h.chartsService.GetVirtualPlaylists(ctx)

	if r.URL.Query().Get("f") == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(service.InjectVirtualPlaylistsJSON(body, vps))
		return
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(service.InjectVirtualPlaylistsXML(body, vps))
}

// GetPlaylist restituisce i brani di una playlist virtuale usando il tag <entry> richiesto da Amperfy, oppure inoltra a Navidrome.
func (h *Handler) GetPlaylist(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !strings.HasPrefix(id, "external-playlist-") {
		h.rp.ServeHTTP(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	vps := h.chartsService.GetVirtualPlaylists(ctx)
	var found *service.VirtualPlaylist
	for i := range vps {
		if vps[i].ID == id {
			found = &vps[i]
			break
		}
	}
	if found == nil {
		http.Error(w, "Playlist not found", http.StatusNotFound)
		return
	}

	if r.URL.Query().Get("f") == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(service.WrapVirtualPlaylistJSON(*found))
		return
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(service.WrapVirtualPlaylistXML(*found))
}
