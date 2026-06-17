// reaper-discord-presence
//
// Tiny background daemon that mirrors REAPER's state (written to a JSON file by
// the companion Lua script) into a Discord Rich Presence over Discord's local
// IPC. No Node, no user token, no self-bot: it speaks the official RPC-over-IPC
// protocol that Discord exposes to native apps.
//
// Build (no console window):
//
//	go build -trimpath -ldflags "-H windowsgui -s -w" -o reaper-discord-presence.exe ./cmd/reaper-discord-presence
//
// Files (all under %APPDATA%\REAPER):
//
//	reaper_discord_presence.json          <- status, written by the Lua script
//	reaper_discord_presence_config.json   <- config (auto-created on first run)
//	reaper_discord_presence.log           <- log (no console in GUI build)
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// errHandshakeRejected means the pipe connected and Discord parsed our
// handshake but refused it (almost always a wrong/typo'd clientId), as opposed
// to Discord simply not running.
var errHandshakeRejected = errors.New("handshake rejected")

// ---------------------------------------------------------------------------
// config & status
// ---------------------------------------------------------------------------

type Config struct {
	ClientID       string `json:"clientId"`
	LargeImageKey  string `json:"largeImageKey"`
	LargeImageText string `json:"largeImageText"`
	PollIntervalMs int    `json:"pollIntervalMs"`
	StaleAfterMs   int    `json:"staleAfterMs"`

	ShowTransportState bool `json:"showTransportState"`
	ShowBpm            bool `json:"showBpm"`
	ShowElapsed        bool `json:"showElapsed"`

	// SmallImageByTransport overlays a small badge keyed by transport state on
	// the large image. Requires art assets keyed "play"/"pause"/"record"/"stop"
	// in the Developer Portal; if they're missing the badge is silently skipped.
	SmallImageByTransport bool `json:"smallImageByTransport"`

	// Up to two profile buttons (visible to OTHER users viewing your profile).
	// Leave a label/url empty to omit that button.
	Button1Label string `json:"button1Label"`
	Button1Url   string `json:"button1Url"`
	Button2Label string `json:"button2Label"`
	Button2Url   string `json:"button2Url"`
}

func defaultConfig() Config {
	return Config{
		ClientID:       "YOUR_DISCORD_APPLICATION_ID",
		LargeImageKey:  "reaper",
		LargeImageText: "", // empty -> auto (REAPER title-bar text)
		PollIntervalMs: 2000,
		StaleAfterMs:   10000,

		ShowTransportState:    true,
		ShowBpm:               true,
		ShowElapsed:           true,
		SmallImageByTransport: true,

		Button1Label: "Get REAPER",
		Button1Url:   "https://www.reaper.fm/",
	}
}

// loadConfig reads the config file, creating it from defaults if missing.
// Missing/zero numeric fields fall back to sane defaults so a hand-edited file
// can't accidentally set a 0 ms poll interval.
func loadConfig(path string) Config {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		// Create a template so the user has something to edit.
		if out, mErr := json.MarshalIndent(cfg, "", "  "); mErr == nil {
			_ = os.WriteFile(path, append(out, '\n'), 0o644)
			log.Printf("created config template: %s (set clientId)", path)
		}
		return cfg
	}
	_ = json.Unmarshal(data, &cfg) // tolerate partial/invalid: keep defaults
	if cfg.PollIntervalMs <= 0 {
		cfg.PollIntervalMs = 2000
	}
	if cfg.StaleAfterMs <= 0 {
		cfg.StaleAfterMs = 10000
	}
	if cfg.LargeImageKey == "" {
		cfg.LargeImageKey = "reaper"
	}
	return cfg
}

func (c Config) clientIDValid() bool {
	return c.ClientID != "" && c.ClientID != "YOUR_DISCORD_APPLICATION_ID"
}

