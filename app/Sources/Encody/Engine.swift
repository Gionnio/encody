import Foundation

/// Contenitore per dati riempiti da un thread e letti da un altro dopo group.wait()
private final class DataBox: @unchecked Sendable {
    var data = Data()
}

struct EngineError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

enum Engine {
    static let videoExtensions: Set<String> = ["mkv", "mp4", "mov", "avi", "m2ts", "ts", "m4v"]

    /// Ordine: percorso dalle Impostazioni → binario incluso nell'app → percorsi comuni
    static func binaryURL() -> URL? {
        let fm = FileManager.default
        if let custom = UserDefaults.standard.string(forKey: "enginePath"), !custom.isEmpty {
            let expanded = (custom as NSString).expandingTildeInPath
            if fm.isExecutableFile(atPath: expanded) { return URL(fileURLWithPath: expanded) }
        }
        if let bundled = Bundle.main.url(forResource: "encody", withExtension: nil),
           fm.isExecutableFile(atPath: bundled.path) {
            return bundled
        }
        let home = fm.homeDirectoryForCurrentUser.path
        for p in ["/opt/homebrew/bin/encody", "/usr/local/bin/encody", "\(home)/go/bin/encody", "\(home)/bin/encody"]
        where fm.isExecutableFile(atPath: p) {
            return URL(fileURLWithPath: p)
        }
        return nil
    }

