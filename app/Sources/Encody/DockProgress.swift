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

/// Badge e barra di avanzamento sull'icona del Dock (come i download di Safari)
@MainActor
enum DockProgress {
    private static var view: DockProgressView?
    private static var lastKey = ""

    /// - itemPercent: avanzamento del file in codifica (0–100)
    /// - overall: avanzamento della coda pesato sulla durata (0–1)
    static func update(itemPercent: Double?, overall: Double, done: Int, total: Int) {
        let tile = NSApp.dockTile
        switch DockStyle.current {
        case .off:
            clear()
        case .files:
            removeBar()
            set(badge: "\(done)/\(total)")
        case .percent:
            let badge = itemPercent.map { "\(Int($0.rounded(.down)))%" } ?? "\(done)/\(total)"
            let bar = max(0, min(1, overall))
            // Il Dock si ridisegna solo se cambia qualcosa di visibile
            let key = badge + "|\(Int(bar * 200))"
            guard key != lastKey else { return }
            lastKey = key
            if view == nil {
                let v = DockProgressView(frame: NSRect(origin: .zero, size: tile.size))
                view = v
                tile.contentView = v
            }
            view?.progress = bar
            tile.badgeLabel = badge
            tile.display()
        }
    }

    static func clear() {
        removeBar()
        set(badge: nil)
    }

    private static func set(badge: String?) {
        guard NSApp.dockTile.badgeLabel != badge else { return }
        lastKey = ""
        NSApp.dockTile.badgeLabel = badge
    }

    private static func removeBar() {
        guard view != nil else { return }
        view = nil
        lastKey = ""
        NSApp.dockTile.contentView = nil
        NSApp.dockTile.display()
    }
}

/// Icona dell'app con una barra di avanzamento a capsula in basso
private final class DockProgressView: NSView {
    var progress: Double = 0

    override func draw(_ dirtyRect: NSRect) {
        NSApp.applicationIconImage?.draw(in: bounds)

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
}
