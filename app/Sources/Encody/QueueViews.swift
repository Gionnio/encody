import AppKit
import SwiftUI
import UniformTypeIdentifiers

struct QueueView: View {
    @Environment(AppModel.self) private var model
    @State private var confirmClear: ClearScope?

    enum ClearScope { case pending, all }

    var body: some View {
        @Bindable var model = model
        // HSplitView è AppKit e non rispetta safeAreaInset: la barra va in un VStack,
        // altrimenti il Form di destra scorre sotto la barra e gli ultimi controlli restano coperti
        VStack(spacing: 0) {
            if model.items.isEmpty {
                EmptyQueueView()
            } else {
            HSplitView {
                QueueListView()
                    .frame(minWidth: 320, idealWidth: 380, maxHeight: .infinity)
                Group {
                    if let item = model.selectedItem {
                        JobEditorView(item: item).id(item.id)
                    } else if model.selection.count > 1 {
                        MultiSelectionView()
                    } else {
                        ContentUnavailableView("Nessun file selezionato", systemImage: "film.stack",
                                               description: Text("Trascina file o cartelle nella lista, oppure usa Aggiungi (⌘O)."))
                    }
                }
                .frame(minWidth: 460, maxWidth: .infinity, maxHeight: .infinity)
            }
            }
            Divider()
            QueueFooter()
        }
        .navigationTitle("Coda")
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                Button { model.openFiles() } label: { Label("Aggiungi", systemImage: "plus") }
                    .help("Aggiungi file o cartelle")
                Menu {
                    Button("Importa coda…") { model.importQueue() }
                    Button("Esporta coda…") { model.exportQueue() }
                        .disabled(model.items.isEmpty)
                } label: {
                    Label("Coda", systemImage: "ellipsis.circle")
                }
                Menu {
                    Button("Rimuovi completati (\(model.finishedCount))") { model.clearFinished() }
                        .disabled(model.finishedCount == 0)
                    Button("Rimuovi da fare (\(model.pendingItems.count))…") { confirmClear = .pending }
                        .disabled(model.pendingItems.isEmpty)
                    Divider()
                    Button("Svuota tutta la coda…", role: .destructive) { confirmClear = .all }
                        .disabled(model.items.allSatisfy(\.isLocked))
                } label: {
                    Label("Svuota", systemImage: "trash")
                }
                .help("Rimuovi file dalla coda (i file su disco non vengono toccati)")
                .disabled(model.items.isEmpty)
                Toggle(isOn: $model.testMode) {
                    Label("Test 5 min", systemImage: "flask")
                }
                .help("Codifica solo i primi 5 minuti di ogni file")
                .disabled(model.isRunning)
                if model.isRunning {
                    Button(role: .destructive) { model.stop() } label: {
                        Label("Stop", systemImage: "stop.fill")
                    }
                    .help("Interrompe la coda (file parziali rimossi)")
                } else {
                    Button { model.start() } label: {
                        Label("Avvia", systemImage: "play.fill")
                    }
                    .disabled(!model.canStart)
                    .keyboardShortcut("r")
                    .help("Avvia la coda (⌘R)")
                }
            }
        }
        .confirmationDialog(confirmClear == .all ? "Svuotare la coda?" : "Rimuovere i file da fare?",
                            isPresented: Binding(get: { confirmClear != nil }, set: { if !$0 { confirmClear = nil } })) {
            Button(confirmClear == .all ? "Svuota coda" : "Rimuovi", role: .destructive) {
                if confirmClear == .all { model.clearAll() } else { model.clearPending() }
                confirmClear = nil
            }
            Button("Annulla", role: .cancel) { confirmClear = nil }
        } message: {
            Text(model.isRunning
                 ? String(localized: "Le impostazioni scelte per questi file andranno perse. I file in codifica restano in coda; i file su disco non vengono toccati.")
                 : String(localized: "Le impostazioni scelte per questi file andranno perse. I file su disco non vengono toccati."))
        }
        .sheet(isPresented: $model.showRecap) {
            if let r = model.recap { RecapSheet(recap: r) }
        }
        .alert("Errore", isPresented: Binding(get: { model.queueError != nil },
                                              set: { if !$0 { model.queueError = nil } })) {
            Button("OK") {}
        } message: {
            Text(model.queueError ?? "")
        }
    }
}

// MARK: - Lista

