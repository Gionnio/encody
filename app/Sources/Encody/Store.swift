import AppKit
import Observation
import SwiftUI
import UniformTypeIdentifiers
import UserNotifications

enum SidebarSection: String, CaseIterable, Identifiable, Hashable {
    case queue, bench, quality
    var id: Self { self }
    var title: String {
        switch self {
        case .queue: "Coda"
        case .bench: "Benchmark"
        case .quality: "Check Qualità"
        }
    }
    var icon: String {
        switch self {
        case .queue: "list.bullet.rectangle"
        case .bench: "gauge.with.dots.needle.67percent"
        case .quality: "checkmark.seal"
        }
    }
}

enum ItemStatus: Equatable {
    case probing, ready, queued, running, done, failed, stopped, invalid

    var label: String {
        switch self {
        case .probing: "Analisi…"
        case .ready: "Pronto"
        case .queued: "In coda"
        case .running: "In corso"
        case .done: "Completato"
        case .failed: "Errore"
        case .stopped: "Interrotto"
        case .invalid: "Non leggibile"
        }
    }
}

/// Scelta unica per le sorgenti HDR: sostituisce la coppia tonemap/inject
enum DynamicRangeMode: Hashable {
    case dynamic // HDR10 + metadati dinamici (DV / HDR10+) reiniettati
    case hdr     // HDR statico (HDR10 o HLG)
    case sdr     // tonemap in SDR BT.709
}

struct JobResult {
    let inBytes: Int64
    let outBytes: Int64
    let estimated: Bool
    let elapsed: Double
    let speed: Double
    let avgFps: Double
}

struct RecapRow: Identifiable {
    let id = UUID()
    let name: String
    let status: ItemStatus
    let inBytes: Int64
    let outBytes: Int64
    let elapsed: Double
    let error: String?
}

struct QueueRecap {
    let rows: [RecapRow]
    let ok: Int
    let fail: Int
    let stopped: Int
    let total: Int
    let totalIn: Int64
    let totalOut: Int64
    let elapsed: Double
    let test: Bool
}

// MARK: - QueueItem

@MainActor @Observable
final class QueueItem: Identifiable {
    let id = UUID()
    let url: URL
    var presetID: String
    var audioMode: String
    var outputURL: URL
    var customOutput = false

    var probe: ProbeResult?
    var probeError: String?
    var plan: JobPlan?
    var planError: String?

    var enabled = true
    var tonemap = false
    var doInject = false
    var cropValue = ""
    var cropEnabled = false
    var cropDetecting = false
    var selectedAudio: Set<Int> = []
    var selectedSubs: Set<Int> = []

    var status: ItemStatus = .probing
    var step = ""
    var percent = 0.0
    var fps = 0.0
    var elapsed = 0.0
    var log: [String] = []
    var errorMessage: String?
    var result: JobResult?

    @ObservationIgnored var pendingSpec: JobSpec?

    init(url: URL, presetID: String, audioMode: String, outputURL: URL) {
        self.url = url
        self.presetID = presetID
        self.audioMode = audioMode
        self.outputURL = outputURL
    }

    var name: String { url.lastPathComponent }
    var isLocked: Bool { status == .queued || status == .running }
    var isRunnable: Bool { enabled && probe != nil && [.ready, .failed, .stopped].contains(status) }

    var rangeMode: DynamicRangeMode {
        get { tonemap ? .sdr : (doInject ? .dynamic : .hdr) }
        set {
            tonemap = newValue == .sdr
            doInject = newValue == .dynamic
        }
    }

    /// Cambia quando cambia una scelta che influenza il piano del motore
    var planKey: String {
        [presetID, "\(tonemap)", "\(doInject)", "\(cropEnabled)", cropValue, audioMode,
         selectedAudio.sorted().map(String.init).joined(separator: ","),
         selectedSubs.sorted().map(String.init).joined(separator: ","),
         outputURL.path, probe == nil ? "0" : "1"].joined(separator: "|")
    }

