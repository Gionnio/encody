import Foundation
import SwiftUI

// Il motore usa chiavi snake_case: si convertono automaticamente in camelCase.
// Attenzione agli acronimi: "avg_fps" → avgFps, "has_vmaf" → hasVmaf.
enum JSONCoding {
    static func makeDecoder() -> JSONDecoder {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return d
    }

    static let decoder = makeDecoder()

    static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.keyEncodingStrategy = .convertToSnakeCase
        e.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        return e
    }()
}

// MARK: - caps

struct Caps: Decodable {
    let version: String
    let tools: ToolPaths
    let hasZscale: Bool
    let hasVmaf: Bool
    let hasXpsnr: Bool?  // motori precedenti non lo riportano
    let cvvdp: String?   // percorso di ColorVideoVDP, vuoto se non installato
    let tonemapAlgo: String
    let presets: [PresetInfo]
}

struct ToolPaths: Decodable {
    let ffmpeg: String
    let ffprobe: String
    let mkvmerge: String
    let doviTool: String
    let hdr10plusTool: String
}

struct PresetInfo: Decodable, Identifiable, Hashable {
    let id: String
    let name: String
    let type: String
    let scale: Int
    let audioBitrate: String
    let passthrough: [String]?
    let passthroughLabel: String

    var isCopy: Bool { type == "copy" }
}

// MARK: - job (stesso formato delle code esportate dalla CLI)

struct PresetRef: Codable, Hashable {
    var id: String
}

struct TrackSel: Codable, Hashable, Sendable {
    var index: Int
    var lang: String?
    var codec: String?
    var profile: String?
    var channels: Int?
    var layout: String?
    var title: String?
    var forced: Bool?
    var isDefault: Bool?

    enum CodingKeys: String, CodingKey {
        case index, lang, codec, profile, channels, layout, title, forced
        case isDefault = "default"
    }
}

struct JobSpec: Codable {
    var inputPath: String
    var outputPath: String
    var preset: PresetRef
    var metaType: String
    var dvProfile: Int?
    var colorPrimaries: String?
    var colorTransfer: String?
    var colorSpace: String?
    var videoMap: String?
    var resolution: String?
    var tonemap: Bool?
    var doInject: Bool
    var crop: String
    var audioMode: String
    var selAudio: [TrackSel]?
    var selSubs: [TrackSel]?
    var duration: Double
    var fpsStr: String
}

// MARK: - probe

struct ProbeTrack: Decodable, Identifiable {
    let index: Int
    let display: String
    let layout: String
    let bitRate: Int64
    let frames: Int64
    let imageBased: Bool
    let suggested: Bool
    let sel: TrackSel

    var id: Int { index }
}

struct ProbeResult: Decodable {
    let path: String
    let name: String
    let size: Int64
    let duration: Double
    let fps: Double
    let width: Int
    let height: Int
    let metaType: String
    let dvProfile: Int
    let audio: [ProbeTrack]
    let subs: [ProbeTrack]
    let warnings: [String]
    let job: JobSpec

    var isHDR: Bool { metaType != "SDR" && !metaType.isEmpty }
    var hasDynamicMetadata: Bool { metaType == "DV" || metaType == "HDR10+" }
}

// MARK: - plan

struct PlanTrack: Decodable, Hashable {
    let index: Int
    let label: String
    let desc: String?
}

struct JobPlan: Decodable {
    let videoLabel: String
    let audio: [PlanTrack]
    let subs: [PlanTrack]
    let canTonemap: Bool
    let canInject: Bool
    let injectBlocker: String
    let warnings: [String]
}

// MARK: - eventi NDJSON

struct BenchResult: Decodable, Identifiable, Hashable, Sendable {
    let id: String
    let name: String
    let metric: String
    let score: Double
    let detail: String?
    let size: Int64
    let fps: Double
}

struct EngineEvent: Decodable, Sendable {
    let type: String
    // coda
    let job: Int?
    let total: Int?
    let name: String?
    let input: String?
    let output: String?
    let step: String?
    let percent: Double?
    let fps: Double?
    let message: String?
    let status: String?
    let error: String?
    let inBytes: Int64?
    let outBytes: Int64?
    let estimated: Bool?
    let elapsed: Double?
    let speed: Double?
    let avgFps: Double?
    let ok: Int?
    let fail: Int?
    let stopped: Int?
    let totalIn: Int64?
    let totalOut: Int64?
    let test: Bool?
    // benchmark
    let id: String?
    let index: Int?
    let result: BenchResult?
    let results: [BenchResult]?
    let warnings: [String]?
    // quality
    let metric: String?
    let value: Double?
    let verdict: String?
    let detail: String?
}