struct QueueListView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        VStack(spacing: 0) {
            QueueListHeader()
            Divider()
            List(selection: $model.selection) {
                ForEach(model.items) { item in
                    QueueRow(item: item)
                }
                .onMove { from, to in
                    guard !model.isRunning else { return }
                    model.items.move(fromOffsets: from, toOffset: to)
                }
            }
            .contextMenu(forSelectionType: UUID.self) { ids in
                selectionMenu(ids)
            }
            .overlay { if model.items.isEmpty { DropHint() } }
            .dropDestination(for: URL.self) { urls, _ in
                model.add(urls: urls)
                return !urls.isEmpty
            }
            .onDeleteCommand { model.removeSelected() }
            .onKeyPress(.space) {
                model.toggleEnabledSelection()
                return .handled
            }
        }
    }

    @ViewBuilder
    private func selectionMenu(_ ids: Set<UUID>) -> some View {
        let sel = model.items.filter { ids.contains($0.id) }
        // completati e in lavorazione non si modificano: per loro niente attiva/preset/audio
        let editable = sel.filter { $0.status != .done && !$0.isLocked }
        let done = sel.filter { $0.status == .done }
        if !sel.isEmpty {
            if !done.isEmpty {
                Button(done.count == 1 ? "Rimetti in coda" : "Rimetti in coda \(done.count) file") {
                    model.requeue(ids: Set(done.map(\.id)))
                }
                .disabled(model.isRunning)
                .help("Torna da fare con le stesse impostazioni, modificabili; il nuovo output non sovrascrive quello fatto")
                Divider()
            }
            if !editable.isEmpty {
                let editIDs = Set(editable.map(\.id))
                Button("Attiva") { model.setEnabled(true, ids: editIDs) }
                Button("Disattiva") { model.setEnabled(false, ids: editIDs) }
                Divider()
                Menu("Preset") {
                    ForEach(model.presets) { p in
                        Button(p.name) { model.applyPreset(p.id, to: editIDs) }
                    }
                }
                Menu("Audio") {
                    Button("Pass") { model.applyAudioMode("copy", to: editIDs) }
                    Button("E-AC3 Smart") { model.applyAudioMode("eac3", to: editIDs) }
                    Button("Stereo AAC") { model.applyAudioMode("aac", to: editIDs) }
                }
                Divider()
            }
            if sel.count == 1, let item = sel.first {
                Button("Mostra originale nel Finder") { model.reveal(item.url) }
                if item.status == .done {
                    Button("Mostra output nel Finder") { model.reveal(item.outputURL) }
                    Button("Verifica qualità") { model.openQuality(for: item) }
                }
                Divider()
            }
            Button(sel.count == 1 ? "Rimuovi" : "Rimuovi \(sel.count) file", role: .destructive) {
                model.remove(ids: ids)
            }
            .disabled(sel.allSatisfy(\.isLocked))
        }
    }
}

/// Flag di massa sopra la lista
private struct QueueListHeader: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        HStack(spacing: 10) {
            Toggle(isOn: allBinding) { EmptyView() }
                .toggleStyle(.checkbox)
                .labelsHidden()
                .help("Attiva o disattiva tutti")
            Text("\(model.enabledCount) di \(model.items.filter { $0.status != .done }.count) attivi")
                .font(.caption)
                .foregroundStyle(.secondary)
            Spacer()
            Button("Tutti") { model.setEnabled(true) }
            Button("Nessuno") { model.setEnabled(false) }
            Button("Inverti") { model.invertEnabled() }
        }
        .buttonStyle(.accentLink)
        .font(.caption)
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .disabled(model.items.isEmpty || model.isRunning)
    }

    /// Spuntato se tutti i file da fare sono attivi
    private var allBinding: Binding<Bool> {
        Binding(
            get: {
                let todo = model.items.filter { $0.status != .done }
                return !todo.isEmpty && todo.allSatisfy(\.enabled)
            },
            set: { model.setEnabled($0) }
        )
    }
}

