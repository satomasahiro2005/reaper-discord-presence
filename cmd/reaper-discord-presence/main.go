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

// VstEntry registers a plugin so that, when it is the top FX on the selected
// track, the presence can show its own icon and a download button. Match is a
// case-insensitive substring tested against REAPER's raw FX name.
type VstEntry struct {
	Match       string `json:"match"`
	Label       string `json:"label"`       // shown on line 3 instead of the raw name
	ImageKey    string `json:"imageKey"`    // small-badge art-asset key (optional)
	DownloadUrl string `json:"downloadUrl"` // becomes button 2 (optional)
}

type Config struct {
	ClientID       string `json:"clientId"`
	LargeImageKey  string `json:"largeImageKey"`
	LargeImageText string `json:"largeImageText"`
	PollIntervalMs int    `json:"pollIntervalMs"`
	StaleAfterMs   int    `json:"staleAfterMs"`

	// AwayAfterMs switches the presence to an "away" status after this much
	// REAPER inactivity (no playback, cursor move, or edit). 0 disables it.
	// Coming back from away resets the elapsed (play) timer to 0.
	AwayAfterMs int    `json:"awayAfterMs"`
	AwayText    string `json:"awayText"` // line 3 while away (e.g. "Away")

	// Deprecated alias for AwayAfterMs (kept so older configs still work).
	HideAfterIdleMs int `json:"hideAfterIdleMs"`

	// Line templates — change these to change what each line shows.
	// Placeholders: {title} {version} {emoji} {transport} {fx} {fxOrTransport}
	// {bpm}. A placeholder with no value becomes empty, and empty segments
	// between " · " separators are dropped automatically.
	DetailsFormat string `json:"detailsFormat"` // line 2; default "{title}"
	StateFormat   string `json:"stateFormat"`   // line 3; default "{emoji} {fxOrTransport} · {bpm}"

	ShowElapsed bool `json:"showElapsed"`

	// SmallImageByTransport overlays a small badge keyed by transport state on
	// the large image. Requires art assets keyed "play"/"pause"/"record"/"stop"
	// in the Developer Portal; if they're missing the badge is silently skipped.
	// A matched VST's ImageKey takes priority over the transport badge.
	SmallImageByTransport bool `json:"smallImageByTransport"`

	// Registered plugins (see VstEntry).
	Vsts []VstEntry `json:"vsts"`

	// Up to two profile buttons (visible to OTHER users viewing your profile).
	// Leave a label/url empty to omit that button. A matched VST's DownloadUrl
	// overrides button 2.
	Button1Label string `json:"button1Label"`
	Button1Url   string `json:"button1Url"`
	Button2Label string `json:"button2Label"`
	Button2Url   string `json:"button2Url"`
}

