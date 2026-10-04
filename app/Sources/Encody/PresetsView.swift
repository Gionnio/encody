import AppKit
import SwiftUI

// MARK: - Sezione Preset

struct PresetsView: View {
    @Environment(AppModel.self) private var model
    @State private var confirmDelete: PresetInfo?

    var body: some View {
        let pm = model.presetsModel
        // elenco a larghezza fissa: HSplitView ne ignorava la larghezza ideale e rendeva lento il ridimensionamento
        HStack(spacing: 0) {
            PresetListView(confirmDelete: $confirmDelete)
                .frame(width: 290)
            Divider()
            Group {
                if pm.draft != nil {
                    PresetEditorView()
                } else if let id = pm.selectedID, let p = model.preset(id) {
                    BuiltinPresetView(preset: p).id(p.id)
                } else {
                    ContentUnavailableView("Nessun preset selezionato", systemImage: "slider.horizontal.3",
                                           description: Text("Scegli un preset per vederlo, oppure creane uno nuovo con +."))
                }
            }
            .frame(minWidth: 480, maxWidth: .infinity, maxHeight: .infinity)
        }
        .navigationTitle("Preset")
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                Menu {
                    Button("Nuovo preset") { pm.create(from: nil) }
                    if let id = pm.selectedID, let p = model.preset(id) {
                        Button("Duplica \(p.name)") { pm.create(from: p) }
                    }
                } label: {
                    Label("Nuovo", systemImage: "plus")
                }
                .help("Nuovo preset, vuoto o copia di quello selezionato")
                Button { pm.importPreset() } label: { Label("Importa", systemImage: "square.and.arrow.down") }
                    .help("Importa un preset da file .json")
                Button {
                    if let id = pm.selectedID, let spec = model.preset(id)?.spec { pm.export(spec) }
                } label: { Label("Esporta", systemImage: "square.and.arrow.up") }
                    .help("Esporta il preset selezionato in un file .json da condividere")
                    .disabled(pm.selectedID == nil)
            }
        }
        .confirmationDialog("Eliminare il preset?", isPresented: Binding(get: { confirmDelete != nil },
                                                                         set: { if !$0 { confirmDelete = nil } })) {
            Button("Elimina", role: .destructive) {
                if let p = confirmDelete { pm.delete(p.id) }
                confirmDelete = nil
            }
            Button("Annulla", role: .cancel) { confirmDelete = nil }
        } message: {
            Text("\(confirmDelete?.name ?? "") verrà eliminato. I file in coda che lo usano passano al preset predefinito.")
        }
        .onAppear { pm.app = model }
    }
}

private struct PresetListView: View {
    @Environment(AppModel.self) private var model
    @Binding var confirmDelete: PresetInfo?

    var body: some View {
        let pm = model.presetsModel
        let custom = model.presets.filter { !$0.isBuiltin }
        let builtin = model.presets.filter(\.isBuiltin)
        List(selection: Binding(get: { pm.isNew ? nil : pm.selectedID }, set: { pm.select($0) })) {
            if pm.isNew, let d = pm.draft {
                Section("Nuovo") {
                    PresetRow(name: d.name.isEmpty ? "Senza nome" : d.name, summary: "non ancora salvato",
                              verified: false, unsaved: true)
                }
            }
            Section("Personalizzati") {
                if custom.isEmpty {
                    Text("Nessuno: crea un preset con + o duplica uno di sistema.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                ForEach(custom) { p in
                    PresetRow(name: p.name, summary: summary(p), verified: p.spec?.verified != nil,
                              unsaved: pm.selectedID == p.id && pm.hasChanges)
                        .tag(p.id)
                        .contextMenu {
                            Button("Duplica") { pm.create(from: p) }
                            if let s = p.spec { Button("Esporta…") { pm.export(s) } }
                            Divider()
                            Button("Elimina…", role: .destructive) { confirmDelete = p }
                        }
                }
            }
            Section("Di sistema") {
                ForEach(builtin) { p in
                    PresetRow(name: p.name, summary: summary(p), verified: false, unsaved: false)
                        .tag(p.id)
                        .contextMenu { Button("Duplica per modificare") { pm.create(from: p) } }
                }
            }
            if let errs = model.caps?.presetErrors, !errs.isEmpty {
                Section("File non caricati") {
                    ForEach(errs, id: \.self) { e in
                        Label(e, systemImage: "exclamationmark.triangle.fill")
                            .font(.caption)
                            .foregroundStyle(.orange)
                    }
                }
            }
        }
        .onDeleteCommand {
            if let id = pm.selectedID, let p = model.preset(id), !p.isBuiltin { confirmDelete = p }
        }
    }

    private func summary(_ p: PresetInfo) -> String {
        guard let s = p.spec else { return p.type }
        return PresetText.summary(s, encoders: model.caps?.encoders ?? [])
    }
}

private struct PresetRow: View {
    let name: String
    let summary: String
    let verified: Bool
    let unsaved: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 4) {
                Text(name).lineLimit(1)
                if unsaved { Circle().fill(.orange).frame(width: 6, height: 6).help("Modifiche non salvate") }
                Spacer(minLength: 4)
                if verified {
                    Image(systemName: "checkmark.seal.fill").foregroundStyle(.green).help("Verificato con una prova reale")
                }
            }
            Text(summary).font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .padding(.vertical, 2)
    }
}