struct QueueRow: View {
    @Environment(AppModel.self) private var model
    @Bindable var item: QueueItem

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Toggle(isOn: $item.enabled) { EmptyView() }
                .toggleStyle(.checkbox)
                .labelsHidden()
                .disabled(item.isLocked || item.status == .done)
                .help(item.enabled ? "Incluso nella prossima esecuzione" : "Escluso dalla prossima esecuzione")
            rowContent
                .opacity(item.enabled || item.status == .done ? 1 : 0.45)
        }
        .padding(.vertical, 3)
        .task(id: item.planKey) {
            try? await Task.sleep(for: .milliseconds(150)) // debounce
            guard !Task.isCancelled else { return }
            await model.refreshPlan(item)
        }
    }

    private var rowContent: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                StatusIcon(status: item.status)
                Text(item.name)
                    .font(.headline)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: 4)
                // ordine fisso: formato HDR, poi grana
                if let p = item.probe { MetaBadge(meta: p.metaType, dvProfile: p.dvProfile) }
                if let g = item.grain { GrainBadge(grain: g) }
            }
            if item.status == .running {
                ThinProgress(value: item.percent / 100)
                HStack {
                    Text(String(format: "%@ · %.0f%% · %.0f fps", item.step, item.percent, item.fps))
                    Spacer()
                    if let r = item.remaining { Text(Fmt.remaining(r)) }
                }
                .font(.caption2.monospacedDigit())
                .foregroundStyle(.secondary)
            } else {
                Text(subtitle)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            if item.status == .done, let r = item.result {
                HStack(spacing: 8) {
                    SavingLabel(inBytes: r.inBytes, outBytes: r.outBytes)
                    if !r.warnings.isEmpty {
                        Label("Da controllare", systemImage: "exclamationmark.triangle.fill")
                            .font(.caption)
                            .foregroundStyle(.orange)
                            .help(r.warnings.map(\.title).joined(separator: "\n"))
                    }
                }
            }
            if item.status == .failed, let e = item.errorMessage {
                Text(e.components(separatedBy: "\n").first ?? e)
                    .font(.caption)
                    .foregroundStyle(.red)
                    .lineLimit(2)
            }
        }
    }

    private var subtitle: String {
        let presetName = model.preset(item.presetID)?.name ?? String(localized: "Preset \(item.presetID)")
        switch item.status {
        case .ready, .done, .failed, .stopped, .queued:
            return presetName + " · " + (item.plan?.videoLabel ?? item.status.label)
        default:
            return item.status.label
        }
    }
}

// MARK: - Barra inferiore

struct QueueFooter: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        HStack(spacing: 14) {
            // impostazioni dei nuovi file in un unico gruppo etichettato
            HStack(spacing: 8) {
                Text("Nuovi file").font(.caption).foregroundStyle(.secondary)
                Picker("Preset", selection: $model.defaultPresetID) {
                    ForEach(model.presets) { Text($0.name).tag($0.id) }
                }
                .labelsHidden()
                .frame(maxWidth: 260)
                .help("Preset dei nuovi file")
                Picker("Audio", selection: $model.defaultAudioMode) {
                    Text("Audio: Pass").tag("copy")
                    Text("Audio: E-AC3 Smart").tag("eac3")
                    Text("Audio: Stereo AAC").tag("aac")
                }
                .labelsHidden()
                .frame(minWidth: 165, maxWidth: 190) // con fixedSize il menu restava largo una lettera
                .help("Audio dei nuovi file")
                Picker("DV/HDR10+", selection: $model.keepDynamicMetadata) {
                    Text("DV: Mantieni").tag(true)
                    Text("DV: Solo HDR10").tag(false)
                }
                .labelsHidden()
                .frame(minWidth: 140, maxWidth: 170) // con fixedSize restava largo una lettera
                .help("Dolby Vision e HDR10+ dei nuovi file: mantenuti (base HDR10 + metadati reiniettati) oppure scartati. Si cambia per file nell'editor.")
                Button { model.chooseOutputDir() } label: {
                    Label(model.outputDir.lastPathComponent, systemImage: "folder")
                }
                .help("Cartella di output: \(model.outputDirPath)")
            }
            .padding(.leading, 10)
            .padding(.trailing, 4)
            .padding(.vertical, 3)
            .background(RoundedRectangle(cornerRadius: 9).fill(.background.opacity(0.6)))
            .overlay(RoundedRectangle(cornerRadius: 9).strokeBorder(.separator))
            .disabled(model.isRunning)
            Spacer()
            summary
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(.bar)
    }

    @ViewBuilder private var summary: some View {
        let s = model.doneSummary
        let running = model.items.filter { $0.status == .running }.count
        VStack(alignment: .trailing, spacing: 2) {
            Text(running > 0 ? "\(model.items.count) file · \(running) in corso · \(s.count) completati"
                             : "\(model.items.count) file · \(model.enabledCount) attivi · \(s.count) completati")
                .font(.caption)
                .foregroundStyle(.secondary)
            HStack(spacing: 6) {
                if s.count > 0 { SavingLabel(inBytes: s.inBytes, outBytes: s.outBytes) }
                if let end = model.queueFinish {
                    Text("fine stimata \(end.formatted(date: .omitted, time: .shortened))")
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(.secondary)
                }
            }
        }
    }
}

// MARK: - Editor del job

struct JobEditorView: View {
    @Environment(AppModel.self) private var model
    @Bindable var item: QueueItem

    var body: some View {
        if let probe = item.probe {
            Form {
                FileHeaderSection(item: item, probe: probe)
                WarningsSection(item: item, probe: probe)
                VideoSection(item: item, probe: probe)
                AudioSection(item: item, probe: probe)
                SubtitleSection(item: item, probe: probe)
                OutputSection(item: item)
                if item.status == .done, let r = item.result {
                    ResultSection(item: item, result: r)
                }
                if !item.log.isEmpty || item.errorMessage != nil {
                    LogSection(item: item)
                }
            }
            .formStyle(.grouped)
        } else if item.status == .probing {
            ProgressView("Analisi del file…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ContentUnavailableView("File non leggibile", systemImage: "exclamationmark.triangle",
                                   description: Text(item.probeError ?? String(localized: "Errore sconosciuto")))
        }
    }
}

