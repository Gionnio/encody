import AppKit
import SwiftUI

/// Cosa mostra l'icona del Dock durante la coda
enum DockStyle: String, CaseIterable, Identifiable {
    case percent, files, off

    var id: String { rawValue }

    var label: String {
        switch self {
        case .percent: "Percentuale e barra"
        case .files: "File completati"
        case .off: "Niente"
        }
    }

    static var current: DockStyle {
        DockStyle(rawValue: UserDefaults.standard.string(forKey: "dockStyle") ?? "") ?? .percent
    }
}

/// Etichetta e barra di avanzamento disegnate sull'icona del Dock (come i download di Safari).
/// Non si usa badgeLabel: da macOS 14 il badge di sistema compare solo se l'app ha il permesso
/// di notifica "Badge", quindi spesso resterebbe invisibile.
@MainActor
enum DockProgress {
    private static var view: DockProgressView?
    private static var lastKey = ""

    /// - itemPercent: avanzamento del file in codifica (0–100)
    /// - overall: avanzamento della coda pesato sulla durata (0–1)
    static func update(itemPercent: Double?, overall: Double, done: Int, total: Int) {
        switch DockStyle.current {
        case .off:
            clear()
        case .files:
            show(label: "\(done)/\(total)", progress: nil)
        case .percent:
            let label = itemPercent.map { "\(Int($0.rounded(.down)))%" } ?? "\(done)/\(total)"
            show(label: label, progress: max(0, min(1, overall)))
        }
    }

    static func clear() {
        guard view != nil else { return }
        view = nil
        lastKey = ""
        NSApp.dockTile.contentView = nil
        NSApp.dockTile.display()
    }

    private static func show(label: String, progress: Double?) {
        // il Dock si ridisegna solo se cambia qualcosa di visibile
        let key = label + "|" + (progress.map { "\(Int($0 * 200))" } ?? "-")
        guard key != lastKey else { return }
        lastKey = key
        let tile = NSApp.dockTile
        if view == nil {
            let v = DockProgressView(frame: NSRect(origin: .zero, size: tile.size))
            view = v
            tile.contentView = v
        }
        view?.label = label
        view?.progress = progress
        tile.display()
    }
}

/// Icona dell'app con un'etichetta a capsula in alto a destra e una barra in basso
private final class DockProgressView: NSView {
    var label = ""
    var progress: Double?

    override func draw(_ dirtyRect: NSRect) {
        NSApp.applicationIconImage?.draw(in: bounds)
        if let progress { drawBar(progress) }
        if !label.isEmpty { drawLabel() }
    }

    private func drawBar(_ progress: Double) {
        let inset = bounds.width * 0.17
        let height = bounds.height * 0.09
        let track = NSRect(x: inset, y: bounds.height * 0.135, width: bounds.width - inset * 2, height: height)
        let radius = height / 2

        NSColor.black.withAlphaComponent(0.55).setFill()
        NSBezierPath(roundedRect: track, xRadius: radius, yRadius: radius).fill()

        let inner = track.insetBy(dx: height * 0.18, dy: height * 0.18)
        let fill = NSRect(x: inner.minX, y: inner.minY, width: max(inner.height, inner.width * progress), height: inner.height)
        NSColor.controlAccentColor.setFill()
        NSBezierPath(roundedRect: fill, xRadius: inner.height / 2, yRadius: inner.height / 2).fill()
    }

    /// Capsula rossa come il badge di sistema, in alto a destra
    private func drawLabel() {
        let font = NSFont.systemFont(ofSize: bounds.height * 0.17, weight: .semibold)
        let text = NSAttributedString(string: label, attributes: [.font: font, .foregroundColor: NSColor.white])
        let size = text.size()
        let h = bounds.height * 0.27
        let w = max(h, size.width + h * 0.6)
        let rect = NSRect(x: bounds.maxX - w - bounds.width * 0.02, y: bounds.maxY - h - bounds.height * 0.02, width: w, height: h)
        let pill = NSBezierPath(roundedRect: rect, xRadius: h / 2, yRadius: h / 2)
        NSColor.systemRed.setFill()
        pill.fill()
        NSColor.white.withAlphaComponent(0.9).setStroke()
        pill.lineWidth = bounds.height * 0.012
        pill.stroke()
        text.draw(at: NSPoint(x: rect.midX - size.width / 2, y: rect.midY - size.height / 2))
    }
}
