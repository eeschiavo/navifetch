package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/GerardPolloRebozado/navifetch/src/model"
	"github.com/GerardPolloRebozado/navifetch/src/util"
)

type AggregatorProvider struct {
	itunes   Provider
	mb       Provider
	limit    int
	cache    sync.Map
	coverMap sync.Map
}

func NewAggregatorProvider(country string, limit int) *AggregatorProvider {
	return &AggregatorProvider{
		itunes: NewItunesProvider(country, limit),
		mb:     NewMusicBrainzProvider(limit),
		limit:  limit,
	}
}

// SearchSongs cerca i brani su iTunes e ripiega su YouTube solo se iTunes non restituisce risultati.
func (a *AggregatorProvider) SearchSongs(ctx context.Context, query string) ([]model.SubsonicSong, error) {
	var songs []model.SubsonicSong
	seenIDs := make(map[string]bool)

	itunesSongs, err := a.itunes.SearchSongs(ctx, query)
	if err == nil {
		for _, s := range itunesSongs {
			if !seenIDs[s.ID] {
				seenIDs[s.ID] = true
				songs = append(songs, s)
				a.storeSongInCache(s, s.CoverArt)
			}
		}
	}

	if len(itunesSongs) == 0 {
		ytSongs := a.searchYouTube(ctx, query)
		for _, s := range ytSongs {
			if !seenIDs[s.ID] {
				seenIDs[s.ID] = true
				songs = append(songs, s)
				a.storeSongInCache(s, "")
			}
		}
	}

	return songs, nil
}

// CacheSong salva un brano nella cache dell'aggregatore e del provider iTunes sottostante.
func (a *AggregatorProvider) CacheSong(s model.SubsonicSong) {
	a.storeSongInCache(s, s.CoverArt)
	a.itunes.CacheSong(s)
}

func (a *AggregatorProvider) storeSongInCache(s model.SubsonicSong, coverArt string) {
	a.cache.Store(s.ID, s)
	if strings.HasPrefix(s.ID, "external-") {
		a.cache.Store(strings.TrimPrefix(s.ID, "external-"), s)
	}
	if coverArt != "" {
		a.coverMap.Store(s.ID, coverArt)
	}
}

type YTDLPFlatItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Channel    string `json:"channel"`
	Uploader   string `json:"uploader"`
	Duration   int64  `json:"duration"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

// searchYouTube cerca brani su YouTube tramite yt-dlp quando iTunes non trova corrispondenze.
func (a *AggregatorProvider) searchYouTube(ctx context.Context, query string) []model.SubsonicSong {
	cmdCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	searchArg := fmt.Sprintf("ytsearch%d:%s", 5, query)
	cmd := exec.CommandContext(cmdCtx, "yt-dlp", "--flat-playlist", "--dump-json", searchArg)

	output, err := cmd.Output()
	if err != nil {
		log.Printf("yt-dlp search error for query '%s': %v", query, err)
		return nil
	}

	lines := strings.Split(string(output), "\n")
	var songs []model.SubsonicSong
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item YTDLPFlatItem
		if err := json.Unmarshal([]byte(line), &item); err != nil || item.ID == "" {
			continue
		}
		songs = append(songs, a.ytdlpItemToSubsonicSong(item))
	}
	return songs
}

func (a *AggregatorProvider) ytdlpItemToSubsonicSong(item YTDLPFlatItem) model.SubsonicSong {
	artist := item.Channel
	if artist == "" {
		artist = item.Uploader
	}
	if artist == "" {
		artist = "Unknown Artist"
	}

	cleanID := "yt-" + item.ID
	externalID := "external-" + cleanID
	if len(item.Thumbnails) > 0 {
		coverURL := item.Thumbnails[len(item.Thumbnails)-1].URL
		if coverURL != "" {
			a.coverMap.Store(externalID, coverURL)
			a.coverMap.Store(cleanID, coverURL)
		}
	}

	dur := item.Duration
	if dur <= 0 {
		dur = 180
	}
	size := item.Duration * 32000
	if size <= 0 {
		size = 5000000
	}

	extArtist := "(external) " + artist
	return model.SubsonicSong{
		ID:                    externalID,
		Parent:                "external-yt-album",
		Title:                 item.Title,
		Artist:                extArtist,
		ArtistID:              "external-yt-artist-" + item.ID,
		DisplayArtist:         extArtist,
		Album:                 "YouTube",
		AlbumID:               "external-yt-album",
		CoverArt:              externalID,
		Duration:              dur,
		Size:                  size,
		IsDir:                 false,
		ContentType:           "audio/mpeg",
		Suffix:                "mp3",
		TranscodedSuffix:      "mp3",
		TranscodedContentType: "audio/mpeg",
		Type:                  "music",
		MediaType:             "song",
		Created:               time.Now(),
		MusicBrainzId:         cleanID,
	}
}

func (a *AggregatorProvider) SearchAlbums(ctx context.Context, query string) ([]model.SubsonicAlbum, error) {
	return a.itunes.SearchAlbums(ctx, query)
}

func (a *AggregatorProvider) GetAlbumSongs(ctx context.Context, albumID string) ([]model.SubsonicSong, error) {
	return a.itunes.GetAlbumSongs(ctx, albumID)
}

// GetSong recupera il brano dalla cache dell'aggregatore, da iTunes o costruisce un fallback YouTube.
func (a *AggregatorProvider) GetSong(ctx context.Context, id string) (*model.SubsonicSong, error) {
	cleanID := strings.TrimPrefix(id, "external-")
	if val, ok := a.cache.Load("external-" + cleanID); ok {
		song := val.(model.SubsonicSong)
		return &song, nil
	}

	song, err := a.itunes.GetSong(ctx, cleanID)
	if err == nil && song != nil {
		return song, nil
	}

	if strings.HasPrefix(cleanID, "yt-") {
		ytID := strings.TrimPrefix(cleanID, "yt-")
		return &model.SubsonicSong{
			ID:                    "external-" + cleanID,
			Parent:                "external-yt-album",
			Title:                 ytID + " (external)",
			Artist:                "YouTube",
			ArtistID:              "external-yt-artist-" + ytID,
			DisplayArtist:         "YouTube",
			Album:                 "YouTube",
			AlbumID:               "external-yt-album",
			CoverArt:              "external-" + cleanID,
			Duration:              180,
			Size:                  5000000,
			IsDir:                 false,
			ContentType:           "audio/mpeg",
			Suffix:                "mp3",
			TranscodedSuffix:      "mp3",
			TranscodedContentType: "audio/mpeg",
			Type:                  "music",
			MediaType:             "song",
			Created:               time.Now(),
			MusicBrainzId:         cleanID,
		}, nil
	}

	return nil, fmt.Errorf("song not found: %s", id)
}

func (a *AggregatorProvider) GetAlbum(ctx context.Context, id string) (*model.SubsonicAlbum, error) {
	return a.itunes.GetAlbum(ctx, id)
}

func (a *AggregatorProvider) GetCoverArt(ctx context.Context, id string, size int64) ([]byte, string, error) {
	cleanID := strings.TrimPrefix(id, "external-")
	externalID := "external-" + cleanID

	unescaped, err := url.QueryUnescape(cleanID)
	if err == nil && (strings.HasPrefix(unescaped, "http://") || strings.HasPrefix(unescaped, "https://")) {
		body, _, contentType, err := util.HTTPGet(ctx, unescaped, nil)
		if err == nil {
			return body, contentType, nil
		}
	}

	if coverURLVal, ok := a.coverMap.Load(externalID); ok {
		coverURL := coverURLVal.(string)
		if coverURL != "" {
			body, _, contentType, err := util.HTTPGet(ctx, coverURL, nil)
			if err == nil {
				return body, contentType, nil
			}
		}
	}

	if strings.HasPrefix(cleanID, "yt-") {
		ytID := strings.TrimPrefix(cleanID, "yt-")
		ytCoverURL := fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", ytID)
		body, _, contentType, err := util.HTTPGet(ctx, ytCoverURL, nil)
		if err == nil {
			return body, contentType, nil
		}
	}

	return a.itunes.GetCoverArt(ctx, cleanID, size)
}