enum PresetText {
    static func summary(_ s: PresetSpec, encoders: [EncoderCap]) -> String {
        if s.encoder == "copy" { return "Remux: video copiato" }
        let e = encoders.first { $0.id == s.encoder }
        var parts = [shortName(s.encoder)]
        switch s.rate.mode {
        case "bitrate": parts.append("\(s.rate.bitrate ?? 0) kbit/s")
        default: parts.append("\(e?.qualityLabel ?? "Q") \(format(s.rate.value ?? 0))")
        }
        if let sp = s.speed, !sp.isEmpty { parts.append(s.encoder == "libsvtav1" ? "preset \(sp)" : sp) }
        if let w = s.scale, w > 0 { parts.append("\(w) px") }
        return parts.joined(separator: " · ")
    }

    static func shortName(_ enc: String) -> String {
        ["libx265": "x265", "hevc_videotoolbox": "VideoToolbox HEVC", "libsvtav1": "SVT-AV1",
         "libx264": "x264", "h264_videotoolbox": "VideoToolbox H.264", "copy": "Remux"][enc] ?? enc
    }

    static func format(_ v: Double) -> String {
        v == v.rounded() ? String(Int(v)) : String(format: "%.1f", v)
    }
}

// MARK: - Preset di sistema (sola lettura)

private struct BuiltinPresetView: View {
    @Environment(AppModel.self) private var model
    let preset: PresetInfo
    @State private var command: String?

    var body: some View {
        Form {
            Section {
                VStack(alignment: .leading, spacing: 6) {
                    HStack {
                        Text(preset.name).font(.title3.weight(.semibold))
                        Chip("di sistema")
                    }
                    if let d = preset.description, !d.isEmpty { Text(d).foregroundStyle(.secondary) }
                }
                Button("Duplica per modificare") { model.presetsModel.create(from: preset) }
            }
            if let s = preset.spec {
                Section("Impostazioni") {
                    LabeledContent("Encoder", value: PresetText.shortName(s.encoder))
                    if s.encoder != "copy" {
                        LabeledContent("Qualità", value: PresetText.summary(s, encoders: model.caps?.encoders ?? []))
                    }
                    if let p = s.params, !p.isEmpty {
                        LabeledContent("Parametri encoder") { Text(p).font(.caption.monospaced()).textSelection(.enabled) }
                    }
                    LabeledContent("Audio", value: "AC3 stereo \(s.audio.bitrate) · copia: \((s.audio.passthrough ?? []).joined(separator: ", "))")
                    LabeledContent("HDR", value: preset.hdr == false ? "convertito in SDR" : "conservato")
                    LabeledContent("Dolby Vision / HDR10+", value: preset.dynamic == true ? "reinseribili" : "non reinseribili")
                }
                Section("Comando FFmpeg (esempio con sorgente HDR10)") {
                    Text(command ?? "…")
                        .font(.caption.monospaced())
                        .textSelection(.enabled)
                        .foregroundStyle(.secondary)
                }
                .task { command = await model.presetsModel.command(for: s) }
            }
        }
        .formStyle(.grouped)
    }
}

// MARK: - Editor

