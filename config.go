package main

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
)

// Config is everything the server needs. The VoltKit secret stays on this server.
type Config struct {
	AppKey  string // what apps send in X-App-Key
	Host    string
	Port    int
	BaseURL string // VoltKit controller, https://...
	APIKey  string // VoltKit tenant secret
}

// loadDotEnv reads KEY=VALUE lines from path into the environment without
// overriding variables that are already set. A missing file is fine.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
}

// readConfig validates the environment (getenv is injectable for tests) and
// returns a Config, or an error naming the variable to fix.
func readConfig(getenv func(string) string) (Config, error) {
	c := Config{
		AppKey:  getenv("LAB_KEY"),
		BaseURL: strings.TrimRight(getenv("VOLTKIT_RTC_URL"), "/"),
		APIKey:  getenv("VOLTKIT_RTC_SECRET"),
		Host:    getenv("HOST"),
	}
	if len(c.AppKey) < 16 {
		return c, errors.New("Set LAB_KEY: the key your app sends (16+ characters).")
	}
	if !strings.HasPrefix(c.BaseURL, "https://") {
		return c, errors.New("Set VOLTKIT_RTC_URL, e.g. https://rtc.voltkit.in")
	}
	if c.APIKey == "" {
		return c, errors.New("Set VOLTKIT_RTC_SECRET: your VoltKit API secret.")
	}
	if c.Host == "" {
		c.Host = "0.0.0.0"
	}
	portText := getenv("PORT")
	if portText == "" {
		portText = "7000"
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return c, errors.New("PORT must be 1–65535.")
	}
	c.Port = port
	return c, nil
}
