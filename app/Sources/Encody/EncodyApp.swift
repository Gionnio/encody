import AppKit
import SwiftUI
import UserNotifications

@main
struct EncodyApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var model: AppModel
    @AppStorage("appTheme") private var appTheme: AppTheme = .system

    init() {
        LegacySettings.migrate() // prima di AppModel, che legge la cartella di output
        _model = State(initialValue: AppModel())
    }

    var body: some Scene {
        WindowGroup("Encody") {
            ContentView()
                .environment(model)
                .frame(minWidth: 1000, minHeight: 640)
                .preferredColorScheme(appTheme.colorScheme)
        }
        .commands {
            CommandGroup(replacing: .appInfo) {
                Button("Informazioni su Encody") { AboutWindow.show() }
            }
            CommandGroup(after: .newItem) {
                Button("Aggiungi file…") { model.openFiles() }
                    .keyboardShortcut("o")
                Button("Importa coda…") { model.importQueue() }
                    .keyboardShortcut("i", modifiers: [.command, .shift])
                Button("Esporta coda…") { model.exportQueue() }
                    .keyboardShortcut("e", modifiers: [.command, .shift])
            }
        }

        Settings {
            SettingsView()
                .environment(model)
                .preferredColorScheme(appTheme.colorScheme)
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        signal(SIGPIPE, SIG_IGN) // un motore che esce presto non deve far crashare l'app
        // Homebrew e i download mettono in quarantena tutto il bundle: il motore incluso, lanciato
        // come processo a parte, alla prima esecuzione può essere ucciso (SIGKILL). Se l'app è partita
        // l'utente l'ha già approvata, quindi si toglie la quarantena dal proprio motore.
        if let engine = Bundle.main.url(forResource: "encody", withExtension: nil) {
            removexattr(engine.path, "com.apple.quarantine", 0)
        }
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        if Bundle.main.bundleIdentifier != nil {
            UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { _, _ in }
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard EngineRun.hasActive else { return .terminateNow }
        let alert = NSAlert()
        alert.messageText = String(localized: "Lavoro in corso")
        alert.informativeText = String(localized: "Uscendo, l'encode in corso verrà interrotto e i file parziali rimossi.")
        alert.addButton(withTitle: "Interrompi ed esci")
        alert.addButton(withTitle: "Annulla")
        guard alert.runModal() == .alertFirstButtonReturn else { return .terminateCancel }
        EngineRun.interruptAll(wait: 6)
        return .terminateNow
    }
}

struct ContentView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        NavigationSplitView {
            List(SidebarSection.allCases, selection: $model.section) { s in
                Label(s.title, systemImage: s.icon)
                    .badge(badge(for: s))
            }
            .navigationSplitViewColumnWidth(min: 180, ideal: 200)
            .safeAreaInset(edge: .bottom) { EngineStatusView().padding(10) }
        } detail: {
            switch model.section ?? .queue {
            case .queue: QueueView()
            case .bench: BenchmarkView()
            case .quality: QualityView()
            case .presets: PresetsView()
            }
        }
        .task { await model.loadCaps() }
    }

    private func badge(for s: SidebarSection) -> Int {
        switch s {
        case .queue: model.enabledCount
        case .bench: model.bench.isRunning ? 1 : 0
        case .quality: model.quality.isRunning ? 1 : 0
        case .presets: 0
        }
    }
}

struct EngineStatusView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let caps = model.caps {
                Label(caps.version, systemImage: "gearshape.2")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                let missing = missingTools(caps)
                if !missing.isEmpty {
                    Label("Mancano: \(missing.joined(separator: ", "))", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption2)
                        .foregroundStyle(.orange)
                        .fixedSize(horizontal: false, vertical: true)
                }
            } else if let err = model.capsError {
                Label(err, systemImage: "xmark.octagon.fill")
                    .font(.caption)
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
                SettingsLink { Text("Apri Impostazioni…") }
                    .font(.caption)
            } else {
                ProgressView().controlSize(.small)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func missingTools(_ c: Caps) -> [String] {
        var m: [String] = []
        if c.tools.mkvmerge.isEmpty { m.append("mkvmerge") }
        if c.tools.doviTool.isEmpty { m.append("dovi_tool") }
        if c.tools.hdr10plusTool.isEmpty { m.append("hdr10plus_tool") }
        if !c.hasZscale { m.append("zscale") }
        if !c.hasVmaf { m.append("libvmaf") }
        if c.hasXpsnr == false { m.append("xpsnr") }
        return m
    }
}
