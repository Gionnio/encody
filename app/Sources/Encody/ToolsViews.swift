import Charts
import SwiftUI

// MARK: - Benchmark

struct BenchmarkView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var bench = model.bench
        Form {
            Section("Sorgente") {
                FilePickRow(title: "File video", url: bench.fileURL) { url in
                    Task { await bench.setFile(url) }
                }
                if let p = bench.probe {
                    HStack(spacing: 6) {
                        MetaBadge(meta: p.metaType, dvProfile: p.dvProfile)
                        Chip(Fmt.bytes(p.size))
                        Chip(Fmt.duration(p.duration))
                        Chip("\(p.width)×\(p.height)")
                    }
                    if p.isHDR && bench.metric == .vmaf {
                        Label("vmaf_v0.6.1 è un modello SDR: su sorgenti \(p.metaType) i valori sono indicativi. Per l'HDR usa XPSNR o ColorVideoVDP.",
                              systemImage: "info.circle")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                if let e = bench.probeError {
                    Text(e).foregroundStyle(.red)
                }
                ForEach(bench.warnings, id: \.self) { w in
                    Label(w, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                }
            }
            .disabled(bench.isRunning)

            if bench.fileURL != nil && bench.probe != nil {
                BenchSegmentsSection(bench: bench)
                    .disabled(bench.isRunning)
            }

            Section {
                MetricPicker(metric: $bench.metric, choices: [.vmaf, .xpsnr, .cvvdp])
                Text(bench.metric.info)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } header: {
                Text("Metrica")
            }
            .disabled(bench.isRunning)

            Section("Preset da confrontare") {
                ForEach(model.presets.filter { !$0.isCopy }) { p in
                    Toggle(p.name, isOn: member($bench.selected, p.id))
                }
            }
            .disabled(bench.isRunning)

            Section {
                HStack {
                    if bench.isRunning {
                        Button("Stop", role: .destructive) { bench.stop() }
                    } else {
                        Button("Avvia benchmark") { bench.start() }
                            .disabled(bench.fileURL == nil || bench.selected.isEmpty || bench.isAnalyzing
                                      || bench.metric.unavailableReason(model.caps) != nil)
                            .keyboardShortcut("r")
                    }
                    Spacer()
                    if let why = bench.metric.unavailableReason(model.caps) {
                        Text(why).foregroundStyle(.red)
                    }
                }
                if bench.isRunning {
                    ProgressView(value: min(bench.percent, 100), total: 100) {
                        Text(progressTitle(bench))
                    } currentValueLabel: {
                        Text(String(format: "%.0f%% · %.1f fps", bench.percent, bench.fps)).monospacedDigit()
                    }
                }
                if let e = bench.errorMessage {
                    Text(e).foregroundStyle(.red).textSelection(.enabled)
                }
                ForEach(bench.failures, id: \.self) { f in
                    Text(f).font(.caption).foregroundStyle(.red).textSelection(.enabled)
                }
            } header: {
                Text("Esecuzione")
            } footer: {
                Text(bench.metric == .cvvdp
                     ? "Si codificano gli spezzoni scelti sopra, solo video. ColorVideoVDP ne analizza 8 s in tutto, divisi tra gli spezzoni. La dimensione è stimata sull'intera durata."
                     : "Si codificano gli spezzoni scelti sopra, solo video. La dimensione è stimata sull'intera durata.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            if !bench.results.isEmpty {
                BenchResultsSection(results: bench.results)
            }
        }
        .formStyle(.grouped)
        .navigationTitle("Benchmark")
    }

    private func progressTitle(_ b: BenchModel) -> String {
        var s = ""
        if !b.currentName.isEmpty { s += "[\(b.index)/\(b.total)] \(b.currentName) · " }
        return s + (b.step.isEmpty ? "Preparazione" : b.step)
    }
}

// MARK: - Spezzoni

private struct BenchSegmentsSection: View {
    let bench: BenchModel

    var body: some View {
        Section {
            if bench.isAnalyzing && bench.segments.isEmpty {
                ProgressView(value: min(bench.analysisPercent, 100), total: 100) {
                    Text(bench.analysisStep.isEmpty ? "Analisi del file" : bench.analysisStep)
                } currentValueLabel: {
                    Text(String(format: "%.0f%%", bench.analysisPercent)).monospacedDigit()
                }
            }
            ForEach(bench.segments) { seg in
                SegmentRow(bench: bench, segment: seg, zone: zoneLabel(seg.id))
            }
            ForEach(bench.segmentWarnings, id: \.self) { w in
                Label(w, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(.orange)
            }
            if let e = bench.analysisError {
                Label("Analisi non riuscita: si userà lo spezzone centrale. \(e)", systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(.orange)
                    .textSelection(.enabled)
            }
        } header: {
            HStack {
                Text("Spezzoni")
                Spacer()
                if bench.hasManualSegments {
                    Button("Ripristina automatici") { bench.analyzeSegments() }
                        .buttonStyle(.link)
                        .font(.caption)
                        .disabled(bench.isAnalyzing)
                }
            }
        } footer: {
            Text("Scelti analizzando bitrate e luminosità: niente neri, dissolvenze o scene troppo scure, complessità sopra la media. Sigla e titoli di coda esclusi. Puoi cambiare l'inizio di ogni spezzone a mano.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private func zoneLabel(_ i: Int) -> String {
        guard bench.segments.count == 3 else { return "Centro" }
        return ["Inizio", "Centro", "Fine"][i]
    }
}

private struct SegmentRow: View {
    let bench: BenchModel
    let segment: BenchSegment
    let zone: String
    @State private var text = ""
    @FocusState private var focused: Bool

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            thumbnail
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 6) {
                    Text(zone).font(.headline)
                    if segment.isManual { Chip("manuale") }
                }
                HStack(spacing: 6) {
                    Text("Inizio")
                        .foregroundStyle(.secondary)
                    TextField("m:ss", text: $text)
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 80)
                        .monospacedDigit()
                        .focused($focused)
                        .onSubmit(commit)
                        .help("Minuti:secondi (o ore:minuti:secondi). Invio per applicare.")
                    Text("· \(Int(segment.duration.rounded())) s")
                        .foregroundStyle(.secondary)
                }
                HStack(spacing: 6) {
                    if let l = segment.luma { Chip("luminosità \(Int((l * 100).rounded()))%") }
                    if let b = segment.bitrate, let s = Fmt.bitrate(b) { Chip(s) }
                }
                if let n = segment.note {
                    Text(n).font(.caption).foregroundStyle(.orange)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, 2)
        .onAppear { text = Fmt.timestamp(segment.start) }
        .onChange(of: segment.start) { _, v in if !focused { text = Fmt.timestamp(v) } }
        .onChange(of: focused) { _, f in if !f { commit() } }
    }

    private var thumbnail: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 8).fill(.quaternary)
            if let img = segment.image {
                Image(nsImage: img)
                    .resizable()
                    .aspectRatio(contentMode: .fill)
            } else {
                ProgressView().controlSize(.small)
            }
        }
        .frame(width: 160, height: 90)
        .clipShape(RoundedRectangle(cornerRadius: 8))
    }

    private func commit() {
        guard let t = Fmt.parseTimestamp(text) else {
            text = Fmt.timestamp(segment.start) // non valido: si torna al valore di prima
            return
        }
        if abs(t - segment.start) >= 1 { bench.setStart(segment.id, to: t) }
        text = Fmt.timestamp(min(t, max(0, bench.fileDuration - bench.segmentSeconds)))
    }
}

