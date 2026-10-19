"use strict";
// A rolling live fMP4 window. Original fragment timestamps share the PCM clock.
window.DesktopVideo = class extends window.LiveVideo {
  constructor(video,descriptor,session) {
    super(video,{...descriptor,duration:Infinity},session);
    this.nextInfo=0;
    this.info=null;
  }
  tick(position) {
    this.target=Math.max(0,position);
    if (this.busy || this.closed) return;
    this.busy=true;
    this.fillLive().catch(error => {if (!this.closed) this.error=error.message;}).finally(() => {this.busy=false;});
  }
  async fillLive() {
    if (this.closed) return;
    if (!this.info || performance.now()>=this.nextInfo) {
      this.nextInfo=performance.now()+250;
      const response=await fetch(this.descriptor.url+"?session="+encodeURIComponent(this.session)+"&info=1",{cache:"no-store",signal:this.controller.signal});
      if (response.status===503) return;
      if (!response.ok) throw new Error(await response.text());
      this.info=await response.json();
    }
    const info=this.info;
    const first=Math.max(info.first,Math.min(info.last,info.first+Math.floor((this.target-info.firstTime)/this.step)));
    for(let index=first;index<=Math.min(info.last,first+2);index++) {
      if (this.closed) return;
      if(this.loaded.has(index)) continue;
      const response=await fetch(this.descriptor.url+"?session="+encodeURIComponent(this.session)+"&segment="+index,{cache:"no-store",signal:this.controller.signal});
      if(response.status===410){this.nextInfo=0;return;}
      if(!response.ok)throw new Error(await response.text());
      const data=await response.arrayBuffer();
      await this.open;
      if(this.closed)return;
      if(!this.buffer) {
        const mime='video/mp4; codecs="'+info.codec+'"';
        if(!this.MSE.isTypeSupported(mime))throw new Error("This browser cannot decode the PC screen stream.");
        this.buffer=this.media.addSourceBuffer(mime);this.buffer.mode="segments";this.media.duration=Infinity;
      }
      const oldEnd=Math.max(0,this.target-4);
      if(this.buffer.buffered.length && this.buffer.buffered.start(0)<oldEnd)await this.remove(0,oldEnd);
      await this.event(this.buffer,"updateend",() => this.buffer.appendBuffer(data));
      this.loaded.add(index);this.error="";
      for(const old of this.loaded)if(old<info.first || old<first-8)this.loaded.delete(old);
    }
  }
};
