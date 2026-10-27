package service

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GerardPolloRebozado/navifetch/src/metadata"
	"github.com/GerardPolloRebozado/navifetch/src/model"
	"github.com/GerardPolloRebozado/navifetch/src/util"
)

type SearchService struct {
	rp        *SubsonicReverseProxy
	metadata  metadata.Provider
	searchSeq sync.Map
}

func NewSearchService(rp *SubsonicReverseProxy, metadata metadata.Provider) *SearchService {
	return &SearchService{
		rp:       rp,
		metadata: metadata,
	}
}

// shouldSearchExternal applica una soglia minima di 3 caratteri e un debounce di 650ms per utente.
func (s *SearchService) shouldSearchExternal(ctx context.Context, user, query string) bool {
	if len(strings.TrimSpace(query)) < 3 {
		return false
	}
	if user == "" {
		user = "default"
	}
	val, _ := s.searchSeq.LoadOrStore(user, &atomic.Uint64{})
	counter := val.(*atomic.Uint64)
	mySeq := counter.Add(1)

	select {
	case <-time.After(650 * time.Millisecond):
	case <-ctx.Done():
		return false
	}
	return counter.Load() == mySeq
}

// SmartSearch unisce i risultati locali di Navidrome con quelli esterni (con debounce) adattando i titoli per Amperfy.
func (s *SearchService) SmartSearch(ctx context.Context, query string, path string, rawQuery string) ([]byte, string, error) {
	subsonicSongs, _, _ := s.rp.SearchNavidrome(ctx, path, rawQuery)

	u, _ := url.ParseQuery(rawQuery)
	format := u.Get("f")
	user := u.Get("u")

	var externalSongs []model.SubsonicSong
	if s.shouldSearchExternal(ctx, user, query) {
		var err error
		externalSongs, err = s.metadata.SearchSongs(ctx, query)
		if err != nil {
			externalSongs = nil
		}
	}

	allSongs := make([]model.SubsonicSong, 0, len(subsonicSongs)+len(externalSongs))
	allSongs = append(allSongs, subsonicSongs...)

	for _, ext := range externalSongs {
		if isDuplicateSong(ext, subsonicSongs) {
			continue
		}
		songCopy := formatExternalTitleForAmperfy(ext, query)
		allSongs = append(allSongs, songCopy)
	}

	if format == "xml" || format == "" {
		xmlBody := WrapExternalSearchXML(allSongs)
		return xmlBody, "text/xml; charset=utf-8", nil
	}

	resp := WrapExternalSearch(allSongs)
	jsonBody, err := json.Marshal(resp)
	if err != nil {
		return nil, "", err
	}

	return jsonBody, "application/json; charset=utf-8", nil
}

// isDuplicateSong verifica se il brano esterno esiste già tra i brani locali con stesso titolo e artista.
func isDuplicateSong(ext model.SubsonicSong, localSongs []model.SubsonicSong) bool {
	extTitle := strings.ToLower(util.CleanExternalSuffix(ext.Title))
	extArtist := strings.ToLower(util.CleanExternalSuffix(ext.Artist))
	for _, loc := range localSongs {
		locTitle := strings.ToLower(util.CleanExternalSuffix(loc.Title))
		locArtist := strings.ToLower(util.CleanExternalSuffix(loc.Artist))
		if extTitle == locTitle && extArtist == locArtist {
			return true
		}
	}
	return false
}

// stripAccents normalizza le vocali accentate per confrontarle come fa il filtro diacritic-insensitive [cd] di Amperfy.
func stripAccents(s string) string {
	r := strings.NewReplacer(
		"à", "a", "á", "a", "â", "a", "ä", "a",
		"è", "e", "é", "e", "ê", "e", "ë", "e",
		"ì", "i", "í", "i", "î", "i", "ï", "i",
		"ò", "o", "ó", "o", "ô", "o", "ö", "o",
		"ù", "u", "ú", "u", "û", "u", "ü", "u",
	)
	return r.Replace(strings.ToLower(s))
}

