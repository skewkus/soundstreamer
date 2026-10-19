"use strict";
const el = id => document.getElementById(id);
let audio, config, clockOffset = 0, bestRTT = 0, clockReady = false;
let nextStart = 0, lastPacket = null, late = 0, drift = 0, calibration = 0;
let active = false, controller, clockBusy = false, lastReceived = 0;
let playbackStartsAt = Infinity;
let gain, roomMuted = false, lastSession = "";
const cookie = name => { try { return decodeURIComponent(document.cookie.split("; ").find(part => part.startsWith(name + "="))?.split("=").slice(1).join("=") || ""); } catch { return ""; } };
const saveCookie = (name, value) => { document.cookie = name + "=" + encodeURIComponent(value) + "; Path=/; Max-Age=31536000; SameSite=Strict"; };
let roomID = cookie("ss_room_id");
if (!/^[a-z0-9-]{8,64}$/.test(roomID)) { roomID = Array.from(crypto.getRandomValues(new Uint8Array(12)), b => b.toString(16).padStart(2,"0")).join(""); saveCookie("ss_room_id",roomID); }
let roomName = cookie("ss_room_name");
el("room-name").value = roomName;
el("room-label").textContent = roomName ? "This browser is " + roomName : "Give this browser a name so you can identify it in admin.";
function applyVolume() { if (gain && audio) gain.gain.setTargetAtTime(roomMuted ? 0 : (config?.volume ?? 1),audio.currentTime,0.03); }
async function reportRoom() {
  try {
    const response = await fetch("/api/room", {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({id:roomID,name:roomName,kind:config?.mode === "video" ? "watch" : "listen"}),signal:AbortSignal.timeout(3000)});
    if (!response.ok) return;
    const setting = await response.json();roomMuted = setting.muted;applyVolume();
  } catch {}
}
el("room-save").addEventListener("click", () => {roomName = el("room-name").value.trim().slice(0,80);saveCookie("ss_room_name",roomName);el("room-label").textContent = roomName ? "This browser is " + roomName : "Room name cleared";void reportRoom();});
const sources = new Set();
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));

function status(message, live = false) {
  el("status").textContent = message;
  el("light").classList.toggle("live", live);
}

async function syncClock() {
  if (clockBusy) return;
  clockBusy = true;
  try {
    const probes = [];
    for (let i = 0; i < 7; i++) {
      const before = performance.now();
      const response = await fetch("/api/clock", {cache: "no-store", signal: AbortSignal.timeout(2500)});
      if (!response.ok) throw new Error("Clock request failed");
      const data = await response.json();
      const after = performance.now();
      probes.push({rtt: after - before, offset: data.serverMs - (before + after) / 2});
      config = data;
    }
    probes.sort((a, b) => a.rtt - b.rtt);
    bestRTT = probes[0].rtt;
    const measured = probes.slice(0, 3).reduce((sum, p) => sum + p.offset, 0) / 3;
    // A server restart resets its monotonic clock; never mix old and new audio.
    const clockReset = clockReady && Math.abs(measured - clockOffset) > 250;
    if (clockReset || (lastSession && config.session !== lastSession)) {
      clearAudio();
      controller?.abort();
    }
    clockOffset = clockReset || !clockReady ? measured : clockOffset * 0.8 + measured * 0.2;
    lastSession = config.session;
    clockReady = true;
    el("title").textContent = config.title;
    if (el("artist")) el("artist").textContent = config.artist || (config.mode === "file" ? "Unknown artist" : "");
    el("mode").textContent = config.mode === "idle" ? "Waiting for media from the station." : config.mode === "video" ? "Video broadcast." : config.mode === "pc" ? "Live PC audio broadcast." : "Live music broadcast.";
    applyVolume();
    el("rooms").textContent = config.listeners;
    el("rtt").textContent = bestRTT.toFixed(1) + " ms";
    el("buffer").textContent = config.bufferMs + " ms";
  } finally {
    clockBusy = false;
  }
}

function clearAudio() {
  for (const source of sources) { try { source.stop(); } catch {} }
  sources.clear();
  nextStart = 0;
  lastPacket = null;
  playbackStartsAt = Infinity;
}

function desiredContextTime(serverMs) {
  const desiredPerformance = serverMs - clockOffset + calibration;
  const stamp = audio.getOutputTimestamp?.();
  if (stamp && stamp.contextTime > 0 && Math.abs(performance.now() - stamp.performanceTime) < 1000) {
    return stamp.contextTime + (desiredPerformance - stamp.performanceTime) / 1000;
  }
  return audio.currentTime + (desiredPerformance - performance.now()) / 1000
    - (audio.baseLatency || 0) - (audio.outputLatency || 0);
}

