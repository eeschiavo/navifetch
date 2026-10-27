package service

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/GerardPolloRebozado/navifetch/src/config"
	"github.com/GerardPolloRebozado/navifetch/src/metadata"
	"github.com/GerardPolloRebozado/navifetch/src/model"
	"github.com/GerardPolloRebozado/navifetch/src/util"
)

type StreamService struct {
	cfg      *config.Config
	metadata metadata.Provider
	locks    sync.Map
}

func NewStreamService(cfg *config.Config, metadata metadata.Provider) *StreamService {
	return &StreamService{
		cfg:      cfg,
		metadata: metadata,
	}
}

// getTrackLock restituisce un mutex dedicato al singolo trackID per evitare download concorrenti dello stesso brano.
func (s *StreamService) getTrackLock(trackID string) *sync.Mutex {
	clean := strings.TrimPrefix(trackID, "external-")
	val, _ := s.locks.LoadOrStore(clean, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// DownloadTrack scarica il brano nella libreria dell'utente in modo atomico e applica i tag ID3 e la copertina.
func (s *StreamService) DownloadTrack(trackID string, username string, permanent bool) (*model.SubsonicSong, string, error) {
	mu := s.getTrackLock(trackID)
	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := s.metadata.GetSong(ctx, trackID)
	if err != nil {
		log.Printf("Download failed metadata lookup for %s: %v", trackID, err)
		return nil, "", err
	}

	libPath := s.cfg.LibraryPathForUser(username)
	artist := util.CleanExternalSuffix(res.Artist)
	album := util.CleanExternalSuffix(res.Album)
	title := util.CleanExternalSuffix(res.Title)
	targetPath := util.GetTrackPath(libPath, artist, album, title, permanent)

	if info, err := os.Stat(targetPath); err == nil && info.Size() > 0 {
		return res, targetPath, nil
	}

	log.Printf("Saving copy for user '%s' in %s", username, targetPath)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return nil, "", err
	}

	cleanMBID := strings.TrimPrefix(trackID, "external-")
	if err := s.downloadAndTagAtomically(targetPath, artist, album, title, cleanMBID, res.CoverArt); err != nil {
		return nil, "", err
	}

	log.Printf("Successfully saved: %s", targetPath)
	return res, targetPath, nil
}

// downloadAndTagAtomically scarica l'audio su file temporaneo, incorpora tag ID3 e copertina con ffmpeg e rinomina atomicamente.
func (s *StreamService) downloadAndTagAtomically(targetPath, artist, album, title, cleanMBID, coverArt string) error {
	tmpTemplate := targetPath + ".tmp.%(ext)s"
	tmpMP3 := targetPath + ".tmp.mp3"
	taggedTmpMP3 := targetPath + ".tagged.tmp.mp3"
	defer os.Remove(tmpMP3)
	defer os.Remove(taggedTmpMP3)

	searchQuery := fmt.Sprintf("ytsearch1:%s - %s Audio", artist, title)
	if strings.HasPrefix(cleanMBID, "yt-") {
		ytID := strings.TrimPrefix(cleanMBID, "yt-")
		searchQuery = fmt.Sprintf("https://www.youtube.com/watch?v=%s", ytID)
	}

	dlArgs := []string{
		"-x", "--audio-format", "mp3", "--audio-quality", "0",
		"--no-playlist", "-o", tmpTemplate, searchQuery,
	}
	if output, err := exec.Command(s.cfg.YTDLPPath, dlArgs...).CombinedOutput(); err != nil {
		log.Printf("yt-dlp failed for %s: %v\nOutput: %s", targetPath, err, string(output))
		return err
	}

	coverPath := s.downloadCoverTemp(coverArt)
	if coverPath != "" {
		defer os.Remove(coverPath)
	}

	if err := applyID3Tags(tmpMP3, taggedTmpMP3, coverPath, title, artist, album, cleanMBID); err != nil {
		log.Printf("ffmpeg tagging fallback for %s: %v", targetPath, err)
		return os.Rename(tmpMP3, targetPath)
	}
	return os.Rename(taggedTmpMP3, targetPath)
}

// downloadCoverTemp scarica l'immagine di copertina in un file temporaneo per l'incorporamento ID3.
func (s *StreamService) downloadCoverTemp(coverArt string) string {
	if coverArt == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	imgBytes, _, err := s.metadata.GetCoverArt(ctx, coverArt, 600)
	if err != nil || len(imgBytes) == 0 {
		rawURL := strings.TrimPrefix(coverArt, "external-")
		if unescaped, uErr := url.QueryUnescape(rawURL); uErr == nil {
			rawURL = unescaped
		}
		if strings.HasPrefix(rawURL, "http") {
			imgBytes, _, _, _ = util.HTTPGet(ctx, rawURL, nil)
		}
	}
	if len(imgBytes) == 0 {
		return ""
	}

	tmpCover, err := os.CreateTemp("", "cover-*.jpg")
	if err != nil {
		return ""
	}
	defer tmpCover.Close()
	_, _ = tmpCover.Write(imgBytes)
	return tmpCover.Name()
}

// applyID3Tags incorpora metadati ID3v2.3 e copertina nel file MP3 tramite ffmpeg senza ricodificare l'audio.
func applyID3Tags(inputMP3, outputMP3, coverPath, title, artist, album, mbid string) error {
	args := []string{"-y", "-i", inputMP3}
	if coverPath != "" {
		args = append(args, "-i", coverPath, "-map", "0:a", "-map", "1:v", "-c:v", "mjpeg",
			"-metadata:s:v", "title=Album cover", "-metadata:s:v", "comment=Cover (front)")
	} else {
		args = append(args, "-map", "0:a")
	}
	args = append(args,
		"-c:a", "copy", "-id3v2_version", "3",
		"-metadata", "title="+title,
		"-metadata", "artist="+artist,
		"-metadata", "album="+album,
		"-metadata", "musicbrainz_trackid="+mbid,
		outputMP3,
	)
	output, err := exec.Command("ffmpeg", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}