private struct PresetEditorView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let pm = model.presetsModel
        if let draft = pm.draft {
            let binding = Binding<PresetSpec>(get: { pm.draft ?? draft }, set: { pm.draft = $0 })
            let enc = model.caps?.encoders?.first { $0.id == draft.encoder }
            VStack(spacing: 0) {
                Form {
                    Group {
                        GeneralSection(spec: binding)
                        VideoSection(spec: binding, cap: enc)
                        AdvancedSection(spec: binding, cap: enc)
                        AudioSection(spec: binding)
                    }
                    .disabled(pm.isTesting)
                    CheckSection()
                    TestSection()
                }
                .formStyle(.grouped)
                // larghezza massima: oltre questa soglia allargare la finestra non ridispone i controlli
                .frame(maxWidth: 760)
                .frame(maxWidth: .infinity)
                Divider()
                EditorBar()
            }
            .task(id: draft) {
                try? await Task.sleep(for: .milliseconds(250)) // debounce
                guard !Task.isCancelled else { return }
                await pm.validate()
            }
        }
    }
}

/// Messaggi del controllo immediato per un campo
private struct FieldIssues: View {
    @Environment(AppModel.self) private var model
    let field: String

    var body: some View {
        ForEach(model.presetsModel.validation?.issues.filter { $0.field == field } ?? [], id: \.self) { i in
            IssueLabel(issue: i)
        }
    }
}

private struct IssueLabel: View {
    let issue: PresetIssue

    var body: some View {
        Label(issue.message, systemImage: icon)
            .font(.caption)
            .foregroundStyle(color)
    }

    private var icon: String {
        switch issue.level {
        case "error": "xmark.octagon.fill"
        case "warning": "exclamationmark.triangle.fill"
        default: "info.circle"
        }
    }

    private var color: Color {
        switch issue.level {
        case "error": .red
        case "warning": .orange
        default: .secondary
        }
    }
}

private struct GeneralSection: View {
    @Binding var spec: PresetSpec

    var body: some View {
        Section("Generale") {
            TextField("Nome", text: $spec.name)
            FieldIssues(field: "name")
            TextField("Descrizione", text: Binding(get: { spec.description ?? "" }, set: { spec.description = $0.isEmpty ? nil : $0 }),
                      prompt: Text("facoltativa, es. per serie animate"), axis: .vertical)
                .lineLimit(1...3)
        }
    }
}

private struct VideoSection: View {
    @Environment(AppModel.self) private var model
    @Binding var spec: PresetSpec
    let cap: EncoderCap?

    var body: some View {
        Section("Video") {
            Picker("Encoder", selection: Binding(get: { spec.encoder }, set: { changeEncoder(to: $0) })) {
                ForEach(model.caps?.encoders ?? []) { e in Text(e.label).tag(e.id) }
            }
            if let cap {
                Text(encoderNote(cap)).font(.caption).foregroundStyle(.secondary)
            }
            FieldIssues(field: "encoder")

            if let cap {
                Picker("Controllo", selection: Binding(get: { spec.rate.mode }, set: { changeMode(to: $0, cap: cap) })) {
                    ForEach(cap.rateModes, id: \.self) { m in
                        Text(m == "bitrate" ? "Bitrate" : (m == "crf" ? "Qualità costante (CRF)" : "Qualità fissa")).tag(m)
                    }
                }
                .pickerStyle(.segmented)
                if spec.rate.mode == "bitrate" {
                    LabeledContent("Bitrate medio") {
                        HStack {
                            TextField("kbit/s", value: Binding(get: { spec.rate.bitrate ?? 0 }, set: { spec.rate.bitrate = $0 }), format: .number)
                                .frame(width: 100)
                            Text("kbit/s").foregroundStyle(.secondary)
                        }
                    }
                    LabeledContent("Bitrate massimo") {
                        HStack {
                            TextField("0 = nessuno", value: Binding(get: { spec.rate.maxrate ?? 0 }, set: { spec.rate.maxrate = $0 == 0 ? nil : $0 }), format: .number)
                                .frame(width: 100)
                            Text("kbit/s").foregroundStyle(.secondary)
                        }
                    }
                } else {
                    // passi da 0,5 per il CRF, interi per la qualità fissa
                    let unit = cap.lowerIsBetter ? 0.5 : 1.0
                    let value = Binding(get: { spec.rate.value ?? cap.qualityDefault },
                                        set: { spec.rate.value = ($0 / unit).rounded() * unit })
                    LabeledContent(cap.qualityLabel) {
                        HStack {
                            // niente step: su macOS diventa un segno di graduazione per passo (~100), lentissimo da ridisegnare
                            Slider(value: value, in: cap.qualityMin...cap.qualityMax)
                                .frame(width: 240)
                            TextField("", value: value, format: .number).frame(width: 52)
                        }
                    }
                    Text(qualityHint(cap)).font(.caption).foregroundStyle(.secondary)
                }
                FieldIssues(field: "rate")

                if let speeds = cap.speeds, !speeds.isEmpty {
                    Picker("Velocità", selection: Binding(get: { spec.speed ?? cap.speedDefault ?? "" }, set: { spec.speed = $0 })) {
                        ForEach(speeds, id: \.self) { s in Text(speedLabel(s, cap: cap)).tag(s) }
                    }
                    Text(cap.id == "libsvtav1"
                         ? "Numeri bassi = più lento, file più piccoli a pari qualità. 4–6 è un buon compromesso."
                         : "Più lento = file più piccoli a pari qualità. medium e slow sono i più usati.")
                        .font(.caption).foregroundStyle(.secondary)
                    FieldIssues(field: "speed")
                }
            }

            Picker("Risoluzione", selection: Binding(get: { spec.scale ?? 0 }, set: { spec.scale = $0 == 0 ? nil : $0 })) {
                Text("Originale").tag(0)
                Text("4K (3840 px)").tag(3840)
                Text("1440p (2560 px)").tag(2560)
                Text("1080p (1920 px)").tag(1920)
                Text("720p (1280 px)").tag(1280)
            }
            FieldIssues(field: "scale")
        }
    }

