package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	NavidromeBase          string
	Port                   string
	MusicLibraryPath       string
	GiuliaMusicLibraryPath string
	NavidromeDBPath        string
	YTDLPPath              string
	MetadataProvider       string
	Country                string
	Limit                  int
}

func LoadConfig() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found or error loading it")
	}

	base := os.Getenv("NAVIDROME_BASE")
	if base == "" {
		log.Fatal("NAVIDROME_BASE env var is required (e.g., http://localhost:4533)")
	}

	libPath := getEnv("MUSIC_LIBRARY_PATH", "/music")
	giuliaLibPath := getEnv("GIULIA_MUSIC_LIBRARY_PATH", "/music-giulia")
	var limit = 10
	if l := getEnv("RESULTS_PER_PAGE", "10"); l != "" {
		if parsed, err := strconv.ParseInt(l, 10, 8); err == nil {
			limit = int(parsed)
		}
	}

	return &Config{
		NavidromeBase:          base,
		Port:                   getEnv("PORT", "8080"),
		MusicLibraryPath:       libPath,
		GiuliaMusicLibraryPath: giuliaLibPath,
		NavidromeDBPath:        getEnv("NAVIDROME_DB_PATH", ""),
		YTDLPPath:              getEnv("YTDLP_PATH", "yt-dlp"),
		MetadataProvider:       getEnv("METADATA_PROVIDER", "aggregator"),
		Country:                getEnv("COUNTRY", "IT"),
		Limit:                  limit,
	}, nil
}

// LibraryPathForUser restituisce il percorso della libreria musicale associata all'utente.
func (c *Config) LibraryPathForUser(username string) string {
	if strings.EqualFold(username, "giulia") && c.GiuliaMusicLibraryPath != "" {
		return c.GiuliaMusicLibraryPath
	}
	return c.MusicLibraryPath
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
