import AppKit
import Foundation
import UniformTypeIdentifiers

struct PresetIssue: Decodable, Hashable {
    let level: String   // error | warning | info
    let field: String
    let message: String
}

struct PresetValidation: Decodable {
    let ok: Bool
    let issues: [PresetIssue]
    let command: String?
}

struct PresetTestCheck: Decodable, Hashable {
    let level: String   // ok | warning | error
    let title: String
    let detail: String?
}

/// Preset personalizzati: elenco, bozza in modifica, controllo immediato, prova reale, salvataggio.
/// I file stanno nella cartella indicata dal motore (caps.presetDir); il motore li rilegge a ogni caps.
@MainActor @Observable
final class PresetsModel {
    var selectedID: String?
    /// Preset in modifica (nuovo, duplicato, importato o personalizzato esistente)
    var draft: PresetSpec?
    var isNew = false
    /// Versione salvata della bozza, per "Annulla modifiche"
    var saved: PresetSpec?

    var validation: PresetValidation?
    var validating = false

    var isTesting = false
    var testStep = ""
    var testChecks: [PresetTestCheck] = []
    var testPassed: Bool?
    var testFPS: Double?
    var testBitrate: Double?
    var testSampleName = ""
    var testedDraft: PresetSpec?   // la bozza a cui si riferisce l'ultima prova
    var sampleURL: URL?
    var errorMessage: String?
    var saveAfterTest = false

    @ObservationIgnored private var testRun: EngineRun?
    @ObservationIgnored weak var app: AppModel?

    var hasChanges: Bool { draft != nil && (isNew || draft != saved) }
    var hasErrors: Bool { validation?.issues.contains { $0.level == "error" } ?? true }
    /// La prova riguarda esattamente la bozza attuale ed è andata bene
    var testIsCurrent: Bool { testedDraft != nil && testedDraft == draft }
    var canSave: Bool { draft != nil && hasChanges && !hasErrors && !isTesting }

    // MARK: selezione

    func select(_ id: String?) {
        guard id != selectedID || draft == nil else { return }
        stopTest()
        selectedID = id
        isNew = false
        resetResults()
        if let id, let p = app?.preset(id), !p.isBuiltin, let spec = p.spec {
            draft = spec
            saved = spec
        } else {
            draft = nil
            saved = nil
        }
    }

    /// Nuovo preset vuoto o copia di uno esistente (di sistema o personalizzato)
    func create(from base: PresetInfo?) {
        stopTest()
        var spec = base?.spec ?? PresetSpec(id: "", name: "", encoder: "libx265",
                                            rate: PresetRate(mode: "crf", value: 18), speed: "medium",
                                            audio: PresetAudio(bitrate: "320k", passthrough: ["aac", "ac3", "eac3", "truehd", "dts", "opus", "flac"]))
        spec.id = UUID().uuidString.lowercased()
        spec.name = base.map { "Copia di \($0.name)" } ?? "Nuovo preset"
        spec.builtin = nil
        spec.verified = nil
        spec.schema = 1
        if spec.encoder == "copy" { // il remux non ha nulla da personalizzare: si parte da x265
            spec.encoder = "libx265"
            spec.rate = PresetRate(mode: "crf", value: 18)
            spec.speed = "medium"
        }
        draft = spec
        saved = nil
        isNew = true
        selectedID = nil
        resetResults()
    }

    func revert() {
        guard let saved else { return }
        draft = saved
        resetResults()
    }

    private func resetResults() {
        validation = nil
        testChecks = []
        testPassed = nil
        testFPS = nil
        testBitrate = nil
        testedDraft = nil
        errorMessage = nil
        saveAfterTest = false
    }

    // MARK: controllo immediato

    func validate() async {
        guard let draft else { return }
        validating = true
        defer { validating = false }
        do {
            let data = try await Engine.runJSON(["preset", "validate"], stdin: JSONCoding.encoder.encode(draft))
            guard draft == self.draft else { return } // nel frattempo è cambiata
            validation = try JSONCoding.decoder.decode(PresetValidation.self, from: data)
        } catch {
            validation = PresetValidation(ok: false, issues: [PresetIssue(level: "error", field: "", message: error.localizedDescription)], command: nil)
        }
    }

    /// Anteprima del comando per un preset di sistema (sola lettura)
    func command(for spec: PresetSpec) async -> String? {
        guard let data = try? await Engine.runJSON(["preset", "validate"], stdin: JSONCoding.encoder.encode(spec)) else { return nil }
        return (try? JSONCoding.decoder.decode(PresetValidation.self, from: data))?.command
    }

    // MARK: prova reale