    /// Crop che toglie davvero qualcosa rispetto al frame pieno
    var cropIsUseful: Bool {
        guard let p = probe else { return false }
        let parts = cropValue.replacingOccurrences(of: "crop=", with: "").split(separator: ":").compactMap { Int($0) }
        guard parts.count == 4 else { return false }
        return parts[0] < p.width || parts[1] < p.height
    }
}

// MARK: - AppModel

@MainActor @Observable
final class AppModel {
    var section: SidebarSection? = .queue
    var caps: Caps?
    var capsError: String?

    var items: [QueueItem] = []
    var selection: Set<UUID> = []
    var defaultPresetID = "1"
    var defaultAudioMode = "copy"
    var testMode = false
    var isRunning = false
    var queueError: String?
    var queueLog: [String] = []
    var recap: QueueRecap?
    var showRecap = false

    var outputDirPath: String {
        didSet { UserDefaults.standard.set(outputDirPath, forKey: "outputDir") }
    }

    let bench = BenchModel()
    let quality = QualityModel()

    @ObservationIgnored private var run: EngineRun?
    @ObservationIgnored private var runningItems: [QueueItem] = []
    @ObservationIgnored private var activity: NSObjectProtocol?
    @ObservationIgnored private var queueFile: URL?
    @ObservationIgnored private var probeChain: Task<Void, Never>?
    @ObservationIgnored private var cropChain: Task<Void, Never>?

    init() {
        let saved = UserDefaults.standard.string(forKey: "outputDir") ?? ""
        outputDirPath = saved.isEmpty
            ? FileManager.default.homeDirectoryForCurrentUser.appending(path: "Movies").path
            : saved
    }

    var outputDir: URL { URL(fileURLWithPath: (outputDirPath as NSString).expandingTildeInPath, isDirectory: true) }
    var presets: [PresetInfo] { caps?.presets ?? [] }
    func preset(_ id: String) -> PresetInfo? { presets.first { $0.id == id } }
    /// Editor visibile solo con un file selezionato
    var selectedItem: QueueItem? {
        guard selection.count == 1 else { return nil }
        return items.first { selection.contains($0.id) }
    }
    var selectedItems: [QueueItem] { items.filter { selection.contains($0.id) } }
    var enabledCount: Int { items.filter { $0.enabled && $0.status != .done }.count }
    var canStart: Bool { !isRunning && caps != nil && items.contains { $0.isRunnable } }

    // MARK: caps

    func loadCaps() async {
        do {
            let data = try await Engine.runJSON(["caps"])
            caps = try JSONCoding.decoder.decode(Caps.self, from: data)
            capsError = nil
            if preset(defaultPresetID) == nil {
                defaultPresetID = presets.first(where: { !$0.isCopy })?.id ?? presets.first?.id ?? "1"
            }
        } catch {
            caps = nil
            capsError = error.localizedDescription
        }
    }

    // MARK: aggiunta file