    /// Un'app avviata dal Finder ha un PATH minimo: si aggiungono i percorsi di Homebrew, MKVToolNix, cargo e go.
    static var environment: [String: String] {
        var env = ProcessInfo.processInfo.environment
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        var paths: [String] = []
        if let extra = UserDefaults.standard.string(forKey: "extraPath"), !extra.isEmpty {
            paths += extra.split(separator: ":").map { (String($0) as NSString).expandingTildeInPath }
        }
        paths += ["/opt/homebrew/bin", "/usr/local/bin", "/Applications/MKVToolNix.app/Contents/MacOS",
                  "\(home)/.cargo/bin", "\(home)/go/bin"]
        paths += (env["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin").split(separator: ":").map(String.init)
        env["PATH"] = paths.joined(separator: ":")
        // messaggi del motore nella stessa lingua dell'app
        env["ENCODY_LANG"] = Bundle.main.preferredLocalizations.first == "en" ? "en" : "it"
        return env
    }

    static func expandVideos(_ urls: [URL]) -> [URL] {
        let fm = FileManager.default
        var out: [URL] = []
        for url in urls {
            var isDir: ObjCBool = false
            guard fm.fileExists(atPath: url.path, isDirectory: &isDir) else { continue }
            if isDir.boolValue {
                guard let en = fm.enumerator(at: url, includingPropertiesForKeys: [.isRegularFileKey],
                                             options: [.skipsHiddenFiles, .skipsPackageDescendants]) else { continue }
                for case let f as URL in en where videoExtensions.contains(f.pathExtension.lowercased()) {
                    out.append(f)
                }
            } else if videoExtensions.contains(url.pathExtension.lowercased()) {
                out.append(url)
            }
        }
        return out.sorted { $0.path.localizedStandardCompare($1.path) == .orderedAscending }
    }

    /// Comandi a risposta singola (caps, probe, crop, plan)
    static func runJSON(_ args: [String], stdin: Data? = nil) async throws -> Data {
        guard let bin = binaryURL() else {
            throw EngineError(message: String(localized: "Motore encody non trovato. Impostane il percorso nelle Impostazioni."))
        }
        let env = environment
        return try await withCheckedThrowingContinuation { cont in
            DispatchQueue.global(qos: .userInitiated).async {
                let p = Process()
                p.executableURL = bin
                p.arguments = args
                p.environment = env
                let out = Pipe(), err = Pipe(), input = Pipe()
                p.standardOutput = out
                p.standardError = err
                p.standardInput = stdin == nil ? FileHandle.nullDevice : input
                do { try p.run() } catch {
                    cont.resume(throwing: error)
                    return
                }
                if let stdin {
                    DispatchQueue.global().async {
                        try? input.fileHandleForWriting.write(contentsOf: stdin)
                        try? input.fileHandleForWriting.close()
                    }
                }
                let errBox = DataBox()
                let group = DispatchGroup()
                group.enter()
                DispatchQueue.global().async {
                    errBox.data = err.fileHandleForReading.readDataToEndOfFile()
                    group.leave()
                }
                let data = out.fileHandleForReading.readDataToEndOfFile()
                group.wait()
                p.waitUntilExit()
                let errData = errBox.data

                if p.terminationStatus == 0 {
                    cont.resume(returning: data)
                    return
                }
                struct ErrOut: Decodable { let error: String }
                let msg = (try? JSONDecoder().decode(ErrOut.self, from: data))?.error
                    ?? String(decoding: errData, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
                cont.resume(throwing: EngineError(message: msg.isEmpty ? String(localized: "encody terminato con codice \(p.terminationStatus)") : msg))
            }
        }
    }
}

/// Processo a lunga durata (run, bench, quality) che emette eventi NDJSON.
/// interrupt() manda SIGINT: il motore ferma i job, pulisce temp e parziali ed esce.
final class EngineRun {
    private static let lock = NSLock()
    private static var active: [ObjectIdentifier: EngineRun] = [:]

    static var hasActive: Bool {
        lock.lock()
        defer { lock.unlock() }
        return !active.isEmpty
    }

    /// Usato in chiusura dell'app: interrompe tutto e attende la pulizia
    static func interruptAll(wait seconds: Double) {
        lock.lock()
        let runs = Array(active.values)
        lock.unlock()
        runs.forEach { $0.interrupt() }
        let deadline = Date().addingTimeInterval(seconds)
        while hasActive && Date() < deadline {
            Thread.sleep(forTimeInterval: 0.05)
        }
    }

    let arguments: [String]
    private let process = Process()

    init(arguments: [String]) {
        self.arguments = arguments
    }

    func start(onEvent: @escaping @MainActor (EngineEvent) -> Void,
               onExit: @escaping @MainActor (Int32, String) -> Void) throws {
        guard let bin = Engine.binaryURL() else {
            throw EngineError(message: String(localized: "Motore encody non trovato. Impostane il percorso nelle Impostazioni."))
        }
        process.executableURL = bin
        process.arguments = arguments
        process.environment = Engine.environment
        process.standardInput = FileHandle.nullDevice
        let out = Pipe(), err = Pipe()
        process.standardOutput = out
        process.standardError = err
        try process.run()

        let key = ObjectIdentifier(self)
        Self.lock.lock()
        Self.active[key] = self
        Self.lock.unlock()

        let process = self.process
        DispatchQueue.global(qos: .userInitiated).async {
            let errBox = DataBox()
            let group = DispatchGroup()
            group.enter()
            DispatchQueue.global().async {
                errBox.data = err.fileHandleForReading.readDataToEndOfFile()
                group.leave()
            }

            let decoder = JSONCoding.makeDecoder()
            let handle = out.fileHandleForReading
            var buffer = Data()
            while true {
                let chunk = handle.availableData
                if chunk.isEmpty { break } // EOF
                buffer.append(chunk)
                while let nl = buffer.firstIndex(of: 0x0A) {
                    let line = buffer.subdata(in: buffer.startIndex..<nl)
                    buffer.removeSubrange(buffer.startIndex...nl)
                    guard !line.isEmpty, let ev = try? decoder.decode(EngineEvent.self, from: line) else { continue }
                    DispatchQueue.main.async { MainActor.assumeIsolated { onEvent(ev) } }
                }
            }

            group.wait()
            process.waitUntilExit()
            let code = process.terminationStatus
            let stderr = String(decoding: errBox.data, as: UTF8.self)
            Self.lock.lock()
            Self.active[key] = nil
            Self.lock.unlock()
            DispatchQueue.main.async { MainActor.assumeIsolated { onExit(code, stderr) } }
        }
    }

    func interrupt() {
        if process.isRunning { process.interrupt() }
    }
}
