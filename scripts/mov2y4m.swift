// mov2y4m: a clip (any QuickTime / MP4 video macOS plays) as the YUV4MPEG2
// file Say plays as its camera (-camera clip.y4m), at Say's frame size, with
// no ffmpeg:
//
//	swift scripts/mov2y4m.swift in.mov out.y4m [seconds=20] [fps=15]
//
// The picture is cut to the camera's 352x288 (CIF) from the middle, as Say
// cuts a camera's, and written 4:2:0 in full range (C420jpeg).
import AVFoundation
import CoreVideo
import Foundation

let args = CommandLine.arguments
guard args.count >= 3 else {
    FileHandle.standardError.write("usage: mov2y4m in.mov out.y4m [seconds=20] [fps=15]\n".data(using: .utf8)!)
    exit(2)
}
let seconds = args.count > 3 ? Double(args[3]) ?? 20 : 20
let fps = args.count > 4 ? Int(args[4]) ?? 15 : 15
let outW = 352, outH = 288

let asset = AVURLAsset(url: URL(fileURLWithPath: args[1]))
let sema = DispatchSemaphore(value: 0)
var track: AVAssetTrack?
Task {
    track = try? await asset.loadTracks(withMediaType: .video).first
    sema.signal()
}
sema.wait()
guard let track else {
    FileHandle.standardError.write("no video in \(args[1])\n".data(using: .utf8)!)
    exit(1)
}
var natural = CGSize.zero
var transform = CGAffineTransform.identity
Task {
    natural = (try? await track.load(.naturalSize)) ?? .zero
    transform = (try? await track.load(.preferredTransform)) ?? .identity
    sema.signal()
}
sema.wait()
let shown = natural.applying(transform)
let srcW = abs(shown.width), srcH = abs(shown.height)
// Scale so that the CIF frame is covered, then cut its middle.
let k = max(Double(outW) / srcW, Double(outH) / srcH)
let scaledW = (Int((srcW * k).rounded()) + 1) & ~1, scaledH = (Int((srcH * k).rounded()) + 1) & ~1

let reader = try AVAssetReader(asset: asset)
reader.timeRange = CMTimeRange(start: .zero, duration: CMTime(seconds: seconds, preferredTimescale: 600))
let output = AVAssetReaderTrackOutput(track: track, outputSettings: [
    kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_420YpCbCr8BiPlanarFullRange,
    kCVPixelBufferWidthKey as String: scaledW,
    kCVPixelBufferHeightKey as String: scaledH,
])
output.alwaysCopiesSampleData = false
reader.add(output)
guard reader.startReading() else {
    FileHandle.standardError.write("cannot read \(args[1]): \(String(describing: reader.error))\n".data(using: .utf8)!)
    exit(1)
}

FileManager.default.createFile(atPath: args[2], contents: nil)
let out = FileHandle(forWritingAtPath: args[2])!
out.write("YUV4MPEG2 W\(outW) H\(outH) F\(fps):1 Ip A1:1 C420jpeg\n".data(using: .utf8)!)

var next = 0.0 // the time of the next frame to keep
var written = 0
while let sample = output.copyNextSampleBuffer() {
    let t = CMSampleBufferGetPresentationTimeStamp(sample).seconds
    guard t + 1e-6 >= next, let px = CMSampleBufferGetImageBuffer(sample) else { continue }
    next += 1.0 / Double(fps)
    CVPixelBufferLockBaseAddress(px, .readOnly)
    let w = CVPixelBufferGetWidthOfPlane(px, 0), h = CVPixelBufferGetHeightOfPlane(px, 0)
    let x0 = max(0, (w - outW) / 2) & ~1, y0 = max(0, (h - outH) / 2) & ~1
    var frame = Data("FRAME\n".utf8)
    var y = [UInt8](repeating: 0, count: outW * outH)
    var cb = [UInt8](repeating: 128, count: outW * outH / 4)
    var cr = [UInt8](repeating: 128, count: outW * outH / 4)
    let yBase = CVPixelBufferGetBaseAddressOfPlane(px, 0)!.assumingMemoryBound(to: UInt8.self)
    let yStride = CVPixelBufferGetBytesPerRowOfPlane(px, 0)
    let cBase = CVPixelBufferGetBaseAddressOfPlane(px, 1)!.assumingMemoryBound(to: UInt8.self)
    let cStride = CVPixelBufferGetBytesPerRowOfPlane(px, 1)
    for row in 0..<outH where y0 + row < h {
        for col in 0..<outW where x0 + col < w {
            y[row * outW + col] = yBase[(y0 + row) * yStride + x0 + col]
        }
    }
    for row in 0..<(outH / 2) where (y0 / 2) + row < h / 2 {
        for col in 0..<(outW / 2) where (x0 / 2) + col < w / 2 {
            let o = ((y0 / 2) + row) * cStride + ((x0 / 2) + col) * 2
            cb[row * (outW / 2) + col] = cBase[o]
            cr[row * (outW / 2) + col] = cBase[o + 1]
        }
    }
    CVPixelBufferUnlockBaseAddress(px, .readOnly)
    frame.append(contentsOf: y)
    frame.append(contentsOf: cb)
    frame.append(contentsOf: cr)
    out.write(frame)
    written += 1
}
out.closeFile()
print("\(written) frames, \(outW)x\(outH) at \(fps) fps, written to \(args[2])")