    func openFiles() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = true
        panel.prompt = "Aggiungi"
        if panel.runModal() == .OK { add(urls: panel.urls) }
    }

    @discardableResult
    func add(urls: [URL], presetID: String? = nil, apply: JobSpec? = nil) -> [QueueItem] {
        var added: [QueueItem] = []
        for url in Engine.expandVideos(urls) {
            // stesso file già in coda e non ancora fatto: niente doppioni (tranne aggiunte esplicite)
            if presetID == nil, apply == nil, items.contains(where: { $0.url == url && $0.status != .done }) { continue }
            let item = QueueItem(url: url, presetID: presetID ?? defaultPresetID,
                                 audioMode: defaultAudioMode, outputURL: uniqueOutput(for: url))
            item.pendingSpec = apply
            items.append(item)
            added.append(item)
            let previous = probeChain
            probeChain = Task { [weak self] in
                await previous?.value
                await self?.probe(item)
            }
        }
        if let first = added.first, selection.isEmpty || presetID != nil { selection = [first.id] }
        return added
    }

    func uniqueOutput(for url: URL, dir: URL? = nil) -> URL {
        let base = url.deletingPathExtension().lastPathComponent + "_enc"
        let d = dir ?? outputDir
        let used = Set(items.map { $0.outputURL.path })
        var candidate = d.appending(path: base + ".mkv")
        var n = 2
        while used.contains(candidate.path) || candidate.path == url.path {
            candidate = d.appending(path: "\(base)_\(n).mkv")
            n += 1
        }
        return candidate
    }

    /// Cartella predefinita cambiata: si aggiornano gli output non personalizzati
    func retargetOutputs() {
        for it in items where !it.customOutput && !it.isLocked && it.status != .done {
            it.outputURL = outputDir // placeholder per escluderlo dal controllo doppioni
            it.outputURL = uniqueOutput(for: it.url)
        }
    }

    func chooseOutputDir() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.canCreateDirectories = true
        panel.directoryURL = outputDir
        panel.prompt = "Usa questa cartella"
        if panel.runModal() == .OK, let url = panel.url {
            outputDirPath = url.path
            retargetOutputs()
        }
    }

    // MARK: probe, crop, plan

    func probe(_ item: QueueItem) async {
        item.status = .probing
        do {
            let data = try await Engine.runJSON(["probe", item.url.path])
            let p = try JSONCoding.decoder.decode(ProbeResult.self, from: data)
            item.probe = p
            item.selectedAudio = Set(p.audio.filter(\.suggested).map(\.index))
            item.selectedSubs = Set(p.subs.filter(\.suggested).map(\.index))
            item.tonemap = defaultTonemap(for: item)
            if let spec = item.pendingSpec {
                apply(spec, to: item)
                item.pendingSpec = nil
            }
            item.status = .ready
            if preset(item.presetID)?.isCopy == false, !item.doInject, item.cropValue.isEmpty {
                enqueueCrop(item)
            }
        } catch {
            item.probeError = error.localizedDescription
            item.status = .invalid
        }
    }

    /// I preset ridotti nascono per la compatibilità: di default convertono in SDR
    func defaultTonemap(for item: QueueItem) -> Bool {
        guard let p = item.probe, p.isHDR, caps?.hasZscale == true,
              let pr = preset(item.presetID), !pr.isCopy else { return false }
        return pr.scale > 0
    }

    private func apply(_ s: JobSpec, to item: QueueItem) {
        if preset(s.preset.id) != nil { item.presetID = s.preset.id }
        item.tonemap = s.tonemap ?? false
        item.doInject = s.doInject
        item.cropValue = s.crop
        item.cropEnabled = !s.crop.isEmpty
        item.audioMode = s.audioMode
        item.selectedAudio = Set((s.selAudio ?? []).map(\.index))
        item.selectedSubs = Set((s.selSubs ?? []).map(\.index))
        if !s.outputPath.isEmpty {
            item.outputURL = URL(fileURLWithPath: s.outputPath)
            item.customOutput = true
        }
    }

    private func enqueueCrop(_ item: QueueItem) {
        let previous = cropChain
        cropChain = Task { [weak self] in
            await previous?.value
            await self?.detectCrop(item)
        }
    }

    func detectCrop(_ item: QueueItem) async {
        guard !item.cropDetecting, !item.isLocked else { return }
        item.cropDetecting = true
        defer { item.cropDetecting = false }
        do {
            struct CropOut: Decodable { let crop: String }
            let data = try await Engine.runJSON(["crop", item.url.path])
            let r = try JSONCoding.decoder.decode(CropOut.self, from: data)
            item.cropValue = r.crop
            item.cropEnabled = item.cropIsUseful
        } catch {
            item.log.append("Rilevamento crop fallito: \(error.localizedDescription)")
        }
    }

    func job(for item: QueueItem) -> JobSpec? {
        guard let p = item.probe else { return nil }
        let isCopy = preset(item.presetID)?.isCopy ?? false
        var j = p.job
        j.preset = PresetRef(id: item.presetID)
        j.outputPath = item.outputURL.path
        j.tonemap = item.tonemap && !isCopy
        j.doInject = item.doInject && !item.tonemap && !isCopy
        j.crop = (item.cropEnabled && !j.doInject && !isCopy) ? item.cropValue : ""
        j.audioMode = item.audioMode
        j.selAudio = p.audio.filter { item.selectedAudio.contains($0.index) }.map(\.sel)
        j.selSubs = p.subs.filter { item.selectedSubs.contains($0.index) }.map(\.sel)
        return j
    }

    func refreshPlan(_ item: QueueItem) async {
        guard item.probe != nil, !item.isLocked, let job = job(for: item) else { return }
        do {
            let input = try JSONCoding.encoder.encode([job])
            let data = try await Engine.runJSON(["plan"], stdin: input)
            guard let plan = try JSONCoding.decoder.decode([JobPlan].self, from: data).first else { return }
            item.plan = plan
            item.planError = nil
            // i vincoli del motore vincono sulle scelte UI
            if item.tonemap && !plan.canTonemap { item.tonemap = false }
            if item.doInject && !plan.canInject { item.doInject = false }
        } catch {
            item.planError = error.localizedDescription
        }
    }

    // MARK: gestione coda

    /// Rimuove i file indicati (quelli in lavorazione restano); seleziona il successivo
    func remove(ids: Set<UUID>) {
        let removable = Set(items.filter { ids.contains($0.id) && !$0.isLocked }.map(\.id))
        guard !removable.isEmpty else { return }
        let firstIndex = items.firstIndex { removable.contains($0.id) } ?? 0
        items.removeAll { removable.contains($0.id) }
        selection.subtract(removable)
        if selection.isEmpty, !items.isEmpty {
            selection = [items[min(firstIndex, items.count - 1)].id]
        }
    }

    func remove(_ item: QueueItem) { remove(ids: [item.id]) }

    func removeSelected() { remove(ids: selection) }

    func clearFinished() {
        remove(ids: Set(items.filter { $0.status == .done || $0.status == .invalid }.map(\.id)))
    }

    // MARK: flag attivo/disattivo

    func setEnabled(_ on: Bool, ids: Set<UUID>? = nil) {
        for it in items where (ids?.contains(it.id) ?? true) && !it.isLocked {
            it.enabled = on
        }
    }

    func invertEnabled() {
        for it in items where !it.isLocked { it.enabled.toggle() }
    }

    /// Spazio sulla selezione: se almeno uno è spento li accende tutti, altrimenti li spegne
    func toggleEnabledSelection() {
        let sel = selectedItems.filter { !$0.isLocked }
        guard !sel.isEmpty else { return }
        let turnOn = sel.contains { !$0.enabled }
        sel.forEach { $0.enabled = turnOn }
    }

    // MARK: modifiche in blocco

    func applyPreset(_ id: String, to ids: Set<UUID>) {
        for it in items where ids.contains(it.id) && !it.isLocked && it.status != .done {
            it.presetID = id
            it.tonemap = defaultTonemap(for: it)
        }
    }

    func applyAudioMode(_ mode: String, to ids: Set<UUID>) {
        for it in items where ids.contains(it.id) && !it.isLocked && it.status != .done {
            it.audioMode = mode
        }
    }

    func reveal(_ url: URL) {
        NSWorkspace.shared.activateFileViewerSelecting([url])
    }

    func openQuality(for item: QueueItem) {
        quality.refURL = item.url
        quality.distURL = item.outputURL
        quality.result = nil
        section = .quality
    }

    func enqueueFromBench(_ r: BenchResult) {
        guard let url = bench.fileURL else { return }
        add(urls: [url], presetID: r.id)
        section = .queue
    }

    func exportQueue() {
        let jobs = items.filter { $0.probe != nil && $0.status != .done }.compactMap { job(for: $0) }
        guard !jobs.isEmpty else { return }
        let panel = NSSavePanel()
        panel.allowedContentTypes = [.json]
        panel.nameFieldStringValue = "coda.json"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        do {
            try JSONCoding.encoder.encode(jobs).write(to: url)
        } catch {
            queueError = "Esportazione fallita: \(error.localizedDescription)"
        }
    }

    /// Compatibile con le code esportate dalla CLI
    func importQueue() {
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.json]
        panel.allowsMultipleSelection = false
        guard panel.runModal() == .OK, let url = panel.url else { return }
        do {
            let specs = try JSONCoding.decoder.decode([JobSpec].self, from: Data(contentsOf: url))
            for s in specs {
                add(urls: [URL(fileURLWithPath: s.inputPath)], presetID: s.preset.id.isEmpty ? nil : s.preset.id, apply: s)
            }
        } catch {
            queueError = "Coda non valida: \(error.localizedDescription)"
        }
    }

    // MARK: esecuzione

    func start() {
        guard canStart else { return }
        let runnable = items.filter(\.isRunnable)
        let jobs = runnable.compactMap { job(for: $0) }
        guard jobs.count == runnable.count else { return }

        let url = FileManager.default.temporaryDirectory.appending(path: "encody-\(UUID().uuidString).json")
        do {
            for j in jobs {
                try FileManager.default.createDirectory(at: URL(fileURLWithPath: j.outputPath).deletingLastPathComponent(),
                                                        withIntermediateDirectories: true)
            }
            try JSONCoding.encoder.encode(jobs).write(to: url)
        } catch {
            queueError = "Impossibile preparare la coda: \(error.localizedDescription)"
            return
        }

        for it in runnable {
            it.status = .queued
            it.percent = 0
            it.fps = 0
            it.step = ""
            it.elapsed = 0
            it.result = nil
            it.errorMessage = nil
            it.log = []
        }
        runningItems = runnable
        queueFile = url
        queueLog = []
        recap = nil
        queueError = nil
        isRunning = true
        activity = ProcessInfo.processInfo.beginActivity(options: [.userInitiated, .idleSystemSleepDisabled],
                                                         reason: "Encoding Encody")

        var args = ["run"]
        if testMode { args.append("--test") }
        args.append(url.path)
        let r = EngineRun(arguments: args)
        run = r
        do {
            try r.start(onEvent: { [weak self] ev in self?.handle(ev) },
                        onExit: { [weak self] code, err in self?.runFinished(code: code, stderr: err) })
        } catch {
            runFinished(code: -1, stderr: error.localizedDescription)
        }
        updateDockBadge()
    }

    func stop() {
        run?.interrupt()
    }

    private func handle(_ ev: EngineEvent) {
        let item: QueueItem? = ev.job.flatMap { $0 >= 0 && $0 < runningItems.count ? runningItems[$0] : nil }
        switch ev.type {
        case "job_start":
            item?.status = .running
            item?.step = ""
            item?.percent = 0
        case "step":
            item?.step = ev.step ?? ""
            item?.percent = 0
        case "progress":
            if let s = ev.step { item?.step = s }
            item?.percent = ev.percent ?? 0
            item?.fps = ev.fps ?? 0
        case "info":
            if let m = ev.message {
                if let item { item.log.append(m) } else { queueLog.append(m) }
            }
        case "job_done":
            guard let item else { break }
            item.elapsed = ev.elapsed ?? 0
            switch ev.status {
            case "ok":
                item.status = .done
                item.percent = 100
                item.result = JobResult(inBytes: ev.inBytes ?? 0, outBytes: ev.outBytes ?? 0, estimated: ev.estimated ?? false,
                                        elapsed: ev.elapsed ?? 0, speed: ev.speed ?? 0, avgFps: ev.avgFps ?? 0)
            case "stop":
                item.status = .stopped
            default:
                item.status = .failed
                item.errorMessage = ev.error
            }
            updateDockBadge()
        case "queue_done":
            buildRecap(ev)
        case "error":
            queueError = ev.message
        default:
            break
        }
    }

    private func buildRecap(_ ev: EngineEvent) {
        let rows = runningItems.map { it in
            RecapRow(name: it.name, status: it.status, inBytes: it.result?.inBytes ?? 0,
                     outBytes: it.result?.outBytes ?? 0, elapsed: it.elapsed, error: it.errorMessage)
        }
        let r = QueueRecap(rows: rows, ok: ev.ok ?? 0, fail: ev.fail ?? 0, stopped: ev.stopped ?? 0,
                           total: ev.total ?? rows.count, totalIn: ev.totalIn ?? 0, totalOut: ev.totalOut ?? 0,
                           elapsed: ev.elapsed ?? 0, test: ev.test ?? false)
        recap = r
        showRecap = true
        var body = "\(r.ok) completati su \(r.total)"
        if r.ok > 0 { body += " · \(Fmt.bytes(r.totalIn)) → \(Fmt.bytes(r.totalOut))" }
        notify(title: r.fail > 0 ? "Coda terminata con errori" : "Coda completata", body: body)
    }

    private func runFinished(code: Int32, stderr: String) {
        for it in runningItems where it.isLocked { it.status = .stopped }
        isRunning = false
        run = nil
        if let a = activity {
            ProcessInfo.processInfo.endActivity(a)
            activity = nil
        }
        if let f = queueFile {
            try? FileManager.default.removeItem(at: f)
            queueFile = nil
        }
        NSApp.dockTile.badgeLabel = nil
        if code != 0 && code != 130 && recap == nil && queueError == nil {
            let msg = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            queueError = msg.isEmpty ? "encody terminato con codice \(code)" : msg
        }
        runningItems = []
    }

    private func updateDockBadge() {
        guard isRunning else {
            NSApp.dockTile.badgeLabel = nil
            return
        }
        let done = runningItems.filter { [.done, .failed, .stopped].contains($0.status) }.count
        NSApp.dockTile.badgeLabel = "\(done)/\(runningItems.count)"
    }

    private func notify(title: String, body: String) {
        guard Bundle.main.bundleIdentifier != nil, !NSApp.isActive,
              UserDefaults.standard.object(forKey: "notifyQueueDone") as? Bool ?? true else { return }
        let c = UNMutableNotificationContent()
        c.title = title
        c.body = body
        c.sound = .default
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: UUID().uuidString, content: c, trigger: nil))
    }

    // MARK: riepilogo per la barra inferiore

    var doneSummary: (count: Int, inBytes: Int64, outBytes: Int64) {
        let done = items.compactMap { $0.status == .done ? $0.result : nil }
        return (done.count, done.reduce(0) { $0 + $1.inBytes }, done.reduce(0) { $0 + $1.outBytes })
    }
}