type Status struct {
	App       string  `json:"app"`
	Version   string  `json:"version"`
	Transport string  `json:"transport"`
	Bpm       float64 `json:"bpm"`
	Timestamp float64 `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// Discord activity payload
// ---------------------------------------------------------------------------

type assets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
	SmallImage string `json:"small_image,omitempty"`
	SmallText  string `json:"small_text,omitempty"`
}

type timestamps struct {
	Start int64 `json:"start,omitempty"` // Unix time in MILLISECONDS (seconds -> bogus elapsed)
}

type button struct {
	Label string `json:"label"`
	Url   string `json:"url"`
}

type activity struct {
	Type       int         `json:"type"` // 0 = Playing
	Details    string      `json:"details,omitempty"`
	State      string      `json:"state,omitempty"`
	Timestamps *timestamps `json:"timestamps,omitempty"`
	Assets     *assets     `json:"assets,omitempty"`
	Buttons    []button    `json:"buttons,omitempty"`
}

// setActivityArgs is the SET_ACTIVITY command frame. Activity is a pointer so a
// nil value marshals to "activity":null, which clears the presence.
type setActivityFrame struct {
	Cmd   string `json:"cmd"`
	Args  args   `json:"args"`
	Nonce string `json:"nonce"`
}

type args struct {
	Pid      int       `json:"pid"`
	Activity *activity `json:"activity"`
}

// ---------------------------------------------------------------------------
// Discord IPC framing
//
// Each message: int32 LE opcode | int32 LE payload length | UTF-8 JSON payload.
// Opcodes: 0 Handshake, 1 Frame, 2 Close, 3 Ping, 4 Pong.
// ---------------------------------------------------------------------------

const (
	opHandshake = 0
	opFrame     = 1
	opClose     = 2
)

// minSendInterval throttles SET_ACTIVITY to stay under Discord's ~5 updates/20s
// rate limit (20s / 5 = 4s). Presence is only pushed when content changed anyway;
// this just paces a burst of rapid changes (e.g. mashing play/stop).
const minSendInterval = 4 * time.Second

func writeFrame(conn net.Conn, op int32, payload []byte) error {
	// The whole frame (8-byte header + JSON body) must be emitted in a SINGLE
	// write; splitting the header from the body can corrupt the IPC stream.
	buf := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(op))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(payload)))
	copy(buf[8:], payload)
	_, err := conn.Write(buf)
	return err
}

func readFrame(conn net.Conn, timeout time.Duration) (int32, []byte, error) {
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		defer conn.SetReadDeadline(time.Time{})
	}
	var hdr [8]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := int32(binary.LittleEndian.Uint32(hdr[0:4]))
	n := binary.LittleEndian.Uint32(hdr[4:8])
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return op, nil, err
		}
	}
	return op, payload, nil
}

// connect dials the first available Discord IPC pipe and performs the
// handshake. Returns a live connection or an error if Discord is unreachable.
func connect(clientID string) (net.Conn, error) {
	var lastErr error
	timeout := 500 * time.Millisecond
	for i := 0; i < 10; i++ {
		pipe := `\\.\pipe\discord-ipc-` + strconv.Itoa(i)
		conn, err := winio.DialPipe(pipe, &timeout)
		if err != nil {
			lastErr = err
			continue
		}
		// Handshake: opcode 0 with {"v":1,"client_id":...}.
		hs, _ := json.Marshal(map[string]any{"v": 1, "client_id": clientID})
		if err := writeFrame(conn, opHandshake, hs); err != nil {
			conn.Close()
			lastErr = err
			continue
		}
		op, payload, err := readFrame(conn, 5*time.Second)
		if err != nil {
			conn.Close()
			lastErr = err
			continue
		}
		if op == opClose {
			conn.Close()
			return nil, fmt.Errorf("%w: %s", errHandshakeRejected, string(payload))
		}
		// op == opFrame here carries the DISPATCH/READY event.
		return conn, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no discord-ipc pipe found")
	}
	return nil, lastErr
}

var nonceCounter int64

func nextNonce() string {
	nonceCounter++
	return strconv.FormatInt(nonceCounter, 10)
}

// setActivity sends a SET_ACTIVITY frame. act==nil clears the presence.
func setActivity(conn net.Conn, act *activity) error {
	frame := setActivityFrame{
		Cmd:   "SET_ACTIVITY",
		Nonce: nextNonce(),
		Args: args{
			Pid:      os.Getpid(),
			Activity: act,
		},
	}
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if err := writeFrame(conn, opFrame, b); err != nil {
		return err
	}
	// Discord replies 1:1 to SET_ACTIVITY; drain the response to keep the pipe
	// balanced and to surface a closed connection promptly.
	op, _, err := readFrame(conn, 5*time.Second)
	if err != nil {
		return err
	}
	if op == opClose {
		return fmt.Errorf("connection closed by discord")
	}
	return nil
}

// ---------------------------------------------------------------------------
// display building
// ---------------------------------------------------------------------------

// user32 calls for reading REAPER's main window title (cross-process; standard
// title bars are readable via GetWindowText without sending WM_GETTEXT).
var (
	user32                   = syscall.NewLazyDLL("user32.dll")
	procEnumWindows          = user32.NewProc("EnumWindows")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
)

// readReaperTitle returns REAPER's title-bar text from "REAPER v" onward, e.g.
// "REAPER v7.74 -Licensed for personal/small business use", or "" if no such
// window exists. This mirrors exactly what REAPER shows in its title bar.
func readReaperTitle() string {
	var found string
	cb := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		n, _, _ := procGetWindowTextLengthW.Call(hwnd)
		if n == 0 {
			return 1 // continue
		}
		buf := make([]uint16, int(n)+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		title := syscall.UTF16ToString(buf)
		if idx := strings.Index(title, "REAPER v"); idx >= 0 {
			found = title[idx:]
			return 0 // stop enumeration
		}
		return 1 // continue
	})
	procEnumWindows.Call(cb, 0)
	return found
}

// transportInfo maps the transport state to a display emoji, a word, and the
// art-asset key used for the small badge.
func transportInfo(t string) (emoji, word, smallKey string) {
	switch t {
	case "recording":
		return "⏺️", "Recording", "record" // ⏺️
	case "playing":
		return "▶️", "Playing", "play" // ▶️
	case "paused":
		return "⏸️", "Paused", "pause" // ⏸️
	default:
		return "⏹️", "Stopped", "stop" // ⏹️
	}
}

// formatBpm renders a tempo with the minimal digits ("120", "128.5").
func formatBpm(b float64) string {
	return strconv.FormatFloat(b, 'f', -1, 64)
}

// buildActivity turns a status + config into a Discord activity, plus a dedupe
// key (the meaningful, timestamp-independent content) used to avoid resending
// an unchanged presence.
func buildActivity(cfg Config, st Status, sessionStart int64) (*activity, string) {
	version := st.Version
	if version == "" {
		version = "?"
	}
	// Prefer REAPER's actual title-bar text (e.g. "REAPER v7.74 -Licensed for
	// personal/small business use") so the version line matches the title bar
	// exactly, including the license string. Fall back to the version the Lua
	// script reported if the REAPER window can't be read.
	details := readReaperTitle()
	if details == "" {
		details = "REAPER v" + version
	}

	emoji, word, smallKey := transportInfo(st.Transport)

	// Line 3 (state): transport + tempo. Deliberately NO project file name.
	var state string
	if cfg.ShowTransportState {
		state = emoji + " " + word
	}
	if cfg.ShowBpm && st.Bpm > 0 {
		bpm := formatBpm(st.Bpm) + " BPM"
		if state != "" {
			state += " · " + bpm
		} else {
			state = bpm
		}
	}

	// Large image hover text: the title-bar string, falling back to the config.
	largeText := cfg.LargeImageText
	if largeText == "" {
		largeText = details
	}

	ass := &assets{
		LargeImage: cfg.LargeImageKey,
		LargeText:  largeText,
	}
	if cfg.SmallImageByTransport {
		ass.SmallImage = smallKey // shows only if such an asset is uploaded
		ass.SmallText = word
	}

	act := &activity{
		Type:    0, // Playing
		Details: details,
		State:   state,
		Assets:  ass,
	}
	if cfg.ShowElapsed && sessionStart > 0 {
		act.Timestamps = &timestamps{Start: sessionStart}
	}

	// Up to two buttons (shown to other users viewing your profile).
	if cfg.Button1Label != "" && cfg.Button1Url != "" {
		act.Buttons = append(act.Buttons, button{Label: cfg.Button1Label, Url: cfg.Button1Url})
	}
	if cfg.Button2Label != "" && cfg.Button2Url != "" {
		act.Buttons = append(act.Buttons, button{Label: cfg.Button2Label, Url: cfg.Button2Url})
	}

	key := strings.Join([]string{
		details, state, ass.LargeImage, ass.LargeText, ass.SmallImage, ass.SmallText,
		cfg.Button1Label, cfg.Button1Url, cfg.Button2Label, cfg.Button2Url,
	}, "\x00")
	return act, key
}

// ---------------------------------------------------------------------------
// single instance (Windows named mutex; auto-released by the OS on exit, so
// there is no stale lock file to clean up after a crash)
// ---------------------------------------------------------------------------

var instanceMutex windows.Handle

func acquireSingleInstance(name string) bool {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return true // don't block startup on a name error
	}
	h, err := windows.CreateMutex(nil, false, namePtr)
	if h != 0 {
		instanceMutex = h // keep the handle for the process lifetime
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// main loop
// ---------------------------------------------------------------------------

func main() {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return
	}
	resDir := filepath.Join(appData, "REAPER")
	statusPath := filepath.Join(resDir, "reaper_discord_presence.json")
	configPath := filepath.Join(resDir, "reaper_discord_presence_config.json")
	logPath := filepath.Join(resDir, "reaper_discord_presence.log")

	// Single-instance check FIRST, before touching the log file: a rejected
	// second instance must not truncate the running instance's log.
	if !acquireSingleInstance("Global\\reaper-discord-presence") {
		return
	}

	if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}
	log.SetFlags(log.LstdFlags)
	log.Printf("reaper-discord-presence started (pid %d)", os.Getpid())

	var (
		conn         net.Conn
		lastSentKey  string
		lastSendTime time.Time // for the SET_ACTIVITY rate-limit debounce
		cleared      = true     // nothing currently shown
		sessionStart int64      // unix MILLIS REAPER (re)appeared; 0 when not running
		curClientID  string
		downLogged   bool   // throttle "Discord unreachable" logging
		badIDLogged  bool   // throttle "set clientId" logging
		rejectedID   string // a clientId Discord refused; don't retry it until it changes
	)

	closeConn := func() {
		if conn != nil {
			conn.Close()
			conn = nil
		}
		lastSentKey = ""
	}

	for {
		cfg := loadConfig(configPath)
		pollInterval := time.Duration(cfg.PollIntervalMs) * time.Millisecond
		staleAfter := time.Duration(cfg.StaleAfterMs) * time.Millisecond

		// React to a clientId change at runtime (no restart needed).
		if cfg.ClientID != curClientID {
			curClientID = cfg.ClientID
			closeConn()
			badIDLogged = false
			downLogged = false
			rejectedID = ""
		}

		if !cfg.clientIDValid() {
			if !badIDLogged {
				log.Printf("clientId not set in %s; waiting", configPath)
				badIDLogged = true
			}
			time.Sleep(pollInterval)
			continue
		}

		// Is REAPER alive? Use the status file's mtime as a heartbeat.
		fi, statErr := os.Stat(statusPath)
		fresh := statErr == nil && time.Since(fi.ModTime()) <= staleAfter

		if !fresh {
			// REAPER closed (or not started yet): clear presence once.
			if conn != nil && !cleared {
				if err := setActivity(conn, nil); err != nil {
					log.Printf("clear failed: %v", err)
					closeConn()
				} else {
					log.Printf("REAPER gone; cleared presence")
				}
				lastSendTime = time.Now()
			}
			cleared = true
			sessionStart = 0
			lastSentKey = ""
			time.Sleep(pollInterval)
			continue
		}

		// REAPER is alive. Read its state.
		data, err := os.ReadFile(statusPath)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		var st Status
		if err := json.Unmarshal(data, &st); err != nil {
			// Possibly a partial write; try again next tick.
			time.Sleep(pollInterval)
			continue
		}

		if sessionStart == 0 {
			sessionStart = time.Now().UnixMilli()
		}
		act, key := buildActivity(cfg, st, sessionStart)

		// Ensure a connection to Discord.
		if conn == nil {
			if cfg.ClientID == rejectedID {
				// Known-bad clientId: don't hammer Discord; wait for an edit.
				time.Sleep(pollInterval)
				continue
			}
			c, cErr := connect(cfg.ClientID)
			if cErr != nil {
				if errors.Is(cErr, errHandshakeRejected) {
					log.Printf("Discord rejected clientId (fix it in %s): %v", configPath, cErr)
					rejectedID = cfg.ClientID
				} else if !downLogged {
					log.Printf("Discord not reachable (is the desktop app running?): %v", cErr)
					downLogged = true
				}
				time.Sleep(pollInterval)
				continue
			}
			conn = c
			downLogged = false
			lastSentKey = "" // force a fresh send after (re)connect
			log.Printf("connected to Discord")
		}

		// Push only when content changed, and no more often than minSendInterval
		// (rate-limit debounce). The latest state is recomputed every poll, so a
		// change deferred by the debounce is sent fresh on the next eligible tick.
		if (key != lastSentKey || cleared) && time.Since(lastSendTime) >= minSendInterval {
			if err := setActivity(conn, act); err != nil {
				log.Printf("set activity failed: %v", err)
				closeConn()
				time.Sleep(pollInterval)
				continue
			}
			lastSentKey = key
			lastSendTime = time.Now()
			cleared = false
			log.Printf("presence: %s | %s", act.Details, act.State)
		}

		time.Sleep(pollInterval)
	}
}
