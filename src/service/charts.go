package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GerardPolloRebozado/navifetch/src/metadata"
	"github.com/GerardPolloRebozado/navifetch/src/model"
	"github.com/GerardPolloRebozado/navifetch/src/util"
)

const (
	appleRSSAlbumsURL = "https://rss.marketingtools.apple.com/api/v2/it/music/most-played/15/albums.json"
	appleRSSSongsURL  = "https://rss.marketingtools.apple.com/api/v2/it/music/most-played/50/songs.json"
	itunesLookupURL   = "https://itunes.apple.com/lookup"
	chartsCacheTTL    = 30 * time.Minute
)

// VirtualPlaylist rappresenta una playlist dinamica generata dalle classifiche Apple Music.
type VirtualPlaylist struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	SongCount int                  `json:"songCount"`
	Duration  int64                `json:"duration"`
	Public    bool                 `json:"public"`
	Owner     string               `json:"owner"`
	Created   string               `json:"created"`
	Changed   string               `json:"changed"`
	Songs     []model.SubsonicSong `json:"entry,omitempty"`
}

// ChartsService gestisce il recupero e la cache di classifiche e nuove uscite Apple Music Italia.
type ChartsService struct {
	metadata     metadata.Provider
	mu           sync.RWMutex
	newestAlbums []model.SubsonicAlbum
	albumsByID   map[string]model.SubsonicAlbum
	topSongs     []model.SubsonicSong
	lastUpdated  time.Time
}

func NewChartsService(meta metadata.Provider) *ChartsService {
	return &ChartsService{
		metadata:   meta,
		albumsByID: make(map[string]model.SubsonicAlbum),
	}
}

// Warmup avvia il pre-caricamento in background delle classifiche e dei nuovi album.
func (c *ChartsService) Warmup() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c.ensureFresh(ctx)
}

func (c *ChartsService) ensureFresh(ctx context.Context) {
	c.mu.RLock()
	fresh := time.Since(c.lastUpdated) < chartsCacheTTL && len(c.newestAlbums) > 0
	c.mu.RUnlock()
	if fresh {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.lastUpdated) < chartsCacheTTL && len(c.newestAlbums) > 0 {
		return
	}

	albums, albumMap, err := c.fetchNewestAlbums(ctx)
	if err != nil {
		log.Printf("ChartsService album fetch error: %v", err)
	} else if len(albums) > 0 {
		c.newestAlbums = albums
		c.albumsByID = albumMap
	}

	songs, err := c.fetchTopSongs(ctx)
	if err != nil {
		log.Printf("ChartsService top songs fetch error: %v", err)
	} else if len(songs) > 0 {
		c.topSongs = songs
	}

	if len(c.newestAlbums) > 0 || len(c.topSongs) > 0 {
		c.lastUpdated = time.Now()
	}
}

// GetNewestAlbums restituisce i 15 album più popolari/recenti da Apple Music Italia.
func (c *ChartsService) GetNewestAlbums(ctx context.Context) ([]model.SubsonicAlbum, error) {
	c.ensureFresh(ctx)
	c.mu.RLock()
	defer c.mu.RUnlock()
	res := make([]model.SubsonicAlbum, len(c.newestAlbums))
	copy(res, c.newestAlbums)
	return res, nil
}

// GetExternalAlbum restituisce un album esterno completo di tracce se presente nella cache delle classifiche.
func (c *ChartsService) GetExternalAlbum(ctx context.Context, cleanAlbumID string) (*model.SubsonicAlbum, bool) {
	c.ensureFresh(ctx)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if alb, ok := c.albumsByID[cleanAlbumID]; ok {
		copyAlb := alb
		return &copyAlb, true
	}
	return nil, false
}

// GetVirtualPlaylists restituisce le playlist virtuali Top 50 Italia e Nuove Uscite Italia.
func (c *ChartsService) GetVirtualPlaylists(ctx context.Context) []VirtualPlaylist {
	c.ensureFresh(ctx)
	c.mu.RLock()
	defer c.mu.RUnlock()

	var newReleaseSongs []model.SubsonicSong
	for _, alb := range c.newestAlbums {
		for i := 0; i < len(alb.Song) && i < 2; i++ {
			newReleaseSongs = append(newReleaseSongs, alb.Song[i])
		}
	}

	return []VirtualPlaylist{
		buildVirtualPlaylist("external-playlist-top50-it", "🍎 Top 50 Italia (Apple Music)", c.topSongs),
		buildVirtualPlaylist("external-playlist-new-it", "🆕 Nuove Uscite Italia", newReleaseSongs),
	}
}

func buildVirtualPlaylist(id, name string, songs []model.SubsonicSong) VirtualPlaylist {
	var totalDur int64
	for _, s := range songs {
		totalDur += s.Duration
	}
	return VirtualPlaylist{
		ID:        id,
		Name:      name,
		SongCount: len(songs),
		Duration:  totalDur,
		Public:    true,
		Owner:     "ernesto",
		Created:   "2026-01-01T00:00:00.000Z",
		Changed:   "2026-01-01T00:00:00.000Z",
		Songs:     songs,
	}
}

