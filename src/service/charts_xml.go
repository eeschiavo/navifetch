package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GerardPolloRebozado/navifetch/src/model"
)

// WrapAlbumListXML serializza una lista di album in formato XML Subsonic (<albumList2> o <albumList>).
func WrapAlbumListXML(albums []model.SubsonicAlbum, isV2 bool) []byte {
	tag := "albumList"
	if isV2 {
		tag = "albumList2"
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1" type="navidrome" serverVersion="0.64.2" openSubsonic="true">`)
	sb.WriteString("<" + tag + ">")
	for _, a := range albums {
		sb.WriteString(albumToXMLTag(a, true))
	}
	sb.WriteString("</" + tag + ">")
	sb.WriteString(`</subsonic-response>`)
	return []byte(sb.String())
}

// WrapAlbumListJSON costruisce la risposta JSON Subsonic per getAlbumList / getAlbumList2.
func WrapAlbumListJSON(albums []model.SubsonicAlbum) any {
	if albums == nil {
		albums = []model.SubsonicAlbum{}
	}
	listObj := map[string]any{"album": albums}
	return map[string]any{
		"subsonic-response": map[string]any{
			"status":        "ok",
			"version":       "1.16.1",
			"type":          "navidrome",
			"serverVersion": "0.64.2",
			"openSubsonic":  true,
			"albumList2":    listObj,
			"albumList":     listObj,
		},
	}
}

// WrapAlbumDetailXML serializza un singolo album completo dei suoi brani in formato XML Subsonic per getAlbum.view.
func WrapAlbumDetailXML(alb *model.SubsonicAlbum) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1" type="navidrome" serverVersion="0.64.2" openSubsonic="true">`)
	if alb != nil {
		sb.WriteString(albumToXMLTag(*alb, false))
		for _, s := range alb.Song {
			sb.WriteString(songToXMLTag(s))
		}
		sb.WriteString(`</album>`)
	}
	sb.WriteString(`</subsonic-response>`)
	return []byte(sb.String())
}

func albumToXMLTag(a model.SubsonicAlbum, selfClosing bool) string {
	parent := a.Parent
	if parent == "" {
		parent = a.ArtistID
	}
	if parent == "" {
		parent = "external-artist"
	}
	artistID := a.ArtistID
	if artistID == "" {
		artistID = "external-artist"
	}
	name := a.Name
	if name == "" {
		name = a.Title
	}
	if name == "" {
		name = a.Album
	}
	title := a.Title
	if title == "" {
		title = name
	}
	albumName := a.Album
	if albumName == "" {
		albumName = name
	}
	songCount := a.SongCount
	if songCount <= 0 && len(a.Song) > 0 {
		songCount = int64(len(a.Song))
	}
	dur := a.Duration
	if dur <= 0 {
		dur = int(songCount) * 180
	}
	year := a.Year
	if year <= 0 {
		year = 2026
	}
	closing := "/>"
	if !selfClosing {
		closing = ">"
	}
	return fmt.Sprintf(
		`<album id="%s" parent="%s" name="%s" title="%s" album="%s" artist="%s" artistId="%s" coverArt="%s" songCount="%d" duration="%d" playCount="%d" created="2026-01-01T00:00:00.000Z" year="%d" genre="%s" isDir="true"%s`,
		xmlEscape(a.ID), xmlEscape(parent), xmlEscape(name), xmlEscape(title), xmlEscape(albumName),
		xmlEscape(a.Artist), xmlEscape(artistID), xmlEscape(a.CoverArt), songCount, dur, a.PlayCount,
		year, xmlEscape(a.Genre), closing,
	)
}