// MARK: - metriche di qualità

enum QualityMetric: String, CaseIterable, Identifiable {
    case vmaf, xpsnr, cvvdp, ssim

    var id: String { rawValue }

    var label: String {
        switch self {
        case .vmaf: "VMAF"
        case .xpsnr: "XPSNR"
        case .cvvdp: "ColorVideoVDP"
        case .ssim: "SSIM"
        }
    }

    var unit: String {
        switch self {
        case .xpsnr: "dB"
        case .cvvdp: "JOD"
        default: ""
        }
    }

    var info: String {
        switch self {
        case .vmaf: "VMAF (0–100): qualità percepita, modello vmaf_v0.6.1 addestrato su SDR. Sopra 95 la differenza è in pratica invisibile."
        case .xpsnr: "XPSNR (dB): PSNR pesato sulla sensibilità visiva, lavora a 10 bit e va bene anche sull'HDR. Sopra 45 dB la differenza è in pratica invisibile."
        case .cvvdp: "ColorVideoVDP (JOD, max 10): modello della visione con display HDR PQ da 1500 nit, il più preciso per l'HDR. Sopra 9,5 la differenza è invisibile. Molto lento."
        case .ssim: "SSIM (0–1): somiglianza strutturale, media sui frame. Più veloce di VMAF."
        }
    }

    /// Soglie (indistinguibile, ottimo, buono): le stesse del verdetto del motore
    var thresholds: (Double, Double, Double) {
        switch self {
        case .vmaf: (95, 90, 80)
        case .xpsnr: (45, 40, 35)
        case .cvvdp: (9.5, 9, 8)
        case .ssim: (0.99, 0.97, 0.95)
        }
    }

    /// Intervallo per la barra del risultato
    var range: ClosedRange<Double> {
        switch self {
        case .vmaf: 0...100
        case .xpsnr: 25...55
        case .cvvdp: 5...10
        case .ssim: 0.9...1
        }
    }

    func format(_ v: Double) -> String {
        switch self {
        case .ssim: String(format: "%.4f", v)
        case .vmaf: String(format: "%.1f", v)
        default: String(format: "%.2f", v)
        }
    }

    func color(_ v: Double) -> Color {
        let t = thresholds
        if v >= t.0 { return .green }
        if v >= t.1 { return .mint }
        if v >= t.2 { return .orange }
        return .red
    }

    func normalized(_ v: Double) -> Double {
        max(0, min(1, (v - range.lowerBound) / (range.upperBound - range.lowerBound)))
    }

    /// Disponibilità in base al motore; nil se disponibile, altrimenti il motivo
    func unavailableReason(_ caps: Caps?) -> String? {
        guard let caps else { return "motore non caricato" }
        switch self {
        case .vmaf: return caps.hasVmaf ? nil : "FFmpeg senza libvmaf"
        case .xpsnr: return caps.hasXpsnr == true ? nil : "FFmpeg senza xpsnr (serve 7.1+)"
        case .cvvdp: return (caps.cvvdp ?? "").isEmpty ? "ColorVideoVDP non installato (Impostazioni)" : nil
        case .ssim: return nil
        }
    }
}

// MARK: - formattazione

enum Fmt {
    /// Base 1000, come il Finder
    static func bytes(_ b: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: b, countStyle: .file)
    }

    static func duration(_ seconds: Double) -> String {
        let t = Int(seconds.rounded())
        let h = t / 3600, m = (t % 3600) / 60, s = t % 60
        if h > 0 { return String(format: "%dh %02dm %02ds", h, m, s) }
        if m > 0 { return String(format: "%dm %02ds", m, s) }
        return "\(s)s"
    }

    static func change(_ inBytes: Int64, _ outBytes: Int64) -> Double {
        guard inBytes > 0 else { return 0 }
        return (Double(outBytes) - Double(inBytes)) / Double(inBytes) * 100
    }

    static func bitrate(_ b: Int64) -> String? {
        guard b > 0 else { return nil }
        return b >= 1_000_000 ? String(format: "%.1f Mb/s", Double(b) / 1e6) : "\(b / 1000) kb/s"
    }
}