// formatExternalTitleForAmperfy lascia il titolo pulito (con eventuale "[query]" per il filtro Amperfy) e "(external) " davanti all'artista.
func formatExternalTitleForAmperfy(s model.SubsonicSong, query string) model.SubsonicSong {
	cleanTitle := util.CleanExternalSuffix(s.Title)
	cleanArtist := util.CleanExternalSuffix(s.Artist)
	s.Artist = "(external) " + cleanArtist
	s.DisplayArtist = s.Artist

	qNorm := stripAccents(strings.TrimSpace(query))
	if qNorm == "" || strings.Contains(stripAccents(cleanTitle), qNorm) {
		s.Title = cleanTitle
		return s
	}
	s.Title = fmt.Sprintf("%s [%s]", cleanTitle, strings.TrimSpace(query))
	return s
}

func WrapExternalSearch(songs []model.SubsonicSong) any {
	if songs == nil {
		songs = []model.SubsonicSong{}
	}
	sr := model.SearchResult3{
		Song:   songs,
		Album:  []map[string]any{},
		Artist: []map[string]any{},
	}
	return map[string]any{
		"subsonic-response": map[string]any{
			"status":        "ok",
			"version":       "1.16.1",
			"type":          "navidrome",
			"serverVersion": "0.64.2",
			"openSubsonic":  true,
			"searchResult3": sr,
			"searchResult2": sr,
			"searchResult":  sr,
		},
	}
}

// WrapExternalSearchXML serializza la risposta di ricerca in formato XML Subsonic completo per Amperfy.
func WrapExternalSearchXML(songs []model.SubsonicSong) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1" type="navidrome" serverVersion="0.64.2" openSubsonic="true">`)
	sb.WriteString(`<searchResult3>`)
	for _, s := range songs {
		sb.WriteString(songToXMLTag(s))
	}
	sb.WriteString(`</searchResult3>`)
	sb.WriteString(`<searchResult2>`)
	for _, s := range songs {
		sb.WriteString(songToXMLTag(s))
	}
	sb.WriteString(`</searchResult2>`)
	sb.WriteString(`</subsonic-response>`)
	return []byte(sb.String())
}

// WrapSingleSongXML serializza un singolo brano in formato XML Subsonic per getSong.view.
func WrapSingleSongXML(s *model.SubsonicSong) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1" type="navidrome" serverVersion="0.64.2" openSubsonic="true">`)
	if s != nil {
		sb.WriteString(songToXMLTag(*s))
	}
	sb.WriteString(`</subsonic-response>`)
	return []byte(sb.String())
}

func songToXMLTag(s model.SubsonicSong) string {
	return songToXMLNamedTag("song", s)
}

func songToXMLNamedTag(tagName string, s model.SubsonicSong) string {
	parent := s.Parent
	if parent == "" {
		parent = s.AlbumID
	}
	if parent == "" {
		parent = "external-album"
	}
	albumID := s.AlbumID
	if albumID == "" {
		albumID = "external-album"
	}
	artistID := s.ArtistID
	if artistID == "" {
		artistID = "external-artist"
	}
	duration := s.Duration
	if duration <= 0 {
		duration = 180
	}
	size := s.Size
	if size <= 0 {
		size = 5000000
	}
	return fmt.Sprintf(
		`<%s id="%s" parent="%s" title="%s" artist="%s" artistId="%s" album="%s" albumId="%s" genre="%s" duration="%d" size="%d" bitRate="256" coverArt="%s" isDir="false" isVideo="false" suffix="mp3" contentType="audio/mpeg" type="music" mediaType="song" created="2026-01-01T00:00:00.000Z"/>`,
		tagName, xmlEscape(s.ID), xmlEscape(parent), xmlEscape(s.Title), xmlEscape(s.Artist), xmlEscape(artistID),
		xmlEscape(s.Album), xmlEscape(albumID), xmlEscape(s.Genre), duration, size, xmlEscape(s.CoverArt),
	)
}

func xmlEscape(s string) string {
	var buf strings.Builder
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func ContentTypeOrJSON(ct string) string {
	if ct != "" {
		return ct
	}
	return "application/json; charset=utf-8"
}
