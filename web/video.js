"use strict";
(() => {
  const video = document.getElementById("video");
  const screen = document.getElementById("screen");
  const pictureStatus = document.getElementById("picture-status");
  let controlsTimer;
  function fullscreenElement() {
    return document.fullscreenElement || document.webkitFullscreenElement || document.mozFullScreenElement || document.msFullscreenElement;
  }
  function playerFullscreen() {
    const element = fullscreenElement();
    return element === document.documentElement || element === screen || element === video || Boolean(video.webkitDisplayingFullscreen);
  }
  function updateFullscreenLayout() {
    document.body.classList.toggle("player-fullscreen",playerFullscreen());
    revealControls();
  }
  function revealControls() {
    screen.classList.remove("controls-idle");
    clearTimeout(controlsTimer);
    controlsTimer=setTimeout(() => screen.classList.add("controls-idle"),3000);
  }
  for (const event of ["pointermove","pointerdown","keydown","focusin"]) screen.addEventListener(event,revealControls);
  for (const event of ["fullscreenchange","webkitfullscreenchange","mozfullscreenchange","MSFullscreenChange"]) document.addEventListener(event,updateFullscreenLayout);
  video.addEventListener("webkitbeginfullscreen",updateFullscreenLayout);
  video.addEventListener("webkitendfullscreen",updateFullscreenLayout);
  revealControls();
  let sourceURL = "", initialized = false, resyncs = 0, lastSeek = -Infinity;
  let frameLoopStarted = false, lastFrame = null, playingRequest = false;
  let previousLive = false;
  let liveVideo = null;
  const wrap = (value, duration) => ((value % duration) + duration) % duration;
  const difference = (actual, desired, duration) => wrap(actual - desired + duration / 2, duration) - duration / 2;

  function target(at = performance.now()) {
    const station = window.SoundStreamer;
    if(station.config?.video?.live) return Math.max(0,station.mediaTimeAt(at));
    const duration = station.config?.video?.duration;
    if (!duration) return null;
    const time = station.mediaTimeAt(at);
    if (time < 0) return null;
    const position=station.config.playback && !station.config.playback.loop ? Math.min(time,Math.max(0,duration-0.04)) : wrap(time, duration);
    return Math.min(position,Math.max(0,(station.config.video.pictureDuration || duration)-0.04));
  }

  function seek(position) {
    // Never seek repeatedly while a slow device is still decoding a seek.
    if (video.seeking || performance.now() - lastSeek < 750) return false;
    video.currentTime = Math.min(position, Math.max(0, video.duration - 0.04));
    lastSeek = performance.now();
    lastFrame = null;
    resyncs++;
    document.getElementById("resyncs").textContent = resyncs;
    return true;
  }

  function startFrameLoop() {
    if (frameLoopStarted || !video.requestVideoFrameCallback) return;
    frameLoopStarted = true;
    const frame = (now, metadata) => {
      lastFrame = {mediaTime: metadata.mediaTime, at: metadata.expectedDisplayTime, rate: video.playbackRate};
      video.requestVideoFrameCallback(frame);
    };
    video.requestVideoFrameCallback(frame);
  }

  async function update() {
    const station = window.SoundStreamer;
    const config = station?.config;
    if (!config) return;
    document.body.classList.toggle("audio-mode",config.mode !== "video");
    const fullscreen = playerFullscreen();
    document.body.classList.toggle("player-fullscreen",fullscreen);
    screen.hidden = config.mode !== "video" && !fullscreen;
    screen.classList.toggle("audio-picture",config.mode !== "video");
    document.getElementById("fullscreen-artist").textContent = config.artist || "";
    document.getElementById("fullscreen-title").textContent = config.title || "Nothing playing";
    if (!config.video) {
      video.pause();
      previousLive=false;initialized=false;lastFrame=null;
      // Some media-backed fullscreen implementations exit when load() empties
      // the video. Keep its paused source attached behind the audio title card.
      // Release it on fullscreen exit, or replace it when the next video starts.
      if (!fullscreen) {
        if (liveVideo) {liveVideo.destroy();liveVideo=null;}
        if (sourceURL) {video.removeAttribute("src");video.load();sourceURL="";}
      }
      pictureStatus.textContent = "Choose a video in admin to start watching.";
      return;
    }
    document.getElementById("mode").textContent = "Live video broadcast.";
    // Bust the native media cache when the server's clock/source resets.
    const currentSource = config.video.url + "?session=" + encodeURIComponent(config.session);
    if (sourceURL !== currentSource) {
      if (liveVideo) {liveVideo.destroy();liveVideo=null;}
      if (config.video.transcode) {
        try {liveVideo=new (config.video.live ? window.DesktopVideo : window.LiveVideo)(video,{...config.video,loop:config.playback?.loop},config.session);}
        catch(error){pictureStatus.textContent=error.message;return;}
      } else video.src = currentSource;
      sourceURL = currentSource;
      video.muted = true;
      video.loop = !config.video.transcode && (config.playback ? config.playback.loop : true);
      initialized = false;
      lastFrame = null;
    }
    if (liveVideo) {
      if(station.joined===false){pictureStatus.textContent="Join the stream to start live video.";return;}
      const wanted=target();liveVideo.tick(wanted ?? config.playback?.position ?? 0);
      if (liveVideo.error) {pictureStatus.textContent=liveVideo.error;return;}
      if (wanted!==null && !liveVideo.contains(wanted)) {if (!video.paused) video.pause();pictureStatus.textContent="Buffering live video…";return;}
    }
    const live = station.playing;
    if (!live) {
      if (!video.paused) video.pause();
      if (previousLive) { initialized = false; lastFrame = null; }
      previousLive = false;
      if (config.playback && !config.playback.playing && !config.playback.loading && video.readyState >= 1 && Number.isFinite(video.duration)) {
        const position = target();
        if (!initialized && position !== null) initialized = seek(position);
        pictureStatus.textContent = "Playback paused by admin.";
      } else pictureStatus.textContent = "Join the broadcast, then allow a moment for audio and video to buffer.";
      return;
    }
    previousLive = true;
    const desired = target();
    if (desired === null || video.readyState < 1 || (!config.video.live && !Number.isFinite(video.duration))) return;
    if (!initialized) {
      initialized = seek(desired);
    }
    if (video.seeking) { pictureStatus.textContent = "Joining the live picture…"; return; }
    if (video.paused && !playingRequest) {
      playingRequest = true;
      try { await video.play(); startFrameLoop(); }
      catch { pictureStatus.textContent = "Video could not play. Try reloading, or use an H.264 MP4."; }
      finally { playingRequest = false; }
    }
    if (video.readyState < 3) { pictureStatus.textContent = "Buffering picture…"; return; }
    const now = performance.now();
    // Frame timestamps estimate displayed picture timing where available.
    const actual = lastFrame && now - lastFrame.at < 200
      ? lastFrame.mediaTime + (now - lastFrame.at) / 1000 * lastFrame.rate
      : video.currentTime;
    const error = config.video.live ? actual-desired : difference(actual, desired, config.video.duration);
    document.getElementById("video-drift").textContent = (error * 1000).toFixed(0) + " ms";
    document.getElementById("position").textContent = desired.toFixed(1) + " s";
    // Wraps and large errors need a seek; small errors get gentle rate correction.
    const acrossLoop = (config.playback ? config.playback.loop : video.loop) && Math.abs(video.currentTime - desired) > config.video.duration / 2;
    if (Math.abs(error) > 0.25 || acrossLoop) {
      seek(desired);
      video.playbackRate = 1;
      pictureStatus.textContent = "Correcting picture timing…";
    } else {
      video.playbackRate = Math.abs(error) < 0.015 ? 1 : 1 - Math.max(-0.03, Math.min(0.03, error * 0.3));
      pictureStatus.textContent = "Watching live. Picture and sound follow the shared timeline.";
    }
  }

  document.getElementById("fullscreen").addEventListener("click", async () => {
    try {
      if (fullscreenElement()) {
        const exit = document.exitFullscreen || document.webkitExitFullscreen || document.mozCancelFullScreen || document.msExitFullscreen;
        if (exit) await exit.call(document);
      } else if (video.webkitDisplayingFullscreen && video.webkitExitFullscreen) {
        video.webkitExitFullscreen();
      } else {
        // Fullscreen a stable page, rather than the media player or its content.
        const page = document.documentElement;
        const enter = page.requestFullscreen || page.webkitRequestFullscreen || page.mozRequestFullScreen || page.msRequestFullscreen;
        if (enter) await enter.call(page);
        else pictureStatus.textContent = "Fullscreen is unavailable in this browser.";
      }
      updateFullscreenLayout();
    } catch { pictureStatus.textContent = "Fullscreen was blocked by this browser."; }
  });
  video.addEventListener("contextmenu", event => event.preventDefault());
  video.addEventListener("error", () => {
    pictureStatus.textContent = "Video unavailable or unsupported. Check the server file and use H.264 MP4.";
  });
  setInterval(() => { update().catch(() => { pictureStatus.textContent = "Rejoining video…"; initialized = false; }); }, 100);
})();
