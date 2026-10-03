import AppKit
import SwiftUI
import UniformTypeIdentifiers

struct QueueView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        // HSplitView è AppKit e non rispetta safeAreaInset: la barra va in un VStack,
        // altrimenti il Form di destra scorre sotto la barra e gli ultimi controlli restano coperti
        VStack(spacing: 0) {
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
                    Divider()
                    Button("Rimuovi completati") { model.clearFinished() }
                        .disabled(model.isRunning)
                } label: {
                    Label("Coda", systemImage: "ellipsis.circle")
                }
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
        if !sel.isEmpty {
            Button("Attiva") { model.setEnabled(true, ids: ids) }
            Button("Disattiva") { model.setEnabled(false, ids: ids) }
            Divider()
            Menu("Preset") {
                ForEach(model.presets) { p in
                    Button(p.name) { model.applyPreset(p.id, to: ids) }
                }
            }
            Menu("Audio") {
                Button("Pass") { model.applyAudioMode("copy", to: ids) }
                Button("E-AC3 Smart") { model.applyAudioMode("eac3", to: ids) }
                Button("Stereo AAC") { model.applyAudioMode("aac", to: ids) }
            }
            Divider()
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
        .buttonStyle(.link)
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
                if let g = item.grain { GrainBadge(grain: g) }
                if let p = item.probe { MetaBadge(meta: p.metaType, dvProfile: p.dvProfile) }
            }
            Text(subtitle)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            if item.status == .running {
                ProgressView(value: min(item.percent, 100), total: 100)
                    .controlSize(.small)
                Text(String(format: "%@ · %.1f%% · %.1f fps", item.step, item.percent, item.fps))
                    .font(.caption2.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
            if item.status == .done, let r = item.result {
                SavingLabel(inBytes: r.inBytes, outBytes: r.outBytes)
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
        let presetName = model.preset(item.presetID)?.name ?? "Preset \(item.presetID)"
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
            Picker("Nuovi file:", selection: $model.defaultPresetID) {
                ForEach(model.presets) { Text($0.name).tag($0.id) }
            }
            .frame(maxWidth: 320)
            .disabled(model.isRunning)
            Picker("Audio:", selection: $model.defaultAudioMode) {
                Text("Pass").tag("copy")
                Text("E-AC3 Smart").tag("eac3")
                Text("Stereo AAC").tag("aac")
            }
            .frame(maxWidth: 190)
            .disabled(model.isRunning)
            Picker("DV/HDR10+:", selection: $model.keepDynamicMetadata) {
                Text("Mantieni").tag(true)
                Text("Solo HDR10").tag(false)
            }
            .frame(maxWidth: 220)
            .help("Dolby Vision e HDR10+ dei nuovi file: mantenuti (base HDR10 + metadati reiniettati) oppure scartati. Si cambia per file nell'editor.")
            .disabled(model.isRunning)
            Button { model.chooseOutputDir() } label: {
                Label(model.outputDir.lastPathComponent, systemImage: "folder")
            }
            .help("Cartella di output: \(model.outputDirPath)")
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
        VStack(alignment: .trailing, spacing: 2) {
            Text("\(model.items.count) file · \(model.enabledCount) attivi · \(s.count) completati")
                .font(.caption)
                .foregroundStyle(.secondary)
            if s.count > 0 {
                SavingLabel(inBytes: s.inBytes, outBytes: s.outBytes)
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
                                   description: Text(item.probeError ?? "Errore sconosciuto"))
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
                    if let g = item.grain { GrainBadge(grain: g) }
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
            w.append("Il file di output esiste già e verrà sovrascritto.")
        }
        if let e = item.planError { w.append("Piano non disponibile: \(e)") }
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
                Picker("Gamma dinamica", selection: $item.rangeMode) {
                    if canDynamic {
                        Text(dynamicLabel).tag(DynamicRangeMode.dynamic)
                    }
                    Text(staticLabel).tag(DynamicRangeMode.hdr)
                    if canSDR {
                        Text("SDR (tonemap \(model.caps?.tonemapAlgo ?? "hable"))").tag(DynamicRangeMode.sdr)
                    }
                }
                RangeExplanation(mode: item.rangeMode, probe: probe)
                if probe.hasDynamicMetadata && !canDynamic, let why = item.plan?.injectBlocker, !why.isEmpty {
                    Label("\(probe.metaType) non disponibile: \(why).", systemImage: "info.circle")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }

            if !isCopy {
                HStack {
                    Toggle("Crop", isOn: $item.cropEnabled)
                        .disabled(item.cropValue.isEmpty || item.doInject)
                    TextField("crop=w:h:x:y", text: $item.cropValue)
                        .textFieldStyle(.roundedBorder)
                        .font(.body.monospaced())
                        .disabled(item.doInject)
                    Button {
                        Task { await model.detectCrop(item) }
                    } label: {
                        if item.cropDetecting {
                            ProgressView().controlSize(.small)
                        } else {
                            Text("Rileva")
                        }
                    }
                    .disabled(item.cropDetecting || item.doInject)
                }
                if item.doInject {
                    Text("Crop non disponibile: i metadati \(probe.metaType) descrivono il frame originale e andrebbero ricalcolati.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
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

    private var dynamicLabel: String {
        probe.metaType == "DV" ? "Dolby Vision (8.1, base HDR10)" : "HDR10+ (base HDR10)"
    }

    private var staticLabel: String {
        switch probe.metaType {
        case "HLG": "HLG"
        case "DV", "HDR10+": "HDR10 statico (metadati \(probe.metaType) scartati)"
        default: "HDR10"
        }
    }
}

/// Spiega in chiaro cosa produce la scelta di gamma dinamica
private struct RangeExplanation: View {
    let mode: DynamicRangeMode
    let probe: ProbeResult

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            ForEach(lines, id: \.self) { l in
                Text(l)
            }
        }
        .font(.caption)
        .foregroundStyle(.secondary)
        .fixedSize(horizontal: false, vertical: true)
    }

    private var lines: [String] {
        let dv = probe.metaType == "DV"
        switch mode {
        case .dynamic:
            var l = [
                dv ? "Output Dolby Vision profilo 8.1: le TV Dolby Vision usano i metadati scena per scena dell'originale, tutte le altre riproducono il base layer HDR10."
                   : "Output HDR10+: le TV HDR10+ usano i metadati scena per scena dell'originale, tutte le altre riproducono l'HDR10.",
                "Come funziona: estrae i metadati, codifica il video, verifica che il numero di frame coincida e li reinserisce, poi rimuxa. Più lento, richiede spazio temporaneo, niente crop né ridimensionamento.",
            ]
            if dv && probe.dvProfile == 7 {
                l.append("La sorgente è profilo 7 (dual layer): viene convertita in 8.1 e l'enhancement layer va perso.")
            }
            return l
        case .hdr:
            if probe.metaType == "HLG" { return ["Mantiene l'HLG della sorgente."] }
            if probe.hasDynamicMetadata {
                return [
                    "Codifica solo il base layer HDR10 e scarta i metadati \(probe.metaType): ogni TV HDR lo riproduce, ma il tonemapping usa valori fissi per tutto il film invece che scena per scena.",
                    "Su TV molto luminose la differenza è minima; su TV meno luminose le scene chiare possono perdere un po' di dettaglio. Veloce e con crop possibile.",
                ]
            }
            return ["Mantiene l'HDR10 della sorgente."]
        case .sdr:
            return ["Converte in SDR BT.709 (linearizzazione, tonemap, conversione colore): guardabile su qualsiasi schermo. Più lento, i metadati HDR vengono scartati."]
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
                Button("Suggerite") { item.selectedAudio = Set(probe.audio.filter(\.suggested).map(\.index)) }
                Button("Tutte") { item.selectedAudio = Set(probe.audio.map(\.index)) }
                Button("Nessuna") { item.selectedAudio = [] }
            }
            .buttonStyle(.link)
            .font(.caption)
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
            return "AC3/E-AC3 e tracce stereo copiate; TrueHD, DTS e FLAC convertiti in E-AC3 640k (max 5.1, 7.1 ridotto a 5.1)."
        case "aac":
            return "Tutto in AAC 256k stereo: massima compatibilità, qualità e spazialità minori."
        default:
            if p?.isCopy == true { return "Remux: tutte le tracce copiate così come sono." }
            let pass = p?.passthroughLabel ?? ""
            return "Copia \(pass.isEmpty ? "i formati supportati" : pass); il resto in AC3 (multicanale 640k, stereo \(p?.audioBitrate ?? "320k"))."
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
                    Button("Suggeriti") { item.selectedSubs = Set(probe.subs.filter(\.suggested).map(\.index)) }
                    Button("Tutti") { item.selectedSubs = Set(probe.subs.map(\.index)) }
                    Button("Nessuno") { item.selectedSubs = [] }
                }
                .buttonStyle(.link)
                .font(.caption)
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
                        Chip("\(sel.filter(\.enabled).count) attivi")
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
                TableColumn("File") { r in
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
