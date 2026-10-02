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
                            .disabled(bench.fileURL == nil || bench.selected.isEmpty
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
                     ? "Spezzone di 45 s dal centro del film, solo video. ColorVideoVDP ne analizza 10 s. La dimensione è stimata sull'intera durata."
                     : "Spezzone di 45 s dal centro del film, solo video. La dimensione è stimata sull'intera durata.")
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