function schedule(view, count, format) {
  if (audio.state !== "running") throw new Error("Audio suspended");
  const at = view.getFloat64(0, true);
  const target = desiredContextTime(at);
  if (target < audio.currentTime + 0.01) {
    late++;
    return; // Never play old audio immediately: rejoin the current timeline.
  }
  // Screen audio calibration can add up to three seconds to the normal buffer.
  if (target - audio.currentTime > (config.video?.live ? 10 : 7)) throw new Error("Timeline changed");
  const contiguous = lastPacket !== null && Math.abs(at - lastPacket - 20) < 0.01;
  if (!contiguous || nextStart < audio.currentTime + 0.01) nextStart = target;
  drift = (nextStart - target) * 1000;
  if (Math.abs(drift) > 100) {
    clearAudio();
    nextStart = target;
  }
  // Small rate adjustments preserve contiguous audio while tracking clock drift.
  const rate = 1 + Math.max(-0.005, Math.min(0.005, (nextStart - target) * 0.1));
  const buffer = audio.createBuffer(format.channels, count, format.sampleRate);
  for (let channel = 0; channel < format.channels; channel++) {
    const samples = buffer.getChannelData(channel);
    for (let i = 0; i < count; i++) samples[i] = view.getInt16(12 + (i * format.channels + channel) * 2, true) / 32768;
  }
  const source = audio.createBufferSource();
  source.buffer = buffer;
  source.playbackRate.value = rate;
  source.connect(gain || audio.destination);
  if (sources.size === 0) playbackStartsAt = at - clockOffset + calibration;
  sources.add(source);
  source.onended = () => { sources.delete(source); source.disconnect(); };
  source.start(nextStart);
  nextStart += buffer.duration / rate;
  lastPacket = at;
  const playback = config.playback;
  status(playback?.loading ? "Preparing media…" : playback && !playback.playing ? "Paused / waiting for media" : roomMuted ? "Muted by admin — still live" : "Listening live", Boolean(!playback || playback.playing));
}

async function readStream(signal) {
  lastReceived = performance.now();
  const response = await fetch("/api/audio?room=" + encodeURIComponent(roomID), {cache: "no-store", signal});
  if (!response.ok || !response.body) throw new Error("Audio stream unavailable");
  // Latch the format for this HTTP response, independent of clock polling.
  const format = {channels:Number(response.headers.get("X-Audio-Channels") || config.channels || 1),sampleRate:Number(response.headers.get("X-Audio-Sample-Rate") || config.sampleRate)};
  if (![1,2].includes(format.channels) || ![48000,44100,32000,24000].includes(format.sampleRate)) throw new Error("Unsupported audio format");
  const reader = response.body.getReader();
  let pending = new Uint8Array(0);
  try {
    while (active) {
      const {done, value} = await reader.read();
      if (done) throw new Error("Stream disconnected");
      lastReceived = performance.now();
      const joined = new Uint8Array(pending.length + value.length);
      joined.set(pending); joined.set(value, pending.length);
      let position = 0;
      while (joined.length - position >= 12) {
        const view = new DataView(joined.buffer, position);
        const count = view.getUint32(8, true);
        if (count !== format.sampleRate / 50) throw new Error("Invalid audio packet");
        const size = 12 + count * format.channels * 2;
        if (joined.length - position < size) break;
        schedule(view, count, format);
        position += size;
      }
      pending = joined.slice(position);
    }
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

async function listen() {
  while (active) {
    controller = null;
    lastReceived = performance.now();
    try {
      await syncClock();
      if (!clockReady) throw new Error("Waiting for clock");
      controller = new AbortController();
      await readStream(controller.signal);
    } catch (error) {
      clearAudio();
      if (audio.state !== "running") {
        active = false;
        el("join").hidden = false;
        el("join").disabled = false;
        el("join").textContent = "Rejoin audio";
        status("Audio suspended. Tap to rejoin.");
        return;
      }
      status("Connection lost. Reconnecting…");
      await wait(1000);
    }
  }
}

el("join").addEventListener("click", async () => {
  el("join").disabled = true;
  try {
    audio ??= new AudioContext({latencyHint: "interactive"});
    if (!gain) {gain = audio.createGain();gain.connect(audio.destination);applyVolume();}
    await audio.resume();
    if (audio.state !== "running") throw new Error("Audio could not start");
    active = true;
    void reportRoom();
    el("join").hidden = true;
    status("Synchronising and buffering…");
    void listen();
  } catch (error) {
    el("join").disabled = false;
    status(error.message);
  }
});

el("offset").addEventListener("change", () => {
  calibration = Math.max(-300, Math.min(300, Number(el("offset").value) || 0));
  el("offset").value = calibration;
  clearAudio();
});
setInterval(() => {
  if (active) {
    el("queued").textContent = (Math.max(0, nextStart - audio.currentTime) * 1000).toFixed(0) + " ms";
    el("drift").textContent = drift.toFixed(1) + " ms";
    el("late").textContent = late;
    if (audio.state !== "running") controller?.abort();
    if (performance.now() - lastReceived > 4000) controller?.abort();
  }
}, 500);
setInterval(() => { if (active) syncClock().catch(() => {}); }, 1000);
setInterval(() => { if (active) void reportRoom(); }, 1500);
void reportRoom();
syncClock().catch(() => { el("title").textContent = "Station unavailable. Join to retry."; });

// Video follows the same output timeline; native video audio stays muted.
window.SoundStreamer = {
  get joined() {return active;},
  get config() { return config; },
  get playing() { return active && audio?.state === "running" && sources.size > 0 && performance.now() >= playbackStartsAt && (config.playback?.playing ?? true); },
  mediaTimeAt(performanceMs) {
    const playback = config?.playback;
    if (playback) return playback.position + (playback.playing ? Math.max(0,(performanceMs + clockOffset - calibration - drift - playback.anchorMs) / 1000) : 0);
    return (performanceMs + clockOffset - calibration - (config?.bufferMs || 0) - drift) / 1000;
  }
};
