--[[
  reaper_discord_presence.lua

  REAPER side of the "REAPER Discord Rich Presence (no Node)" setup.

  Responsibility (kept deliberately tiny):
    * Read REAPER state (version / project / transport).
    * Write it to a status JSON in the REAPER resource folder.
    * Launch the companion Go daemon (reaper-discord-presence.exe) once.

  The daemon watches the JSON and talks to Discord over local IPC.
  This script never touches Discord directly.

  Placed in:  %APPDATA%\REAPER\Scripts\reaper_discord_presence.lua
  Started by: __startup.lua  (dofile)
]]--

local res_path    = reaper.GetResourcePath()
local status_path = res_path .. "/reaper_discord_presence.json"
local exe_path    = res_path .. "/Scripts/reaper-discord-presence.exe"

-- Single-instance guard: each run bumps a shared generation token; any older
-- instance of this script sees it changed and stops on its next tick, so
-- re-running never leaves two loops racing on the status file.
local MY_GEN = tostring((tonumber(reaper.GetExtState("reaper_discord_presence", "gen")) or 0) + 1)
reaper.SetExtState("reaper_discord_presence", "gen", MY_GEN, false)

-- ---------------------------------------------------------------------------
-- helpers
-- ---------------------------------------------------------------------------

local function json_escape(s)
  s = s or ""
  s = s:gsub("\\", "\\\\")
  s = s:gsub("\"", "\\\"")
  s = s:gsub("\n", "\\n")
  s = s:gsub("\r", "\\r")
  s = s:gsub("\t", "\\t")
  return s
end

-- REAPER GetPlayState() bit flags: 1=playing, 2=paused, 4=recording.
-- Check recording first (it also sets the playing bit), then paused, then playing.
local function transport_name(state)
  if (state & 4) == 4 then return "recording" end
  if (state & 2) == 2 then return "paused"    end
  if (state & 1) == 1 then return "playing"   end
  return "stopped"
end

-- NOTE: the project file name is intentionally NOT collected or written, so it
-- never leaves this machine and never appears in the Discord presence.

local function launch_daemon()
  local f = io.open(exe_path, "rb")
  if not f then
    -- exe not deployed yet; nothing to launch.
    return
  end
  f:close()
  -- "start" detaches so REAPER is not blocked. The exe itself guards against
  -- a second instance via a lock file, so launching every REAPER start is safe.
  os.execute('start "" "' .. exe_path .. '"')
end

-- Raw name of the top FX (slot 0) on the first selected track, "" if none.
local function top_fx_name()
  local track = reaper.GetSelectedTrack(0, 0)
  if not track then return "" end
  if reaper.TrackFX_GetCount(track) < 1 then return "" end
  local _, name = reaper.TrackFX_GetFXName(track, 0, "")
  return name or ""
end

-- Audio block size (samples) has no ReaScript API, so read it from reaper.ini
-- (ASIO). Sample rate is read live from the API in the loop; ini is a fallback.
local function read_audio_ini()
  local srate, bsize = 0, 0
  local f = io.open(reaper.GetResourcePath() .. "/reaper.ini", "r")
  if f then
    for line in f:lines() do
      local s = line:match("^asio_srate=(%d+)")
      if s then srate = tonumber(s) end
      local b = line:match("^asio_bsize=(%d+)")
      if b then bsize = tonumber(b) end
    end
    f:close()
  end
  return srate, bsize
end
local ini_srate, audio_bufsize = read_audio_ini()

-- ---------------------------------------------------------------------------
-- main deferred loop
-- ---------------------------------------------------------------------------

local POLL_SECONDS = 2.0
local next_run = 0.0

-- Idle tracking: the fingerprint changes whenever the user plays, moves the
-- edit cursor, or edits the project. When it stops changing, idle time grows
-- and the daemon can hide the presence.
local last_fingerprint = nil
local last_activity = reaper.time_precise()

