package metadata

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GerardPolloRebozado/navifetch/src/model"
	"github.com/GerardPolloRebozado/navifetch/src/util"
	"github.com/torabit/itunes"
)

type ItunesProvider struct {
	client  *itunes.Client
	country string
	limit   int
	cache   sync.Map
}

func NewItunesProvider(country string, limit int) *ItunesProvider {
	return &ItunesProvider{
		client:  itunes.New(),
		country: country,
		limit:   limit,
	}
}

func (p *ItunesProvider) SearchSongs(ctx context.Context, query string) ([]model.SubsonicSong, error) {
	log.Printf("Searching iTunes for %s", query)

	res, err := p.client.Search(ctx, itunes.Term(query), itunes.Limit(p.limit), itunes.Media("music"),
		itunes.Entity("song"), itunes.Country(p.country))
	if err != nil {
		return nil, err
	}
	subsonicSongs := make([]model.SubsonicSong, 0)
	for _, song := range res.Results {
		subsonicSongs = append(subsonicSongs, p.ItunesSongToSubsonicSong(song))
	}

	return subsonicSongs, nil
}

func (p *ItunesProvider) SearchAlbums(ctx context.Context, query string) ([]model.SubsonicAlbum, error) {
	res, err := p.client.Search(ctx, itunes.Term(query), itunes.Limit(p.limit), itunes.Media("music"),
		itunes.Entity("album"), itunes.Country(p.country))
	if err != nil {
		return nil, err
	}
	albums := make([]model.SubsonicAlbum, 0)
	for _, album := range res.Results {
		albums = append(albums, p.ItunesAlbumToSubsonicAlbum(album))
	}

	return albums, nil
}

func (p *ItunesProvider) GetAlbumSongs(ctx context.Context, albumID string) ([]model.SubsonicSong, error) {
	parsedId, err := strconv.ParseInt(albumID, 10, 64)
	if err != nil {
		return nil, err
	}
	res, err := p.client.Lookup(ctx, itunes.ID(parsedId), itunes.Entity("song"), itunes.Country(p.country))
	if err != nil {
		return nil, err
	}
	songs := make([]model.SubsonicSong, 0)
	for _, song := range res.Results {
		if song.WrapperType == "track" || song.TrackId != 0 {
			songs = append(songs, p.ItunesSongToSubsonicSong(song))
		}
	}
	return songs, nil
}

// GetSong recupera i metadati del brano dalla cache in memoria oppure tramite lookup su iTunes.
func (p *ItunesProvider) GetSong(ctx context.Context, id string) (*model.SubsonicSong, error) {
	cleanID := strings.TrimPrefix(id, "external-")
	if val, ok := p.cache.Load(id); ok {
		song := val.(model.SubsonicSong)
		return &song, nil
	}
	if val, ok := p.cache.Load(cleanID); ok {
		song := val.(model.SubsonicSong)
		return &song, nil
	}

	parsedId, err := strconv.ParseInt(cleanID, 10, 64)
	if err != nil {
		return nil, err
	}
	res, err := p.client.Lookup(ctx, itunes.ID(parsedId), itunes.Entity("song"), itunes.Country(p.country))
	if err != nil {
		return nil, err
	}
	if len(res.Results) == 0 {
		return nil, fmt.Errorf("song not found")
	}
	song := p.ItunesSongToSubsonicSong(res.Results[0])
	return &song, nil
}

func (p *ItunesProvider) GetCoverArt(ctx context.Context, id string, _ int64) ([]byte, string, error) {
	unescaped, err := url.QueryUnescape(id)
	if err == nil && (strings.HasPrefix(unescaped, "http://") || strings.HasPrefix(unescaped, "https://")) {
		body, _, contentType, err := util.HTTPGet(ctx, unescaped, nil)
		return body, contentType, err
	}

	cleanID := strings.TrimPrefix(id, "external-")
	parsedId, err := strconv.ParseInt(cleanID, 10, 64)
	if err != nil {
		return nil, "", err
	}
	res, err := p.client.Lookup(ctx, itunes.ID(parsedId), itunes.Entity("song"), itunes.Country(p.country))
	if err != nil {
		return nil, "", err
	}
	if len(res.Results) == 0 {
		return nil, "", fmt.Errorf("song not found")
	}
	coverURL := strings.ReplaceAll(res.Results[0].ArtworkUrl100, "100x100bb", "600x600bb")
	body, _, contentType, err := util.HTTPGet(ctx, coverURL, nil)
	return body, contentType, err
}