func defaultConfig() Config {
	return Config{
		ClientID:        "YOUR_DISCORD_APPLICATION_ID",
		LargeImageKey:   "reaper",
		LargeImageText:  "", // empty -> auto (REAPER title-bar text)
		PollIntervalMs:  2000,
		StaleAfterMs:    10000,
		AwayAfterMs:     600000, // 10 min of inactivity -> "away"; 0 disables
		AwayText:        "Away",

		DetailsFormat: "REAPER v{version}", // robust: from reaper.GetAppVersion(). Use "{title}" to mirror the title bar (adds the license line, but reads the window title).
		StateFormat:   "{emoji} {fxOrTransport} · {bpm}",

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
	if cfg.AwayAfterMs == 0 && cfg.HideAfterIdleMs > 0 {
		cfg.AwayAfterMs = cfg.HideAfterIdleMs // migrate the old hideAfterIdleMs key
	}
	if cfg.AwayText == "" {
		cfg.AwayText = "Away"
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
	App         string  `json:"app"`
	Version     string  `json:"version"`
	Transport   string  `json:"transport"`
	Bpm         float64 `json:"bpm"`
	Fx          string  `json:"fx"`          // raw name of the top FX on the selected track
	Srate       float64 `json:"srate"`       // audio sample rate in Hz (e.g. 48000)
	Bufsize     int     `json:"bufsize"`     // audio block size in samples (e.g. 128)
	IdleSeconds float64 `json:"idleSeconds"` // seconds since the last REAPER activity
	Timestamp   float64 `json:"timestamp"`
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

// formatSrate renders a sample rate in kHz ("48kHz", "44.1kHz"), "" if unknown.
func formatSrate(hz float64) string {
	if hz <= 0 {
		return ""
	}
	return strconv.FormatFloat(hz/1000, 'f', -1, 64) + "kHz"
}

// cleanFxName turns REAPER's raw FX name into a friendly plugin name:
// "VSTi: Serum (Xfer Records)" -> "Serum", "JS: ReaEQ" -> "ReaEQ".
func cleanFxName(raw string) string {
	name := raw
	if i := strings.Index(name, ": "); i >= 0 && i <= 9 { // strip "VST3i: " etc.
		name = name[i+2:]
	}
	if strings.HasSuffix(name, ")") { // strip trailing " (vendor)"
		if j := strings.LastIndex(name, " ("); j > 0 {
			name = name[:j]
		}
	}
	return strings.TrimSpace(name)
}

// renderTemplate substitutes {placeholders} and then tidies the result: empty
// segments between " · " separators are dropped, and whitespace is collapsed.
// So "{emoji} {fxOrTransport} · {bpm}" with no BPM renders "▶️ Serum".
func renderTemplate(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	if strings.Contains(out, "·") {
		segs := strings.Split(out, "·")
		kept := segs[:0]
		for _, s := range segs {
			if strings.TrimSpace(s) != "" {
				kept = append(kept, strings.TrimSpace(s))
			}
		}
		out = strings.Join(kept, " · ")
	}
	return strings.TrimSpace(strings.Join(strings.Fields(out), " "))
}

// matchVst returns the registered VST whose Match substring appears in the raw
// FX name (case-insensitive), or nil.
func matchVst(cfg Config, rawFx string) *VstEntry {
	if rawFx == "" {
		return nil
	}
	lower := strings.ToLower(rawFx)
	for i := range cfg.Vsts {
		if cfg.Vsts[i].Match != "" && strings.Contains(lower, strings.ToLower(cfg.Vsts[i].Match)) {
			return &cfg.Vsts[i]
		}
	}
	return nil
}

// buildActivity turns a status + config into a Discord activity, plus a dedupe
// key (the meaningful, timestamp-independent content) used to avoid resending
// an unchanged presence.
func buildActivity(cfg Config, st Status, sessionStart int64) (*activity, string) {
	version := st.Version
	if version == "" {
		version = "?"
	}
	// {title}: REAPER's actual title-bar text (version + license string), with a
	// fallback if the window can't be read.
	title := readReaperTitle()
	if title == "" {
		title = "REAPER v" + version
	}

	emoji, word, smallKey := transportInfo(st.Transport)
	matched := matchVst(cfg, st.Fx)

	// {fx}: the registered VST label, else the cleaned top-FX name ("" if none).
	fxLabel := ""
	if matched != nil && matched.Label != "" {
		fxLabel = matched.Label
	} else {
		fxLabel = cleanFxName(st.Fx)
	}
	fxOrTransport := fxLabel
	if fxOrTransport == "" {
		fxOrTransport = word
	}
	bpm := ""
	if st.Bpm > 0 {
		bpm = formatBpm(st.Bpm) + " BPM"
	}

	// The two text lines are produced from user-editable templates. Deliberately
	// no project file name is exposed to any placeholder.
	bufsize := ""
	if st.Bufsize > 0 {
		bufsize = strconv.Itoa(st.Bufsize) + " spls"
	}
	vars := map[string]string{
		"title": title, "version": version,
		"emoji": emoji, "transport": word,
		"fx": fxLabel, "fxOrTransport": fxOrTransport, "bpm": bpm,
		"srate": formatSrate(st.Srate), "bufsize": bufsize,
	}
	detailsFmt := cfg.DetailsFormat
	if detailsFmt == "" {
		detailsFmt = "{title}"
	}
	stateFmt := cfg.StateFormat
	if stateFmt == "" {
		stateFmt = "{emoji} {fxOrTransport} · {bpm}"
	}
	details := renderTemplate(detailsFmt, vars)
	if details == "" {
		details = title
	}
	state := renderTemplate(stateFmt, vars)

	// Large image hover text: the title-bar string, falling back to the config.
	largeText := cfg.LargeImageText
	if largeText == "" {
		largeText = title
	}

	// Small badge: a matched VST's icon wins; otherwise the transport badge.
	ass := &assets{LargeImage: cfg.LargeImageKey, LargeText: largeText}
	if matched != nil && matched.ImageKey != "" {
		ass.SmallImage = matched.ImageKey
		ass.SmallText = matched.Label
	} else if cfg.SmallImageByTransport {
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

	// Buttons (shown to OTHER users viewing your profile, not yourself).
	if cfg.Button1Label != "" && cfg.Button1Url != "" {
		act.Buttons = append(act.Buttons, button{Label: cfg.Button1Label, Url: cfg.Button1Url})
	}
	// Button 2: a matched VST's download link overrides the configured button 2.
	b2l, b2u := cfg.Button2Label, cfg.Button2Url
	if matched != nil && matched.DownloadUrl != "" {
		lbl := matched.Label
		if lbl == "" {
			lbl = "plugin"
		}
		b2l, b2u = "Get "+lbl, matched.DownloadUrl
	}
	if b2l != "" && b2u != "" {
		if len(b2l) > 32 {
			b2l = b2l[:32]
		}
		act.Buttons = append(act.Buttons, button{Label: b2l, Url: b2u})
	}

	key := strings.Join([]string{
		details, state, ass.LargeImage, ass.LargeText, ass.SmallImage, ass.SmallText,
		cfg.Button1Label, cfg.Button1Url, b2l, b2u,
	}, "\x00")
	return act, key
}

// buildAwayActivity is shown after AwayAfterMs of inactivity: the configured
// away text on line 3 (no FX/BPM), with the timer counting the away duration.
// The normal play timer is reset to 0 when the user comes back.
func buildAwayActivity(cfg Config, st Status, awayStart int64) (*activity, string) {
	version := st.Version
	if version == "" {
		version = "?"
	}
	title := readReaperTitle()
	if title == "" {
		title = "REAPER v" + version
	}
	vars := map[string]string{
		"title": title, "version": version,
		"emoji": "", "transport": "", "fx": "", "fxOrTransport": "", "bpm": "", "srate": "", "bufsize": "",
	}
	detailsFmt := cfg.DetailsFormat
	if detailsFmt == "" {
		detailsFmt = "REAPER v{version}"
	}
	details := renderTemplate(detailsFmt, vars)
	if details == "" {
		details = title
	}
	state := cfg.AwayText
	if state == "" {
		state = "Away"
	}
	largeText := cfg.LargeImageText
	if largeText == "" {
		largeText = title
	}
	act := &activity{
		Type:    0,
		Details: details,
		State:   state,
		Assets:  &assets{LargeImage: cfg.LargeImageKey, LargeText: largeText},
	}
	if cfg.ShowElapsed && awayStart > 0 {
		act.Timestamps = &timestamps{Start: awayStart}
	}
	if cfg.Button1Label != "" && cfg.Button1Url != "" {
		act.Buttons = append(act.Buttons, button{Label: cfg.Button1Label, Url: cfg.Button1Url})
	}
	if cfg.Button2Label != "" && cfg.Button2Url != "" {
		act.Buttons = append(act.Buttons, button{Label: cfg.Button2Label, Url: cfg.Button2Url})
	}
	key := "AWAY\x00" + details + "\x00" + state + "\x00" + cfg.LargeImageKey
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
		sessionStart int64      // unix MILLIS the current play timer started; 0 when not running
		away         bool       // currently showing the "away" status
		awayStart    int64      // unix MILLIS the away period started
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
			away = false
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

		// Away mode: after AwayAfterMs of inactivity (no playback, cursor move,
		// or edit), show the "away" status instead of the normal one. Returning
		// from away resets the play timer to 0.
		isAway := cfg.AwayAfterMs > 0 && st.IdleSeconds*1000 >= float64(cfg.AwayAfterMs)
		var act *activity
		var key string
		if isAway {
			if !away {
				away = true
				awayStart = time.Now().UnixMilli()
				log.Printf("away (idle %.0fs)", st.IdleSeconds)
			}
			act, key = buildAwayActivity(cfg, st, awayStart)
		} else {
			if away || sessionStart == 0 {
				// Start the play timer on first appearance, and restart it (from
				// 0) whenever the user comes back from away.
				if away {
					log.Printf("back from away; play timer reset")
				}
				away = false
				sessionStart = time.Now().UnixMilli()
			}
			act, key = buildActivity(cfg, st, sessionStart)
		}

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