local function loop()
  -- Stop if a newer instance has started (single-instance guard).
  if reaper.GetExtState("reaper_discord_presence", "gen") ~= MY_GEN then return end
  local now = reaper.time_precise()
  if now >= next_run then
    next_run = now + POLL_SECONDS

    local version   = reaper.GetAppVersion()          -- e.g. "7.74/x64"
    local state     = reaper.GetPlayState()
    local transport = transport_name(state)
    local bpm       = reaper.Master_GetTempo()         -- project tempo, e.g. 120.0
    local fx        = top_fx_name()
    -- Device audio info via the proper API (matches REAPER's top-right status).
    local function devinfo(attr)
      if not reaper.GetAudioDeviceInfo then return "" end
      local ok, v = reaper.GetAudioDeviceInfo(attr, "")
      if ok then return v else return "" end
    end
    local srate = tonumber(devinfo("SRATE")) or 0
    if srate <= 0 then srate = ini_srate end
    if srate <= 0 then srate = reaper.GetSetProjectInfo(0, "PROJECT_SRATE", 0, false) end
    local bsize  = tonumber(devinfo("BSIZE")) or audio_bufsize  -- block size (samples)
    local bps    = tonumber(devinfo("BPS")) or 0                -- bits per sample
    local driver = devinfo("MODE")                              -- e.g. "ASIO"
    local n_in   = reaper.GetNumAudioInputs  and reaper.GetNumAudioInputs()  or 0
    local n_out  = reaper.GetNumAudioOutputs and reaper.GetNumAudioOutputs() or 0
    local inlat, outlat = reaper.GetInputOutputLatency()        -- latency in DEVICE samples
    local lat_in  = (srate > 0) and (inlat  / srate * 1000) or 0  -- ms
    local lat_out = (srate > 0) and (outlat / srate * 1000) or 0  -- ms

    -- MIDI input counts as activity too, so playing a controller un-idles.
    local midi_tok = "0"
    if reaper.MIDI_GetRecentInputEvent then
      local mret, _, mts = reaper.MIDI_GetRecentInputEvent(0)
      midi_tok = tostring(mret) .. ":" .. tostring(mts)
    end
    local fingerprint = string.format("%d|%.3f|%.3f|%d|%s|%s",
      state,
      reaper.GetPlayPosition(),                -- moves continuously while playing
      reaper.GetCursorPosition(),              -- moves when the edit cursor moves
      reaper.GetProjectStateChangeCount(0),    -- increments on any edit
      fx,
      midi_tok)                                -- changes when MIDI input arrives
    if fingerprint ~= last_fingerprint then
      last_fingerprint = fingerprint
      last_activity = now
    end
    local idle = now - last_activity

    local json = string.format(
      '{"app":"REAPER","version":"%s","transport":"%s","bpm":%.3f,"fx":"%s","srate":%.0f,"bufsize":%d,"bps":%d,"nIn":%d,"nOut":%d,"driver":"%s","latIn":%.1f,"latOut":%.1f,"idleSeconds":%.1f,"timestamp":%.3f}',
      json_escape(version),
      json_escape(transport),
      bpm,
      json_escape(fx),
      srate,
      bsize,
      bps,
      n_in,
      n_out,
      json_escape(driver),
      lat_in,
      lat_out,
      idle,
      now
    )

    -- Write every poll. The file's mtime doubles as a liveness heartbeat: the
    -- daemon treats a file older than staleAfterMs as "REAPER closed" and clears
    -- the presence. So we must keep touching the file even when nothing changed,
    -- otherwise an idle (stopped, unchanged project) REAPER would look dead.
    -- The daemon de-duplicates content itself before calling Discord, so a tiny
    -- ~100-byte write every 2 s is harmless. (timestamp differs each write.)
    local f = io.open(status_path, "w")
    if f then
      f:write(json)
      f:close()
    end
  end

  reaper.defer(loop)
end

-- On REAPER quit (or this script being stopped) remove the status file so the
-- daemon sees it disappear and clears the Discord presence right away, instead
-- of waiting out the staleAfterMs timeout.
reaper.atexit(function()
  os.remove(status_path)
end)

launch_daemon()
loop()