    private func encoderNote(_ c: EncoderCap) -> String {
        var s = [c.hardware ? "Usa la GPU del Mac: molto veloce, controllo fine limitato." : "Usa la CPU: lento ma con il massimo controllo."]
        s.append(c.tenBit ? "Conserva l'HDR (10 bit)." : "8 bit: le sorgenti HDR vengono convertite in SDR.")
        s.append(c.dynamic ? "Dolby Vision e HDR10+ reinseribili." : "Dolby Vision e HDR10+ non reinseribili.")
        return s.joined(separator: " ")
    }

    private func qualityHint(_ c: EncoderCap) -> String {
        switch c.id {
        case "libx265", "libx264": "CRF più basso = qualità più alta e file più grandi. 16–18 trasparente, 20–22 buono, oltre 24 si vedono artefatti."
        case "libsvtav1": "CRF più basso = qualità più alta. Per il 4K 24–30 è un buon intervallo; sotto 20 i file crescono molto."
        default: "Qualità più alta = file più grandi. 60–70 è un buon intervallo per il 4K."
        }
    }

    private func speedLabel(_ s: String, cap: EncoderCap) -> String {
        s == cap.speedDefault ? "\(s) (predefinita)" : s
    }

    private func changeEncoder(to id: String) {
        guard id != spec.encoder, let c = model.caps?.encoders?.first(where: { $0.id == id }) else { return }
        let old = model.caps?.encoders?.first { $0.id == spec.encoder }
        spec.encoder = id
        if !c.rateModes.contains(spec.rate.mode) || old?.qualityLabel != c.qualityLabel || old?.qualityMax != c.qualityMax {
            spec.rate = PresetRate(mode: c.rateModes.first ?? "crf", value: c.qualityDefault)
        }
        spec.speed = (c.speeds?.isEmpty ?? true) ? nil : c.speedDefault
        if c.paramsFlag == nil || c.paramsFlag?.isEmpty == true || old?.paramsFlag != c.paramsFlag {
            spec.params = nil // i parametri di un encoder non valgono per un altro
        }
    }

    private func changeMode(to mode: String, cap: EncoderCap) {
        guard mode != spec.rate.mode else { return }
        spec.rate = mode == "bitrate" ? PresetRate(mode: mode, bitrate: 20000, maxrate: nil)
                                      : PresetRate(mode: mode, value: cap.qualityDefault)
    }
}

private struct AdvancedSection: View {
    @Binding var spec: PresetSpec
    let cap: EncoderCap?

    /// Parametri tarati sull'Impero colpisce ancora (stessi dei preset di sistema Grana)
    private static let grainParams = "no-sao=1:aq-mode=3:aq-strength=0.8:psy-rd=3:psy-rdoq=10:deblock=-2,-2:ipratio=1.2:pbratio=1.1:rskip=2:rskip-edge-threshold=2:strong-intra-smoothing=0"