// MARK: - Benchmark

@MainActor @Observable
final class BenchModel {
    var fileURL: URL?
    var probe: ProbeResult?
    var probeError: String?
    var selected: Set<String> = ["1", "3"]
    var metric: QualityMetric = .vmaf
    var isRunning = false
    var currentName = ""
    var step = ""
    var percent = 0.0
    var fps = 0.0
    var index = 0
    var total = 0
    var results: [BenchResult] = []
    var failures: [String] = []
    var warnings: [String] = []
    var errorMessage: String?

    @ObservationIgnored private var run: EngineRun?

    func setFile(_ url: URL) async {
        guard !isRunning else { return }
        fileURL = url
        probe = nil
        probeError = nil
        results = []
        failures = []
        warnings = []
        errorMessage = nil
        do {
            let data = try await Engine.runJSON(["probe", url.path])
            probe = try JSONCoding.decoder.decode(ProbeResult.self, from: data)
            warnings = probe?.warnings ?? []
            // VMAF è un modello SDR: sull'HDR si parte da XPSNR
            if probe?.isHDR == true, metric == .vmaf { metric = .xpsnr }
            if probe?.isHDR == false, metric == .xpsnr { metric = .vmaf }
        } catch {
            probeError = error.localizedDescription
        }
    }

    func start() {
        guard let fileURL, !selected.isEmpty, !isRunning else { return }
        results = []
        failures = []
        errorMessage = nil
        index = 0
        total = selected.count
        percent = 0
        step = ""
        currentName = ""
        let r = EngineRun(arguments: ["bench", "--presets", selected.sorted().joined(separator: ","),
                                      "--metric", metric.rawValue, fileURL.path])
        run = r
        isRunning = true
        do {
            try r.start(onEvent: { [weak self] ev in self?.handle(ev) },
                        onExit: { [weak self] code, err in self?.finish(code: code, stderr: err) })
        } catch {
            isRunning = false
            errorMessage = error.localizedDescription
        }
    }