// InjectVirtualPlaylistsXML inserisce le playlist virtuali nella risposta XML di getPlaylists.view.
func InjectVirtualPlaylistsXML(navidromeXML []byte, vps []VirtualPlaylist) []byte {
	var tags strings.Builder
	for _, vp := range vps {
		tags.WriteString(fmt.Sprintf(
			`<playlist id="%s" name="%s" songCount="%d" duration="%d" public="true" owner="%s" created="%s" changed="%s"/>`,
			xmlEscape(vp.ID), xmlEscape(vp.Name), vp.SongCount, vp.Duration, xmlEscape(vp.Owner), vp.Created, vp.Changed,
		))
	}
	xmlStr := string(navidromeXML)
	if strings.Contains(xmlStr, "</playlists>") {
		xmlStr = strings.Replace(xmlStr, "</playlists>", tags.String()+"</playlists>", 1)
		return []byte(xmlStr)
	}
	if strings.Contains(xmlStr, "<playlists/>") {
		xmlStr = strings.Replace(xmlStr, "<playlists/>", "<playlists>"+tags.String()+"</playlists>", 1)
		return []byte(xmlStr)
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1" type="navidrome" serverVersion="0.64.2" openSubsonic="true"><playlists>` + tags.String() + `</playlists></subsonic-response>`)
}

// InjectVirtualPlaylistsJSON inserisce le playlist virtuali nella risposta JSON di getPlaylists.view.
func InjectVirtualPlaylistsJSON(navidromeJSON []byte, vps []VirtualPlaylist) []byte {
	var raw map[string]any
	if err := json.Unmarshal(navidromeJSON, &raw); err != nil {
		raw = map[string]any{
			"subsonic-response": map[string]any{
				"status": "ok", "version": "1.16.1", "type": "navidrome", "serverVersion": "0.64.2", "openSubsonic": true,
			},
		}
	}
	sr, _ := raw["subsonic-response"].(map[string]any)
	if sr == nil {
		sr = map[string]any{"status": "ok", "version": "1.16.1"}
		raw["subsonic-response"] = sr
	}
	plContainer, _ := sr["playlists"].(map[string]any)
	if plContainer == nil {
		plContainer = map[string]any{}
		sr["playlists"] = plContainer
	}
	existing, _ := plContainer["playlist"].([]any)
	for _, vp := range vps {
		existing = append(existing, map[string]any{
			"id": vp.ID, "name": vp.Name, "songCount": vp.SongCount, "duration": vp.Duration,
			"public": vp.Public, "owner": vp.Owner, "created": vp.Created, "changed": vp.Changed,
		})
	}
	plContainer["playlist"] = existing
	out, _ := json.Marshal(raw)
	return out
}

// WrapVirtualPlaylistXML serializza una playlist virtuale usando <entry .../> per ogni brano come richiesto da Amperfy.
func WrapVirtualPlaylistXML(vp VirtualPlaylist) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1" type="navidrome" serverVersion="0.64.2" openSubsonic="true">`)
	sb.WriteString(fmt.Sprintf(
		`<playlist id="%s" name="%s" songCount="%d" duration="%d" public="true" owner="%s" created="%s" changed="%s">`,
		xmlEscape(vp.ID), xmlEscape(vp.Name), vp.SongCount, vp.Duration, xmlEscape(vp.Owner), vp.Created, vp.Changed,
	))
	for _, s := range vp.Songs {
		sb.WriteString(songToXMLNamedTag("entry", s))
	}
	sb.WriteString(`</playlist></subsonic-response>`)
	return []byte(sb.String())
}

// WrapVirtualPlaylistJSON costruisce la risposta JSON Subsonic per getPlaylist.view di una playlist virtuale.
func WrapVirtualPlaylistJSON(vp VirtualPlaylist) any {
	return map[string]any{
		"subsonic-response": map[string]any{
			"status":        "ok",
			"version":       "1.16.1",
			"type":          "navidrome",
			"serverVersion": "0.64.2",
			"openSubsonic":  true,
			"playlist":      vp,
		},
	}
}

func batchItemToSubsonicAlbum(item itunesBatchItem) model.SubsonicAlbum {
	artURL := strings.ReplaceAll(item.ArtworkURL100, "100x100bb", "600x600bb")
	year := 2026
	if len(item.ReleaseDate) >= 4 {
		if y, err := strconv.Atoi(item.ReleaseDate[:4]); err == nil {
			year = y
		}
	}
	return model.SubsonicAlbum{
		ID:        fmt.Sprintf("external-%d", item.CollectionID),
		Parent:    fmt.Sprintf("external-%d", item.ArtistID),
		Album:     item.CollectionName,
		Title:     item.CollectionName,
		Name:      item.CollectionName,
		IsDir:     true,
		CoverArt:  "external-" + url.QueryEscape(artURL),
		SongCount: item.TrackCount,
		Created:   time.Now(),
		ArtistID:  fmt.Sprintf("external-%d", item.ArtistID),
		Artist:    "(external) " + item.ArtistName,
		Year:      year,
		Genre:     item.PrimaryGenreName,
	}
}

func batchItemToSubsonicSong(item itunesBatchItem) model.SubsonicSong {
	artURL := strings.ReplaceAll(item.ArtworkURL100, "100x100bb", "600x600bb")
	dur := item.TrackTimeMillis / 1000
	if dur <= 0 {
		dur = 180
	}
	size := (dur * 256000) / 8
	if size <= 0 {
		size = 5000000
	}
	albumID := fmt.Sprintf("external-%d", item.CollectionID)
	extArtist := "(external) " + item.ArtistName
	return model.SubsonicSong{
		ID:            fmt.Sprintf("external-%d", item.TrackID),
		Parent:        albumID,
		Title:         item.TrackName,
		Artist:        extArtist,
		DisplayArtist: extArtist,
		ArtistID:      fmt.Sprintf("external-%d", item.ArtistID),
		Album:         item.CollectionName,
		AlbumID:       albumID,
		Genre:         item.PrimaryGenreName,
		CoverArt:      "external-" + url.QueryEscape(artURL),
		Duration:      dur,
		Size:          size,
		Suffix:        "mp3",
		ContentType:   "audio/mpeg",
		Type:          "music",
		MediaType:     "song",
		Created:       time.Now(),
	}
}