type appleRSSResponse struct {
	Feed struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	} `json:"feed"`
}

type itunesBatchLookupResponse struct {
	Results []itunesBatchItem `json:"results"`
}

type itunesBatchItem struct {
	WrapperType      string `json:"wrapperType"`
	CollectionID     int64  `json:"collectionId"`
	TrackID          int64  `json:"trackId"`
	ArtistID         int64  `json:"artistId"`
	CollectionName   string `json:"collectionName"`
	TrackName        string `json:"trackName"`
	ArtistName       string `json:"artistName"`
	PrimaryGenreName string `json:"primaryGenreName"`
	ArtworkURL100    string `json:"artworkUrl100"`
	ReleaseDate      string `json:"releaseDate"`
	TrackCount       int64  `json:"trackCount"`
	TrackTimeMillis  int64  `json:"trackTimeMillis"`
}

func fetchRSSIDs(ctx context.Context, rssURL string) ([]string, error) {
	body, _, _, err := util.HTTPGet(ctx, rssURL, nil)
	if err != nil {
		return nil, err
	}
	var rss appleRSSResponse
	if err := json.Unmarshal(body, &rss); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rss.Feed.Results))
	for _, r := range rss.Feed.Results {
		if r.ID != "" {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

func (c *ChartsService) fetchNewestAlbums(ctx context.Context) ([]model.SubsonicAlbum, map[string]model.SubsonicAlbum, error) {
	ids, err := fetchRSSIDs(ctx, appleRSSAlbumsURL)
	if err != nil || len(ids) == 0 {
		return nil, nil, fmt.Errorf("empty album RSS: %w", err)
	}
	lookupURL := fmt.Sprintf("%s?id=%s&entity=song&country=IT&limit=200", itunesLookupURL, strings.Join(ids, ","))
	body, _, _, err := util.HTTPGet(ctx, lookupURL, nil)
	if err != nil {
		return nil, nil, err
	}
	var lookup itunesBatchLookupResponse
	if err := json.Unmarshal(body, &lookup); err != nil {
		return nil, nil, err
	}
	return c.assembleAlbumsFromLookup(ids, lookup.Results)
}

func (c *ChartsService) assembleAlbumsFromLookup(orderedIDs []string, items []itunesBatchItem) ([]model.SubsonicAlbum, map[string]model.SubsonicAlbum, error) {
	albumMap := make(map[string]model.SubsonicAlbum)
	for _, item := range items {
		if item.WrapperType == "collection" && item.CollectionID != 0 {
			key := strconv.FormatInt(item.CollectionID, 10)
			albumMap[key] = batchItemToSubsonicAlbum(item)
		}
	}
	for _, item := range items {
		if item.WrapperType == "track" && item.TrackID != 0 && item.CollectionID != 0 {
			key := strconv.FormatInt(item.CollectionID, 10)
			alb, ok := albumMap[key]
			if !ok {
				continue
			}
			song := batchItemToSubsonicSong(item)
			c.metadata.CacheSong(song)
			alb.Song = append(alb.Song, song)
			alb.SongCount = int64(len(alb.Song))
			alb.Duration += int(song.Duration)
			albumMap[key] = alb
		}
	}
	ordered := make([]model.SubsonicAlbum, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if alb, ok := albumMap[id]; ok {
			albumMap["external-"+id] = alb
			ordered = append(ordered, alb)
		}
	}
	return ordered, albumMap, nil
}

func (c *ChartsService) fetchTopSongs(ctx context.Context) ([]model.SubsonicSong, error) {
	ids, err := fetchRSSIDs(ctx, appleRSSSongsURL)
	if err != nil || len(ids) == 0 {
		return nil, fmt.Errorf("empty song RSS: %w", err)
	}
	lookupURL := fmt.Sprintf("%s?id=%s&country=IT&limit=200", itunesLookupURL, strings.Join(ids, ","))
	body, _, _, err := util.HTTPGet(ctx, lookupURL, nil)
	if err != nil {
		return nil, err
	}
	var lookup itunesBatchLookupResponse
	if err := json.Unmarshal(body, &lookup); err != nil {
		return nil, err
	}
	songByID := make(map[string]model.SubsonicSong, len(lookup.Results))
	for _, item := range lookup.Results {
		if item.TrackID != 0 {
			song := batchItemToSubsonicSong(item)
			c.metadata.CacheSong(song)
			songByID[strconv.FormatInt(item.TrackID, 10)] = song
		}
	}
	ordered := make([]model.SubsonicSong, 0, len(ids))
	for _, id := range ids {
		if s, ok := songByID[id]; ok {
			ordered = append(ordered, s)
		}
	}
	return ordered, nil
}