    func stop() { run?.interrupt() }

    private func handle(_ ev: EngineEvent) {
        switch ev.type {
        case "bench_start":
            warnings = ev.warnings ?? []
            total = ev.total ?? total
        case "bench_preset_start":
            currentName = ev.name ?? ""
            index = (ev.index ?? 0) + 1
            percent = 0
        case "step":
            step = ev.step ?? ""
            percent = 0
        case "progress":
            if let s = ev.step { step = s }
            percent = ev.percent ?? 0
            fps = ev.fps ?? 0
        case "bench_result":
            if let r = ev.result {
                results.append(r)
                results.sort { $0.score > $1.score }
            }
        case "bench_error":
            failures.append("\(ev.name ?? ev.id ?? "?"): \(ev.error ?? "errore")")
        case "bench_done":
            if let r = ev.results { results = r }
        case "error":
            errorMessage = ev.message
        default:
            break
        }
    }

    private func finish(code: Int32, stderr: String) {
        isRunning = false
        run = nil
        step = ""
        currentName = ""
        if code != 0 && code != 130 && errorMessage == nil {
            let msg = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            errorMessage = msg.isEmpty ? "Benchmark terminato con codice \(code)" : msg
        }
    }
}

// MARK: - Quality check

struct QualityResult {
    let metric: QualityMetric
    let value: Double
    let verdict: String
    let detail: String?
}

