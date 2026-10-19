"use strict";
(() => {
  const $ = id => document.getElementById(id);
  const isAdmin = location.pathname === "/admin", apiBase = isAdmin ? "/api/admin" : "/api/control";
  document.body.classList.toggle("control-mode",!isAdmin);
  if (!isAdmin) {$("login").hidden=true;document.querySelector('[data-tab="settings"]').remove();$("panel-settings").remove();$("freeze").remove();}
  let setupRequired=false,wasFrozen=false;
  let serverSettingsDirty = false, videoSettingsDirty = false, audioSettingsDirty = false;
  let authenticated = false, state, busy = false, polling = false, seekEditing = false;
  let libraryItems = [], libraryTotal = 0, libraryRequest = 0, searchTimer;
  let queueSignature = "", roomSignature = "", playlistSignature = "", sourceSignature = "", lastScan = null, libraryRevision = null, categorySignature = "", filterSignature = "";
  const selectedCategories={audio:"",video:""};
  let folderKind = "music", folderPath = "", folderParent = "", noticeTimer;
  const formatTime = value => { const n = Math.max(0, Math.floor(value || 0)); return n >= 3600 ? Math.floor(n / 3600) + ":" + String(Math.floor(n / 60) % 60).padStart(2,"0") + ":" + String(n % 60).padStart(2,"0") : Math.floor(n / 60) + ":" + String(n % 60).padStart(2,"0"); };
  const node = (tag, className, text) => { const e = document.createElement(tag); if (className) e.className = className; if (text !== undefined) e.textContent = text; return e; };
  const button = (text, handler, label = text) => { const b = node("button", "quiet", text); b.type = "button"; b.setAttribute("aria-label",label); b.addEventListener("click",handler); return b; };
  function categoryOptions(select,kind,value,placeholder) {
    select.replaceChildren();
    if(placeholder) {const option=node("option","",placeholder);option.value="";option.disabled=true;select.append(option);}
    for(const category of state.categories[kind] || []) {const option=node("option","",category.name);option.value=category.id;select.append(option);}
    select.value=value;
  }
  function updateCategoryFilter() {
    const kind=$("kind").value,group=kind==="audio" ? "music" : "video",categories=state.categories[group] || [];
    if(!categories.some(c => c.id===selectedCategories[kind]))selectedCategories[kind]="";
    const signature=JSON.stringify([kind,categories,selectedCategories[kind]]);
    if(signature===filterSignature)return;filterSignature=signature;
    const select=$("category-filter"),all=node("option","","All");all.value="";select.replaceChildren(all);
    for(const category of categories){const option=node("option","",category.name);option.value=category.id;select.append(option);}
    select.value=selectedCategories[kind];
  }
  function renderCategories() {
    const signature=JSON.stringify([state.categories,state.folderCategories,state.musicFolders,state.videoFolders]);
    if(signature===categorySignature)return;categorySignature=signature;
    for(const kind of ["music","video"]) {
      const list=$(kind+"-categories");list.replaceChildren();
      for(const category of state.categories[kind]) {
        const row=node("div","category-row"),controls=node("div","source-actions");
        const rename=button("Rename",() => {const name=prompt("Category name:",category.name);if(name?.trim())action("renameCategory",{kind,id:category.id,name:name.trim()});});
        const remove=button("Delete",() => action("deleteCategory",{kind,id:category.id}));
        const inUse=(state[kind+"Folders"] || []).some(path => (state.folderCategories[kind][path] || (kind==="music" ? state.categories.music[0].id : ""))===category.id);
        remove.disabled=inUse || state.categories[kind].length===1;remove.title=inUse ? "Reassign folders first" : "";
        controls.append(rename,remove);row.append(node("span","row-title",category.name),controls);list.append(row);
      }
    }
    if($("folder-dialog").open) {const select=$("folder-category"),value=select.value;categoryOptions(select,folderKind,value,"Choose a category");$("folder-select").disabled=!folderPath || !select.value;}
  }
  function notice(message, error = false) { $("notice").textContent = message; $("notice").classList.toggle("error",error); $("notice").hidden = false; clearTimeout(noticeTimer); noticeTimer = setTimeout(() => { $("notice").hidden = true; }, error ? 10000 : 3500); }
  function showLogin() { authenticated = false; $("login").hidden = !isAdmin; $("remote").hidden = true; }
  async function request(path, payload) {
    const response = await fetch(path, {cache:"no-store", signal:AbortSignal.timeout(15000), ...(payload ? {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(payload)} : {})});
    if (!response.ok) { if (response.status === 401) showLogin(); throw new Error(await response.text()); }
    return response.json();
  }
  async function action(actionName, values = {}) {
    if (!isAdmin && state?.frozen) {notice("Controls are frozen by admin",true);return false;}
    if (busy) return false;
    busy = true;
    try { render(await request(apiBase + "/action", {action:actionName,...values})); return true; }
    catch (e) { notice(e.message,true); return false; }
    finally { busy = false; }
  }
  function mediaRow(media) {
    const row = node("div","media-row"), info = node("div","row-info");
    info.append(node("span","row-title",media.kind === "audio" && state?.titleSource !== "filename" ? (media.artist || "Unknown artist") + " — " + media.title : media.title));
    const detail = node("span","row-detail"), badge = node("span","kind-badge",media.kind === "video" ? "Video" : "Music");
    detail.append(badge,document.createTextNode([media.categoryName,media.album,formatTime(media.duration)].filter(Boolean).join(" · ")));
    info.append(detail); row.append(info); return row;
  }
  function renderLibrary() {
    $("library-list").replaceChildren();
    if (!libraryItems.length) { $("library-list").append(node("p","empty",state?.libraryCount ? "No matches. Try another search." : "Choose your music or video folder in Settings to build your library.")); }
    for (const media of libraryItems) {
      const row = mediaRow(media), controls = node("div","row-actions");
      controls.append(button("Play",() => action("playNow",{id:media.id}),"Play " + media.title + " now"),button("+ Queue",async () => { if (await action("enqueue",{id:media.id})) notice("Added to queue"); },"Add " + media.title + " to queue"));
      row.append(controls); $("library-list").append(row);
    }
    $("library-status").textContent = libraryTotal ? `${libraryItems.length} of ${libraryTotal} matching items` : "";
    $("more").hidden = libraryItems.length >= libraryTotal;
  }
  async function loadLibrary(append = false) {
    if (!authenticated) return;
    const sequence = ++libraryRequest;
    const query = new URLSearchParams({q:$("search").value,kind:$("kind").value,category:$("category-filter").value,limit:"50",offset:String(append ? libraryItems.length : 0)});
    try { const data = await request(apiBase + "/library?" + query); if (sequence !== libraryRequest) return; libraryItems = append ? libraryItems.concat(data.items) : data.items; libraryTotal = data.total; renderLibrary(); applyFreeze(); }
    catch (e) { notice(e.message,true); }
  }
  function render(next) {
    if (!isAdmin && wasFrozen && !next.frozen) {document.querySelectorAll("#remote button, #remote input, #remote select").forEach(e => e.disabled=false);queueSignature="";roomSignature="";}
    state = next;
    updateCategoryFilter();
    authenticated = true; $("login").hidden = true; $("remote").hidden = false;
    $("connection").textContent = "Connected";
    $("playback-state").textContent = state.loading ? "PREPARING" : state.playing ? "NOW PLAYING" : state.current ? "PAUSED" : "READY";
    if (isAdmin) {
    $("pc-audio").hidden = !state.pcAudioAvailable;
    $("pc-screen").hidden = !state.pcAudioAvailable;
    $("pc-screen").textContent = state.pcScreen && (state.playing || state.loading) ? "Stop PC screen stream" : "Stream PC screen + audio";
 $("pc-audio").textContent = state.pcAudio && !state.pcScreen && (state.playing || state.loading) ? "Stop PC audio" : "Stream PC audio";
 $("pc-audio-hint").hidden = !state.pcAudio;
    }
 $("media-kind").textContent = state.pcScreen ? "PC screen + audio" : state.pcAudio ? "PC audio" : state.current?.kind === "video" ? "Video" : state.current ? "Music" : "Station";
    $("now-title").textContent = state.pcScreen ? "PC screen + audio" : state.pcAudio ? "PC audio" : state.current ? (state.current.kind === "audio" && state.titleSource !== "filename" ? (state.current.artist || "Unknown artist") + " — " : "") + state.current.title : "Choose something to play";
    $("now-artist").textContent = state.pcScreen ? "Live primary monitor and PC audio" : state.pcAudio ? "Live from the server’s default sound output" : [state.current?.artist,state.current?.album].filter(Boolean).join(" · ") || (state.current ? "Live throughout your home" : "Browse your library below.");
    $("play-pause").textContent = state.playing || state.loading ? (state.pcAudio ? "Stop capture" : "Pause") : "Play";
    $("seek").disabled = state.pcAudio || !state.current || state.loading;
    if (!seekEditing) { $("seek").max = state.current?.duration || 1; $("seek").value = state.position; $("elapsed").textContent = formatTime(state.position); }
    $("duration").textContent = state.pcAudio ? "Live" : formatTime(state.current?.duration);
    $("preparing").hidden = !state.loading;
    $("preparing").textContent = state.pcScreen ? "Starting screen and audio capture…" : state.pcAudio ? "Starting Windows audio capture…" : state.progress > 0 ? `Preparing video · ${Math.round(state.progress)}%. Playback starts when it is ready.` : state.current?.kind === "video" ? "Buffering video. Playback starts after the first short chunk is ready." : "Preparing media. You can pause to cancel.";
    $("queue-count").textContent = state.queue.length;
    $("shuffle").checked = state.shuffle; $("repeat").value = state.repeat;
    if (isAdmin) {
    $("theme").value = state.theme;
    if(!serverSettingsDirty) {
      $("listen-port").value=state.port;
      $("https-enabled").checked=state.httpsEnabled;
      $("https-hostname").value=state.httpsHostname || "";
      $("cert-file").value=state.certFile || "";
      $("key-file").value=state.keyFile || "";
    }
    const protocol=state.activeHTTPS ? "HTTPS" : "HTTP", savedProtocol=state.httpsEnabled ? "HTTPS" : "HTTP";
    $("port-status").textContent="Currently listening: "+protocol+" on port "+(state.activePort || state.port)+" · Saved for next restart: "+savedProtocol+" on port "+state.port;
    for(const id of ["cert-file","key-file"]) {$(id).required=$("https-enabled").checked;$(id).disabled=!$("https-enabled").checked;}
    $("https-hostname").disabled=!$("https-enabled").checked;
    if(!audioSettingsDirty) {
      $("audio-channels").value=String(state.audioChannels);
      $("audio-sample-rate").value=String(state.audioSampleRate);
    }
    const audioLabel=(channels,rate) => (channels===2 ? "Stereo" : "Mono")+" · "+(rate/1000)+" kHz · 16-bit";
    $("audio-settings-status").textContent="Active: "+audioLabel(state.activeAudioChannels,state.activeAudioSampleRate)+". Saved for next restart: "+audioLabel(state.audioChannels,state.audioSampleRate)+".";
    if(!videoSettingsDirty) {
      $("force-video-transcode").checked=state.forceVideoTranscode;
      $("video-transcode-resolution").value=state.videoTranscodeResolution;
    }
    $("video-transcode-resolution").disabled=!$("force-video-transcode").checked;
    $("title-source").value=state.titleSource;
    $("screen-fps").value = String(state.screenFPS);
    if(document.activeElement!==$("screen-audio-offset")) $("screen-audio-offset").value=state.screenAudioOffsetMs;
    const sources = JSON.stringify([state.musicFolders,state.videoFolders,state.scanning,state.scanFolder,state.categories,state.folderCategories,state.folderRecursive]);
    if (sources !== sourceSignature) {
      sourceSignature = sources;
      for (const kind of ["music","video"]) {
        const list = $(kind + "-folders"), paths = state[kind + "Folders"] || [];
        list.replaceChildren();
        if (!paths.length) list.append(node("p","hint","No folders added"));
        for (const path of paths) {
          const row = node("div","source-path");
          const controls=node("div","source-actions");
          const rescan=button(state.scanning && state.scanFolder===path ? "Rescanning…" : "Rescan",() => action("rescanFolder",{kind,path}),"Rescan folder " + path);
          const remove = button("Remove",() => action("removeSource",{kind,path}),"Remove folder " + path);
          rescan.disabled=state.scanning;remove.disabled = state.scanning;
          controls.append(rescan,remove);
          const category=node("select","category-select"),assigned=state.folderCategories[kind][path] || (kind==="music" ? state.categories.music[0].id : "");
          category.setAttribute("aria-label","Category for " + path);category.required=true;
          categoryOptions(category,kind,assigned,"Choose category (required)");
          category.addEventListener("change",async () => {if(!(await action("folderCategory",{kind,path,category:category.value})))category.value=assigned;});
          const info=node("div","source-info");info.append(node("span","row-detail",path),category);
          const recursive=node("input");recursive.type="checkbox";recursive.checked=Boolean(state.folderRecursive[kind][path]);recursive.disabled=state.scanning;
          const label=node("label","source-recursive");label.append(recursive,document.createTextNode(" Include subfolders"));
          recursive.addEventListener("change",async () => {if(!(await action("folderRecursion",{kind,path,enabled:recursive.checked})))recursive.checked=!recursive.checked;});
          info.append(label);
          row.append(info,controls);list.append(row);
        }
      }
    }
    document.querySelectorAll("[data-source]").forEach(b => b.disabled=state.scanning);
    renderCategories();
    $("resume").checked = state.resumeOnStartup;
    if (document.activeElement !== $("volume")) $("volume").value = state.volume;
    $("volume-value").textContent = Math.round(state.volume * 100) + "%";
    $("scan-status").textContent = state.scanning ? `Scanning ${state.scanFolder || "all folders"} · ${state.scanDone} of ${state.scanTotal || "…"} files` : `${state.libraryCount} items in the library` + (state.scanErrors ? ` · ${state.scanErrors} files/folders could not be read` : "");
    $("rescan").disabled = state.scanning;
    $("freeze").textContent=state.frozen ? "Unfreeze controls" : "Freeze controls";}
    if ((lastScan === true && !state.scanning) || (libraryRevision !== null && libraryRevision !== state.libraryRevision)) loadLibrary(); lastScan = state.scanning; libraryRevision=state.libraryRevision;
    const newQueue = JSON.stringify([state.titleSource,state.queue]);
    if (queueSignature !== newQueue) {
      queueSignature = newQueue; $("queue-list").replaceChildren();
      if (!state.queue.length) $("queue-list").append(node("p","empty","The queue is empty. Add songs or videos from Library."));
      state.queue.forEach((item,index) => {
        const row = mediaRow(item.media); row.classList.add("queue-row"); const controls = node("div","row-actions");
        const up = button("↑",() => action("move",{key:item.key,to:index-1}),"Move " + item.media.title + " up"); up.disabled = index === 0;
        const down = button("↓",() => action("move",{key:item.key,to:index+1}),"Move " + item.media.title + " down"); down.disabled = index === state.queue.length-1;
        controls.append(up,down,button("Remove",() => action("remove",{key:item.key})));row.append(controls);$("queue-list").append(row);
      });
    }
    const newRooms = JSON.stringify(state.rooms);
    if (roomSignature !== newRooms) {
      roomSignature = newRooms; $("room-list").replaceChildren();
      if (!state.rooms.length) $("room-list").append(node("p","empty","No rooms connected. Open /stream on a room device and join."));
      state.rooms.forEach(room => { const row = node("div","room-row"), info = node("div","row-info");info.append(node("span","row-title",room.name || "Unnamed room"),node("span","row-detail",(room.kind === "watch" ? "Watching" : "Listening") + (room.muted ? " · Muted" : " · Connected")));row.append(info,button(room.muted ? "Unmute" : "Mute",() => action("muteRoom",{id:room.id,enabled:!room.muted})));$("room-list").append(row); });
    }
    const newPlaylists = JSON.stringify(state.playlists);
    if (playlistSignature !== newPlaylists) { playlistSignature = newPlaylists;const selected = $("playlists").value;$("playlists").replaceChildren(node("option","","Choose a playlist"));$("playlists").firstChild.value = "";for (const name of state.playlists) {const option = node("option","",name);option.value = name;$("playlists").append(option);}$("playlists").value = selected; }
    if (state.error && $("notice").textContent !== state.error) notice(state.error,true);
    applyFreeze();
  }
  function applyFreeze(){
    const frozen=!isAdmin && Boolean(state?.frozen);$("frozen-banner").hidden=!frozen;
    if (frozen) document.querySelectorAll("#remote button, #remote input, #remote select").forEach(e => e.disabled=true);
    wasFrozen=frozen;
  }
  async function poll() {
    if (polling || busy) return; polling = true;
    try { const wasAuthenticated = authenticated; render(await request(apiBase + "/state"));if (!wasAuthenticated) loadLibrary(); }
    catch (e) { $("connection").textContent = authenticated ? "Reconnecting…" : "Sign in"; }
    finally { polling = false; }
  }
  async function browseFolder(path) {
    try {const data = await request("/api/admin/folders?path=" + encodeURIComponent(path));folderPath = data.path;folderParent = data.parent;$("folder-path").value = folderPath;$("folder-select").disabled = !folderPath || !$("folder-category").value;$("folder-up").disabled = !folderPath;$("folder-list").replaceChildren();for (const folder of data.folders) {const b = button("▸ " + folder.name,() => browseFolder(folder.path));b.className = "folder-item";$("folder-list").append(b);}if (!data.folders.length) $("folder-list").append(node("p","hint","No subfolders. You can select this folder."));}
    catch(e) {notice(e.message,true);}
  }
  $("login-form").addEventListener("submit",async event => {event.preventDefault();try{if(setupRequired && $("password").value!==$("setup-confirm").value)throw new Error("Passwords do not match");await request(setupRequired ? "/api/admin/setup" : "/api/admin/login",{password:$("password").value});setupRequired=false;$("password").value = "";$("setup-confirm").value="";$("login-error").textContent = "";await poll();}catch(e){$("login-error").textContent = e.message;}});
  document.querySelectorAll("[data-tab]").forEach(tab => tab.addEventListener("click",() => {document.querySelectorAll("[data-tab]").forEach(t => t.setAttribute("aria-selected",String(t === tab)));for (const name of isAdmin ? ["library","queue","rooms","settings"] : ["library","queue","rooms"]) $("panel-" + name).hidden = name !== tab.dataset.tab;}));
  $("play-pause").addEventListener("click",() => action(state.playing || state.loading ? "pause" : "play"));
  $("next").addEventListener("click",() => action("next"));$("stop").addEventListener("click",() => action("stop"));
  $("seek").addEventListener("input",() => {seekEditing = true;$("elapsed").textContent = formatTime(Number($("seek").value));});
  $("seek").addEventListener("change",async () => {await action("seek",{position:Number($("seek").value)});seekEditing = false;});
  if (isAdmin) {
  $("pc-screen").addEventListener("click",() => action(state.pcScreen && (state.playing || state.loading) ? "pause" : "pcScreen"));
  $("pc-audio").addEventListener("click",() => action(state.pcAudio && !state.pcScreen && (state.playing || state.loading) ? "pause" : "pcAudio"));
  }
  $("search").addEventListener("input",() => {clearTimeout(searchTimer);searchTimer = setTimeout(() => loadLibrary(),250);});document.querySelectorAll("[data-kind]").forEach(b => b.addEventListener("click",() => {
    $("kind").value=b.dataset.kind;
    updateCategoryFilter();
    document.querySelectorAll("[data-kind]").forEach(t => t.setAttribute("aria-pressed",String(t===b)));
    loadLibrary();
  }));$("more").addEventListener("click",() => loadLibrary(true));
  $("category-filter").addEventListener("change",() => {selectedCategories[$("kind").value]=$("category-filter").value;loadLibrary();});
  $("shuffle").addEventListener("change",() => action("shuffle",{enabled:$("shuffle").checked}));$("repeat").addEventListener("change",() => action("repeat",{repeat:$("repeat").value}));
  $("clear-queue").addEventListener("click",() => {if (confirm("Remove every item from the queue? The current track will keep playing.")) action("clearQueue");});
  $("save-playlist").addEventListener("click",() => {const name = prompt("Playlist name (saves the current track and queue):");if (name?.trim()) action("savePlaylist",{name:name.trim()});});
  $("add-playlist").addEventListener("click",() => {if ($("playlists").value) action("loadPlaylist",{name:$("playlists").value});});
  $("delete-playlist").addEventListener("click",() => {const name = $("playlists").value;if (name && confirm('Delete playlist "' + name + '"?')) action("deletePlaylist",{name});});
  if (isAdmin) {
  $("freeze").addEventListener("click",() => action("freeze",{enabled:!state.frozen}));
  $("password-form").addEventListener("submit",async event => {event.preventDefault();try{if($("new-password").value!==$("confirm-password").value)throw new Error("Passwords do not match");await request("/api/admin/password",{currentPassword:$("current-password").value,newPassword:$("new-password").value});$("password-form").reset();notice("Admin password changed");}catch(e){notice(e.message,true);}});
  document.querySelectorAll("[data-source]").forEach(b => b.addEventListener("click",() => {folderKind = b.dataset.source;$("folder-recursive").checked=false;categoryOptions($("folder-category"),folderKind,folderKind==="music" ? state.categories.music[0].id : "","Choose a category");$("folder-select").disabled=true;$("folder-title").textContent = "Select " + folderKind + " folder";$("folder-dialog").showModal();browseFolder(state[folderKind + "Folders"]?.at(-1) || "");}));
  $("folder-category").addEventListener("change",() => $("folder-select").disabled=!folderPath || !$("folder-category").value);
  for(const kind of ["music","video"])$("add-"+kind+"-category").addEventListener("submit",async event => {event.preventDefault();const input=$("new-"+kind+"-category");if(await action("addCategory",{kind,name:input.value.trim()}))input.value="";});
  $("folder-close").addEventListener("click",() => $("folder-dialog").close());$("folder-roots").addEventListener("click",() => browseFolder(""));$("folder-up").addEventListener("click",() => browseFolder(folderParent));
  $("folder-path-form").addEventListener("submit",event => {event.preventDefault();browseFolder($("folder-path").value);});
  $("folder-select").addEventListener("click",async () => {if (await action("addSource",{kind:folderKind,path:folderPath,category:$("folder-category").value,includeSubfolders:$("folder-recursive").checked})) $("folder-dialog").close();});
  $("screen-audio-offset-form").addEventListener("submit",async event => {event.preventDefault();if(await action("screenAudioOffset",{to:Number($("screen-audio-offset").value)}))notice("Screen audio offset applied and saved");});
  $("screen-audio-offset-reset").addEventListener("click",async () => {if(await action("screenAudioOffset",{to:0}))notice("Screen audio offset reset to 0");});
  $("screen-fps").addEventListener("change",() => action("screenFPS",{to:Number($("screen-fps").value)}));
  $("audio-settings-form").addEventListener("input",() => {audioSettingsDirty=true;});
  $("audio-settings-form").addEventListener("submit",async event => {event.preventDefault();if(await action("audioSettings",{audioChannels:Number($("audio-channels").value),audioSampleRate:Number($("audio-sample-rate").value)})){audioSettingsDirty=false;notice("Audio settings saved. Exit and restart Sound Streamer to apply them.");}});
  $("video-transcode-form").addEventListener("input",() => {videoSettingsDirty=true;});
  $("force-video-transcode").addEventListener("change",() => {videoSettingsDirty=true;$("video-transcode-resolution").disabled=!$("force-video-transcode").checked;});
  $("video-transcode-form").addEventListener("submit",async event => {event.preventDefault();if(await action("videoTranscodeSettings",{enabled:$("force-video-transcode").checked,name:$("video-transcode-resolution").value})){videoSettingsDirty=false;notice("Video settings saved and applied. Playing videos briefly re-buffer.");}});
  $("port-form").addEventListener("input",() => {serverSettingsDirty=true;});
  $("https-enabled").addEventListener("change",() => {serverSettingsDirty=true;$("https-hostname").disabled=!$("https-enabled").checked;for(const id of ["cert-file","key-file"]){$(id).required=$("https-enabled").checked;$(id).disabled=!$("https-enabled").checked;}});
  $("port-form").addEventListener("submit",async event => {event.preventDefault();if(await action("serverSettings",{to:Number($("listen-port").value),enabled:$("https-enabled").checked,httpsHostname:$("https-hostname").value,certFile:$("cert-file").value,keyFile:$("key-file").value})){serverSettingsDirty=false;notice("Server settings saved. Exit and restart Sound Streamer to apply them.");}});
  $("title-source").addEventListener("change",() => action("titleSource",{name:$("title-source").value}));
  $("theme").addEventListener("change",() => action("theme",{name:$("theme").value}));
  $("rescan").addEventListener("click",() => action("rescan"));
  $("volume").addEventListener("input",() => {$("volume-value").textContent = Math.round(Number($("volume").value)*100) + "%";});$("volume").addEventListener("change",() => action("volume",{value:Number($("volume").value)}));
  $("resume").addEventListener("change",() => action("resume",{enabled:$("resume").checked}));
  $("logout").addEventListener("click",async () => {try{await request("/api/admin/logout",{});showLogin();}catch(e){notice(e.message,true);}});
  request("/api/admin/auth").then(auth => {setupRequired=auth.setupRequired;$("login-title").textContent=setupRequired ? "Set up admin" : "Admin";$("login-hint").textContent=setupRequired ? "Choose your admin password (at least 8 characters)." : "Enter your admin password.";$("login-submit").textContent=setupRequired ? "Set password" : "Sign in";$("setup-confirm-wrap").hidden=!setupRequired;}).catch(e => notice(e.message,true));
  }
  setInterval(poll,1000);poll();
})();
