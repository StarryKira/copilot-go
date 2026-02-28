package copilot

import (
	"io"
	"net/http"
	"regexp"
	"time"
)

const (
	aurURL            = "https://aur.archlinux.org/cgit/aur.git/plain/PKGBUILD?h=visual-studio-code-bin"
	fallbackVSCodeVer = "1.104.3"
)

func GetVSCodeVersion() string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(aurURL)
	if err != nil {
		return fallbackVSCodeVer
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fallbackVSCodeVer
	}

	re := regexp.MustCompile(`pkgver=([0-9]+\.[0-9]+\.[0-9]+)`)
	matches := re.FindSubmatch(body)
	if len(matches) < 2 {
		return fallbackVSCodeVer
	}
	return string(matches[1])
}