    func runTest(thenSave: Bool = false) {
        guard let draft, !isTesting, !hasErrors else { return }
        let file = FileManager.default.temporaryDirectory.appending(path: "encody-preset-\(UUID().uuidString).json")
        do {
            try JSONCoding.encoder.encode(draft).write(to: file)
        } catch {
            errorMessage = error.localizedDescription
            return
        }
        var args = ["preset", "test", "--spec", file.path]
        if let sampleURL { args += ["--sample", sampleURL.path] }
        testChecks = []
        testPassed = nil
        testFPS = nil
        testBitrate = nil
        testStep = ""
        errorMessage = nil
        isTesting = true
        saveAfterTest = thenSave
        let tested = draft
        let r = EngineRun(arguments: args)
        testRun = r
        do {
            try r.start(onEvent: { [weak self] ev in self?.handleTest(ev, tested: tested) },
                        onExit: { [weak self] code, err in
                            try? FileManager.default.removeItem(at: file)
                            guard let self, self.testRun === r else { return }
                            self.isTesting = false
                            self.testRun = nil
                            if code != 0 && code != 130 && self.testPassed == nil && self.errorMessage == nil {
                                let msg = err.trimmingCharacters(in: .whitespacesAndNewlines)
                                self.errorMessage = msg.isEmpty ? "Prova terminata con codice \(code)" : msg
                            }
                            if self.saveAfterTest, self.testPassed == true, self.testIsCurrent {
                                self.save()
                            }
                            self.saveAfterTest = false
                        })
        } catch {
            isTesting = false
            errorMessage = error.localizedDescription
        }
    }

    func stopTest() {
        testRun?.interrupt()
        testRun = nil
        isTesting = false
    }

    private func handleTest(_ ev: EngineEvent, tested: PresetSpec) {
        switch ev.type {
        case "step":
            testStep = ev.step ?? ""
        case "preset_test":
            testChecks = ev.checks ?? []
            testPassed = ev.passed
            testFPS = ev.fps
            testBitrate = ev.bitrateKbps
            testSampleName = ev.sample ?? ""
            testedDraft = tested
        case "error":
            errorMessage = ev.message
        default:
            break
        }
    }

    // MARK: salvataggio, eliminazione, import/export

    private var presetDir: URL {
        if let d = app?.caps?.presetDir, !d.isEmpty { return URL(fileURLWithPath: d, isDirectory: true) }
        return FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appending(path: "Encody/presets", directoryHint: .isDirectory)
    }

    /// Salva solo una bozza senza errori e provata con successo così com'è
    func save() {
        guard var spec = draft, !hasErrors else { return }
        guard testIsCurrent, testPassed == true else {
            runTest(thenSave: true)
            return
        }
        spec.verified = PresetVerified(at: ISO8601DateFormatter().string(from: Date()),
                                       ffmpeg: app?.caps?.ffmpegVersion ?? "", fps: testFPS ?? 0)
        do {
            try FileManager.default.createDirectory(at: presetDir, withIntermediateDirectories: true)
            try JSONCoding.encoder.encode(spec).write(to: presetDir.appending(path: "\(spec.id).json"), options: .atomic)
        } catch {
            errorMessage = "Salvataggio non riuscito: \(error.localizedDescription)"
            return
        }
        draft = spec
        saved = spec
        testedDraft = spec
        isNew = false
        selectedID = spec.id
        Task { await app?.loadCaps() }
    }

    /// Elimina il file del preset; i file in coda che lo usavano passano al preset predefinito
    func delete(_ id: String) {
        let fm = FileManager.default
        let files = (try? fm.contentsOfDirectory(at: presetDir, includingPropertiesForKeys: nil)) ?? []
        for f in files where f.pathExtension == "json" {
            if let data = try? Data(contentsOf: f),
               let spec = try? JSONCoding.decoder.decode(PresetSpec.self, from: data), spec.id == id {
                try? fm.removeItem(at: f)
            }
        }
        if let app {
            if app.defaultPresetID == id { app.defaultPresetID = "1" }
            for it in app.items where it.presetID == id && !it.isLocked && it.status != .done {
                it.presetID = app.defaultPresetID
            }
            app.bench.selected.remove(id)
        }
        if selectedID == id || draft?.id == id {
            draft = nil
            saved = nil
            selectedID = nil
            isNew = false
            resetResults()
        }
        Task { await app?.loadCaps() }
    }

    func export(_ spec: PresetSpec) {
        let panel = NSSavePanel()
        panel.allowedContentTypes = [.json]
        panel.nameFieldStringValue = spec.name.replacingOccurrences(of: "/", with: "-") + ".json"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        var out = spec
        out.builtin = nil
        do {
            try JSONCoding.encoder.encode(out).write(to: url, options: .atomic)
        } catch {
            errorMessage = "Esportazione non riuscita: \(error.localizedDescription)"
        }
    }

    /// Un preset importato si apre come bozza: va provato su questo Mac prima di salvarlo
    func importPreset() {
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.json]
        panel.allowsMultipleSelection = false
        guard panel.runModal() == .OK, let url = panel.url else { return }
        do {
            var spec = try JSONCoding.decoder.decode(PresetSpec.self, from: Data(contentsOf: url))
            if app?.preset(spec.id) != nil || spec.id.isEmpty { spec.id = UUID().uuidString.lowercased() }
            spec.builtin = nil
            spec.verified = nil
            stopTest()
            draft = spec
            saved = nil
            isNew = true
            selectedID = nil
            resetResults()
        } catch {
            errorMessage = "File non valido: non è un preset di Encody (\(error.localizedDescription))."
        }
    }

    func chooseSample() {
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.movie, .video, UTType(filenameExtension: "mkv") ?? .movie]
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK { sampleURL = panel.url }
    }
}
