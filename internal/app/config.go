package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/bright/reviewagent/internal/review"
)

type Config struct {
	Port          string
	WebhookSecret string
	GitHubToken   string
	LLMBaseURL    string
	LLMAPIKey     string
	LLMModel      string
	DatabaseURL   string
	AdminToken    string
	Policy        review.Policy
}

func LoadConfig() (Config, error) {
	minConfidence, err := floatValue("MIN_CONFIDENCE", .75)
	if err != nil {
		return Config{}, err
	}
	maxFiles, err := intValue("MAX_FILES", 20)
	if err != nil {
		return Config{}, err
	}
	maxPatchLines, err := intValue("MAX_PATCH_LINES", 1200)
	if err != nil {
		return Config{}, err
	}
	if minConfidence < 0 || minConfidence > 1 || maxFiles < 1 || maxPatchLines < 1 {
		return Config{}, fmt.Errorf("review policy values are out of range")
	}
	return Config{
		Port: value("PORT", "8080"), WebhookSecret: os.Getenv("WEBHOOK_SECRET"), GitHubToken: os.Getenv("GITHUB_TOKEN"), DatabaseURL: value("DATABASE_URL", "file:reviewagent.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), AdminToken: os.Getenv("ADMIN_TOKEN"),
		LLMBaseURL: value("LLM_BASE_URL", "https://api.openai.com/v1"), LLMAPIKey: os.Getenv("LLM_API_KEY"), LLMModel: value("LLM_MODEL", "gpt-4.1-mini"),
		Policy: review.Policy{MinConfidence: minConfidence, MaxFiles: maxFiles, MaxPatchLines: maxPatchLines},
	}, nil
}
func value(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
func intValue(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return n, nil
}
func floatValue(key string, fallback float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	return n, nil
}