private struct FileHeaderSection: View {
    @Environment(AppModel.self) private var model
    let item: QueueItem
    let probe: ProbeResult

    var body: some View {
        Section {
            VStack(alignment: .leading, spacing: 8) {
                Text(probe.name)
                    .font(.title3.weight(.semibold))
                    .textSelection(.enabled)
                HStack(spacing: 6) {
                    MetaBadge(meta: probe.metaType, dvProfile: probe.dvProfile)
                    Chip(Fmt.bytes(probe.size))
                    Chip(Fmt.duration(probe.duration))
                    Chip("\(probe.width)×\(probe.height)")
                    Chip(String(format: "%.3f fps", probe.fps))
                    if let g = item.grain { GrainBadge(grain: g) }  // dopo il formato HDR, come nella riga
                }
                if let g = item.grain, g.level != .low, item.presetID != GrainInfo.presetID,
                   let grainPreset = model.preset(GrainInfo.presetID) {
                    HStack(spacing: 8) {
                        Label("Film con grana \(g.level.label): i preset normali tendono a lisciarla.",
                              systemImage: "circle.dotted")
                            .font(.caption)
                            .foregroundStyle(g.level.color)
                        Spacer(minLength: 4)
                        Button("Usa \(grainPreset.name)") { item.presetID = GrainInfo.presetID }
                            .controlSize(.small)
                            .disabled(item.isLocked)
                    }
                }
                if item.status == .running {
                    ProgressView(value: min(item.percent, 100), total: 100) {
                        Text(item.step)
                    } currentValueLabel: {
                        Text(String(format: "%.1f%% · %.1f fps", item.percent, item.fps))
                            .monospacedDigit()
                    }
                }
                if item.isLocked {
                    Label("Job in coda: le impostazioni sono bloccate.", systemImage: "lock.fill")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
        }
    }
}

private struct WarningsSection: View {
    let item: QueueItem
    let probe: ProbeResult

    var body: some View {
        let warnings = allWarnings
        if !warnings.isEmpty {
            Section {
                ForEach(warnings, id: \.self) { w in
                    Label(w, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.orange)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private var allWarnings: [String] {
        var w = probe.warnings
        for pw in item.plan?.warnings ?? [] where !w.contains(pw) { w.append(pw) }
        if item.status != .done, FileManager.default.fileExists(atPath: item.outputURL.path) {
            w.append(String(localized: "Il file di output esiste già e verrà sovrascritto."))
        }
        if let e = item.planError { w.append(String(localized: "Piano non disponibile: \(e)")) }
        return w
    }
}

private struct VideoSection: View {
    @Environment(AppModel.self) private var model
    @Bindable var item: QueueItem
    let probe: ProbeResult

    var body: some View {
        let isCopy = model.preset(item.presetID)?.isCopy ?? false
        Section("Video") {
            Picker("Preset", selection: $item.presetID) {
                ForEach(model.presets) { Text($0.name).tag($0.id) }
            }
            .onChange(of: item.presetID) {
                item.tonemap = model.defaultTonemap(for: item)
            }

            if probe.isHDR && !isCopy {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Gamma dinamica")
                    HStack(alignment: .top, spacing: 8) {
                        ForEach(rangeOptions, id: \.mode) { o in
                            RangeTile(title: o.title, detail: o.detail, help: RangeExplanation.lines(mode: o.mode, probe: probe),
                                      selected: item.rangeMode == o.mode) { item.rangeMode = o.mode }
                        }
                    }
                }
                if probe.hasDynamicMetadata && !canDynamic, let why = item.plan?.injectBlocker, !why.isEmpty {
                    Label("\(probe.metaType) non disponibile: \(why).", systemImage: "info.circle")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }

            if !isCopy {
                CropRow(item: item, probe: probe)
            }

            LabeledContent("Risultato") {
                Text(item.plan?.videoLabel ?? "…")
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.trailing)
            }
        }
        .disabled(item.isLocked)
    }

    private var canDynamic: Bool { probe.hasDynamicMetadata && item.plan?.canInject == true }
    private var canSDR: Bool { item.plan?.canTonemap ?? (model.caps?.hasZscale ?? false) }

    private struct RangeOption { let mode: DynamicRangeMode; let title: String; let detail: String }

    /// Solo le scelte possibili per questo file e preset
    private var rangeOptions: [RangeOption] {
        var o: [RangeOption] = []
        if canDynamic {
            let dv = probe.metaType == "DV"
            var d = String(localized: "Base HDR10 + metadati scena per scena.")
            if dv && probe.dvProfile == 7 { d += " " + String(localized: "Profilo 7 → 8.1.") }
            o.append(RangeOption(mode: .dynamic, title: dv ? "Dolby Vision 8.1" : "HDR10+", detail: d))
        }
        if item.plan?.mustTonemap != true {
            let hlg = probe.metaType == "HLG"
            o.append(RangeOption(mode: .hdr, title: hlg ? "HLG" : "HDR10",
                                 detail: probe.hasDynamicMetadata ? String(localized: "Solo base HDR10, tonemap fisso. Più veloce.")
                                                                  : String(localized: "Mantiene l'HDR della sorgente.")))
        }
        if canSDR {
            o.append(RangeOption(mode: .sdr, title: "SDR", detail: String(localized: "Per schermi non HDR. Più lento.")))
        }
        return o
    }
}

/// Spiegazione completa di ogni scelta di gamma dinamica, mostrata come tooltip delle schede
private enum RangeExplanation {
    static func lines(mode: DynamicRangeMode, probe: ProbeResult) -> String {
        rawLines(mode: mode, probe: probe).joined(separator: "\n\n")
    }

    private static func rawLines(mode: DynamicRangeMode, probe: ProbeResult) -> [String] {
        let dv = probe.metaType == "DV"
        switch mode {
        case .dynamic:
            var l = [
                dv ? String(localized: "Output Dolby Vision profilo 8.1: le TV Dolby Vision usano i metadati scena per scena dell'originale, tutte le altre riproducono il base layer HDR10.")
                   : String(localized: "Output HDR10+: le TV HDR10+ usano i metadati scena per scena dell'originale, tutte le altre riproducono l'HDR10."),
                String(localized: "Come funziona: estrae i metadati, codifica il video, verifica che il numero di frame coincida e li reinserisce, poi rimuxa. Più lento e richiede spazio temporaneo. Crop e ridimensionamento sono possibili: le bande nere del Dolby Vision vengono ricalcolate."),
            ]
            if dv && probe.dvProfile == 7 {
                l.append(String(localized: "La sorgente è profilo 7 (dual layer): viene convertita in 8.1 e l'enhancement layer va perso."))
            }
            return l
        case .hdr:
            if probe.metaType == "HLG" { return [String(localized: "Mantiene l'HLG della sorgente.")] }
            if probe.hasDynamicMetadata {
                return [
                    String(localized: "Codifica solo il base layer HDR10 e scarta i metadati \(probe.metaType): ogni TV HDR lo riproduce, ma il tonemapping usa valori fissi per tutto il film invece che scena per scena."),
                    String(localized: "Su TV molto luminose la differenza è minima; su TV meno luminose le scene chiare possono perdere un po' di dettaglio. Più veloce."),
                ]
            }
            return [String(localized: "Mantiene l'HDR10 della sorgente.")]
        case .sdr:
            return [String(localized: "Converte in SDR BT.709 (linearizzazione, tonemap, conversione colore): guardabile su qualsiasi schermo. Più lento, i metadati HDR vengono scartati.")]
        }
    }
}

private struct AudioSection: View {
    @Environment(AppModel.self) private var model
    @Bindable var item: QueueItem
    let probe: ProbeResult

    var body: some View {
        Section {
            Picker("Modalità", selection: $item.audioMode) {
                Text("Pass").tag("copy")
                Text("E-AC3 Smart").tag("eac3")
                Text("Stereo AAC").tag("aac")
            }
            .pickerStyle(.segmented)
            Text(modeDescription)
                .font(.caption)
                .foregroundStyle(.secondary)

            if probe.audio.isEmpty {
                Text("Nessuna traccia audio.").foregroundStyle(.secondary)
            }
            ForEach(probe.audio) { t in
                Toggle(isOn: member($item.selectedAudio, t.index)) {
                    TrackRow(track: t, planDesc: planDesc(for: t.index))
                }
            }
        } header: {
            HStack {
                Text("Audio")
                Spacer()
                Group {
                    Button("Suggerite") { item.selectedAudio = Set(probe.audio.filter(\.suggested).map(\.index)) }
                    Button("Tutte") { item.selectedAudio = Set(probe.audio.map(\.index)) }
                    Button("Nessuna") { item.selectedAudio = [] }
                }
                .buttonStyle(.accentLink)
                .font(.caption)
            }
        }
        .disabled(item.isLocked)
    }

    private func planDesc(for index: Int) -> String? {
        guard item.selectedAudio.contains(index) else { return nil }
        return item.plan?.audio.first { $0.index == index }?.desc
    }

    private var modeDescription: String {
        let p = model.preset(item.presetID)
        switch item.audioMode {
        case "eac3":
            return String(localized: "AC3/E-AC3 e tracce stereo copiate; TrueHD, DTS e FLAC convertiti in E-AC3 640k (max 5.1, 7.1 ridotto a 5.1).")
        case "aac":
            return String(localized: "Tutto in AAC 256k stereo: massima compatibilità, qualità e spazialità minori.")
        default:
            if p?.isCopy == true { return String(localized: "Remux: tutte le tracce copiate così come sono.") }
            let pass = p?.passthroughLabel ?? ""
            let what = pass.isEmpty ? String(localized: "i formati supportati") : pass
            let stereo = p?.audioBitrate ?? "320k"
            return String(localized: "Copia \(what); il resto in AC3 (multicanale 640k, stereo \(stereo)).")
        }
    }
}

private struct SubtitleSection: View {
    @Bindable var item: QueueItem
    let probe: ProbeResult

    var body: some View {
        if !probe.subs.isEmpty {
            Section {
                ForEach(probe.subs) { t in
                    Toggle(isOn: member($item.selectedSubs, t.index)) {
                        TrackRow(track: t, planDesc: subPlan(t.index))
                    }
                }
            } header: {
                HStack {
                    Text("Sottotitoli")
                    Spacer()
                    Group {
                        Button("Suggeriti") { item.selectedSubs = Set(probe.subs.filter(\.suggested).map(\.index)) }
                        Button("Tutti") { item.selectedSubs = Set(probe.subs.map(\.index)) }
                        Button("Nessuno") { item.selectedSubs = [] }
                    }
                    .buttonStyle(.accentLink)
                    .font(.caption)
                }
            } footer: {
                Text("★ = italiano forced. I sottotitoli vengono copiati; i mov_text degli MP4 diventano SRT perché l'MKV non li supporta.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .disabled(item.isLocked)
        }
    }
}

extension SubtitleSection {
    fileprivate func subPlan(_ index: Int) -> String? {
        guard item.selectedSubs.contains(index) else { return nil }
        return item.plan?.subs.first { $0.index == index }?.desc
    }
}

private struct OutputSection: View {
    @Environment(AppModel.self) private var model
    @Bindable var item: QueueItem

    var body: some View {
        Section("Output") {
            LabeledContent("File") {
                Text(item.outputURL.path)
                    .lineLimit(2)
                    .truncationMode(.middle)
                    .textSelection(.enabled)
                    .multilineTextAlignment(.trailing)
            }
            HStack {
                Button("Cambia…") { chooseOutput() }
                Button("Cartella predefinita") {
                    item.customOutput = false
                    item.outputURL = model.outputDir // escluso dal controllo doppioni
                    item.outputURL = model.uniqueOutput(for: item.url)
                }
                .disabled(!item.customOutput)
                Spacer()
            }
        }
        .disabled(item.isLocked)
    }

    private func chooseOutput() {
        let panel = NSSavePanel()
        panel.allowedContentTypes = [UTType(filenameExtension: "mkv") ?? .movie]
        panel.nameFieldStringValue = item.outputURL.lastPathComponent
        panel.directoryURL = item.outputURL.deletingLastPathComponent()
        panel.canCreateDirectories = true
        guard panel.runModal() == .OK, var url = panel.url else { return }
        if url.pathExtension.lowercased() != "mkv" { url = url.appendingPathExtension("mkv") }
        item.outputURL = url
        item.customOutput = true
    }
}

private struct ResultSection: View {
    @Environment(AppModel.self) private var model
    let item: QueueItem
    let result: JobResult

    var body: some View {
        Section("Risultato") {
            LabeledContent("Originale", value: Fmt.bytes(result.inBytes) + (result.estimated ? " (stima test)" : ""))
            LabeledContent("Nuovo", value: Fmt.bytes(result.outBytes))
            LabeledContent("Variazione") { SavingLabel(inBytes: result.inBytes, outBytes: result.outBytes) }
            LabeledContent("Tempo",
                           value: "\(Fmt.duration(result.elapsed)) · \(String(format: "%.2fx realtime · %.1f fps medi", result.speed, result.avgFps))")
            HStack {
                Button("Mostra nel Finder") { model.reveal(item.outputURL) }
                Button("Verifica qualità") { model.openQuality(for: item) }
                Spacer()
            }
        }
        if !result.checks.isEmpty {
            Section {
                ForEach(result.checks, id: \.self) { CheckRow(check: $0) }
            } header: {
                Text("Verifica del file")
            } footer: {
                Text("Encody rilegge il file finito: durata, decodifica di inizio e fine, HDR e tracce.")
            }
        }
    }
}

private struct LogSection: View {
    let item: QueueItem

    var body: some View {
        Section("Log") {
            if let e = item.errorMessage {
                Text(e)
                    .font(.caption.monospaced())
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
            }
            ForEach(Array(item.log.enumerated()), id: \.offset) { _, line in
                Text(line)
                    .font(.caption.monospaced())
                    .textSelection(.enabled)
            }
        }
    }
}

// MARK: - Selezione multipla

private struct MultiSelectionView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let sel = model.selectedItems
        let editable = sel.filter { !$0.isLocked && $0.status != .done }
        let ids = Set(sel.map(\.id))
        Form {
            Section {
                VStack(alignment: .leading, spacing: 6) {
                    Text("\(sel.count) file selezionati")
                        .font(.title3.weight(.semibold))
                    HStack(spacing: 6) {
                        Chip(Fmt.bytes(sel.compactMap(\.probe?.size).reduce(0, +)))
                        Chip(Fmt.duration(sel.compactMap(\.probe?.duration).reduce(0, +)))
                        Chip(String(localized: "\(sel.filter(\.enabled).count) attivi"))
                    }
                }
            }

            Section("Inclusione") {
                HStack {
                    Button("Attiva") { model.setEnabled(true, ids: ids) }
                    Button("Disattiva") { model.setEnabled(false, ids: ids) }
                    Spacer()
                    Button("Rimuovi dalla coda", role: .destructive) { model.remove(ids: ids) }
                        .disabled(sel.allSatisfy(\.isLocked))
                }
                Text("Scorciatoie: Spazio attiva/disattiva, ⌫ rimuove. ⌘ e ⇧ per selezionare più file.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                Picker("Preset", selection: presetBinding(editable)) {
                    if commonPreset(editable) == nil { Text("Misti").tag("") }
                    ForEach(model.presets) { Text($0.name).tag($0.id) }
                }
                Picker("Audio", selection: audioBinding(editable)) {
                    if commonAudio(editable) == nil { Text("Misti").tag("") }
                    Text("Pass").tag("copy")
                    Text("E-AC3 Smart").tag("eac3")
                    Text("Stereo AAC").tag("aac")
                }
            } header: {
                Text("Modifica in blocco")
            } footer: {
                Text("Si applica ai \(editable.count) file modificabili. Tracce, crop e gamma dinamica si scelgono file per file.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .disabled(editable.isEmpty)
        }
        .formStyle(.grouped)
    }

    private func commonPreset(_ items: [QueueItem]) -> String? {
        let set = Set(items.map(\.presetID))
        return set.count == 1 ? set.first : nil
    }

    private func commonAudio(_ items: [QueueItem]) -> String? {
        let set = Set(items.map(\.audioMode))
        return set.count == 1 ? set.first : nil
    }

    private func presetBinding(_ items: [QueueItem]) -> Binding<String> {
        Binding(get: { commonPreset(items) ?? "" },
                set: { if !$0.isEmpty { model.applyPreset($0, to: Set(items.map(\.id))) } })
    }

    private func audioBinding(_ items: [QueueItem]) -> Binding<String> {
        Binding(get: { commonAudio(items) ?? "" },
                set: { if !$0.isEmpty { model.applyAudioMode($0, to: Set(items.map(\.id))) } })
    }
}

// MARK: - Recap

struct RecapSheet: View {
    let recap: QueueRecap
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Recap coda").font(.title2.bold())
            if recap.test {
                Text("Test mode: gli originali sono stimati sulla porzione codificata.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Table(recap.rows) {
                TableColumn("") { r in StatusIcon(status: r.status) }
                    .width(22)
                TableColumn(String(localized: "File")) { r in
                    Text(r.name).lineLimit(1).truncationMode(.middle).help(r.name)
                }
                TableColumn("Originale") { r in
                    Text(r.status == .done ? Fmt.bytes(r.inBytes) : "—").monospacedDigit()
                }
                .width(90)
                TableColumn("Nuovo") { r in
                    Text(r.status == .done ? Fmt.bytes(r.outBytes) : "—").monospacedDigit()
                }
                .width(90)
                TableColumn("Variazione") { r in
                    if r.status == .done {
                        SavingLabel(inBytes: r.inBytes, outBytes: r.outBytes)
                    } else {
                        Text(r.error?.components(separatedBy: "\n").first ?? r.status.label)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .help(r.error ?? "")
                    }
                }
                .width(min: 170)
                TableColumn("Tempo") { r in
                    Text(r.elapsed > 0 ? Fmt.duration(r.elapsed) : "—").monospacedDigit()
                }
                .width(80)
            }
            .frame(minHeight: 220)

            Divider()
            HStack(alignment: .top) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("\(recap.ok) completati · \(recap.fail) falliti · \(recap.stopped) interrotti su \(recap.total)")
                    Text("Tempo totale \(Fmt.duration(recap.elapsed))")
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if recap.ok > 0 {
                    VStack(alignment: .trailing, spacing: 2) {
                        Text("\(Fmt.bytes(recap.totalIn)) → \(Fmt.bytes(recap.totalOut))")
                            .font(.headline.monospacedDigit())
                        SavingLabel(inBytes: recap.totalIn, outBytes: recap.totalOut)
                    }
                }
            }
            HStack {
                Spacer()
                Button("Chiudi") { dismiss() }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(minWidth: 760, minHeight: 440)
    }
}

// MARK: - Ritocco grafico: componenti della Coda

/// Barra di avanzamento sottile nel colore di Encody
struct ThinProgress: View {
    let value: Double

    var body: some View {
        GeometryReader { g in
            ZStack(alignment: .leading) {
                Capsule().fill(.quaternary)
                Capsule().fill(Color.encody).frame(width: max(5, g.size.width * min(max(value, 0), 1)))
            }
        }
        .frame(height: 5)
    }
}

/// Una scelta di gamma dinamica: titolo, riga di spiegazione, spiegazione completa nel tooltip
private struct RangeTile: View {
    let title: String
    let detail: String
    let help: String
    let selected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.callout.weight(.semibold))
                Text(detail).font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .topLeading)
            .padding(.horizontal, 10)
            .padding(.vertical, 7)
            .background(RoundedRectangle(cornerRadius: 10).fill(selected ? Color.encody.opacity(0.12) : Color.clear))
            .overlay(RoundedRectangle(cornerRadius: 10)
                .strokeBorder(selected ? Color.encody : Color.secondary.opacity(0.3), lineWidth: selected ? 1.5 : 1))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(help)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// Crop su una riga: misura leggibile, interruttore, menu per rilevare o modificare il valore
private struct CropRow: View {
    @Environment(AppModel.self) private var model
    @Bindable var item: QueueItem
    let probe: ProbeResult
    @State private var editing = false

    var body: some View {
        LabeledContent("Crop") {
            HStack(spacing: 8) {
                if item.cropDetecting {
                    ProgressView().controlSize(.small)
                    Text("rilevamento…").font(.caption).foregroundStyle(.secondary)
                } else {
                    Text(description)
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(.secondary)
                }
                Menu {
                    Button("Rileva di nuovo") { Task { await model.detectCrop(item) } }
                    Button("Modifica valore…") { editing = true }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .fixedSize()
                .disabled(item.cropDetecting)
                .popover(isPresented: $editing) {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Valore FFmpeg").font(.caption).foregroundStyle(.secondary)
                        TextField("crop=w:h:x:y", text: $item.cropValue)
                            .textFieldStyle(.roundedBorder)
                            .font(.body.monospaced())
                            .frame(width: 220)
                    }
                    .padding(12)
                }
                Toggle("Crop", isOn: $item.cropEnabled)
                    .toggleStyle(.switch)
                    .labelsHidden()
                    .controlSize(.small)
                    .disabled(item.cropValue.isEmpty)
            }
        }
    }

    /// "3840×1608 · bande 276 px" dal valore crop=w:h:x:y
    private var description: String {
        let p = item.cropValue.replacingOccurrences(of: "crop=", with: "").split(separator: ":").compactMap { Int($0) }
        guard p.count == 4 else { return item.cropValue.isEmpty ? String(localized: "nessun crop rilevato") : item.cropValue }
        let bands = (probe.height - p[1]) + (probe.width - p[0])
        return bands > 0 ? String(localized: "\(p[0])×\(p[1]) · bande \(bands) px") : String(localized: "nessuna banda nera")
    }
}

/// Coda vuota: area di trascinamento a tutta larghezza
private struct EmptyQueueView: View {
    @Environment(AppModel.self) private var model
    @State private var targeted = false

    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "film.stack")
                .font(.system(size: 44, weight: .light))
                .foregroundStyle(Color.encody)
            Text("Trascina qui film, serie o cartelle")
                .font(.title3.weight(.semibold))
            Text("mkv, mp4, mov, m2ts, ts, avi, m4v")
                .font(.caption)
                .foregroundStyle(.secondary)
            Button("Aggiungi file…") { model.openFiles() }
                .buttonStyle(.borderedProminent)
                .keyboardShortcut("o")
            Text("Verranno codificati con \(model.preset(model.defaultPresetID)?.name ?? "—"): si cambia in basso, in Nuovi file.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(30)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(RoundedRectangle(cornerRadius: 12).fill(Color.encody.opacity(targeted ? 0.14 : 0.06)))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(Color.encody, style: StrokeStyle(lineWidth: 3, dash: [9, 6])))
        .padding(20)
        .dropDestination(for: URL.self) { urls, _ in
            model.add(urls: urls)
            return !urls.isEmpty
        } isTargeted: { targeted = $0 }
    }
}
