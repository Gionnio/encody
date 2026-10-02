import AppKit
import SwiftUI

struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @AppStorage("enginePath") private var enginePath = ""
    @AppStorage("extraPath") private var extraPath = ""
    @AppStorage("appTheme") private var appTheme: AppTheme = .system
    @AppStorage("notifyQueueDone") private var notifyQueueDone = true
    @State private var installer = CVVDPInstaller()

    var body: some View {
        Form {
            Section("Generale") {
                Picker("Tema", selection: $appTheme) {
                    ForEach(AppTheme.allCases) { Text($0.label).tag($0) }
                }
                .pickerStyle(.segmented)
                Toggle(isOn: $notifyQueueDone) {
                    Text("Notifica a fine coda")
                    Text("Avvisa quando la coda finisce mentre Encody è in secondo piano.")
                }
            }

            Section {
                TextField("Percorso del motore encody", text: $enginePath, prompt: Text("vuoto = motore incluso nell'app"))
                HStack {
                    Button("Scegli…") { chooseEngine() }
                    Button("Ricarica") { Task { await model.loadCaps() } }
                    Spacer()
                }
                LabeledContent("In uso") {
                    Text(Engine.binaryURL()?.path ?? "non trovato")
                        .font(.caption.monospaced())
                        .foregroundStyle(Engine.binaryURL() == nil ? Color.red : Color.secondary)
                        .textSelection(.enabled)
                }
                TextField("PATH aggiuntivo", text: $extraPath, prompt: Text("es. ~/tools/bin:/opt/local/bin"))
            } header: {
                Text("Motore")
            } footer: {
                Text("Homebrew, MKVToolNix.app, ~/.cargo/bin e ~/go/bin sono già inclusi nel PATH del motore. Dopo una modifica premi Ricarica.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("Output") {
                LabeledContent("Cartella predefinita") {
                    HStack {
                        Text(model.outputDirPath)
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .foregroundStyle(.secondary)
                        Button("Scegli…") { model.chooseOutputDir() }
                    }
                }
            }

            Section("Strumenti rilevati") {
                if let c = model.caps {
                    LabeledContent("Versione", value: c.version)
                    ToolRow(name: "ffmpeg", path: c.tools.ffmpeg)
                    ToolRow(name: "ffprobe", path: c.tools.ffprobe)
                    ToolRow(name: "mkvmerge", path: c.tools.mkvmerge, purpose: "inject metadati dinamici")
                    ToolRow(name: "dovi_tool", path: c.tools.doviTool, purpose: "Dolby Vision")
                    ToolRow(name: "hdr10plus_tool", path: c.tools.hdr10plusTool, purpose: "HDR10+")
                    FeatureRow(name: "zscale", ok: c.hasZscale, purpose: "conversione HDR → SDR")
                    FeatureRow(name: "libvmaf", ok: c.hasVmaf, purpose: "VMAF (modello SDR)")
                    FeatureRow(name: "xpsnr", ok: c.hasXpsnr == true, purpose: "XPSNR, anche HDR (FFmpeg 7.1+)")
                    ToolRow(name: "ColorVideoVDP", path: c.cvvdp ?? "", purpose: "metrica HDR più precisa, facoltativa")
                    if (c.cvvdp ?? "").isEmpty || installer.isRunning || installer.message != nil {
                        CVVDPInstallRow(installer: installer) { Task { await model.loadCaps() } }
                    }
                } else if let e = model.capsError {
                    Text(e).foregroundStyle(.red)
                } else {
                    ProgressView()
                }
            }
        }
        .formStyle(.grouped)
        .frame(width: 580, height: 640)
    }

    private func chooseEngine() {
        let p = NSOpenPanel()
        p.canChooseFiles = true
        p.canChooseDirectories = false
        p.allowsMultipleSelection = false
        p.prompt = "Usa questo motore"
        if p.runModal() == .OK, let url = p.url {
            enginePath = url.path
            Task { await model.loadCaps() }
        }
    }
}

private struct ToolRow: View {
    let name: String
    let path: String
    var purpose: String? = nil