private struct BenchResultsSection: View {
    @Environment(AppModel.self) private var model
    let results: [BenchResult]

    var body: some View {
        // Un benchmark usa una sola metrica per tutti i preset
        let metric = results.first.flatMap { QualityMetric(rawValue: $0.metric) } ?? .vmaf
        Section("Risultati · \(metric.label)") {
            if results.count > 1 {
                Chart(results) { r in
                    PointMark(x: .value("Dimensione (GB)", Double(r.size) / 1e9),
                              y: .value(metric.label, r.score))
                        .symbolSize(90)
                        .annotation(position: .top) {
                            Text(r.name).font(.caption2).foregroundStyle(.secondary)
                        }
                }
                .chartYScale(domain: .automatic(includesZero: false))
                .chartXScale(domain: .automatic(includesZero: false))
                .chartXAxisLabel("Dimensione stimata (GB)")
                .chartYAxisLabel(metric.unit.isEmpty ? metric.label : "\(metric.label) (\(metric.unit))")
                .frame(height: 220)
                .padding(.vertical, 6)
            }
            ForEach(Array(results.enumerated()), id: \.element.id) { i, r in
                HStack(spacing: 10) {
                    Text("\(i + 1).")
                        .font(.body.monospacedDigit())
                        .foregroundStyle(.secondary)
                        .frame(width: 24, alignment: .trailing)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(r.name).fontWeight(i == 0 ? .semibold : .regular)
                        Text(([ "\(Fmt.bytes(r.size)) stimati", "\(Int(r.fps.rounded())) fps"] + [r.detail].compactMap { $0 })
                            .joined(separator: " · "))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    Text(metric.format(r.score))
                        .font(.title3.monospacedDigit().weight(.semibold))
                        .foregroundStyle(metric.color(r.score))
                    Button("In coda") { model.enqueueFromBench(r) }
                        .help("Aggiunge il file alla coda con questo preset")
                }
            }
        }
    }
}