func (p *ItunesProvider) GetAlbum(ctx context.Context, id string) (*model.SubsonicAlbum, error) {
	cleanID := strings.TrimPrefix(id, "external-")
	parsedId, err := strconv.ParseInt(cleanID, 10, 64)
	if err != nil {
		return nil, err
	}
	res, err := p.client.Lookup(ctx, itunes.ID(parsedId), itunes.Entity("album"), itunes.Country(p.country))
	if err != nil {
		return nil, err
	}
	if len(res.Results) == 0 {
		return nil, fmt.Errorf("album not found")
	}
	album := p.ItunesAlbumToSubsonicAlbum(res.Results[0])
	return &album, nil
}

// ItunesSongToSubsonicSong converte un risultato iTunes in SubsonicSong con copertina HD e dimensione valida per Amperfy.
func (p *ItunesProvider) ItunesSongToSubsonicSong(rec itunes.Result) model.SubsonicSong {
	artURL := strings.ReplaceAll(rec.ArtworkUrl100, "100x100bb", "600x600bb")
	duration := rec.TrackTimeMillis / 1000
	if duration <= 0 {
		duration = 180
	}
	size := (duration * 256000) / 8
	if size <= 0 {
		size = 5000000
	}

	song := model.SubsonicSong{
		Parent:                fmt.Sprintf("external-%d", rec.CollectionId),
		ID:                    fmt.Sprintf("external-%d", rec.TrackId),
		Title:                 "(external) " + rec.TrackName,
		Artist:                rec.ArtistName,
		ArtistID:              fmt.Sprintf("external-%d", rec.ArtistId),
		Album:                 rec.CollectionName,
		AlbumID:               fmt.Sprintf("external-%d", rec.CollectionId),
		Genre:                 rec.PrimaryGenreName,
		CoverArt:              "external-" + url.QueryEscape(artURL),
		Duration:              duration,
		Size:                  size,
		IsDir:                 false,
		IsVideo:               false,
		Suffix:                "mp3",
		ContentType:           "audio/mpeg",
		TranscodedSuffix:      "mp3",
		TranscodedContentType: "audio/mpeg",
		Type:                  "music",
		MediaType:             "song",
		Created:               time.Now(),
		ChannelCount:          2,
		BitDepth:              16,
		SamplingRate:          44100,
		Bpm:                   1,
		Comment:               "itunes",
		SortName:              rec.TrackName,
		DisplayArtist:         rec.ArtistName,
		DisplayAlbumArtist:    rec.ArtistName,
		DisplayComposer:       rec.ArtistName,
		ExplicitStatus:        "clean",
	}
	p.cache.Store(song.ID, song)
	p.cache.Store(strings.TrimPrefix(song.ID, "external-"), song)
	return song
}

func (p *ItunesProvider) ItunesAlbumToSubsonicAlbum(rec itunes.Result) model.SubsonicAlbum {
	artURL := strings.ReplaceAll(rec.ArtworkUrl100, "100x100bb", "600x600bb")
	return model.SubsonicAlbum{
		ID:        fmt.Sprintf("external-%d", rec.CollectionId),
		Parent:    fmt.Sprintf("external-%d", rec.ArtistId),
		Album:     rec.CollectionName,
		Title:     rec.CollectionName,
		Name:      rec.CollectionName,
		IsDir:     true,
		CoverArt:  "external-" + url.QueryEscape(artURL),
		SongCount: int64(rec.TrackCount),
		Created:   time.Now(),
		ArtistID:  fmt.Sprintf("external-%d", rec.ArtistId),
		Artist:    rec.ArtistName,
		Genre:     rec.PrimaryGenreName,
	}
}
