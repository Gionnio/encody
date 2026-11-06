import AppKit
import SwiftUI

/// Strumento per la libreria: file Dolby Vision profilo 7 senza livello di miglioramento, che molti
/// lettori (Fire TV, TV Sony) non riproducono. Il motore (`encody dvfix`) li cerca in una cartella e li
/// corregge senza ricodificare: RPU in 8.1, stesse tracce, originale in ".encody-originali".
@MainActor @Observable
final class DVFixModel {
    struct Item: Identifiable {
        let id: Int
        let url: URL
        let size: Int64
        var status: Status = .found
        var percent = 0.0
        var message: String?
    }
    enum Status { case found, running, done, failed, stopped }

    var folder: URL?
    var items: [Item] = []
    var scanned: Int?
    var searching = false
    var fixing = false
    var error: String?
    private var run: EngineRun?

    var busy: Bool { searching || fixing }
    var pending: Int { items.filter { $0.status == .found }.count }

    func chooseFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.prompt = String(localized: "Scegli")
        guard panel.runModal() == .OK, let url = panel.url else { return }
        folder = url
        search()
    }

    func search() {
        guard let folder, !busy else { return }
        items = []
        scanned = nil
        error = nil
        searching = true
        start(["dvfix", folder.path])
    }

    func fix() {
        guard let folder, !busy, pending > 0 else { return }
        error = nil
        fixing = true
        // il motore rifà la ricerca: l'ordine dei file è lo stesso, gli indici dei lavori coincidono
        items = []
        start(["dvfix", "--apply", folder.path])
    }

    func stop() { run?.interrupt() }

    private func start(_ args: [String]) {
        let r = EngineRun(arguments: args)
        run = r
        do {
            try r.start(onEvent: { [weak self] in self?.handle($0) },
                        onExit: { [weak self] code, err in self?.finished(code: code, stderr: err) })
        } catch {
            finished(code: -1, stderr: error.localizedDescription)
        }
    }

    private func handle(_ ev: EngineEvent) {
        let i = ev.job.flatMap { $0 >= 0 && $0 < items.count ? $0 : nil }
        switch ev.type {
        case "dvfix_found":
            if let p = ev.path {
                items.append(Item(id: items.count, url: URL(fileURLWithPath: p), size: ev.size ?? 0))
            }
        case "dvfix_scan":
            scanned = ev.scanned
            searching = false
        case "dvfix_start":
            if let i { items[i].status = .running }
        case "progress":
            if let i { items[i].percent = ev.percent ?? 0 }
        case "dvfix_done":
            guard let i else { break }
            switch ev.status {
            case "ok": items[i].status = .done
            case "stop": items[i].status = .stopped
            default:
                items[i].status = .failed
                items[i].message = ev.error
            }
        case "error":
            error = ev.message
        default:
            break
        }
    }

    private func finished(code: Int32, stderr: String) {
        searching = false
        fixing = false
        run = nil
        for i in items.indices where items[i].status == .running { items[i].status = .stopped }
        if code != 0 && code != 130 && error == nil {
            let msg = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            error = msg.isEmpty ? String(localized: "encody terminato con codice \(code)") : msg
        }
    }
}

struct DVFixView: View {
    @State private var model = DVFixModel()

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Dolby Vision profilo 7 senza livello di miglioramento")
                    .font(.title3.weight(.semibold))
                Text("Alcuni file dichiarano il profilo 7 ma non contengono il secondo strato: Fire TV, TV Sony e altri lettori non li riproducono. Encody li cerca in una cartella e li converte in 8.1 senza ricodificare, con le stesse tracce. Gli originali vanno nella cartella nascosta \".encody-originali\".")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            HStack {
                Image(systemName: "folder")
                Text(model.folder?.path ?? String(localized: "Nessuna cartella scelta"))
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .foregroundStyle(model.folder == nil ? .secondary : .primary)
                Spacer()
                Button("Scegli cartella…") { model.chooseFolder() }
                    .disabled(model.busy)
            }

            Group {
                if model.searching {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text("Ricerca dei file…").foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if model.items.isEmpty {
                    Text(model.scanned.map { String(localized: "Nessun file da correggere (\($0) MKV controllati).") }
                         ?? String(localized: "Scegli una cartella della libreria."))
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    List(model.items) { item in
                        DVFixRow(item: item, root: model.folder)
                    }
                    .listStyle(.bordered(alternatesRowBackgrounds: true))
                }
            }
            .frame(minHeight: 220)

            if let e = model.error {
                Text(e).font(.caption).foregroundStyle(.red).textSelection(.enabled)
            }

            HStack {
                if let s = model.scanned, !model.items.isEmpty {
                    Text("\(model.items.count) file su \(s) MKV controllati")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if model.busy {
                    Button("Stop") { model.stop() }
                } else {
                    Button("Cerca di nuovo") { model.search() }
                        .disabled(model.folder == nil)
                    Button("Correggi \(model.pending) file") { model.fix() }
                        .buttonStyle(.borderedProminent)
                        .disabled(model.pending == 0)
                }
            }
        }
        .padding(20)
        .frame(minWidth: 560, minHeight: 420)
    }
}

private struct DVFixRow: View {
    let item: DVFixModel.Item
    let root: URL?

    var body: some View {
        HStack(spacing: 10) {
            icon.frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                Text(item.url.lastPathComponent).lineLimit(1).truncationMode(.middle)
                Text(detail)
                    .font(.caption)
                    .foregroundStyle(item.status == .failed ? .red : .secondary)
                    .lineLimit(2)
            }
            Spacer()
            if item.status == .running {
                ProgressView(value: item.percent, total: 100).frame(width: 90)
            }
        }
        .padding(.vertical, 2)
    }

    @ViewBuilder private var icon: some View {
        switch item.status {
        case .found: Image(systemName: "circle").foregroundStyle(.secondary)
        case .running: ProgressView().controlSize(.small)
        case .done: Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .failed: Image(systemName: "xmark.octagon.fill").foregroundStyle(.red)
        case .stopped: Image(systemName: "stop.circle").foregroundStyle(.orange)
        }
    }

    private var detail: String {
        if item.status == .failed, let m = item.message { return m.components(separatedBy: "\n").first ?? m }
        var rel = item.url.deletingLastPathComponent().path
        if let r = root?.path, rel.hasPrefix(r) { rel = String(rel.dropFirst(r.count)).trimmingCharacters(in: CharacterSet(charactersIn: "/")) }
        let size = Fmt.bytes(item.size)
        switch item.status {
        case .done: return String(localized: "Convertito in 8.1 · \(size)")
        case .stopped: return String(localized: "Interrotto: originale invariato")
        default: return rel.isEmpty ? size : "\(rel) · \(size)"
        }
    }
}
