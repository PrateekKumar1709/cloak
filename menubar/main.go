// Command cloak-menubar is an optional macOS/Linux/Windows menubar companion
// for Cloak. It shows a live "protected / on-device" counter and quick actions.
//
// It is a self-contained module (separate go.mod) so the core `cloak` binary
// stays dependency-free. Build with: make menubar
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/getlantern/systray"
)

const dashboard = "http://127.0.0.1:7777"

type stats struct {
	Requests      int `json:"requests"`
	LocalAnswered int `json:"local_answered"`
	Categories    map[string]int `json:"categories"`
}

func main() {
	systray.Run(onReady, func() {})
}

func onReady() {
	systray.SetIcon(shieldIcon())
	systray.SetTitle("🛡 Cloak")
	systray.SetTooltip("Cloak: the privacy layer for AI")

	mStatus := systray.AddMenuItem("Connecting…", "Live protection status")
	mStatus.Disable()
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("Open dashboard", "Open the Cloak dashboard")
	mQuit := systray.AddMenuItem("Quit menubar", "Quit this menubar app (Cloak keeps running)")

	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			if st, ok := fetchStats(); ok {
				redactions := 0
				for _, n := range st.Categories {
					redactions += n
				}
				systray.SetTitle(fmt.Sprintf("🛡 %d", redactions))
				mStatus.SetTitle(fmt.Sprintf("%d protected · %d on-device · %d redacted",
					st.Requests, st.LocalAnswered, redactions))
			} else {
				systray.SetTitle("🛡 -")
				mStatus.SetTitle("Cloak not running")
			}
			<-ticker.C
		}
	}()

	for {
		select {
		case <-mOpen.ClickedCh:
			openBrowser(dashboard)
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func fetchStats() (stats, bool) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(dashboard + "/api/stats")
	if err != nil {
		return stats{}, false
	}
	defer resp.Body.Close()
	var st stats
	if json.NewDecoder(resp.Body).Decode(&st) != nil {
		return stats{}, false
	}
	return st, true
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, url)...).Start()
}

// shieldIcon draws a small lemon-green rounded square as PNG bytes.
func shieldIcon() []byte {
	const s = 22
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	accent := color.RGBA{0xc4, 0xf0, 0x42, 0xff}
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			// rounded corners
			if (x < 3 || x > s-4) && (y < 3 || y > s-4) {
				continue
			}
			img.Set(x, y, accent)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
