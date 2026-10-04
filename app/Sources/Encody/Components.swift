import AppKit
import SwiftUI

struct MetaBadge: View {
    let meta: String
    var dvProfile: Int = 0

    var body: some View {
        Text(label)
            .font(.caption2.weight(.bold))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(color.opacity(0.18), in: Capsule())
            .foregroundStyle(color)
    }

    private var label: String { meta == "DV" && dvProfile > 0 ? "DV P\(dvProfile)" : meta }

    private var color: Color {
        switch meta {
        case "DV": .purple
        case "HDR10+": .orange
        case "HDR10": .yellow
        case "HLG": .teal
        default: .gray
        }
    }
}

/// Badge della grana: solo media e alta (la bassa è il caso normale)
struct GrainBadge: View {
    let grain: GrainInfo

    var body: some View {
        if grain.level != .low {
            Label("Grana \(grain.level.label)", systemImage: "circle.dotted")
                .labelStyle(.titleAndIcon)
                .font(.caption2.weight(.bold))
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(grain.level.color.opacity(0.18), in: Capsule())
                .foregroundStyle(grain.level.color)
                .help(String(format: String(localized: "Indice di grana %.2f (bassa < 1, media 1–2, alta ≥ 2)"), grain.index))
        }
    }
}

struct Chip: View {
    let text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text)
            .font(.caption.monospacedDigit())
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(Color.secondary.opacity(0.12), in: Capsule())
            .foregroundStyle(.secondary)
    }
}

struct StatusIcon: View {
    let status: ItemStatus

    var body: some View {
        switch status {
        case .probing, .running:
            ProgressView().controlSize(.mini).frame(width: 14, height: 14)
        case .ready:
            Image(systemName: "circle").foregroundStyle(.secondary)
        case .queued:
            Image(systemName: "clock").foregroundStyle(.secondary)
        case .done:
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .failed:
            Image(systemName: "xmark.circle.fill").foregroundStyle(.red)
        case .stopped:
            Image(systemName: "stop.circle.fill").foregroundStyle(.orange)
        case .invalid:
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.red)
        }
    }
}

struct SavingLabel: View {
    let inBytes: Int64
    let outBytes: Int64

    var body: some View {
        let pct = Fmt.change(inBytes, outBytes)
        let diff = inBytes - outBytes
        let saved = diff >= 0
        Label(saved
              ? String(format: String(localized: "%.1f%% · risparmiati %@"), pct, Fmt.bytes(diff))
              : String(format: String(localized: "+%.1f%% · aumentato di %@"), pct, Fmt.bytes(-diff)),
              systemImage: saved ? "arrow.down.circle.fill" : "arrow.up.circle.fill")
            .font(.caption.weight(.medium))
            .foregroundStyle(saved ? Color.green : Color.orange)
    }
}

/// Riga descrittiva di una traccia audio/sottotitoli con l'eventuale piano del motore
struct TrackRow: View {
    let track: ProbeTrack
    var planDesc: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Text("#\(track.index)")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.tertiary)
                Text((track.sel.lang ?? "und").uppercased())
                    .font(.caption.weight(.bold))
                    .padding(.horizontal, 4)
                    .background(Color.secondary.opacity(0.12), in: RoundedRectangle(cornerRadius: 3))
                Text(track.display).fontWeight(.medium)
                if !track.layout.isEmpty {
                    Text(track.layout).foregroundStyle(.secondary)
                }
                if track.suggested {
                    Image(systemName: "star.fill").font(.caption2).foregroundStyle(.yellow)
                        .help("Suggerita")
                }
            }
            if !details.isEmpty {
                Text(details.joined(separator: " · "))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if let planDesc {
                Text("→ " + planDesc)
                    .font(.caption)
                    .foregroundStyle(planDesc.contains("⚠️") ? Color.orange : Color.accentColor)
            }
        }
    }

    private var details: [String] {
        var d: [String] = []
        if let t = track.sel.title, !t.isEmpty { d.append("“\(t)”") }
        if let br = Fmt.bitrate(track.bitRate), track.sel.channels ?? 0 > 0 { d.append("~" + br) }
        if track.sel.channels ?? 0 == 0 {
            d.append(track.imageBased ? String(localized: "immagine") : String(localized: "testo"))
            if track.frames > 0 { d.append("\(track.frames) righe") }
        }
        if track.sel.isDefault == true { d.append("default") }
        if track.sel.forced == true { d.append("forced") }
        return d
    }
}

func member<T: Hashable>(_ set: Binding<Set<T>>, _ value: T) -> Binding<Bool> {
    Binding(
        get: { set.wrappedValue.contains(value) },
        set: { on in
            if on { set.wrappedValue.insert(value) } else { set.wrappedValue.remove(value) }
        }
    )
}

struct FilePickRow: View {
    let title: String
    let url: URL?
    let onPick: (URL) -> Void

    var body: some View {
        LabeledContent(title) {
            HStack {
                Text(url?.lastPathComponent ?? String(localized: "Trascina qui o scegli…"))
                    .foregroundStyle(url == nil ? Color.secondary : Color.primary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .help(url?.path ?? "")
                Button("Scegli…") {
                    let p = NSOpenPanel()
                    p.canChooseFiles = true
                    p.canChooseDirectories = false
                    p.allowsMultipleSelection = false
                    if p.runModal() == .OK, let u = p.url { onPick(u) }
                }
            }
        }
        .dropDestination(for: URL.self) { urls, _ in
            guard let u = urls.first else { return false }
            onPick(u)
            return true
        }
    }
}

struct DropHint: View {
    var body: some View {
        VStack(spacing: 10) {
            Image(systemName: "arrow.down.doc")
                .font(.system(size: 40, weight: .light))
                .foregroundStyle(.secondary)
            Text("Trascina qui file o cartelle")
                .font(.headline)
            Text("mkv, mp4, mov, m2ts, ts, avi, m4v")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .allowsHitTesting(false)
    }
}
