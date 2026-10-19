"use strict";
// Small fragmented-MP4 chunks, encoded once by the server and shared by rooms.
window.LiveVideo = class {
  constructor(video, descriptor, session) {
    const MSE = window.MediaSource || window.ManagedMediaSource;
    if (!MSE) throw new Error("This browser cannot play live-transcoded video. Try a browser with Media Source support.");
    this.video = video;
    this.descriptor = descriptor;
    this.duration = descriptor.pictureDuration || descriptor.duration;
    this.step = descriptor.chunkSeconds;
    this.session = session;
    this.MSE = MSE;
    this.media = new MSE();
    this.controller = new AbortController();
    this.closed = false;
    this.busy = false;
    this.error = "";
    this.target = 0;
    this.loaded = new Set();
    this.open = this.event(this.media, "sourceopen");
    // Avoid an unhandled rejection if the source is replaced before its first tick.
    this.open.catch(() => {});
    this.objectURL = URL.createObjectURL(this.media);
    video.disableRemotePlayback = true;
    video.src = this.objectURL;
  }

  event(object, name, action) {
    return new Promise((resolve, reject) => {
      const done = () => {cleanup();resolve();};
      const failed = () => {cleanup();reject(new Error("Video buffer could not decode this stream."));};
      const aborted = () => {cleanup();reject(new Error("Video source replaced"));};
      const cleanup = () => {object.removeEventListener(name,done);object.removeEventListener("error",failed);this.controller.signal.removeEventListener("abort",aborted);};
      object.addEventListener(name,done,{once:true});object.addEventListener("error",failed,{once:true});this.controller.signal.addEventListener("abort",aborted,{once:true});
      if (this.controller.signal.aborted) aborted();
      else if (action) {try {action();} catch(error) {cleanup();reject(error);}}
    });
  }

  contains(position) {
    const ranges = this.buffer?.buffered;
    if (!ranges) return false;
    for (let i=0;i<ranges.length;i++) if (position >= ranges.start(i)-0.025 && position < ranges.end(i)-0.015) return true;
    return false;
  }

  tick(position) {
    this.target = Math.max(0,Math.min(position,this.duration-0.04));
    if (!this.busy && !this.closed) {
      this.busy = true;
      this.fill().catch(error => {if (!this.closed) this.error=error.message;}).finally(() => {this.busy=false;});
    }
  }

  async remove(start,end) {
    if (end<=start) return;
    await this.event(this.buffer,"updateend",() => this.buffer.remove(start,end));
  }

  async fill() {
    if (this.closed) return;
    const first = Math.floor(this.target/this.step);
    // Keep no more than two chunks ahead; seeking requests its destination directly.
    const last = Math.min(first+2,Math.ceil(this.duration/this.step)-1);
    const indices=[];for(let index=first;index<=last;index++)indices.push(index);
    const loopHead=this.descriptor.loop && this.target>=this.duration-8;
    if(loopHead){indices.push(0);if(this.duration>this.step)indices.push(1);}
    const downloads=new Map();
    for(const index of new Set(indices)) {
      const sample=Math.min(index*this.step+0.1,this.duration-0.04);
      if(this.loaded.has(index)&&this.contains(sample))continue;
      const download=(async()=>{
        const response=await fetch(this.descriptor.url+"?session="+encodeURIComponent(this.session)+"&segment="+index,{cache:"no-store",signal:this.controller.signal});
        if(!response.ok)throw new Error(await response.text());
        return {codec:response.headers.get("X-Video-Codec"),data:await response.arrayBuffer()};
      })();
      download.catch(()=>{});downloads.set(index,download);
    }
    for (const index of new Set(indices)) {
      if (this.closed) return;
      const sample=Math.min(index*this.step+0.1,this.duration-0.04);
      if (this.loaded.has(index) && this.contains(sample)) continue;
      const {codec,data}=await downloads.get(index);
      if (this.closed) return;
      await this.open;
      const mime='video/mp4; codecs="'+codec+'"';
      if (!this.MSE.isTypeSupported(mime)) throw new Error("This browser does not support the live video codec.");
      if (!this.buffer) {
        this.codec=codec;this.buffer=this.media.addSourceBuffer(mime);
        this.buffer.mode="segments";this.media.duration=this.duration;
      } else if (this.codec!==codec) {
        if (!this.buffer.changeType) throw new Error("This browser cannot change the live video format.");
        this.buffer.changeType(mime);this.codec=codec;
      }
      // Every independently encoded chunk starts at zero; place it on the movie timeline.
      this.buffer.timestampOffset=index*this.step;
      this.buffer.appendWindowStart=0;
      this.buffer.appendWindowEnd=Math.min((index+1)*this.step,this.duration);
      this.buffer.appendWindowStart=index*this.step;
      await this.trim();
      await this.event(this.buffer,"updateend",() => this.buffer.appendBuffer(data));
      this.loaded.add(index);this.error="";
      await this.trim();
      // A waking/backgrounded browser may have moved far ahead while this was encoding.
      if (Math.floor(this.target/this.step)!==first) return;
    }
  }

  async trim() {
    const oldEnd=Math.max(0,Math.floor((this.target-8)/this.step)*this.step);
    const futureStart=Math.min(this.duration,Math.ceil((this.target+12)/this.step)*this.step);
    const keepHead=this.descriptor.loop && this.target>=this.duration-8;
    const oldStart=keepHead ? Math.min(2*this.step,oldEnd) : 0;
    let ranges=this.buffer.buffered;
    if(ranges.length && ranges.start(0)<oldEnd) await this.remove(oldStart,oldEnd);
    ranges=this.buffer.buffered;
    if(ranges.length && ranges.end(ranges.length-1)>futureStart) await this.remove(futureStart,this.duration);
    for(const old of this.loaded) if((old*this.step<oldEnd && !(keepHead && old<2)) || old*this.step>=futureStart)this.loaded.delete(old);
  }

  destroy() {
    this.closed=true;this.controller.abort();URL.revokeObjectURL(this.objectURL);
  }
};