    var body: some View {
        Section {
            if let flag = cap?.paramsFlag, !flag.isEmpty {
                LabeledContent("Parametri encoder") {
                    TextField("", text: Binding(get: { spec.params ?? "" }, set: { spec.params = $0.isEmpty ? nil : $0 }),
                              prompt: Text(cap?.paramsHelp ?? ""), axis: .vertical)
                        .font(.body.monospaced())
                        .lineLimit(1...4)
                }
                HStack {
                    Text("Passati con \(flag).").font(.caption).foregroundStyle(.secondary)
                    Spacer()
                    if cap?.id == "libx265" {
                        Button("Conserva la grana") { spec.params = Self.grainParams }
                            .controlSize(.small)
                            .help("Parametri dei preset Grana: niente SAO, deblock leggero, psy-rd/psy-rdoq alti, aq-mode 3")
                    }
                    if cap?.id == "libsvtav1" {
                        Button("Sintesi della grana") { spec.params = "tune=0:film-grain=8:film-grain-denoise=1" }
                            .controlSize(.small)
                            .help("Toglie la grana e la ricrea in riproduzione: file molto più piccoli, ma non tutti i player la supportano")
                    }
                }
                FieldIssues(field: "params")
            }
            LabeledContent("Parametri extra FFmpeg") {
                TextField("", text: Binding(get: { spec.extra ?? "" }, set: { spec.extra = $0.isEmpty ? nil : $0 }),
                          prompt: Text("es. -g 240 -bf 5"))
                    .font(.body.monospaced())
            }
            Text("Solo aggiuntivi: input, tracce, filtri, tag colore e audio li gestisce Encody.")
                .font(.caption).foregroundStyle(.secondary)
            FieldIssues(field: "extra")
        } header: {
            Text("Avanzate")
        }
    }
}

private struct AudioSection: View {
    @Binding var spec: PresetSpec
    private let codecs = ["truehd", "dts", "eac3", "ac3", "aac", "flac", "opus"]

