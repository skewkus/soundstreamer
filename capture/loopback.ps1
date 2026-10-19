$ErrorActionPreference = 'Stop'
$source = @'
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Runtime.InteropServices;
using System.Threading;

[ComImport, Guid("A95664D2-9614-4F35-A746-DE8DB63617E6"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
interface IMMDeviceEnumerator {
    [PreserveSig] int EnumAudioEndpoints(int flow, uint mask, out IntPtr devices);
    [PreserveSig] int GetDefaultAudioEndpoint(int flow, int role, out IMMDevice device);
    [PreserveSig] int GetDevice([MarshalAs(UnmanagedType.LPWStr)] string id, out IMMDevice device);
    [PreserveSig] int RegisterEndpointNotificationCallback(IntPtr client);
    [PreserveSig] int UnregisterEndpointNotificationCallback(IntPtr client);
}
[ComImport, Guid("D666063F-1587-4E43-81F1-B948E807363F"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
interface IMMDevice {
    [PreserveSig] int Activate(ref Guid iid, uint context, IntPtr parameters, [MarshalAs(UnmanagedType.IUnknown)] out object instance);
    [PreserveSig] int OpenPropertyStore(uint access, out IntPtr store);
    [PreserveSig] int GetId(out IntPtr id);
    [PreserveSig] int GetState(out uint state);
}
[ComImport, Guid("1CB9AD4C-DBFA-4C32-B178-C2F568A703B2"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
interface IAudioClient {
    [PreserveSig] int Initialize(int mode, uint flags, long duration, long periodicity, IntPtr format, IntPtr session);
    [PreserveSig] int GetBufferSize(out uint frames);
    [PreserveSig] int GetStreamLatency(out long latency);
    [PreserveSig] int GetCurrentPadding(out uint frames);
    [PreserveSig] int IsFormatSupported(int mode, IntPtr format, out IntPtr closest);
    [PreserveSig] int GetMixFormat(out IntPtr format);
    [PreserveSig] int GetDevicePeriod(out long normal, out long minimum);
    [PreserveSig] int Start();
    [PreserveSig] int Stop();
    [PreserveSig] int Reset();
    [PreserveSig] int SetEventHandle(IntPtr handle);
    [PreserveSig] int GetService(ref Guid iid, [MarshalAs(UnmanagedType.IUnknown)] out object instance);
}
[ComImport, Guid("C8ADBD64-E71E-48A0-A4DE-185C395CD317"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
interface IAudioCaptureClient {
    [PreserveSig] int GetBuffer(out IntPtr data, out uint frames, out uint flags, out ulong devicePosition, out ulong clockPosition);
    [PreserveSig] int ReleaseBuffer(uint frames);
    [PreserveSig] int GetNextPacketSize(out uint frames);
}
public static class SoundStreamerLoopback {
    static void Check(int result) { Marshal.ThrowExceptionForHR(result); }
    public static void Run() {
        IMMDeviceEnumerator enumerator = null;
        IMMDevice device = null;
        IAudioClient audio = null;
        IAudioCaptureClient capture = null;
        IntPtr format = IntPtr.Zero;
        try {
            enumerator = (IMMDeviceEnumerator)Activator.CreateInstance(Type.GetTypeFromCLSID(new Guid("BCDE0395-E52F-467C-8E3D-C4579291692E")));
            Check(enumerator.GetDefaultAudioEndpoint(0, 0, out device));
            object instance;
            Guid audioId = typeof(IAudioClient).GUID;
            Check(device.Activate(ref audioId, 23, IntPtr.Zero, out instance));
            audio = (IAudioClient)instance;
            Check(audio.GetMixFormat(out format));
            int formatSize = 18 + (ushort)Marshal.ReadInt16(format, 16);
            int sampleRate = Marshal.ReadInt32(format, 4);
            int blockAlign = (ushort)Marshal.ReadInt16(format, 12);
            if (sampleRate < 8000 || sampleRate > 384000 || blockAlign < 1 || formatSize > 4096)
                throw new InvalidOperationException("Unsupported Windows output format.");
            byte[] waveFormat = new byte[formatSize];
            Marshal.Copy(format, waveFormat, 0, formatSize);
            Check(audio.Initialize(0, 0x20000, 1000000, 0, format, IntPtr.Zero));
            Guid captureId = typeof(IAudioCaptureClient).GUID;
            Check(audio.GetService(ref captureId, out instance));
            capture = (IAudioCaptureClient)instance;
            Stream output = Console.OpenStandardOutput();
            BinaryWriter writer = new BinaryWriter(output);
            writer.Write(new byte[] {82,73,70,70}); writer.Write(uint.MaxValue);
            writer.Write(new byte[] {87,65,86,69,102,109,116,32}); writer.Write(formatSize); writer.Write(waveFormat);
            writer.Write(new byte[] {100,97,116,97}); writer.Write(uint.MaxValue); writer.Flush();
            Check(audio.Start());
            Queue<byte[]> packets = new Queue<byte[]>();
            int offset = 0, queued = 0;
            long writtenFrames = 0;
            Stopwatch clock = Stopwatch.StartNew();
            // Keep a continuous timeline even when Windows sends no silent packets.
            // A short source buffer absorbs capture packet delivery jitter.
            const int sourceBufferMs = 30;
            while (true) {
                uint frames;
                Check(capture.GetNextPacketSize(out frames));
                while (frames > 0) {
                    IntPtr data; uint flags; ulong position, timestamp;
                    Check(capture.GetBuffer(out data, out frames, out flags, out position, out timestamp));
                    byte[] packet = new byte[checked((int)frames * blockAlign)];
                    try { if ((flags & 2) == 0) Marshal.Copy(data, packet, 0, packet.Length); }
                    finally { Check(capture.ReleaseBuffer(frames)); }
                    packets.Enqueue(packet); queued += packet.Length;
                    // Bound native capture backlog to 200ms after a stalled reader.
                    while (queued > sampleRate * blockAlign / 5 && packets.Count > 1) {
                        queued -= packets.Peek().Length - offset; packets.Dequeue(); offset = 0;
                    }
                    Check(capture.GetNextPacketSize(out frames));
                }
                long dueFrames = Math.Max(0, clock.ElapsedMilliseconds - sourceBufferMs) * sampleRate / 1000;
                int framesPerTick = Math.Max(1, sampleRate / 100);
                while (writtenFrames + framesPerTick <= dueFrames) {
                    byte[] tick = new byte[framesPerTick * blockAlign];
                    int filled = 0;
                    while (filled < tick.Length && packets.Count > 0) {
                        byte[] packet = packets.Peek();
                        int count = Math.Min(tick.Length - filled, packet.Length - offset);
                        Buffer.BlockCopy(packet, offset, tick, filled, count);
                        filled += count; offset += count; queued -= count;
                        if (offset == packet.Length) { packets.Dequeue(); offset = 0; }
                    }
                    writer.Write(tick); writer.Flush(); writtenFrames += framesPerTick;
                }
                Thread.Sleep(3);
            }
        } finally {
            if (audio != null) audio.Stop();
            if (format != IntPtr.Zero) Marshal.FreeCoTaskMem(format);
            if (capture != null) Marshal.ReleaseComObject(capture);
            if (audio != null) Marshal.ReleaseComObject(audio);
            if (device != null) Marshal.ReleaseComObject(device);
            if (enumerator != null) Marshal.ReleaseComObject(enumerator);
        }
    }
}
'@
Add-Type -TypeDefinition $source
try { [SoundStreamerLoopback]::Run() }
catch { [Console]::Error.WriteLine("PC audio capture failed: " + $_.Exception.Message); exit 1 }