/// Picker delle metriche: quelle non disponibili restano visibili ma con il motivo nel tooltip
struct MetricPicker: View {
    @Environment(AppModel.self) private var model
    @Binding var metric: QualityMetric
    let choices: [QualityMetric]

    var body: some View {
        Picker("Metrica", selection: $metric) {
            ForEach(choices) { m in
                Text(m.label).tag(m)
            }
        }
        .pickerStyle(.segmented)
        .labelsHidden()
        if let why = metric.unavailableReason(model.caps) {
            Label(why, systemImage: "exclamationmark.triangle.fill")
                .font(.caption)
                .foregroundStyle(.orange)
        }
    }
}

// MARK: - Quality check

struct QualityView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var q = model.quality
        Form {
            Section("File") {
                FilePickRow(title: "Originale (reference)", url: q.refURL) { q.refURL = $0; q.result = nil }
                FilePickRow(title: "Encodato", url: q.distURL) { q.distURL = $0; q.result = nil }
            }
            .disabled(q.isRunning)

            Section {
                MetricPicker(metric: $q.metric, choices: QualityMetric.allCases)
                    .disabled(q.isRunning)
                Text(q.metric.info)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } header: {
                Text("Metrica")
            } footer: {
                Text(q.metric == .cvvdp
                     ? "Il file encodato viene riportato alla risoluzione dell'originale. ColorVideoVDP analizza 10 s dal centro del file. Con un crop applicato il confronto non è attendibile."
                     : "Il file encodato viene riportato alla risoluzione dell'originale. Con un crop applicato il confronto non è attendibile.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                HStack {
                    if q.isRunning {
                        Button("Stop", role: .destructive) { q.stop() }
                    } else {
                        Button("Avvia analisi") { q.start() }
                            .disabled(!q.canStart || q.metric.unavailableReason(model.caps) != nil)
                            .keyboardShortcut("r")
                    }
                    Spacer()
                }
                if q.isRunning {
                    ProgressView(value: min(q.percent, 100), total: 100) {
                        Text(q.step.isEmpty ? "Preparazione" : q.step)
                    } currentValueLabel: {
                        Text(String(format: "%.0f%% · %.1f fps", q.percent, q.fps)).monospacedDigit()
                    }
                }
                if let e = q.errorMessage {
                    Text(e).foregroundStyle(.red).textSelection(.enabled)
                }
            }

            if let r = q.result {
                Section("Risultato") {
                    HStack(alignment: .firstTextBaseline, spacing: 16) {
                        Text(r.metric.format(r.value))
                            .font(.system(size: 44, weight: .bold, design: .rounded))
                            .monospacedDigit()
                            .foregroundStyle(r.metric.color(r.value))
                        VStack(alignment: .leading) {
                            Text(r.metric.unit.isEmpty ? r.metric.label : "\(r.metric.label) · \(r.metric.unit)")
                                .font(.headline)
                            Text(r.verdict).foregroundStyle(.secondary)
                            if let d = r.detail {
                                Text(d).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                            }
                        }
                    }
                    ProgressView(value: r.metric.normalized(r.value))
                        .tint(r.metric.color(r.value))
                }
            }
        }
        .formStyle(.grouped)
        .navigationTitle("Check Qualità")
    }
}