    var body: some View {
        LabeledContent {
            Text(path.isEmpty ? "non trovato" : path)
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
        } label: {
            Label {
                VStack(alignment: .leading, spacing: 0) {
                    Text(name)
                    if let purpose { Text(purpose).font(.caption2).foregroundStyle(.secondary) }
                }
            } icon: {
                Image(systemName: path.isEmpty ? "xmark.circle.fill" : "checkmark.circle.fill")
                    .foregroundStyle(path.isEmpty ? Color.red : Color.green)
            }
        }
    }
}

private struct FeatureRow: View {
    let name: String
    let ok: Bool
    let purpose: String

    var body: some View {
        LabeledContent {
            Text(ok ? "disponibile" : "assente").foregroundStyle(.secondary)
        } label: {
            Label {
                VStack(alignment: .leading, spacing: 0) {
                    Text(name)
                    Text(purpose).font(.caption2).foregroundStyle(.secondary)
                }
            } icon: {
                Image(systemName: ok ? "checkmark.circle.fill" : "xmark.circle.fill")
                    .foregroundStyle(ok ? Color.green : Color.red)
            }
        }
    }
}

// MARK: - ColorVideoVDP

/// Installa ColorVideoVDP (pacchetto cvvdp, con PyTorch) in un ambiente Python isolato
/// in ~/Library/Application Support/Encody/cvvdp, dove il motore lo cerca.
@MainActor @Observable
final class CVVDPInstaller {
    var isRunning = false
    var message: String?
    var failed = false

    static var venvURL: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Encody/cvvdp")
    }

    func install(onDone: @escaping @MainActor () -> Void) {
        guard !isRunning else { return }
        isRunning = true
        failed = false
        message = "Download di ColorVideoVDP e PyTorch (circa 1 GB)…"
        let venv = Self.venvURL.path
        // python3 di sistema (Xcode) è troppo vecchio: si preferiscono Homebrew e python.org
        var env = Engine.environment
        env["PATH"] = "/Library/Frameworks/Python.framework/Versions/Current/bin:" + (env["PATH"] ?? "")
        let script = """
        set -e
        mkdir -p "$(dirname "$VENV")"
        python3 -m venv "$VENV"
        "$VENV/bin/pip" install --disable-pip-version-check -q --upgrade pip
        "$VENV/bin/pip" install --disable-pip-version-check -q cvvdp
        "$VENV/bin/cvvdp" --help >/dev/null
        """
        env["VENV"] = venv
        DispatchQueue.global(qos: .userInitiated).async {
            let p = Process()
            p.executableURL = URL(fileURLWithPath: "/bin/zsh")
            p.arguments = ["-c", script]
            p.environment = env
            p.standardInput = FileHandle.nullDevice
            let err = Pipe()
            p.standardError = err
            p.standardOutput = FileHandle.nullDevice
            var status: Int32 = -1
            var stderr = ""
            do {
                try p.run()
                let data = err.fileHandleForReading.readDataToEndOfFile()
                p.waitUntilExit()
                status = p.terminationStatus
                stderr = String(decoding: data, as: UTF8.self)
            } catch {
                stderr = error.localizedDescription
            }
            let lastLines = stderr.split(separator: "\n").suffix(4).joined(separator: "\n")
            Task { @MainActor in
                self.isRunning = false
                self.failed = status != 0
                self.message = status == 0 ? "ColorVideoVDP installato." : "Installazione non riuscita:\n\(lastLines)"
                onDone()
            }
        }
    }
}

private struct CVVDPInstallRow: View {
    let installer: CVVDPInstaller
    let onDone: @MainActor () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Button("Installa ColorVideoVDP") { installer.install(onDone: onDone) }
                    .disabled(installer.isRunning)
                if installer.isRunning { ProgressView().controlSize(.small) }
                Spacer()
            }
            Text(installer.message ?? "Richiede Python 3.10+ (Homebrew o python.org). Viene installato in un ambiente separato in ~/Library/Application Support/Encody/cvvdp: per rimuoverlo basta cancellare la cartella.")
                .font(.caption)
                .foregroundStyle(installer.failed ? Color.red : Color.secondary)
                .textSelection(.enabled)
        }
    }
}