    var body: some View {
        Section {
            Picker("AC3 stereo", selection: $spec.audio.bitrate) {
                ForEach(["192k", "224k", "256k", "320k", "384k", "448k", "640k"], id: \.self) { Text($0).tag($0) }
            }
            // un solo menu invece di sette caselle: ogni casella è un controllo AppKit da riposizionare
            // a ogni ridimensionamento della finestra
            LabeledContent("Copia senza ricodificare") {
                Menu(passLabel) {
                    ForEach(codecs, id: \.self) { c in
                        Toggle(label(c), isOn: Binding(
                            get: { spec.audio.passthrough?.contains(c) ?? false },
                            set: { on in
                                var p = spec.audio.passthrough ?? []
                                if on { if !p.contains(c) { p.append(c) } } else { p.removeAll { $0 == c } }
                                spec.audio.passthrough = p
                            }))
                    }
                }
                .fixedSize()
            }
            FieldIssues(field: "audio")
        } header: {
            Text("Audio predefinito")
        } footer: {
            Text("Valgono in modalità Pass: i codec spuntati restano identici, gli altri diventano AC3 (multicanale sempre a 640k).")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private var passLabel: String {
        let sel = codecs.filter { spec.audio.passthrough?.contains($0) ?? false }
        return sel.isEmpty ? "Nessuno (tutto in AC3)" : sel.map(label).joined(separator: ", ")
    }

    private func label(_ c: String) -> String {
        ["truehd": "TrueHD", "dts": "DTS", "eac3": "E-AC3", "ac3": "AC3", "aac": "AAC", "flac": "FLAC", "opus": "Opus"][c] ?? c
    }
}

private struct CheckSection: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let pm = model.presetsModel
        let general = pm.validation?.issues.filter { !["name", "rate", "speed", "scale", "params", "extra", "audio", "encoder"].contains($0.field) } ?? []
        Section("Controllo") {
            if pm.validation == nil {
                ProgressView().controlSize(.small)
            } else if pm.hasErrors {
                Label("Ci sono errori da correggere (indicati sotto i campi).", systemImage: "xmark.octagon.fill")
                    .foregroundStyle(.red)
            } else {
                Label("Nessun errore", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
            }
            ForEach(general, id: \.self) { IssueLabel(issue: $0) }
            if let cmd = pm.validation?.command {
                DisclosureGroup("Comando FFmpeg (esempio con sorgente HDR10)") {
                    HStack(alignment: .top) {
                        Text(cmd)
                            .font(.caption.monospaced())
                            .textSelection(.enabled)
                            .foregroundStyle(.secondary)
                        Spacer(minLength: 4)
                        Button {
                            NSPasteboard.general.clearContents()
                            NSPasteboard.general.setString(cmd, forType: .string)
                        } label: { Image(systemName: "doc.on.doc") }
                            .buttonStyle(.borderless)
                            .help("Copia il comando")
                    }
                }
            }
        }
    }
}

private struct TestSection: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let pm = model.presetsModel
        Section {
            LabeledContent("Clip di prova") {
                HStack {
                    Text(pm.sampleURL?.lastPathComponent ?? "4K HDR10 generato")
                        .lineLimit(1).truncationMode(.middle)
                        .foregroundStyle(.secondary)
                    if pm.sampleURL != nil {
                        Button("Usa generato") { pm.sampleURL = nil }
                    }
                    Button("Scegli file…") { pm.chooseSample() }
                }
            }
            HStack {
                if pm.isTesting {
                    ProgressView().controlSize(.small)
                    Text(pm.testStep.isEmpty ? "Preparazione" : pm.testStep).foregroundStyle(.secondary)
                    Spacer()
                    Button("Stop") { pm.stopTest() }
                } else {
                    Button("Prova preset") { pm.runTest() }
                        .disabled(pm.hasErrors)
                    Spacer()
                    if let passed = pm.testPassed {
                        if !pm.testIsCurrent {
                            Label("Preset modificato dopo la prova", systemImage: "arrow.triangle.2.circlepath")
                                .font(.caption).foregroundStyle(.orange)
                        } else {
                            Label(passed ? "Prova superata" : "Prova non superata",
                                  systemImage: passed ? "checkmark.seal.fill" : "xmark.seal.fill")
                                .foregroundStyle(passed ? .green : .red)
                        }
                    }
                }
            }
            ForEach(pm.testChecks, id: \.self) { c in
                VStack(alignment: .leading, spacing: 2) {
                    Label(c.title, systemImage: c.level == "ok" ? "checkmark.circle.fill"
                          : (c.level == "warning" ? "exclamationmark.triangle.fill" : "xmark.octagon.fill"))
                        .foregroundStyle(c.level == "ok" ? Color.green : (c.level == "warning" ? .orange : .red))
                    if let d = c.detail, !d.isEmpty {
                        Text(d).font(.caption.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                            .padding(.leading, 22)
                    }
                }
            }
            if let fps = pm.testFPS, fps > 0 {
                LabeledContent("Velocità sul clip") {
                    Text(String(format: "%.1f fps · un film di 2 ore ≈ %@", fps, Fmt.duration(2 * 3600 * 23.976 / fps)))
                        .monospacedDigit()
                }
            }
            if let kbps = pm.testBitrate, kbps > 0 {
                LabeledContent("Bitrate sul clip", value: String(format: "%.1f Mbit/s", kbps / 1000))
            }
            if let e = pm.errorMessage {
                Text(e).foregroundStyle(.red).textSelection(.enabled)
            }
        } header: {
            Text("Prova reale")
        } footer: {
            Text("Codifica 3 secondi e controlla parametri accettati, codec, profondità, tag colore, metadati HDR10 e decodifica. Velocità e bitrate sul clip generato sono solo indicativi: per stime realistiche scegli un tuo file.")
                .font(.caption).foregroundStyle(.secondary)
        }
    }
}

/// Barra in basso: stato e azioni principali
private struct EditorBar: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let pm = model.presetsModel
        HStack {
            if let v = pm.draft?.verified, !pm.hasChanges {
                Label("Verificato il \(v.at.prefix(10)) con FFmpeg \(v.ffmpeg)", systemImage: "checkmark.seal.fill")
                    .font(.caption).foregroundStyle(.green)
            } else if pm.hasChanges {
                Text(pm.testIsCurrent && pm.testPassed == true ? "Prova superata: puoi salvare."
                     : "Il preset si salva dopo una prova reale superata.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if !pm.isNew, pm.hasChanges {
                Button("Annulla modifiche") { pm.revert() }
            }
            if pm.isNew {
                Button("Annulla") { pm.select(nil) }
            }
            Button(pm.testIsCurrent && pm.testPassed == true ? "Salva" : "Prova e salva") { pm.save() }
                .keyboardShortcut("s")
                .buttonStyle(.borderedProminent)
                .disabled(!pm.canSave)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(.bar)
    }
}