@MainActor @Observable
final class QualityModel {
    var refURL: URL?
    var distURL: URL?
    var metric: QualityMetric = .vmaf
    var isRunning = false
    var step = ""
    var percent = 0.0
    var fps = 0.0
    var result: QualityResult?
    var errorMessage: String?

    @ObservationIgnored private var run: EngineRun?

    var canStart: Bool { refURL != nil && distURL != nil && !isRunning }

    func start() {
        guard canStart, let refURL, let distURL else { return }
        result = nil
        errorMessage = nil
        percent = 0
        step = ""
        let r = EngineRun(arguments: ["quality", "--metric", metric.rawValue, "--ref", refURL.path, "--dist", distURL.path])
        run = r
        isRunning = true
        do {
            try r.start(onEvent: { [weak self] ev in self?.handle(ev) },
                        onExit: { [weak self] code, err in self?.finish(code: code, stderr: err) })
        } catch {
            isRunning = false
            errorMessage = error.localizedDescription
        }
    }

    func stop() { run?.interrupt() }

    private func handle(_ ev: EngineEvent) {
        switch ev.type {
        case "step":
            step = ev.step ?? ""
            percent = 0
        case "progress":
            if let s = ev.step { step = s }
            percent = ev.percent ?? 0
            fps = ev.fps ?? 0
        case "quality_result":
            result = QualityResult(metric: ev.metric.flatMap(QualityMetric.init) ?? metric, value: ev.value ?? 0,
                                   verdict: ev.verdict ?? "", detail: ev.detail)
        case "error":
            errorMessage = ev.message
        default:
            break
        }
    }

    private func finish(code: Int32, stderr: String) {
        isRunning = false
        run = nil
        if code != 0 && code != 130 && errorMessage == nil {
            let msg = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            errorMessage = msg.isEmpty ? "Analisi terminata con codice \(code)" : msg
        }
    }
}
